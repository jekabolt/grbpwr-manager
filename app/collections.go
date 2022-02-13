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

type CollectionsCtxKey struct{}

// admin panel
func (s *Server) addCollection(w http.ResponseWriter, r *http.Request) {
	data := &CollectionRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addCollection:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if err := data.Validate(); err != nil {
		log.Error().Err(err).Msgf("addCollection:render.Validate [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	// upload raw base64 images from request
	if !strings.Contains(data.MainImage.FullSize, "https://") {
		for i, c := range data.Article.Content {
			contentImg, err := s.Bucket.UploadContentImage(c.Image.FullSize)
			if err != nil {
				log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage [%v]", err.Error())
				render.Render(w, r, ErrInternalServerError(err))
				return
			}
			data.Article.Content[i].Image = contentImg
		}

		mainImage, err := s.Bucket.UploadNewsMainImage(data.MainImage.FullSize)
		if err != nil {
			log.Error().Err(err).Msgf("addCollection:s.Bucket.UploadImage [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		data.MainImage = mainImage
	}

	if _, err := s.DB.AddCollection(data.Collection); err != nil {
		log.Error().Err(err).Msgf("addCollection:s.DB.AddCollection [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}

	render.Render(w, r, NewCollectionResponse(data.Collection))
}

func (s *Server) deleteCollectionBySeason(w http.ResponseWriter, r *http.Request) {
	cCollection, ok := r.Context().Value(CollectionsCtxKey{}).(*store.Collection)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyCollectionById:empty context")))
	}

	if err := s.DB.DeleteCollectionBySeason(fmt.Sprint(cCollection.Season)); err != nil {
		log.Error().Err(err).Msgf("deleteCollectionById:s.DB.DeleteCollectionById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	render.Render(w, r, NewCollectionResponse(cCollection))
}

func (s *Server) modifyCollectionBySeason(w http.ResponseWriter, r *http.Request) {
	cArticle, ok := r.Context().Value(CollectionsCtxKey{}).(*store.Collection)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyCollectionById:empty context")))
	}

	data := &CollectionRequest{}

	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("modifyCollectionById:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if err := s.DB.ModifyCollectionBySeason(fmt.Sprint(cArticle.Season), data.Collection); err != nil {
		log.Error().Err(err).Msgf("modifyCollectionById:s.DB.ModifyCollectionById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	render.Render(w, r, NewCollectionResponse(data.Collection))
}

// site
func (s *Server) getAllCollectionsList(w http.ResponseWriter, r *http.Request) {
	collections := []*store.Collection{}
	var err error
	if collections, err = s.DB.GetAllCollections(); err != nil {
		log.Error().Err(err).Msgf("getAllCollectionsList:s.DB.GetAllCollections [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	if err := render.RenderList(w, r, NewCollectionListResponse(store.BulkCollectionPreview(collections))); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) getCollectionBySeason(w http.ResponseWriter, r *http.Request) {
	collection := r.Context().Value(CollectionsCtxKey{}).(*store.Collection)
	if err := render.Render(w, r, NewCollectionResponse(collection)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) CollectionsCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		var article *store.Collection
		season := strings.TrimPrefix(r.URL.Path, "/api/collections/")

		if season != "" {
			article, err = s.DB.GetCollectionBySeason(season)
			if err != nil {
				log.Error().Msgf("s.DB.GetCollectionById [%v]", err.Error())
				render.Render(w, r, ErrNotFound)
				return
			}
		}

		if season == "" {
			render.Render(w, r, ErrNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), CollectionsCtxKey{}, article)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type CollectionRequest struct {
	*store.Collection
}

func (a *CollectionRequest) Bind(r *http.Request) error {
	return a.Collection.Validate()
}
