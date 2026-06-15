package app

import (
	"context"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/config"
	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4sync"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/stockreserve"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/storefrontcleanup"
	"github.com/jekabolt/grbpwr-manager/internal/stripereconcile"
	"github.com/jekabolt/grbpwr-manager/internal/tiermanagement"
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
	sc   *storefrontcleanup.Worker
	tm   *tiermanagement.Worker
	sr   *stripereconcile.Worker
	ga4w *ga4sync.Worker
	bqc  dependency.BQClient
	re   dependency.RevalidationService
	rm   *stockreserve.Manager
	// Stripe processors (live + test). Held so their in-process payment monitors
	// can be stopped on shutdown before the DB is closed.
	stripeMain *stripe.Processor
	stripeTest *stripe.Processor
	c          *config.Config
	done       chan struct{}
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

	reservationMgr := stockreserve.NewDefaultManager()
	a.rm = reservationMgr
	// NOTE: the order cleanup worker is created later, after the Stripe
	// processors exist, so its safety-net expiry can verify payment with Stripe.

	a.sc = storefrontcleanup.New(&a.c.StorefrontCleanup, a.db)
	if err = a.sc.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start storefront cleanup worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	a.tm = tiermanagement.New(&a.c.TierManagement, a.db, a.ma)
	if err = a.tm.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start tier management worker",
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

	// Hold the concrete processors so App.Stop can stop their in-process payment
	// monitors before the DB is closed.
	if p, ok := stripeMain.(*stripe.Processor); ok {
		a.stripeMain = p
	}
	if p, ok := stripeTest.(*stripe.Processor); ok {
		a.stripeTest = p
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

	// Order cleanup safety-net: route expired card orders through the Stripe
	// processors so a succeeded-but-unrecorded payment is confirmed instead of
	// cancelled. Wired here so it can verify payment status with Stripe.
	expirer := &stripeOrderExpirer{repo: a.db}
	if p, ok := stripeMain.(*stripe.Processor); ok {
		expirer.main = p
	}
	if p, ok := stripeTest.(*stripe.Processor); ok {
		expirer.test = p
	}
	a.oc = ordercleanup.New(&a.c.OrderCleanup, a.db, reservationMgr, expirer)
	if err = a.oc.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start order cleanup worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	// Revalidation (Vercel ISR) is a non-critical, best-effort cache-freshness
	// side effect. If its client can't be constructed, log and continue with a
	// no-op revalidator instead of crash-looping the whole process — the
	// storefront/admin must still boot and serve.
	if rev, revErr := revalidation.New(ctx, &a.c.Revalidation); revErr != nil {
		slog.Default().WarnContext(ctx, "failed to create revalidation service; continuing with revalidation disabled",
			slog.String("err", revErr.Error()),
		)
		a.re = revalidation.NewDisabled()
	} else {
		a.re = rev
	}

	// GA4 Analytics integration
	ga4Client, err := ga4.NewClient(ctx, &a.c.GA4)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new ga4 client",
			slog.String("err", err.Error()),
		)
		return err
	}

	// BigQuery client (optional — disabled when not configured)
	a.bqc, err = bq.NewClient(ctx, &a.c.BigQuery)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create bigquery client",
			slog.String("err", err.Error()),
		)
		return err
	}

	// GA4 sync worker (only if GA4 is enabled)
	if a.c.GA4.Enabled {
		if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
			a.ga4w = ga4sync.New(ga4Client, a.bqc, mysqlStore.GA4Data(), mysqlStore.BQCache(), mysqlStore.SyncStatus(), &a.c.GA4Sync)
			if err = a.ga4w.Start(ctx); err != nil {
				slog.Default().ErrorContext(ctx, "couldn't start ga4 sync worker",
					slog.String("err", err.Error()),
				)
				return err
			}
			slog.Default().InfoContext(ctx, "ga4 sync worker started")
		}
	}

	// GA4 Measurement Protocol client for server-side event tracking
	ga4mpClient := ga4mp.New(&a.c.GA4MP)

	if p, ok := stripeMain.(*stripe.Processor); ok {
		p.SetGA4MP(ga4mpClient)
	}
	if p, ok := stripeTest.(*stripe.Processor); ok {
		p.SetGA4MP(ga4mpClient)
	}

	adminS := admin.New(a.db, a.b, a.ma, stripeMain, stripeTest, a.re, reservationMgr, ga4mpClient)

	var frontendS *frontend.Server
	frontendS, err = frontend.New(a.db, a.ma, stripeMain, stripeTest, a.re, reservationMgr, &a.c.StorefrontAuth)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create frontend server",
			slog.String("err", err.Error()),
		)
		return err
	}

	// start API server
	a.c.HTTP.CommitHash = getCommitHash()
	a.hs = httpapi.New(&a.c.HTTP)

	// Set up database health checker if store supports it
	if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
		healthChecker := httpapi.NewDatabaseHealthChecker(mysqlStore.Ping)
		a.hs.SetHealthChecker(healthChecker)
	}

	// Set up Resend webhook handler (bounce/complaint suppression + list-unsubscribe)
	webhookHandler, err := mail.NewWebhookHandler(a.db, a.c.Mailer.WebhookSecret)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create webhook handler",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.hs.SetWebhookHandler(webhookHandler)

	// Stripe webhook: server-to-server payment confirmation (primary path; the
	// in-process monitor and lazy CheckForTransactions remain fallbacks). Enabled
	// only when a signing secret is configured for at least one processor.
	var stripeProcs []*stripe.Processor
	if p, ok := stripeMain.(*stripe.Processor); ok {
		stripeProcs = append(stripeProcs, p)
	}
	if p, ok := stripeTest.(*stripe.Processor); ok {
		stripeProcs = append(stripeProcs, p)
	}
	if stripeWebhook := stripe.NewWebhookHandler(stripeProcs...); stripeWebhook.Enabled() {
		a.hs.SetStripeWebhookHandler(stripeWebhook)
		slog.Default().InfoContext(ctx, "stripe webhook handler enabled")
	} else {
		slog.Default().InfoContext(ctx, "stripe webhook handler disabled (no signing secret configured)")
	}

	// Operational status registry for the admin-gated GET /statusz endpoint.
	// Each worker implements health.Reporter (records last-success at the end of a
	// clean tick); the store provides DB pool stats; the GA4/BQ clients expose
	// their circuit-breaker state. nil entries (e.g. ga4 worker when GA4 is off)
	// are skipped so the endpoint reflects what is actually running.
	a.hs.SetHealthRegistry(a.buildHealthRegistry(ga4Client))

	if err = a.hs.Start(ctx, adminS, frontendS, authS); err != nil {
		slog.Default().ErrorContext(ctx, "cannot start http server")
		return err
	}

	return nil
}

