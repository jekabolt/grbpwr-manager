package main

import (
	"encoding/json"
	"fmt"

	"github.com/caarlos0/env/v6"
	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Config struct {
	Port   string `env:"PORT" envDefault:"8080"`
	Host   string `env:"HOST" envDefault:"localhost:8080"`
	Origin string `env:"ORIGIN" envDefault:"*"`

	BuntDBProductsPath string `env:"BUNT_DB_PRODUCTS_PATH" envDefault:"products.db"`
	BuntDBArticlesPath string `env:"BUNT_DB_ARTICLES_PATH" envDefault:"articles.db"`
	BuntDBSalesPath    string `env:"BUNT_DB_SALES_PATH" envDefault:"sales.db"`

	Debug bool `env:"DEBUG" envDefault:"false"`
}

func main() {
	cfg := &Config{}
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

	db, err := store.InitDB(cfg.BuntDBProductsPath, cfg.BuntDBArticlesPath, cfg.BuntDBSalesPath)
	if err != nil {
		log.Fatal().Err(err).Msg(fmt.Sprintf("Failed to InitDB err:[%s]", err.Error()))
	}

	s := app.InitServer(db, cfg.Port, cfg.Host, cfg.Origin)

	log.Fatal().Err(s.Serve()).Msg("InitServer")
}
