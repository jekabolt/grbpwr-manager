package app

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/rs/zerolog/log"
)

func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	bs, err := ioutil.ReadAll(r.Body)

	if err != nil {
		log.Error().Err(err).Msgf("uploadImage:ioutil.ReadAll [%v]", err.Error())
		err := map[string]interface{}{"uploadImage:ioutil.ReadAll": err}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(err)
		return
	}

	contentType := r.Header.Get("Content-Type")

	url, err := s.Bucket.UploadImage(bs, contentType)
	if err != nil {
		log.Error().Msgf("uploadImages.Bucket.UploadImage [%v]", err)
		err := map[string]interface{}{"uploadImages.Bucket.UploadImage": err.Error()}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(err)
		return
	}

	resp := map[string]interface{}{
		"status": http.StatusText(http.StatusCreated),
		"url":    url}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
