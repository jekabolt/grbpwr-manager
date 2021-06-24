package app

import (
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog/log"
)

// admin panel
func (s *Server) addProduct(w http.ResponseWriter, r *http.Request) {
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func (s *Server) deleteProductById(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func (s *Server) getProductsById(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func (s *Server) modifyProductsById(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

// site
func (s *Server) getProductsListByCategory(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func (s *Server) getAllProductsList(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}

func (s *Server) getFeaturedProducts(w http.ResponseWriter, r *http.Request) {
	// h := chi.URLParam(r, "hash")
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Can't read body")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	ttl, err := strconv.ParseInt(r.Header.Get("TTL"), 10, 64)
	if err != nil {
		ttl = s.TTL
	}
	log.Debug().Int64("ttl", ttl).Msg("Using ttl")

	record, err := store.GetShort(s.DB, b, s.MinShortLen, ttl)
	if err != nil {
		log.Error().Err(err).Msg("Can't get short link")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	_, err = w.Write([]byte(record))
	if err != nil {
		log.Error().Err(err).Msg("Can't Write resp")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
}
