package app

import (
	"net/http"

	"github.com/go-chi/render"
	"github.com/jekabolt/grbpwr-manager/store"
)

// errors

type ErrResponse struct {
	Err            error `json:"-"` // low-level runtime error
	HTTPStatusCode int   `json:"-"` // http response status code

	StatusText string `json:"status"`          // user-level status message
	AppCode    int64  `json:"code,omitempty"`  // application-specific error code
	ErrorText  string `json:"error,omitempty"` // application-level error message, for debugging
}

func (e *ErrResponse) Render(w http.ResponseWriter, r *http.Request) error {
	render.Status(r, e.HTTPStatusCode)
	return nil
}

func ErrInvalidRequest(err error) render.Renderer {
	return &ErrResponse{
		Err:            err,
		HTTPStatusCode: http.StatusBadRequest,
		StatusText:     "Invalid request.",
		ErrorText:      err.Error(),
	}
}

func ErrRender(err error) render.Renderer {
	return &ErrResponse{
		Err:            err,
		HTTPStatusCode: http.StatusUnprocessableEntity,
		StatusText:     "Error rendering response.",
		ErrorText:      err.Error(),
	}
}

func ErrInternalServerError(err error) render.Renderer {
	return &ErrResponse{
		Err:            err,
		HTTPStatusCode: http.StatusInternalServerError,
		StatusText:     http.StatusText(http.StatusInternalServerError),
		ErrorText:      err.Error(),
	}
}

func ErrUnauthorizedError(err error) render.Renderer {
	return &ErrResponse{
		Err:            err,
		HTTPStatusCode: http.StatusUnauthorized,
		StatusText:     http.StatusText(http.StatusUnauthorized),
		ErrorText:      err.Error(),
	}
}

var ErrNotFound = &ErrResponse{HTTPStatusCode: 404, StatusText: "Resource not found."}

// news article

type ArticleResponse struct {
	StatusCode  int                `json:"statusCode,omitempty"`
	NewsArticle *store.NewsArticle `json:"article,omitempty"`
}

func NewArticleResponse(article *store.NewsArticle, statusCode int) *ArticleResponse {
	resp := &ArticleResponse{NewsArticle: article, StatusCode: statusCode}
	return resp
}

func NewArticleResponseNoStatusCode(article *store.NewsArticle) *ArticleResponse {
	resp := &ArticleResponse{NewsArticle: article}
	return resp
}

func (rd *ArticleResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return rd.NewsArticle.Validate()
}

func NewArticleListResponse(articles []*store.NewsArticle) []render.Renderer {
	list := []render.Renderer{}
	for _, article := range articles {
		list = append(list, NewArticleResponseNoStatusCode(article))
	}
	return list
}

// collections

type CollectionResponse struct {
	StatusCode int               `json:"statusCode,omitempty"`
	Collection *store.Collection `json:"collection,omitempty"`
}

func NewCollectionResponse(collection *store.Collection, statusCode int) *CollectionResponse {
	resp := &CollectionResponse{Collection: collection, StatusCode: statusCode}
	return resp
}

func NewCollectionResponseNoStatusCode(collection *store.Collection) *CollectionResponse {
	resp := &CollectionResponse{Collection: collection}
	return resp
}

func (cr *CollectionResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return cr.Collection.Validate()
}

func NewCollectionListResponse(collections []*store.Collection) []render.Renderer {
	list := []render.Renderer{}
	for _, collection := range collections {
		list = append(list, NewCollectionResponseNoStatusCode(collection))
	}
	return list
}

// product

type ProductResponse struct {
	StatusCode int            `json:"statusCode,omitempty"`
	Product    *store.Product `json:"product,omitempty"`
}

func NewProductResponse(product *store.Product, statusCode int) *ProductResponse {
	resp := &ProductResponse{Product: product, StatusCode: statusCode}
	return resp
}

func NewProductResponseNoStatusCode(product *store.Product) *ProductResponse {
	resp := &ProductResponse{Product: product}
	return resp
}

func (rd *ProductResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func NewProductListResponse(products []*store.Product) []render.Renderer {
	list := []render.Renderer{}
	for _, product := range products {
		list = append(list, NewProductResponseNoStatusCode(product))
	}
	return list
}

// image

type ImageResponse struct {
	Status string `json:"status"`
	Url    string `json:"url"`
}

func NewImageResponse(status, url string) *ImageResponse {
	resp := &ImageResponse{Status: status, Url: url}
	return resp
}

func (i *ImageResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

// auth

type AuthResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func NewAuthResponse(ar *AuthResponse) *AuthResponse {
	return ar
}

func (i *AuthResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

// subscription

type SubscriptionResponse struct {
	StatusCode int               `json:"statusCode,omitempty"`
	Subscriber *store.Subscriber `json:"subscriber,omitempty"`
}

func NewSubscriptionResponse(statusCode int) *SubscriptionResponse {
	return &SubscriptionResponse{StatusCode: statusCode}
}

func NewSubscriptionResponseNoStatusCode(subscriber *store.Subscriber) *SubscriptionResponse {
	return &SubscriptionResponse{Subscriber: subscriber}
}

func NewSubscriptionResponseStatusCodeOnly(statusCode int) *SubscriptionResponse {
	return &SubscriptionResponse{StatusCode: statusCode}
}

func (sr *SubscriptionResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func NewSubscriptionsResponse(subscribers []*store.Subscriber) []render.Renderer {
	list := []render.Renderer{}
	for _, s := range subscribers {
		list = append(list, NewSubscriptionResponseNoStatusCode(s))
	}
	return list
}

// mainpage

type MainPageResponse struct {
	StatusCode int              `json:"statusCode,omitempty"`
	Hero       *store.Hero      `json:"hero,omitempty"`
	Products   []*store.Product `json:"products,omitempty"`
}

func NewMainPageResponse(h *store.Hero, products []*store.Product) *MainPageResponse {
	return &MainPageResponse{
		StatusCode: http.StatusOK,
		Hero:       h,
		Products:   products,
	}
}

func NewHeroUpdateResponse(h *store.Hero) *MainPageResponse {
	return &MainPageResponse{
		StatusCode: http.StatusOK,
		Hero:       h,
	}
}

func (mp *MainPageResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (h *MainPageResponse) Bind(r *http.Request) error {
	return nil
}
