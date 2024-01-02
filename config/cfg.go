package config

import (
	"fmt"

	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/rates"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/spf13/viper"
)

// Config represents the global configuration for the service.
type Config struct {
	DB     store.Config   `mapstructure:"mysql"`
	Logger log.Config     `mapstructure:"logger"`
	HTTP   httpapi.Config `mapstructure:"http"`
	Auth   auth.Config    `mapstructure:"auth"`
	Bucket bucket.Config  `mapstructure:"bucket"`
	Mailer mail.Config    `mapstructure:"mailer"`
	Rates  rates.Config   `mapstructure:"rates"`
}

// LoadConfig loads the configuration from a file.
func LoadConfig(cfgFile string) (*Config, error) {
	viper.SetConfigType("toml")
	viper.SetConfigFile(cfgFile)
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("./config")
		viper.AddConfigPath("$HOME/config/grbpwr-products-manager")
		viper.AddConfigPath("/etc/grbpwr-products-manager")
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config into struct: %v", err)
	}
	return &config, nil
}
