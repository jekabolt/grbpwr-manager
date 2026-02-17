package app

import (
	"context"
	"fmt"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/config"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/stripereconcile"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

var commitHash string

func getCommitHash() string {
	return commitHash
}

func SetCommitHash(hash string) {
	commitHash = hash
}

// App is the main application
type App struct {
	hs   *httpapi.Server
	db   dependency.Repository
	b    dependency.FileStore
	ma   dependency.Mailer
	oc   *ordercleanup.Worker
	sr   *stripereconcile.Worker
	re   dependency.RevalidationService
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
	slog.Default().InfoContext(ctx, "starting product manager")

	a.db, err = store.New(ctx, a.c.DB)
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't connect to mysql",
			slog.String("err", err.Error()),
		)
		return err
	}

	a.ma, err = mail.New(&a.c.Mailer, a.db.Mail())
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't connect to mailer",
			slog.String("err", err.Error()),
		)
		return err
	}
	err = a.ma.Start(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start mailer worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	a.oc = ordercleanup.New(&a.c.OrderCleanup, a.db)
	if err = a.oc.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start order cleanup worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	cache.SetDefaultCurrency(a.c.Rates.BaseCurrency)

	a.b, err = bucket.New(&a.c.Bucket, a.db.Media())
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't init bucket",
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("cannot init bucket %v", err.Error())
	}

	authS, err := auth.New(&a.c.Auth, a.db.Admin())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new auth server",
			slog.String("err", err.Error()),
		)
		return err
	}

	stripeMain, err := stripe.New(ctx, &a.c.StripePayment, a.db, a.ma, entity.CARD)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new stripe processor",
			slog.String("err", err.Error()),
		)
		return err
	}

	stripeTest, err := stripe.New(ctx, &a.c.StripePaymentTest, a.db, a.ma, entity.CARD_TEST)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new stripe processor",
			slog.String("err", err.Error()),
		)
		return err
	}

	// Stripe reconciliation: clean orphaned pre-order PaymentIntents (main + test)
	var stripeCleaners []stripereconcile.PreOrderPICleaner
	if p, ok := stripeMain.(*stripe.Processor); ok {
		stripeCleaners = append(stripeCleaners, p)
	}
	if p, ok := stripeTest.(*stripe.Processor); ok {
		stripeCleaners = append(stripeCleaners, p)
	}
	if len(stripeCleaners) > 0 {
		a.sr = stripereconcile.New(&a.c.StripeReconcile, stripeCleaners...)
		if err = a.sr.Start(ctx); err != nil {
			slog.Default().ErrorContext(ctx, "couldn't start stripe reconcile worker",
				slog.String("err", err.Error()),
			)
			return err
		}
	}

	a.re, err = revalidation.New(ctx, &a.c.Revalidation)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new revalidation service",
			slog.String("err", err.Error()),
		)
		return err
	}
	adminS := admin.New(a.db, a.b, a.ma, stripeMain, stripeTest, a.re)

	frontendS := frontend.New(a.db, a.ma, stripeMain, stripeTest, a.re)

	// start API server
	a.c.HTTP.CommitHash = getCommitHash()
	a.hs = httpapi.New(&a.c.HTTP)

	// Set up database health checker if store supports it
	if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
		healthChecker := httpapi.NewDatabaseHealthChecker(mysqlStore.Ping)
		a.hs.SetHealthChecker(healthChecker)
	}

	if err = a.hs.Start(ctx, adminS, frontendS, authS); err != nil {
		slog.Default().ErrorContext(ctx, "cannot start http server")
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
