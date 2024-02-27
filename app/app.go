package app

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/config"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/payment/trongrid"
	"github.com/jekabolt/grbpwr-manager/internal/payment/usdt"
	"github.com/jekabolt/grbpwr-manager/internal/rates"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"golang.org/x/exp/slog"
)

// App is the main application
type App struct {
	hs   *httpapi.Server
	db   dependency.Repository
	b    dependency.FileStore
	ma   dependency.Mailer
	r    dependency.RatesService
	c    *config.Config
	done chan struct{}
}

// New returns a new instance of App
func New(c *config.Config) *App {
	return &App{
		c:    c,
		done: make(chan struct{}),
	}
}

// Start starts the app
func (a *App) Start(ctx context.Context) error {
	var err error
	slog.Default().InfoCtx(ctx, "starting product manager")

	a.db, err = store.New(ctx, a.c.DB)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "couldn't connect to mysql")
		return err
	}

	a.ma, err = mail.New(&a.c.Mailer, a.db.Mail())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "couldn't connect to mailer")
		return err
	}
	err = a.ma.Start(ctx)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "couldn't start mailer worker")
		return err
	}

	a.r = rates.New(&a.c.Rates, a.db.Rates())
	err = a.r.Start()
	if err != nil {
		slog.Default().ErrorCtx(ctx, "couldn't start rates worker")
		return err
	}

	a.b, err = bucket.New(&a.c.Bucket, a.db.Media())
	if err != nil {
		return fmt.Errorf("cannot init bucket %v", err.Error())
	}

	authS, err := auth.New(&a.c.Auth, a.db.Admin())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "failed create new auth server")
		return err
	}

	tg := trongrid.New(&a.c.Trongrid)

	usdtTron, err := usdt.New(ctx, &a.c.USDTTronPayment, a.db, a.ma, tg)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "failed create new usdt tron processor")
		return err
	}

	adminS := admin.New(a.db, a.b, usdtTron)

	frontendS := frontend.New(a.db, a.ma, a.r, usdtTron)

	// start API server
	a.hs = httpapi.New(&a.c.HTTP)
	if err = a.hs.Start(ctx, adminS, frontendS, authS); err != nil {
		slog.Default().ErrorCtx(ctx, "cannot start http server")
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
