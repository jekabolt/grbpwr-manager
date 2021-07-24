package main

import (
	"encoding/json"
	"fmt"

	"github.com/caarlos0/env/v6"
	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Config struct {
	Port   string `env:"PORT" envDefault:"8081"`
	Host   string `env:"HOST" envDefault:"localhost:8080"`
	Origin string `env:"ORIGIN" envDefault:"*"`

	Bucket *bucket.Bucket
	DB     *store.DB

	Debug bool `env:"DEBUG" envDefault:"false"`
}

func main() {
	cfg := &Config{
		Bucket: &bucket.Bucket{},
		DB:     &store.DB{},
	}
	err := env.Parse(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse env variables")
	}

	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	b, _ := json.Marshal(cfg)
	log.Info().Str("config", string(b)).Msg("Started with config")

	err = cfg.DB.InitDB()
	if err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to InitDB err:[%s]", err.Error()))
	}

	err = cfg.Bucket.InitBucket()
	if err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to init bucket err:[%s]", err.Error()))
	}

	s := app.InitServer(cfg.DB, cfg.Bucket, cfg.Port, cfg.Host, cfg.Origin)

	log.Fatal().Err(s.Serve()).Msg("InitServer")
}
