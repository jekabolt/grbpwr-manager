package app

import (
	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB      store.ProductStore
	Bucket  *bucket.Bucket
	JWTAuth *jwtauth.JWTAuth

	Port        string
	Hosts       []string
	AdminSecret string

	Debug bool
}

func InitServer(db store.ProductStore, bucket *bucket.Bucket, port, jwtSecret, adminSecret string, hosts []string, debug bool) *Server {
	return &Server{
		DB:          db,
		Bucket:      bucket,
		Port:        port,
		Hosts:       hosts,
		AdminSecret: adminSecret,
		JWTAuth:     jwtauth.New("HS256", []byte(jwtSecret), nil),
		Debug:       debug,
	}
}
