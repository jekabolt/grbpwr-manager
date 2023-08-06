package config

import (
	"fmt"
	"reflect"
	"strings"

	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
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
}

// LoadConfig inits config from file or suggested files and updates with env
func LoadConfig(cfgFile string) (*Config, error) {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("./config")
		viper.AddConfigPath("$HOME/config")
		viper.AddConfigPath("/etc/grbpwr-products-manager")
	}
	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	viperBindEnvs(cfg)

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config error: %v", err)
	}

	return &cfg, nil
}

func viperBindEnvs(iface interface{}, parts ...string) {
	ifv := reflect.ValueOf(iface)
	ift := reflect.TypeOf(iface)
	for i := 0; i < ift.NumField(); i++ {
		v := ifv.Field(i)
		t := ift.Field(i)
		tv, ok := t.Tag.Lookup("mapstructure")
		if !ok {
			tv = strings.ToLower(t.Name)
		}
		if tv == "-" {
			continue
		}

		switch v.Kind() {
		case reflect.Struct:
			viperBindEnvs(v.Interface(), append(parts, tv)...)
		default:
			// Bash doesn't allow env variable names with a dot so
			// bind the double underscore version.
			keyDot := strings.Join(append(parts, tv), ".")
			keyUnderscore := strings.Join(append(parts, tv), "__")
			_ = viper.BindEnv(keyDot, strings.ToUpper(keyUnderscore))
		}
	}
}
