package app

import (
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB     *store.DB
	Port   string
	Host   string
	origin string
}

func InitServer(db *store.DB, port, host, origin string) *Server {
	return &Server{
		DB:     db,
		Port:   port,
		Host:   host,
		origin: origin,
	}
}
