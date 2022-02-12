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

type NewsCtxKey struct{}

// admin panel
func (s *Server) addNewsArticle(w http.ResponseWriter, r *http.Request) {
	data := &NewsArticleRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addNewsArticle:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	// upload raw base64 images from request
	if !strings.Contains(data.MainImage.FullSize, "https://") {
		for i, c := range data.Content {
			contentImg, err := s.Bucket.UploadContentImage(c.Image.FullSize)
			if err != nil {
				log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage [%v]", err.Error())
				render.Render(w, r, ErrInternalServerError(err))
				return
			}
			data.Content[i].Image = contentImg
		}

		mainImage, err := s.Bucket.UploadNewsMainImage(data.MainImage.FullSize)
		if err != nil {
			log.Error().Err(err).Msgf("addNewsArticle:s.Bucket.UploadImage [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		data.MainImage = *mainImage
	}

	if _, err := s.DB.AddNewsArticle(data.NewsArticle); err != nil {
		log.Error().Err(err).Msgf("addNewsArticle:s.DB.AddNewsArticle [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}

	render.Render(w, r, NewArticleResponse(data.NewsArticle))
}

func (s *Server) deleteNewsArticleById(w http.ResponseWriter, r *http.Request) {
	cArticle, ok := r.Context().Value(NewsCtxKey{}).(*store.NewsArticle)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyNewsArticleById:empty context")))
	}

	if err := s.DB.DeleteNewsArticleById(fmt.Sprint(cArticle.Id)); err != nil {
		log.Error().Err(err).Msgf("deleteNewsArticleById:s.DB.DeleteNewsArticleById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	render.Render(w, r, NewArticleResponse(cArticle))
}

func (s *Server) modifyNewsArticleById(w http.ResponseWriter, r *http.Request) {

	cArticle, ok := r.Context().Value(NewsCtxKey{}).(*store.NewsArticle)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyNewsArticleById:empty context")))
	}

	data := &NewsArticleRequest{}

	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("modifyNewsArticleById:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if err := s.DB.ModifyNewsArticleById(fmt.Sprint(cArticle.Id), data.NewsArticle); err != nil {
		log.Error().Err(err).Msgf("modifyNewsArticleById:s.DB.ModifyNewsArticleById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	render.Render(w, r, NewArticleResponse(data.NewsArticle))
}

// site
func (s *Server) getAllNewsArticlesList(w http.ResponseWriter, r *http.Request) {
	articles := []*store.NewsArticle{}
	var err error
	if articles, err = s.DB.GetAllNewsArticles(); err != nil {
		log.Error().Err(err).Msgf("getAllNewsArticlesList:s.DB.GetAllNewsArticles [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	if err := render.RenderList(w, r, NewArticleListResponse(articles)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) getNewsArticleById(w http.ResponseWriter, r *http.Request) {
	article := r.Context().Value(NewsCtxKey{}).(*store.NewsArticle)
	if err := render.Render(w, r, NewArticleResponse(article)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) NewsCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		var article *store.NewsArticle
		articleID := strings.TrimPrefix(r.URL.Path, "/api/news/")

		if articleID != "" {
			article, err = s.DB.GetNewsArticleById(articleID)
			if err != nil {
				log.Error().Msgf("s.DB.GetNewsArticleById [%v]", err.Error())
				render.Render(w, r, ErrNotFound)
				return
			}
		}

		if articleID == "" {
			render.Render(w, r, ErrNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), NewsCtxKey{}, article)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type NewsArticleRequest struct {
	*store.NewsArticle
}

func (a *NewsArticleRequest) Bind(r *http.Request) error {
	return a.NewsArticle.Validate()
}
