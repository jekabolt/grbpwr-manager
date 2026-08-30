package app

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/acctposting"
	"github.com/jekabolt/grbpwr-manager/internal/aftership"
	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4sync"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/archivecleanup"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/campaigndispatch"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/jekabolt/grbpwr-manager/internal/deliverysync"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fileaccess"
	"github.com/jekabolt/grbpwr-manager/internal/fxsync"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/marketingaggregate"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/opexmaterialize"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/patternaccess"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/runpackaccess"
	"github.com/jekabolt/grbpwr-manager/internal/shippinglabel"
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
	hs  *httpapi.Server
	db  dependency.Repository
	b   dependency.FileStore
	ma  dependency.Mailer
	cdw *campaigndispatch.Worker
	oc  *ordercleanup.Worker
	dsw *deliverysync.Worker
	sc  *storefrontcleanup.Worker
	acw *archivecleanup.Worker
	tm  *tiermanagement.Worker
	maw *marketingaggregate.Worker
	om  *opexmaterialize.Worker
	ap  *acctposting.Worker
	sr  *stripereconcile.Worker
	fxw *fxsync.Worker
	// dgw is the DESIGN band generation worker. NIL WHENEVER DESIGN_GENERATION_ENABLED IS OFF:
	// a disabled feature is not a worker that ticks and finds nothing, it is a worker that was
	// never built — the queue it drains costs money to drain.
	dgw  *designgen.Worker
	ga4w *ga4sync.Worker
	bqc  dependency.BQClient
	re   dependency.RevalidationService
	rm   *stockreserve.Manager
	// Stripe processors (live + test). Held so their in-process payment monitors
	// can be stopped on shutdown before the DB is closed.
	stripeMain *stripe.Processor
	stripeTest *stripe.Processor
	// adminS is retained so Stop can drain the admin server's in-flight async
	// storefront revalidations (best-effort Vercel calls) at shutdown.
	adminS *admin.Server
	// patternSvc is retained so Stop can flush its access-stat debounce while the DB
	// is still open.
	patternSvc *patternaccess.Service
	// runPackSvc is retained for the same reason: it debounces run-pack access stats.
	runPackSvc *runpackaccess.Service
	// fileLinkSvc is retained for the same reason again: the public file link debounces its
	// hit counters, and the flush writes rows — so it must stop while the DB is still open.
	fileLinkSvc *fileaccess.Service
	// frontendS/authS are retained so Stop can terminate their in-memory
	// rate-limiter cleanup goroutines (lifecycle discipline; they are singletons).
	frontendS *frontend.Server
	authS     *auth.Server
	c         *config.Config
	done      chan struct{}
	// stopping guards Stop so it runs exactly once, regardless of which path
	// triggers it: an OS signal, the listener-crash bridge (see Start), or the
	// boot-error cleanup in cmd/run.go. Without it, a second caller would panic
	// on close(a.done) (double close).
	stopping atomic.Bool
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

	// Background dictionary-revision poller (R9 versioned invalidation): every instance reloads its
	// in-memory merch dictionaries within DefaultDictionaryPollInterval of a colour/collection/tag/
	// country change made on any instance. ctx is app-lifetime, so it stops on shutdown.
	if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
		go cache.PollDictionaryRevisions(ctx, mysqlStore.Dictionary(), mysqlStore.Cache(), cache.DefaultDictionaryPollInterval)
	}

	// House gross-margin target into the cache: every tech-card costing read resolves an effective
	// target against it, so it is loaded once here rather than queried per read (UpsertAlertSettings
	// refreshes it). A failure leaves the built-in default in place — a costing tab that shows the
	// default target is fine; refusing to boot over it is not.
	if t, err := a.db.Metrics().GetAlertThresholds(ctx); err != nil {
		slog.Default().WarnContext(ctx, "can't load house target margin; using the built-in default",
			slog.String("err", err.Error()))
	} else {
		cache.SetTargetMarginPct(t.TargetMarginPct)
	}

	a.maw = marketingaggregate.New(&a.c.MarketingAggregate, a.db)
	if err = a.maw.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start marketing aggregate worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	a.ma, err = mail.New(&a.c.Mailer, a.db.Mail(), a.db.StorefrontAccount())
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

	a.cdw, err = campaigndispatch.New(&a.c.CampaignDispatch, a.db, a.ma)
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't construct campaign dispatch worker",
			slog.String("err", err.Error()),
		)
		return err
	}
	if err = a.cdw.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start campaign dispatch worker",
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

	// Start the OPEX materialiser AFTER the base currency is set: its startup tick folds each
	// recurring template to base via cache.GetBaseCurrency(), and a materialised opex_line is
	// insert-only (a wrong-base fold on the first tick would be permanent). Every other worker is
	// base-currency-independent, so this is the one ordering that matters here (infra-01).
	a.om = opexmaterialize.New(&a.c.OpexMaterialize, a.db)
	if err = a.om.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start opex materialize worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	// Accounting posting worker. Gated (off unless ACCOUNTING_ENABLED): the outbox producers enqueue
	// events regardless, so enabling it later just drains the queue from the cutover. Started AFTER the
	// base currency is set (like opexmaterialize) — the whole ledger is EUR-native.
	if a.c.Accounting.Enabled {
		// Ship-from origin for the VAT resolver (phase 2, wave 1); not an accounting.* config key, so it
		// is derived from the shipping-label config here rather than bound via env (07 §7.1).
		a.c.Accounting.OriginCountry = a.c.ShippingLabel.ShipFromAddress().CountryISO2
		a.ap = acctposting.New(&a.c.Accounting, a.db)
		if err = a.ap.Start(ctx); err != nil {
			slog.Default().ErrorContext(ctx, "couldn't start accounting posting worker",
				slog.String("err", err.Error()),
			)
			return err
		}
	}

	// External FX-rate sync (ECB reference rates → costing_fx_rate). Gated: off unless enabled.
	// Wired AFTER the base currency is set (above): each fetch expresses rates relative to the
	// configured base via cache.GetBaseCurrency().
	if a.c.FxSync.Enabled {
		a.fxw = fxsync.New(&a.c.FxSync, a.db.TechCards())
		if err = a.fxw.Start(ctx); err != nil {
			slog.Default().ErrorContext(ctx, "couldn't start fx sync worker",
				slog.String("err", err.Error()),
			)
			return err
		}
	}

	// Write validation must know which hosts are OURS before any pattern url can be
	// stored: dto is fail-closed and rejects every pattern url until this is configured
	// (it cannot import bucket — dependency imports dto — so the hosts are pushed in).
	dto.SetManagedPatternHosts(bucket.ManagedHosts(&a.c.Bucket)...)

	a.b, err = bucket.New(&a.c.Bucket, a.db.Media())
	if err != nil {
		slog.Default().ErrorContext(ctx, "couldn't init bucket",
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("cannot init bucket %v", err.Error())
	}
	// HEIC is optional: warn at boot if libheif can't be loaded so the gap is visible
	// immediately, but do not fail startup — non-HEIC uploads and everything else
	// still work.
	if herr := bucket.HEICAvailable(); herr != nil {
		slog.Default().WarnContext(ctx, "libheif unavailable; HEIC image uploads will fail (other uploads unaffected)",
			slog.String("err", herr.Error()),
		)
	}

	// Tech-card archive cleanup. It lives HERE rather than beside the other cleanup workers
	// above because this is the first line at which both of its dependencies exist: it is the
	// only cleanup worker that talks to the bucket as well as the DB.
	//
	// Nil config on purpose: neither knob is worth an operator's attention. The retention window
	// is an owner decision that already has exactly one home (bucket.ArchiveRetention), and an
	// hourly sweep of two folders needs no tuning — so there is no archive_cleanup section in
	// config, and no second place for seven days to be true.
	a.acw = archivecleanup.New(nil, a.db, a.b)
	if err = a.acw.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start archive cleanup worker",
			slog.String("err", err.Error()),
		)
		return err
	}

	authS, err := auth.New(&a.c.Auth, a.db.Admin())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create new auth server",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.authS = authS

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

	// AfterShip tracker for the real delivery signal; a disabled no-op when no API key is
	// configured (delivery then falls back entirely to the per-carrier timer safety net). The
	// same tracker instance is shared with the webhook handler below.
	tracker := aftership.New(&a.c.AfterShip)
	a.dsw = deliverysync.New(&a.c.DeliverySync, a.db, tracker, a.ma)
	// Sendcloud label provider (carrier tracking-number + label generation); a disabled no-op when
	// no API keys are configured, so GenerateShippingLabel reports labels-not-configured and
	// operators keep entering tracking numbers manually. The ship-from (warehouse) origin is
	// stamped on every generated label.
	labelProvider := shippinglabel.New(&a.c.ShippingLabel)
	shipFrom := a.c.ShippingLabel.ShipFromAddress()
	if err = a.dsw.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start delivery sync worker",
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

	// Password hasher for admin-account management RPCs. Hashes are self-describing
	// (salt + iterations stored inline), so this shares the auth service's config.
	adminPwHasher, err := pwhash.New(a.c.Auth.PasswordHasherSaltSize, a.c.Auth.PasswordHasherIterations)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create admin password hasher",
			slog.String("err", err.Error()),
		)
		return err
	}

	// OpenRouter client for AI tech-card operation drafting (#66), note markdown formatting and
	// campaign auto-translation — one client, one model slug, three features. Nil-safe/disabled
	// when OPENROUTER_API_KEY is unset, and each handler then reports it as not configured.
	aiOpsClient := openrouter.New(a.c.OpenRouter)
	// Ask the provider once, in the background, whether that one slug is still served. It returns
	// immediately, refuses nothing and can only write a log line — see WarnIfModelRetired. It is
	// here because the alternative is how the last outage was found: by a person pressing a button
	// weeks later, on one of the three features.
	aiOpsClient.WarnIfModelRetired()

	// ─── DESIGN band, generative half ─────────────────────────────────────────────────────────
	//
	// The image client is a SECOND OpenRouter client, not more options on the first: POST
	// /api/v1/images is a different endpoint with a different catalogue, and `openai/gpt-image-2`
	// is absent from the chat one entirely — no value of OPENROUTER_MODEL could reach it.
	//
	// Its retired-slug probe stands beside the chat client's for the reason that line exists at
	// all: a slug pulled from the provider's catalogue turns the feature into a 404 in a fifth of
	// a second, and the last time it happened here it was found weeks later, by a person pressing
	// a button. The probe returns immediately, refuses nothing, and stays silent when no key is
	// set — so an untouched deployment sees no new line.
	designImages := orimages.New(a.c.OpenRouterImages)
	designImages.WarnIfModelRetired()

	// The worker is GATED, and the gate means NOT CONSTRUCTED — the precedent is ACCOUNTING_ENABLED
	// above. An inert feature must not be a worker that wakes every few seconds to ask an empty
	// table for work it is not allowed to do; and the queue it drains is the one that spends the
	// owner's money, so "off" has to mean the code does not run at all.
	//
	// ⚠ THE HANDLER AND THIS WORKER SHIP TOGETHER OR NOT AT ALL. StartDesignRun without a worker
	// means every press of GENERATE creates a run that stays `pending` forever, holds its budget
	// reservation until midnight, and eventually kills the button with budget_exceeded for a
	// reason nobody can see.
	// Настройки воркера приходят из общего конфига, как у всех остальных компонентов. Раньше здесь
	// стоял designgen.ConfigFromEnv() — временный мост: секцию нельзя было завести, пока config/cfg.go
	// правил другой автор той же волны. Мост снят, чтения снова одно.
	//
	// Умолчания и инвариант «RunTimeout < ClaimLease» применяет сам designgen.New, поэтому
	// незаполненная секция здесь безопасна: очередь не сможет воскресить прогон, чей воркер
	// ещё жив и платит.
	designCfg := a.c.DesignGen
	if designCfg.Enabled {
		a.dgw, err = designgen.New(&designCfg, a.db, a.b, designgen.Providers{
			// flat and render — the raster route.
			Image: designgen.NewImageProvider(designImages),
			// vector — Recraft's vector model, reached through the SAME image endpoint (owner rule
			// P-5); the direct Recraft transport is the fallback and is chosen by RECRAFT_ROUTE.
			Vector: designgen.NewVectorProvider(recraft.New(a.c.Recraft, recraft.NewOpenRouterGenerator(designImages))),
			// threed — Meshy, directly, because OpenRouter has no 3D modality to route to.
			Threed: designgen.NewThreedProvider(meshy.New(a.c.Meshy)),
		})
		if err != nil {
			slog.Default().ErrorContext(ctx, "couldn't construct design generation worker",
				slog.String("err", err.Error()),
			)
			return err
		}
		if err = a.dgw.Start(ctx); err != nil {
			slog.Default().ErrorContext(ctx, "couldn't start design generation worker",
				slog.String("err", err.Error()),
			)
			return err
		}
	}

	adminS, err := admin.New(a.db, a.b, a.ma, stripeMain, stripeTest, a.re, reservationMgr, ga4mpClient, adminPwHasher, labelProvider, shipFrom, a.c.Security.HeroEmbedAllowedHosts, a.c.Mailer.TestRecipients, aiOpsClient, jpk.Taxpayer{
		NIP:       a.c.JPK.NIP,
		FullName:  a.c.JPK.FullName,
		Email:     a.c.JPK.Email,
		Phone:     a.c.JPK.Phone,
		TaxOffice: a.c.JPK.TaxOffice,
	}, a.c.Accounting.NormalLossRate())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create admin server",
			slog.String("err", err.Error()),
		)
		return err
	}
	// THE PAID HANDLERS READ THE SAME FLAG THE WORKER WAS GATED ON, AND FROM THE SAME VALUE.
	//
	// Not from a second os.Getenv, and not from a copy of the string: the two are one decision.
	// Diverging gives exactly two states, each worse than "off" — StartDesignRun open with no
	// worker leaves every paid run in `pending` until midnight, holding its reservation; a worker
	// up with the handler closed is a loop polling a queue nothing can enqueue into.
	adminS.SetDesignGenerationEnabled(designCfg.Enabled)
	// AND THE DOOR GETS THE WORKER'S OWN PRE-FLIGHT, not a second opinion about it.
	//
	// StartDesignRun reserves money the moment it files a row. A run whose route is unwired, whose
	// key is missing, or whose output the media store cannot keep would be accepted, hold the
	// reservation, and be failed by the very first pass — once per click. PreflightKind is the same
	// call that pass makes, on the same providers and the same sink, so the door refuses exactly
	// what the worker would refuse for free, and stops refusing by itself the day the sink learns
	// the type. Wired only when the worker exists; without it the flag above has already closed
	// every paid verb.
	if a.dgw != nil {
		adminS.SetDesignKindGate(a.dgw.PreflightKind)
	}
	a.adminS = adminS

	var frontendS *frontend.Server
	frontendS, err = frontend.New(a.db, a.ma, stripeMain, stripeTest, a.re, reservationMgr, &a.c.StorefrontAuth)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed create frontend server",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.frontendS = frontendS

	// start API server
	a.c.HTTP.CommitHash = getCommitHash()
	a.hs = httpapi.New(&a.c.HTTP)

	// Set up database health checker if store supports it
	if mysqlStore, ok := a.db.(*store.MYSQLStore); ok {
		healthChecker := httpapi.NewDatabaseHealthChecker(mysqlStore.Ping)
		a.hs.SetHealthChecker(healthChecker)
	}

	// Set up Resend webhook handler (bounce/complaint suppression + list-unsubscribe)
	webhookHandler, err := mail.NewWebhookHandler(a.db, a.c.Mailer.WebhookSecret, a.c.Mailer.UnsubscribePepper)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create webhook handler",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.hs.SetWebhookHandler(webhookHandler)

	// Tokenized pattern read path (Ф7): stable capability urls for private выкройки —
	// minted into admin responses (view_url/download_url) and printed QR codes, resolved
	// at /api/p/{token} into short-lived presigned origin urls. The same service serves
	// the card-level viewer manifest (/api/pv/{token}) behind the one-QR-per-fabric-scope
	// tech-pack print. Fails closed on a missing pepper (config.Validate guards it too,
	// with a friendlier message).
	patternSvc, err := patternaccess.New(a.db.PatternObjects(), a.db.TechCards(), a.b,
		a.c.PatternToken.Pepper, strings.TrimRight(a.c.PatternToken.PublicBaseURL, "/"))
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create pattern access service",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.patternSvc = patternSvc
	a.hs.SetPatternAccessHandler(patternSvc)
	a.hs.SetPatternViewerHandler(patternSvc.ManifestHandler())
	a.adminS.SetPatternURLService(patternSvc, strings.TrimRight(a.c.PatternToken.PublicBaseURL, "/"))

	// Публичный наряд на партию (/api/rp/{token}): та же капабилити-схема, что у вьюера выкроек,
	// на том же pepper — скоуп токена ('r') подписан вместе с id, поэтому один секрет обслуживает
	// три непересекающихся пространства идентичности и разделять его незачем. Свои бюджеты
	// rate-limit живут внутри сервиса: за наряд отвечает цех с одного NAT, а не админская вкладка.
	runPackSvc, err := runpackaccess.New(a.db.ProductionRuns(), a.db.TechCards(), a.c.PatternToken.Pepper)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create run pack access service",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.runPackSvc = runPackSvc
	a.hs.SetRunPackHandler(runPackSvc.Handler())
	a.adminS.SetRunPackTokenService(runPackSvc)

	// Публичная ссылка на файл библиотеки (/api/f/{token}, Ф7): та же капабилити-схема и тот же
	// pepper — скоуп ('f') подписан вместе с id, поэтому один секрет обслуживает четыре
	// непересекающихся пространства идентичности. Base url нужен ЗДЕСЬ (в отличие от наряда):
	// ссылку копируют наружу, в мессенджер, и собрать её из origin панели нельзя. ACL объектов
	// бакета сервис не трогает никогда — публичность даёт маршрут, а не бакет.
	// `a.b` приезжает сюда дважды не по недосмотру: подписыватель и читатель — два РАЗНЫХ
	// узких интерфейса (Presigner и Reader), и то, что сегодня их удовлетворяет один бакет,
	// не повод давать маршруту весь FileStore целиком.
	fileLinkSvc, err := fileaccess.New(a.db.Files(), a.b, a.b,
		a.c.PatternToken.Pepper, strings.TrimRight(a.c.PatternToken.PublicBaseURL, "/"))
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create file link service",
			slog.String("err", err.Error()),
		)
		return err
	}
	a.fileLinkSvc = fileLinkSvc
	a.hs.SetFileLinkHandler(fileLinkSvc.Handler())
	a.adminS.SetFileLinkService(fileLinkSvc)

	// Files-library upload (POST /api/files/upload). The only admin write that is not
	// a gRPC method — a file cannot fit inside one message — so it is wrapped here in
	// the admin authorization middleware by hand. That wrapping is the whole of its
	// authentication: without it the endpoint would be open, since the gRPC
	// interceptor never sees a plain HTTP route.
	a.hs.SetFileUploadHandler(authS.WithAdminAuthz(a.adminS.FileUploadHandler()))
	// Перезаливка превью (POST /api/files/{id}/preview) — тот же случай и та же
	// ручная обёртка авторизацией: картинка приходит multipart-ом, мимо gRPC, а
	// значит мимо интерцептора, который проверяет права у всех остальных методов.
	a.hs.SetFilePreviewHandler(authS.WithAdminAuthz(a.adminS.FilePreviewHandler()))
	// Импорт тех-карты архивом (POST /api/techcard-archive/upload) — третий и последний
	// админский write мимо gRPC: 256-мегабайтный ZIP в одно gRPC-сообщение не влезает. Та же
	// ручная обёртка авторизацией и по той же причине: интерцептор, проверяющий права у всех
	// остальных методов, простого HTTP-маршрута не видит вовсе. Сам маршрут дополнительно
	// требует tech_cards:write внутри хендлера — обёртка аутентифицирует, секцию решает он.
	a.hs.SetTechCardArchiveUploadHandler(authS.WithAdminAuthz(a.adminS.TechCardArchiveUploadHandler()))

	// Stripe webhook: OPTIONAL real-time server-to-server payment confirmation.
	// When a signing secret is configured for a processor it delivers the fastest
	// (immediate push) confirmation, but it is not the sole mechanism: confirmation
	// is always backstopped by the in-process payment monitor, lazy
	// CheckForTransactions on order reads, and the ordercleanup safety-net worker.
	// So a deployment with no webhook secret (the current prod config — the secrets
	// in .do/app.yaml are blank) still confirms payments correctly, only with added
	// latency after a restart (up to one ordercleanup tick). Mounted only when at
	// least one processor has a signing secret; set the Stripe-dashboard signing
	// secrets in .do/app.yaml to enable the immediate path.
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

	// AfterShip webhook: OPTIONAL real-time delivery confirmation. Mounted only when a signing
	// secret is configured; the delivery-sync worker's AfterShip poll reconciles anything the
	// webhook misses, and the per-carrier timer is the final safety net — so a blank secret still
	// auto-delivers, just without the immediate push.
	if aftershipWebhook := aftership.NewWebhookHandler(a.c.AfterShip.WebhookSecret, a.db, a.ma); aftershipWebhook.Enabled() {
		a.hs.SetAftershipWebhookHandler(aftershipWebhook)
		slog.Default().InfoContext(ctx, "aftership webhook handler enabled")
	} else {
		slog.Default().InfoContext(ctx, "aftership webhook handler disabled (no signing secret configured)")
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

	// Bridge an unexpected listener exit to a full shutdown. hs.Start is
	// non-blocking; if the HTTP server later stops on its own (fatal serve error,
	// a bind failure surfaced post-start), nothing else would notice and the
	// process would hang with live workers and a dead API. Watch hs.Done() and
	// tear the app down so Done() fires and cmd/run.go exits non-zero, letting the
	// platform restart the instance. During a normal shutdown a.Stop already ran,
	// so this call is a no-op via the stopping guard.
	go func() {
		<-a.hs.Done()
		slog.Default().ErrorContext(context.Background(), "http server exited unexpectedly; shutting down")
		a.Stop(context.Background())
	}()

	return nil
}

