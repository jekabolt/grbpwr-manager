package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/render"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog/log"
)

type ArchiveCtxKey struct{}

// admin panel
func (s *Server) addArchiveArticle(w http.ResponseWriter, r *http.Request) {
	data := &ArticleRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addArchiveArticle:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	// upload raw base64 images from request
	if !strings.Contains(data.MainImage, "https://") {
		mainUrl, err := s.Bucket.UploadImage(data.MainImage)
		if err != nil {
			log.Error().Err(err).Msgf("addArchiveArticle:s.Bucket.UploadImage [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		data.MainImage = mainUrl
	}

	if err := s.DB.AddArchiveArticle(data.ArchiveArticle); err != nil {
		log.Error().Err(err).Msgf("addArchiveArticle:s.DB.AddArchiveArticle [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}

	render.Render(w, r, NewArticleResponse(data.ArchiveArticle, http.StatusCreated))
}

func (s *Server) deleteArchiveArticleById(w http.ResponseWriter, r *http.Request) {
	cArticle, ok := r.Context().Value(ArchiveCtxKey{}).(*store.ArchiveArticle)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyArchiveArticleById:empty context")))
	}

	if err := s.DB.DeleteArchiveArticleById(fmt.Sprint(cArticle.Id)); err != nil {
		log.Error().Err(err).Msgf("deleteArchiveArticleById:s.DB.DeleteArchiveArticleById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	render.Render(w, r, NewArticleResponse(cArticle, http.StatusOK))
}

func (s *Server) modifyArchiveArticleById(w http.ResponseWriter, r *http.Request) {

	cArticle, ok := r.Context().Value(ArchiveCtxKey{}).(*store.ArchiveArticle)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyArchiveArticleById:empty context")))
	}

	data := &ArticleRequest{}

	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("modifyArchiveArticleById:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if err := s.DB.ModifyArchiveArticleById(fmt.Sprint(cArticle.Id), data.ArchiveArticle); err != nil {
		log.Error().Err(err).Msgf("modifyArchiveArticleById:s.DB.ModifyArchiveArticleById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	render.Render(w, r, NewArticleResponse(data.ArchiveArticle, http.StatusOK))
}

// site
func (s *Server) getAllArchiveArticlesList(w http.ResponseWriter, r *http.Request) {
	articles := []*store.ArchiveArticle{}
	var err error
	if articles, err = s.DB.GetAllArchiveArticles(); err != nil {
		log.Error().Err(err).Msgf("getAllArchiveArticlesList:s.DB.GetAllArchiveArticles [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	if err := render.RenderList(w, r, NewArticleListResponse(articles)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) getArchiveArticleById(w http.ResponseWriter, r *http.Request) {
	article := r.Context().Value(ArchiveCtxKey{}).(*store.ArchiveArticle)
	if err := render.Render(w, r, NewArticleResponse(article, http.StatusOK)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) ArchiveCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		var article *store.ArchiveArticle
		articleID := strings.TrimPrefix(r.URL.Path, "/api/archive/")

		if articleID != "" {
			article, err = s.DB.GetArchiveArticleById(articleID)
			if err != nil {
				log.Error().Msgf("s.DB.GetArchiveArticleById [%v]", err.Error())
				render.Render(w, r, ErrNotFound)
				return
			}
		}

		if articleID == "" {
			render.Render(w, r, ErrNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), ArchiveCtxKey{}, article)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ArticleRequest struct {
	*store.ArchiveArticle
}

func (a *ArticleRequest) Bind(r *http.Request) error {
	return a.ArchiveArticle.Validate()
}
