package app

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/go-chi/render"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog/log"
)

type NewsletterSubscribeRequest struct {
	*store.Subscriber
}

func (s *Server) subscribeNewsletter(w http.ResponseWriter, r *http.Request) {
	data := &NewsletterSubscribeRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("subscribeNewsletter:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if _, err := s.DB.AddSubscriber(data.Subscriber); err != nil {
		log.Error().Err(err).Msgf("subscribeNewsletter:s.DB.AddSubscriber [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}
	render.Render(w, r, NewSubscriptionResponseStatusCodeOnly(http.StatusCreated))
}

func (s *Server) getAllSubscribers(w http.ResponseWriter, r *http.Request) {
	subscribers := []*store.Subscriber{}
	var err error
	if subscribers, err = s.DB.GetAllSubscribers(); err != nil {
		log.Error().Err(err).Msgf("getAllSubscribers:s.DB.GetAllSubscribers [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	if err := render.RenderList(w, r, NewSubscriptionsResponse(subscribers)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) deleteSubscriberByEmail(w http.ResponseWriter, r *http.Request) {
	emailB64 := strings.TrimPrefix(r.URL.Path, "/api/subscribe/")
	emailDecoded, err := base64.StdEncoding.DecodeString(emailB64)
	if err != nil {
		log.Error().Err(err).Msgf("deleteSubscriberByEmail:base64.StdEncoding.DecodeString [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	email := string(emailDecoded)

	err = s.DB.DeleteSubscriberByEmail(email)
	if err != nil {
		log.Error().Msgf("deleteSubscriberByEmail:s.DB.DeleteSubscriberByEmail [%s] [%s]", email, err.Error())
		render.Render(w, r, ErrNotFound)
		return
	}
	render.Render(w, r, NewSubscriptionResponseStatusCodeOnly(http.StatusOK))
}

func (p *NewsletterSubscribeRequest) Bind(r *http.Request) error {
	return p.Validate()
}
