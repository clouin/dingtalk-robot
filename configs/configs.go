package configs

import (
	"os"
	"strings"
)

// GetConfig get configs with key
func GetConfig(key string) (string, error) {
	// Check the environment variable
	envPrefix := os.Getenv("DINGTALK_ENV_PREFIX")
	envKey := envPrefix + strings.ToUpper(key)
	result := os.Getenv(envKey)

	return result, nil
}

func GetAccessToken() string {
	if len(AccessToken) > 0 {
		return AccessToken
	}

	value, err := GetConfig(AccessTokenKey)
	if err == nil {
		return value
	}
	return ""
}

func GetSecret() string {
	if len(Secret) > 0 {
		return Secret
	}

	value, err := GetConfig(SecretKey)
	if err == nil {
		return value
	}
	return ""
}
