package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/render"
	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/jekabolt/grbpwr-manager/store"
	"github.com/rs/zerolog/log"
)

type ProductCtxKey struct{}

// admin panel
func (s *Server) addProduct(w http.ResponseWriter, r *http.Request) {
	data := &ProductRequest{}
	if err := render.Bind(r, data); err != nil {
		log.Error().Err(err).Msgf("addProduct:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	// upload raw base64 images from request
	if !strings.Contains(data.MainImage.FullSize, "https://") {
		productImages := []bucket.Image{}
		for _, rawB64Image := range data.ProductImages {
			prdImg, err := s.Bucket.UploadProductImage(rawB64Image.FullSize)
			if err != nil {
				log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage [%v]", err.Error())
				render.Render(w, r, ErrInternalServerError(err))
				return
			}
			productImages = append(productImages, *prdImg)
		}
		data.ProductImages = productImages

		mainImage, err := s.Bucket.UploadProductMainImage(data.MainImage.FullSize)
		if err != nil {
			log.Error().Err(err).Msgf("addProduct:s.Bucket.UploadImage:main [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		data.MainImage = *mainImage
	}

	if _, err := s.DB.AddProduct(data.Product); err != nil {
		log.Error().Err(err).Msgf("addProduct:s.DB.AddProduct [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
	}
	render.Render(w, r, NewProductResponse(data.Product))
}

func (s *Server) deleteProductById(w http.ResponseWriter, r *http.Request) {
	cProduct, ok := r.Context().Value(ProductCtxKey{}).(*store.Product)
	if !ok {
		render.Render(w, r, ErrInvalidRequest(fmt.Errorf("deleteProductById:empty context")))
	}

	if err := s.DB.DeleteProductById(fmt.Sprint(cProduct.Id)); err != nil {
		log.Error().Err(err).Msgf("deleteProductById:s.DB.DeleteProductById [%v]", err.Error())
		render.Render(w, r, ErrInternalServerError(err))
		return
	}
	render.Render(w, r, NewProductResponse(cProduct))
}

func (s *Server) modifyProductsById(w http.ResponseWriter, r *http.Request) {

	cProduct, ok := r.Context().Value(ProductCtxKey{}).(*store.Product)
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

	render.Render(w, r, NewProductResponse(data.Product))
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
	if err := render.RenderList(w, r, NewProductListResponse(store.BulkProductPreview(products))); err != nil {
		render.Render(w, r, ErrRender(err))
		return
	}
}

func (s *Server) getProductById(w http.ResponseWriter, r *http.Request) {
	product := r.Context().Value(ProductCtxKey{}).(*store.Product)
	if err := render.Render(w, r, NewProductResponse(product)); err != nil {
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

		ctx := context.WithValue(r.Context(), ProductCtxKey{}, product)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ProductRequest struct {
	*store.Product
}

func (p *ProductRequest) Bind(r *http.Request) error {
	return p.Validate()
}