// Stop stops the application and waits for all services to exit.
// Shutdown order: drain the API server first (so no new request reaches a worker
// or the DB), then stop the workers, then close the database.
func (a *App) Stop(ctx context.Context) {
	// Drain in-flight gRPC/REST requests and stop the listener before tearing
	// anything down, so handlers don't race against stopped workers or a closed
	// connection pool.
	if a.hs != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		if err := a.hs.Shutdown(shutdownCtx); err != nil {
			slog.Default().ErrorContext(ctx, "error draining http server on shutdown",
				slog.String("err", err.Error()),
			)
		}
		cancel()
	}

	// Stop workers before closing DB — avoids panics and error storms from workers
	// hitting a closed connection. In-flight emails remain in DB and will be retried on next run.
	if a.ma != nil {
		_ = a.ma.Stop()
	}
	if a.oc != nil {
		_ = a.oc.Stop()
	}
	if a.sc != nil {
		_ = a.sc.Stop()
	}
	if a.tm != nil {
		_ = a.tm.Stop()
	}
	if a.sr != nil {
		_ = a.sr.Stop()
	}
	if a.ga4w != nil {
		_ = a.ga4w.Stop()
	}

	// Stop the in-memory stock reservation manager's cleanup goroutine.
	if a.rm != nil {
		a.rm.Stop()
	}

	// Stop the in-process Stripe payment monitors AFTER the workers but BEFORE the
	// DB is closed: monitors derive from a processor-wide parent context and may be
	// mid-write (mark-paid / expire), so they must drain against a live connection
	// pool rather than race a closed one.
	monStopCtx, monStopCancel := context.WithTimeout(ctx, 15*time.Second)
	if a.stripeMain != nil {
		a.stripeMain.StopAllMonitors(monStopCtx)
	}
	if a.stripeTest != nil {
		a.stripeTest.StopAllMonitors(monStopCtx)
	}
	monStopCancel()

	if a.bqc != nil {
		a.bqc.Close()
	}
	a.db.Close()
	close(a.done)
}

