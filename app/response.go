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

var ErrNotFound = &ErrResponse{HTTPStatusCode: 404, StatusText: "Resource not found."}

// archive article

type ArticleResponse struct {
	StatusCode     int                   `json:"statusCode,omitempty"`
	ArchiveArticle *store.ArchiveArticle `json:"article,omitempty"`
}

func NewArticleResponse(article *store.ArchiveArticle, statusCode int) *ArticleResponse {
	resp := &ArticleResponse{ArchiveArticle: article, StatusCode: statusCode}
	return resp
}

func NewArticleResponseNoStatusCode(article *store.ArchiveArticle) *ArticleResponse {
	resp := &ArticleResponse{ArchiveArticle: article}
	return resp
}

func (rd *ArticleResponse) Render(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func NewArticleListResponse(articles []*store.ArchiveArticle) []render.Renderer {
	list := []render.Renderer{}
	for _, article := range articles {
		list = append(list, NewArticleResponseNoStatusCode(article))
	}
	return list
}

// product

type ProductResponse struct {
	StatusCode int            `json:"statusCode,omitempty"`
	Product    *store.Product `json:"product,omitempty"`
}

func NewProductResponse(article *store.Product, statusCode int) *ProductResponse {
	resp := &ProductResponse{Product: article, StatusCode: statusCode}
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
