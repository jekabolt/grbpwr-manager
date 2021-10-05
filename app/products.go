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

// admin panel
func (s *Server) addProduct(w http.ResponseWriter, r *http.Request) {
	data := &ProductRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addProduct:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	// upload raw base64 images from request
	if !strings.Contains(data.MainImage, "https://") {
		urls := []string{}
		for _, rawB64Image := range data.ProductImages {
			url, err := s.Bucket.UploadImage(rawB64Image)
			if err != nil {
				log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage [%v]", err.Error())
				render.Render(w, r, ErrInternalServerError(err))
				return
			}
			urls = append(urls, url)
		}
		data.ProductImages = urls

		mainUrl, err := s.Bucket.UploadImage(data.MainImage)
		if err != nil {
			log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage:main [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		data.MainImage = mainUrl
	}

	if err := s.DB.AddProduct(data.Product); err != nil {
		log.Error().Err(err).Msgf("addProduct:s.DB.AddProduct [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}
	render.Render(w, r, NewProductResponse(data.Product, http.StatusCreated))
}

func (s *Server) deleteProductById(w http.ResponseWriter, r *http.Request) {
	cProduct, ok := r.Context().Value("product").(*store.Product)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("deleteProductById:empty context")))
	}

	if err := s.DB.DeleteProductById(fmt.Sprint(cProduct.Id)); err != nil {
		log.Error().Err(err).Msgf("deleteProductById:s.DB.DeleteProductById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	render.Render(w, r, NewProductResponse(cProduct, http.StatusOK))
}

func (s *Server) modifyProductsById(w http.ResponseWriter, r *http.Request) {

	cProduct, ok := r.Context().Value("product").(*store.Product)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("modifyProductsById:empty context")))
	}

	data := &ProductRequest{}

	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("modifyProductsById:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if err := s.DB.ModifyProductById(fmt.Sprint(cProduct.Id), data.Product); err != nil {
		log.Error().Err(err).Msgf("modifyProductsById:s.DB.ModifyProductById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}

	render.Render(w, r, NewProductResponse(data.Product, http.StatusOK))
}

// site
func (s *Server) getAllProductsList(w http.ResponseWriter, r *http.Request) {

	products := []*store.Product{}
	var err error
	if products, err = s.DB.GetAllProducts(); err != nil {
		log.Error().Err(err).Msgf("getAllProductsList:s.DB.GetAllProducts [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	if err := render.RenderList(w, r, NewProductListResponse(products)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) getProductById(w http.ResponseWriter, r *http.Request) {
	product := r.Context().Value("product").(*store.Product)
	if err := render.Render(w, r, NewProductResponse(product, http.StatusOK)); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) ProductCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		var product *store.Product
		productId := strings.TrimPrefix(r.URL.Path, "/api/product/")

		if productId != "" {
			product, err = s.DB.GetProductById(productId)
			if err != nil {
				log.Error().Msgf("s.DB.GetProductById [%v]", err.Error())
				render.Render(w, r, ErrNotFound)
				return
			}
		}

		if productId == "" {
			render.Render(w, r, ErrNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), "product", product)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ProductRequest struct {
	*store.Product
}

func (p *ProductRequest) Bind(r *http.Request) error {
	return p.Validate()
}
