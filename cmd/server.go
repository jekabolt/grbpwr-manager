package main

import (
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse env variables")
	}

	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	confBytes, _ := json.Marshal(cfg)
	log.Info().Str("config:", "").Msg(string(confBytes))

	db, err := cfg.Bunt.InitDB()
	if err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to InitDB err:[%s]", err.Error()))
	}

	b, err := bucket.BucketFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to init get bucket env err:[%s]", err.Error()))
	}
	if err := b.InitBucket(); err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to init bucket err:[%s]", err.Error()))
	}

	s := app.InitServer(db, b, cfg)

	log.Fatal().Err(s.Serve()).Msg("InitServer")
}