// Stop stops the application and waits for all services to exit.
// Shutdown order: drain the API server first (so no new request reaches a worker
// or the DB), then stop the workers, then close the database.
func (a *App) Stop(ctx context.Context) {
	// Idempotent: the signal handler, the listener-crash bridge (see Start), and
	// the boot-error cleanup can all reach here. Only the first proceeds; the rest
	// return immediately so close(a.done) runs exactly once.
	if !a.stopping.CompareAndSwap(false, true) {
		return
	}

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

	// Pattern access service: flush pending access stats and stop its limiters/ticker
	// while the DB is still open (the flush writes rows).
	if a.patternSvc != nil {
		a.patternSvc.Stop()
	}
	// Same contract for the run pack service: its flush writes rows, so it stops before the
	// DB does.
	if a.runPackSvc != nil {
		a.runPackSvc.Stop()
	}
	// И для публичной ссылки на файл — по тому же договору: её сброс тоже пишет строки.
	if a.fileLinkSvc != nil {
		a.fileLinkSvc.Stop()
	}

	// The HTTP listener has drained, so no new admin RPC can spawn a revalidation.
	// Cancel and wait (bounded) for any in-flight ones so best-effort Vercel ISR
	// calls don't keep retrying after shutdown. It also drains detached waitlist
	// notifications, which DO touch the DB — so keep this before the DB close.
	if a.adminS != nil {
		revalStopCtx, revalStopCancel := context.WithTimeout(ctx, 10*time.Second)
		a.adminS.StopRevalidation(revalStopCtx)
		revalStopCancel()
	}

	// Terminate the in-memory rate-limiter cleanup goroutines (frontend + auth).
	// They are effectively singletons living the whole process, but stopping them
	// keeps lifecycle discipline consistent with the other background components.
	if a.frontendS != nil {
		a.frontendS.StopRateLimiter()
	}
	if a.authS != nil {
		a.authS.StopRateLimiter()
	}

	// Stop workers before closing DB — avoids panics and error storms from workers
	// hitting a closed connection. In-flight emails remain in DB and will be retried on next run.
	if a.cdw != nil {
		_ = a.cdw.Stop()
	}
	if a.ma != nil {
		_ = a.ma.Stop()
	}
	if a.oc != nil {
		_ = a.oc.Stop()
	}
	if a.dsw != nil {
		_ = a.dsw.Stop()
	}
	if a.sc != nil {
		_ = a.sc.Stop()
	}
	// Inside the workers block, i.e. ABOVE a.db.Close() below: this worker's tick runs the
	// import-expiry UPDATE, and Stop does not return until that goroutine is gone.
	if a.acw != nil {
		_ = a.acw.Stop()
	}
	if a.tm != nil {
		_ = a.tm.Stop()
	}
	if a.maw != nil {
		_ = a.maw.Stop()
	}
	if a.om != nil {
		_ = a.om.Stop()
	}
	if a.ap != nil {
		_ = a.ap.Stop()
	}
	if a.fxw != nil {
		_ = a.fxw.Stop()
	}
	// Inside the workers block, i.e. ABOVE a.db.Close(): a pass that has already paid a provider
	// finishes writing the charge and the picture on a context that ignores cancellation, and Stop
	// waits for it. Moving this below the close would turn a redeploy landing mid-generation into
	// a purchase with no record of it.
	if a.dgw != nil {
		_ = a.dgw.Stop()
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
	// Nil-guarded: Stop is also the boot-error cleanup path, where Start may have
	// failed before store.New assigned a.db.
	if a.db != nil {
		a.db.Close()
	}
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
	if a.cdw != nil {
		addWorker(a.cdw)
	}
	if a.oc != nil {
		addWorker(a.oc)
	}
	if a.dsw != nil {
		addWorker(a.dsw)
	}
	if a.sc != nil {
		addWorker(a.sc)
	}
	if a.acw != nil {
		addWorker(a.acw)
	}
	if a.tm != nil {
		addWorker(a.tm)
	}
	if a.maw != nil {
		addWorker(a.maw)
	}
	if a.om != nil {
		addWorker(a.om)
	}
	if a.ap != nil {
		addWorker(a.ap)
	}
	if a.fxw != nil {
		addWorker(a.fxw)
	}
	if a.dgw != nil {
		addWorker(a.dgw)
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
