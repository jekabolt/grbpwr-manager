package app

import (
	"net/http"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB      store.ProductStore
	Bucket  *bucket.Bucket
	JWTAuth *jwtauth.JWTAuth

	Port        string
	Host        string
	Origin      string
	AdminSecret string

	Debug bool
}

func InitServer(db store.ProductStore, bucket *bucket.Bucket, port, host, origin, jwtSecret, adminSecret string, debug bool) *Server {

	return &Server{
		DB:          db,
		Bucket:      bucket,
		Port:        port,
		Host:        host,
		Origin:      origin,
		AdminSecret: adminSecret,
		JWTAuth:     jwtauth.New("HS256", []byte(jwtSecret), nil),
		Debug:       debug,
	}
}

func (s *Server) setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
	w.Header().Set("Access-Control-Allow-Origin", s.Origin)
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	s.setCORSHeaders(w)
}
