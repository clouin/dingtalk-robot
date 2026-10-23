// Package observability 提供结构化日志与指标。
package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// NewLogger 按级别与格式创建 logger。
//
// format 为 "json" 时输出 JSON，其余取值输出 key=value 文本行。两种格式共用同一条
// 属性改写规则：时间戳为秒级 RFC3339（带时区偏移）、级别为小写，使容器日志与
// 常见的 logrus 系输出（如 Watchtower）观感一致。
func NewLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

// SetupLogger 创建 logger 并安装为全局默认。
func SetupLogger(level slog.Level, format string) *slog.Logger {
	logger := NewLogger(os.Stdout, level, format)
	slog.SetDefault(logger)
	return logger
}

// replaceAttr 统一时间与级别的表示形式。
//
// 只处理顶层属性：分组内的同名键属于调用方语义，不应被改写。
func replaceAttr(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) != 0 {
		return attr
	}

	switch attr.Key {
	case slog.TimeKey:
		return slog.String(slog.TimeKey, attr.Value.Time().Format(time.RFC3339))
	case slog.LevelKey:
		if level, ok := attr.Value.Any().(slog.Level); ok {
			return slog.String(slog.LevelKey, strings.ToLower(level.String()))
		}
	}
	return attr
}
