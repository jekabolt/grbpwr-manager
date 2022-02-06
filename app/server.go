package app

import (
	"github.com/jekabolt/grbpwr-manager/auth"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB     store.Store
	Bucket *bucket.Bucket
	Auth   *auth.Auth
	Config *config.Config
}

func InitServer(db store.Store, bucket *bucket.Bucket, cfg *config.Config) *Server {
	a := cfg.Auth.New()
	return &Server{
		DB:     db,
		Bucket: bucket,
		Auth:   a,
		Config: cfg,
	}
}