// Done returns a channel that is closed after the application has exited
func (a *App) Done() chan struct{} {
	return a.done
}

// buildHealthRegistry collects the constructed workers (those that implement
// health.Reporter), the DB pool-stats provider, and the analytics circuit
// breakers into the registry consumed by GET /statusz. Workers that were not
// started (nil) are skipped. ga4Client is passed explicitly because it is a
// local in Start, not a field on App.
func (a *App) buildHealthRegistry(ga4Client *ga4.Client) *health.Registry {
	reg := &health.Registry{}

	// Workers. Each appended only if non-nil and actually implements Reporter.
	// a.ma is a dependency.Mailer interface; the concrete *mail.Mailer is a
	// Reporter, so it is type-asserted.
	addWorker := func(r health.Reporter) {
		if r != nil {
			reg.Workers = append(reg.Workers, r)
		}
	}
	if a.ma != nil {
		if r, ok := a.ma.(health.Reporter); ok {
			addWorker(r)
		}
	}
	if a.oc != nil {
		addWorker(a.oc)
	}
	if a.sc != nil {
		addWorker(a.sc)
	}
	if a.tm != nil {
		addWorker(a.tm)
	}
	if a.sr != nil {
		addWorker(a.sr)
	}
	if a.ga4w != nil {
		addWorker(a.ga4w)
	}
	if a.rm != nil {
		addWorker(a.rm)
	}

	// DB pool stats (only the MySQL store exposes them).
	if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
		reg.DB = mysqlStore
	}

	// Circuit breakers (cheap getters on the analytics clients).
	if ga4Client != nil {
		reg.Breakers = append(reg.Breakers, health.BreakerReporter{
			BreakerName: "ga4",
			StateFunc: func() circuitbreaker.State {
				return ga4Client.CircuitBreakerState()
			},
		})
	}
	if a.bqc != nil {
		bqc := a.bqc
		reg.Breakers = append(reg.Breakers, health.BreakerReporter{
			BreakerName: "bigquery",
			StateFunc: func() circuitbreaker.State {
				return bqc.CircuitBreakerState()
			},
		})
	}

	return reg
}

// stripeOrderExpirer routes an order's safety-net expiry to the correct Stripe
// processor (live vs test) by its payment method, running the provider-checked
// expiry that confirms a succeeded payment instead of cancelling it. For
// non-card methods (or when a processor is unavailable) it falls back to the
// store-level expiry, which only cancels orders whose payment is not done.
// Implements ordercleanup.PaymentExpirer.
type stripeOrderExpirer struct {
	repo dependency.Repository
	main ordercleanup.PaymentExpirer
	test ordercleanup.PaymentExpirer
}

func (e *stripeOrderExpirer) ExpireOrderPayment(ctx context.Context, orderUUID string) error {
	payment, err := e.repo.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get payment for order %s: %w", orderUUID, err)
	}

	pm, ok := cache.GetPaymentMethodById(payment.PaymentMethodID)
	if ok {
		switch pm.Method.Name {
		case entity.CARD:
			if e.main != nil {
				return e.main.ExpireOrderPayment(ctx, orderUUID)
			}
		case entity.CARD_TEST:
			if e.test != nil {
				return e.test.ExpireOrderPayment(ctx, orderUUID)
			}
		}
	}

	// Non-card method or processor unavailable: the store-level expiry only
	// cancels orders whose payment is not done, so it is safe as a fallback.
	_, err = e.repo.Order().ExpireOrderPayment(ctx, orderUUID)
	return err
}
