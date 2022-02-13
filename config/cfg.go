package config

import (
	"fmt"

	"github.com/caarlos0/env/v6"
	"github.com/jekabolt/grbpwr-manager/auth"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/jekabolt/grbpwr-manager/store/bunt"
)

type Config struct {
	Port  string   `env:"PORT" envDefault:"8081"`
	Hosts []string `env:"HOSTS" envSeparator:"|"`
	Bunt  *bunt.Config

	Auth *auth.Config
	Hero *store.Hero

	Debug bool `env:"DEBUG" envDefault:"false"`
}

func GetConfig() (*Config, error) {
	var err error

	cfg := &Config{
		Hero: &store.Hero{},
		Auth: &auth.Config{},
		Bunt: &bunt.Config{},
	}

	err = env.Parse(cfg)
	if err != nil {
		return nil, fmt.Errorf("GetConfig:env.Parse: [%s]", err.Error())
	}

	return cfg, nil
}
