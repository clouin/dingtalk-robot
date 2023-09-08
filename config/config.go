package config

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

var Content *Config

type Config struct {
	DingTalk DingTalkConfig `json:"dingtalk" mapstructure:"dingtalk" yaml:"dingtalk"`
	Log      LogConfig      `json:"log"      mapstructure:"log"      yaml:"log"`
}

type DingTalkConfig struct {
	AccessToken string `json:"access_token" mapstructure:"access_token" yaml:"access_token"`
	Secret      string `json:"secret"       mapstructure:"secret"       yaml:"secret"`
}

type LogConfig struct {
	Level string `json:"level" mapstructure:"level" yaml:"level"`
}

func LoadConfig() error {
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	// 绑定环境变量
	viper.BindEnv("dingtalk.access_token", "ACCESS_TOKEN")
	viper.BindEnv("dingtalk.secret", "SECRET")
	viper.BindEnv("log.level", "LOG_LEVEL")

	// 设置默认值
	viper.SetDefault("log.level", "info")

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./local")
	viper.AddConfigPath("./config")

	if err := viper.ReadInConfig(); err != nil {
		log.Errorf("viper read config.yaml failed: %v", err)
	}
	if err := viper.Unmarshal(&Content); err != nil {
		log.Errorf("viper unmarshal failed: %v", err)
		return err
	}

	viper.WriteConfig()
	return nil
}

func SaveConfig(key, value string) {
	viper.Set(key, value) //通过set去修改配置
	viper.WriteConfig()
}
