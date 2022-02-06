package app

import (
	"net/http"
	"strings"

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
		log.Error().Err(err).Msgf("getMainPage:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	if hero, err = s.DB.GetHero(); err != nil {
		log.Error().Err(err).Msgf("getMainPage:s.DB.GetHero [%v]", err.Error())
		if strings.Contains(err.Error(), "not found") {
			//use default
			if err := render.Render(w, r, NewMainPageResponse(&store.Hero{
				ContentLink: s.Config.Hero.ContentLink,
				ContentType: s.Config.Hero.ContentType,
				ExploreLink: s.Config.Hero.ExploreLink,
				ExploreText: s.Config.Hero.ExploreText,
			}, products)); err != nil {
				render.Render(w, r, ErrRender(err))
				return
			}
			return
		}
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
		log.Error().Err(err).Msgf("updateMainPage:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if data, err = s.DB.UpsertHero(data); err != nil {
		log.Error().Err(err).Msgf("updateMainPage:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	if err := render.Render(w, r, NewHeroUpdateResponse(data)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}
