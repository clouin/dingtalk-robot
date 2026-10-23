package enterprise

import (
	"context"
	"sync"
	"time"
)

// refreshAhead 是提前刷新的时间窗口。
const refreshAhead = 300 * time.Second

type fetchFunc func(ctx context.Context) (token string, expiry time.Time, err error)

// tokenCache 缓存访问令牌。持锁执行获取，使并发请求共享同一次刷新。
type tokenCache struct {
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (c *tokenCache) get(ctx context.Context, now time.Time, fetch fetchFunc) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && now.Add(refreshAhead).Before(c.expiry) {
		return c.token, nil
	}

	token, expiry, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	c.expiry = expiry
	return token, nil
}

func (c *tokenCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.expiry = time.Time{}
}
