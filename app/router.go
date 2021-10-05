package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/go-chi/docgen"
	"github.com/go-chi/httprate"
	"github.com/go-chi/render"
	"github.com/rs/zerolog/log"
)

func (s *Server) Serve() error {
	r := chi.NewRouter()

	cors := cors.New(cors.Options{
		AllowedOrigins: []string{s.Origin},
		AllowedMethods: []string{
			http.MethodHead,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodOptions,
			http.MethodDelete,
		},
		OptionsPassthrough: true,

		Debug: s.Debug,
	})

	// Init middlewares
	r.Use(cors.Handler)
	r.Use(middleware.Logger)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(render.SetContentType(render.ContentTypeJSON))

	r.Use(httprate.Limit(
		7,              // requests
		15*time.Second, // per duration
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		}),
	))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	r.Options("/*", s.handleOptions)

	if s.Debug {
		fmt.Println(docgen.JSONRoutesDoc(r))
	}

	r.Route("/api", func(r chi.Router) {
		r.Route("/archive/{id}", func(sr chi.Router) {
			sr.Use(s.ArchiveCtx)
			sr.Get("/", s.getArchiveArticleById)
			sr.Put("/", s.modifyArchiveArticleById)
			sr.Delete("/", s.deleteArchiveArticleById)
		})
		r.Get("/archive", s.getAllArchiveArticlesList)
		r.Post("/archive", s.addArchiveArticle)

		r.Route("/product/{id}", func(sr chi.Router) {
			sr.Use(s.ArchiveCtx)
			sr.Get("/", s.getProductById)
			sr.Put("/", s.modifyProductsById)
			sr.Delete("/", s.deleteProductById)
		})
		r.Get("/product", s.getAllProductsList)
		r.Post("/product", s.addProduct)

		r.Post("/image", s.uploadImage)
	})

	// r.Group()

	// r.Route("/api", func(sr chi.Router) {

	// 	r.Put("/product", s.modifyProductsById)
	// 	r.Delete("/product", s.deleteProductById)

	// 	r.Put("/article", s.deleteArchiveArticleById)
	// 	r.Delete("/article", s.getArchiveArticleById)

	// 	r.Post("/product", s.addProduct)

	// 	r.Post("/article", s.addArchiveArticle)

	// 	r.Post("/image", s.uploadImage)

	// 	// r.Get("/", rs.Get)       // GET /posts/{id} - Read a single post by :id.
	// 	// r.Put("/", rs.Update)    // PUT /posts/{id} - Update a single post by :id.
	// 	// r.Delete("/", rs.Delete) // DELETE /posts/{id} - Delete a single post by :id.
	// })

	// r.Get("/product", s.getAllProductsList)
	// r.Get("/product/{id}", s.getProductById)

	// r.Get("/article", s.modifyArchiveArticleById)
	// r.Get("/article/{id}", s.getAllArchiveArticlesList)

	// log.Info().Msg("Listening on :" + s.Port)

	// }

	return http.ListenAndServe(":"+s.Port, r)
}

func PostCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Fatal().Msg("kek")
		ctx := context.WithValue(r.Context(), "id", chi.URLParam(r, "id"))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
