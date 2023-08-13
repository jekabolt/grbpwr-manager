package app

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/config"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"golang.org/x/exp/slog"
)

// App is the main application
type App struct {
	hs   *httpapi.Server
	db   dependency.Repository
	c    *config.Config
	done chan struct{}
}

// New returns a new instance of App
func New(c *config.Config, rep dependency.Repository) *App {
	return &App{
		c:    c,
		done: make(chan struct{}),
		db:   rep,
	}
}

// Start starts the app
func (a *App) Start(ctx context.Context) error {
	var err error
	slog.Default().InfoCtx(ctx, "starting product manager")

	a.db, err = store.New(ctx, a.c.DB)
	if err != nil {
		slog.Default().With(err).ErrorCtx(ctx, "couldn't connect to mysql")
		return err
	}
	authS, err := auth.New(&a.c.Auth, a.db.Admin())
	if err != nil {
		slog.Default().With(err).ErrorCtx(ctx, "failed create new auth server")
		return err
	}

	adminS := admin.New(a.db)

	frontendS := frontend.New(a.db)

	// start API server
	a.hs = httpapi.New(&a.c.HTTP)
	if err = a.hs.Start(ctx, adminS, frontendS, authS); err != nil {
		slog.Default().With(err).ErrorCtx(ctx, "cannot start http server")
		return err
	}

	return nil
}

// Stop stops the application and waits for all services to exit
func (a *App) Stop(ctx context.Context) {

	a.db.Close()
	close(a.done)
}

// Done returns a channel that is closed after the application has exited
func (a *App) Done() chan struct{} {
	return a.done
}
