package app

import (
	"net/http"

	"github.com/go-chi/render"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog/log"
)

// site
func (s *Server) getMainPage(w http.ResponseWriter, r *http.Request) {
	products := []*store.Product{}
	hero := &store.Hero{}
	var err error
	if products, err = s.DB.GetAllProducts(); err != nil {
		log.Error().Err(err).Msgf("getAllProductsList:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	if hero, err = s.DB.GetHero(); err != nil {
		log.Error().Err(err).Msgf("getAllProductsList:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	if err := render.Render(w, r, NewMainPageResponse(hero, products)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) updateMainPage(w http.ResponseWriter, r *http.Request) {
	data := &store.Hero{}
	var err error

	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addProduct:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if data, err = s.DB.UpsertHero(data); err != nil {
		log.Error().Err(err).Msgf("getAllProductsList:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	if err := render.Render(w, r, NewHeroUpdateResponse(data)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}
