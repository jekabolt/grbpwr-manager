package app

import (
	"log"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/minio/minio-go"
)

type Server struct {
	DB *store.DB

	Port   string
	Host   string
	origin string
}

func InitServer(db *store.DB, port, host, origin string) *Server {

	client, err := minio.New("nyc3.digitaloceanspaces.com", accessKey, secKey, ssl)
	if err != nil {
		log.Fatal(err)
	}

	return &Server{
		DB:     db,
		Port:   port,
		Host:   host,
		origin: origin,
	}
}
