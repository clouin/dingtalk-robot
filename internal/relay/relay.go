// Package relay 负责投递编排：能力检查、限流、重试与结果映射。
package relay

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/time/rate"

	"dingtalk-robot/internal/apierr"
	"dingtalk-robot/internal/config"
	"dingtalk-robot/internal/message"
	"dingtalk-robot/internal/provider"
)

// UpstreamThrottleRetryAfter 是通道没有给出等待建议时的兜底时长。
//
// 等待建议属于通道知识：自定义机器人封禁 10 分钟，企业通道是 QPS 级抖动（1 秒），
// 由各自 provider 随结果返回。这里只兜住"通道没有给出建议"的情况。
// 本地令牌桶拒绝使用预约延迟（reservation.Delay()），不引用此常量。
const UpstreamThrottleRetryAfter = provider.CustomThrottleRetryAfter

// Metrics 是编排层需要的指标能力。
type Metrics interface {
	ObserveDelivery(provider, class string, duration time.Duration)
	IncRateLimit(action string)
}

// Config 是编排配置。
type Config struct {
	Provider    provider.Provider
	RateLimit   *rate.Limiter
	Strategy    string
	Retries     int
	BackoffBase time.Duration
	Timeout     time.Duration
	Metrics     Metrics
	Logger      *slog.Logger
	Sleep       func(ctx context.Context, d time.Duration) error
}

// Relay 把一条消息投递到配置的通道。
type Relay struct {
	cfg Config
}

// New 创建编排器。
func New(cfg Config) *Relay {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	return &Relay{cfg: cfg}
}

// ProviderName 返回当前通道名。
func (r *Relay) ProviderName() string { return r.cfg.Provider.Name() }

// Outcome 描述一次投递的最终状态。
type Outcome struct {
	Result   provider.Result
	Attempts int
	Duration time.Duration
}

// Send 执行投递；返回的 error 一定是 *apierr.Error 或 nil。
//
// 校验分两步：先做与通道无关的可投递性检查，再问通道能否表达这条消息。
// 顺序不能反——空正文的 feedCard 首先是一条非法消息，而不是"通道不支持"。
func (r *Relay) Send(ctx context.Context, m *message.Message) (Outcome, error) {
	if err := m.Validate(); err != nil {
		return Outcome{}, err
	}
	if err := r.cfg.Provider.Validate(m); err != nil {
		return Outcome{}, err
	}

	// 预算覆盖排队、重试与投递：等待令牌同样消耗 send.timeout，
	// 否则 wait 策略在极端配置下会一直等到客户端断开。
	sendCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	if err := r.acquire(sendCtx); err != nil {
		return Outcome{}, err
	}

	start := time.Now()
	var outcome Outcome

	for {
		outcome.Attempts++

		attemptStart := time.Now()
		result := r.cfg.Provider.Send(sendCtx, m)
		r.observe(result.Class, time.Since(attemptStart))
		outcome.Result = result

		if result.Class != provider.ClassRetryable || outcome.Attempts > r.cfg.Retries {
			break
		}
		if sendCtx.Err() != nil {
			break
		}

		backoff := r.cfg.BackoffBase
		for i := 1; i < outcome.Attempts; i++ {
			backoff *= 2
		}
		r.cfg.Logger.Debug("retrying delivery",
			"provider", r.cfg.Provider.Name(),
			"attempt", outcome.Attempts,
			"backoff", backoff.String(),
			"errcode", result.ErrCode,
		)
		if err := r.cfg.Sleep(sendCtx, backoff); err != nil {
			break
		}
	}

	outcome.Duration = time.Since(start)
	if outcome.Result.Class == provider.ClassOK {
		return outcome, nil
	}
	return outcome, mapResult(outcome.Result)
}

// acquire 取得一个出站额度。
func (r *Relay) acquire(ctx context.Context) error {
	if r.cfg.RateLimit == nil {
		return nil
	}

	if r.cfg.Strategy == config.StrategyReject {
		reservation := r.cfg.RateLimit.Reserve()
		if delay := reservation.Delay(); delay > 0 {
			reservation.Cancel()
			r.rateLimitEvent("rejected")
			return apierr.RateLimited("local rate limit exceeded", delay)
		}
		return nil
	}

	if err := r.cfg.RateLimit.Wait(ctx); err != nil {
		r.rateLimitEvent("cancelled")
		return apierr.RateLimited("rate limit wait cancelled before a token was available", 0)
	}
	return nil
}

func (r *Relay) observe(class provider.ErrorClass, duration time.Duration) {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ObserveDelivery(r.cfg.Provider.Name(), string(class), duration)
	}
}

func (r *Relay) rateLimitEvent(action string) {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.IncRateLimit(action)
	}
}

// mapResult 把上游结果映射为对外错误。
//
// 上游字符串码在返回前统一附加，因此五条分支都不必各自处理——
// 这也是两条通道保持同构的原因：映射只发生在这一处。
func mapResult(result provider.Result) *apierr.Error {
	var err *apierr.Error
	switch result.Class {
	case provider.ClassThrottled:
		retryAfter := result.RetryAfter
		if retryAfter <= 0 {
			retryAfter = UpstreamThrottleRetryAfter
		}
		err = apierr.RateLimited(resultMessage(result, "upstream rate limited"), retryAfter)
		err.Upstream = result.ErrCode
	case provider.ClassConfig, provider.ClassRejected, provider.ClassAuth:
		err = apierr.UpstreamRejected(result.ErrCode, resultMessage(result, "upstream rejected the message"))
	case provider.ClassRetryable:
		if result.ErrCode == 0 {
			err = apierr.UpstreamTimeout(resultMessage(result, "upstream did not respond in time"))
		} else {
			err = apierr.UpstreamRejected(result.ErrCode, resultMessage(result, "upstream failed"))
		}
	default:
		err = apierr.UpstreamRejected(result.ErrCode, resultMessage(result, "upstream failed"))
	}
	err.UpstreamCode = result.UpstreamCode
	return err
}

func resultMessage(result provider.Result, fallback string) string {
	if result.ErrMsg != "" {
		return result.ErrMsg
	}
	return fallback
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
