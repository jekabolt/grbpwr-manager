package app

import (
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB     store.ProductStore
	Bucket *bucket.Bucket

	Port   string
	Host   string
	origin string
}

func InitServer(db store.ProductStore, bucket *bucket.Bucket, port, host, origin string) *Server {
	return &Server{
		DB:     db,
		Bucket: bucket,
		Port:   port,
		Host:   host,
		origin: origin,
	}
}
