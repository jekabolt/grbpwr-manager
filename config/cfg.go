package config

import (
	"fmt"

	"github.com/caarlos0/env/v6"
	"github.com/jekabolt/grbpwr-manager/auth"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Config struct {
	Port        string   `env:"PORT" envDefault:"8081"`
	Hosts       []string `env:"HOSTS" envSeparator:"|"`
	StorageType string   `env:"STORAGE_TYPE" envDefault:"bunt"` // bunt, redis

	Auth *auth.Config
	Hero *store.Hero

	Debug bool `env:"DEBUG" envDefault:"false"`
}

func GetConfig() (*Config, error) {
	var err error

	cfg := &Config{
		Hero: &store.Hero{},
		Auth: &auth.Config{},
	}

	err = env.Parse(cfg)
	if err != nil {
		return nil, fmt.Errorf("GetConfig:env.Parse: [%s]", err.Error())
	}

	return cfg, nil
}
