package app

import (
	"net/http"

	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
)

type Server struct {
	DB     store.ProductStore
	Bucket *bucket.Bucket

	Port   string
	Host   string
	Origin string
	Debug  bool
}

func InitServer(db store.ProductStore, bucket *bucket.Bucket, port, host, origin string, debug bool) *Server {
	return &Server{
		DB:     db,
		Bucket: bucket,
		Port:   port,
		Host:   host,
		Origin: origin,
		Debug:  debug,
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
