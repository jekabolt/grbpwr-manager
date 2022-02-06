package app

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/go-chi/jwtauth/v5"
	"github.com/go-chi/render"
	"github.com/rs/zerolog/log"
)

func (s *Server) Router() *chi.Mux {
	r := chi.NewRouter()

	cors := cors.New(cors.Options{
		AllowedOrigins: s.Config.Hosts,
		AllowedMethods: []string{
			http.MethodHead,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodOptions,
			http.MethodDelete,
		},
		Debug: s.Config.Debug,
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
		10,             // requests
		15*time.Second, // per duration
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		}),
	))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	r.Post("/auth", s.auth)

	r.Route("/api", func(r chi.Router) {

		r.Route("/archive/{id}", func(r chi.Router) {
			r.Use(s.ArchiveCtx)

			// with jwt auth
			r.Group(func(r chi.Router) {

				r.Use(jwtauth.Verifier(s.Auth.JWTAuth))
				r.Use(s.Authenticator)

				r.Put("/", s.modifyArchiveArticleById)
				r.Delete("/", s.deleteArchiveArticleById)
			})

			r.Get("/", s.getArchiveArticleById) // public

		})

		// with jwt auth
		r.Group(func(r chi.Router) {

			r.Use(jwtauth.Verifier(s.Auth.JWTAuth))
			r.Use(s.Authenticator)

			r.Post("/archive", s.addArchiveArticle)
			r.Post("/product", s.addProduct)
			r.Post("/image", s.uploadImage)

		})

		r.Route("/product/{id}", func(r chi.Router) {
			r.Use(s.ProductCtx)

			// with jwt auth
			r.Group(func(r chi.Router) {

				r.Use(jwtauth.Verifier(s.Auth.JWTAuth))
				r.Use(s.Authenticator)

				r.Put("/", s.modifyProductsById)
				r.Delete("/", s.deleteProductById)

			})

			r.Get("/", s.getProductById) // public

		})

		r.Group(func(r chi.Router) {
			r.Use(jwtauth.Verifier(s.Auth.JWTAuth))
			r.Use(s.Authenticator)

			r.Get("/subscribe", s.getAllSubscribers)
			r.Delete("/subscribe/{emailB64}", s.deleteSubscriberByEmail)

			r.Post("/main", s.updateMainPage)
		})

		r.Get("/archive", s.getAllArchiveArticlesList) // public
		r.Get("/product", s.getAllProductsList)        // public
		r.Get("/main", s.getMainPage)                  // public
		r.Post("/subscribe", s.subscribeNewsletter)    // public

	})

	return r
}

func (s *Server) Serve() error {
	log.Info().Msg("Listening on :" + s.Config.Port)
	return http.ListenAndServe(":"+s.Config.Port, s.Router())
}
