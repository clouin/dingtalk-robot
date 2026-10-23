package custom

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Sign 按官方算法计算签名：以 secret 为密钥对 "timestamp\nsecret" 做 HMAC-SHA256，
// 再做标准 Base64。返回的是未 URL 编码的原值。
func Sign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignedURL 构造带 access_token（以及可选 timestamp/sign）的 Webhook 地址。
// secret 为空表示该机器人未启用加签，此时不附加签名参数。
func SignedURL(endpoint, accessToken, secret string, now time.Time) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid webhook endpoint %q: %w", endpoint, err)
	}

	query := parsed.Query()
	query.Set("access_token", accessToken)
	if secret != "" {
		timestamp := strconv.FormatInt(now.UnixMilli(), 10)
		query.Set("timestamp", timestamp)
		query.Set("sign", Sign(timestamp, secret))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
