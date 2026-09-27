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
	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
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
	"github.com/jekabolt/grbpwr-manager/internal/fal"
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
	designstore "github.com/jekabolt/grbpwr-manager/internal/store/design"
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
	// aireg is the AI providers' live configuration (keys, enable switches, routes, breakers) and its
	// config_version poller. Built right after the DB and never nil after a successful boot: every
	// provider client's KeyFunc reads it.
	aireg *registry.Registry
	// dgw is the DESIGN band generation worker. NIL WHENEVER DESIGN_GENERATION_ENABLED IS OFF:
	// a disabled feature is not a worker that ticks and finds nothing, it is a worker that was
	// never built — the queue it drains costs money to drain.
	dgw *designgen.Worker
	// dgs — подметальщик резервов, ЗЕРКАЛО dgw: он не nil ровно тогда, когда nil воркер.
	// Выключенная генерация обязана перестать ТРАТИТЬ, но не перестать УБИРАТЬ за собой: строки,
	// заведённые до выключения, иначе держат деньги дня до полуночи и не закрываются никогда.
	dgs  *designgen.Sweeper
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

	// ─── AI providers: the key registry every provider client reads its key through ───────────
	//
	// Right after the DB because the clients below are built with its KeyFuncs. A master key that
	// is set but malformed is a BOOT ERROR: the operator meant to encrypt, and running on with
	// every stored key unreadable would quietly fall back to env. An empty one is a warning:
	// stored keys cannot be opened (the panel says "re-enter"), env keys answer exactly as before.
	aiKeyRing, err := keyring.New(a.c.AI.KeysMasterKey)
	if err != nil {
		slog.Default().ErrorContext(ctx, "invalid AI_KEYS_MASTER_KEY",
			slog.String("err", err.Error()),
		)
		return err
	}
	if !aiKeyRing.Enabled() {
		slog.Default().WarnContext(ctx, "AI_KEYS_MASTER_KEY is not set: AI provider keys come from env only, "+
			"a key stored in the database cannot be opened, and saving one is refused")
	}
	a.aireg = registry.New(a.db.AI(), aiKeyRing, registry.EnvKeys{
		OpenRouter:       a.c.OpenRouter.APIKey,
		OpenRouterImages: a.c.OpenRouterImages.APIKey,
		Fal:              a.c.Fal.APIKey,
		Meshy:            a.c.Meshy.APIKey,
		Recraft:          a.c.Recraft.Direct.APIKey,
	})
	// The first reload is the boot log (one line per provider: enabled, key source, last4 — never
	// the key). A failure here is a boot error: automigrate has created the tables by now, so a
	// registry that cannot read them is a broken deploy, not a feature to degrade.
	if err = a.aireg.Reload(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't load the AI provider configuration",
			slog.String("err", err.Error()),
		)
		return err
	}
	if err = a.aireg.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start the AI provider registry poller",
			slog.String("err", err.Error()),
		)
		return err
	}
	// Every provider client reads its key through the registry: a key saved in the panel, or a
	// provider switched off there, reaches the next request without a redeploy. Set on the config
	// itself, ONCE, so every constructor below — fal.New is called three times — gets the hook and
	// a fourth one added later cannot forget it. The images client has its own func: the same
	// openrouter row, with OPENROUTER_IMAGES_API_KEY as its env fallback.
	a.c.OpenRouter.KeyFunc = a.aireg.KeyFunc(entity.AIProviderOpenRouter)
	a.c.OpenRouterImages.KeyFunc = a.aireg.OpenRouterImagesKeyFunc()
	a.c.Fal.KeyFunc = a.aireg.KeyFunc(entity.AIProviderFal)
	a.c.Meshy.KeyFunc = a.aireg.KeyFunc(entity.AIProviderMeshy)
	a.c.Recraft.Direct.KeyFunc = a.aireg.KeyFunc(entity.AIProviderRecraft)

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

	// OpenRouter chat client. Since B-18 it is the TRANSPORT the AI router calls for every chat
	// purpose routed to openrouter (aiOpsClient.Transport() — this very configuration: base URL, the
	// key read per request through the registry's KeyFunc wired above (a key stored in admin → AI
	// providers, else OPENROUTER_API_KEY), budget base, attribution headers) and the source of the
	// router's default slugs (admin.AIRouterDefaults: OPENROUTER_MODEL, _ANALYSIS, _IDEAS). With no
	// key from either source, or with openrouter switched off in the panel, the transport is passed
	// over and each door reports it as not configured.
	aiOpsClient := openrouter.New(a.c.OpenRouter)

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
	// The per-run engines (PLAYGROUND phase 2) are one table, keyed off the client's own slug; every
	// slug in it is probed, since a person can pick any of them.
	// B-16: the Gemini / Seedream rows join the table (and the probe) only while their flag is on.
	designEngines := designgen.EngineTable(designImages.Model(), a.c.DesignGen.EngineFlags())
	designEngineSlugs := make([]string, 0, len(designEngines))
	for _, e := range designEngines {
		designEngineSlugs = append(designEngineSlugs, e.Slug)
	}
	// The client's own slug is probed even when it is not a row — it is still what every unnamed run
	// is drawn by.
	designImages.WarnIfModelsRetired(append(designEngineSlugs, designImages.Model())...)
	if len(designEngines) == 0 {
		// G-02 Codex 2: an OPENROUTER_MODEL_IMAGE the engine table has no row for offers no per-run
		// engine (its ratios, reference ceiling and price are unknown) — said once, at boot.
		slog.Default().WarnContext(ctx, "design generation: the image model is not in the engine table, "+
			"so no per-run engine is offered (no picker; params.image refused; unnamed runs reserve the "+
			"kind's own price)", slog.String("model", designImages.Model()),
			slog.String("flag", "OPENROUTER_MODEL_IMAGE"))
	}

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
	// ⚠ NORMALISED BEFORE ANY FIELD OF IT IS READ. designgen.New would do this itself, but the 3D
	// route is chosen BELOW, from ThreedProvider — and a raw `MESHY` typed into the dashboard does
	// not equal the lower-case constant, so an un-normalised read would wire fal and log fal while
	// the operator had asked for Meshy. Idempotent; New() applies it again.
	designgen.Normalize(&designCfg)
	// The worker resolves a frozen params.image against the same table the door checks it with — ONE
	// function, handed to both (B-13): the image.generate route's first model when it is a row the table
	// can default to, else the image client's env slug (a typo in the panel keeps the picker, with one
	// warning per slug). WarnIfModelsRetired above keeps probing the env table at boot.
	designCfg.Engines = designgen.EngineTableFunc(a.aireg, designImages.Model(), designCfg.EngineFlags())
	// The configured 3D route as the door and the band see it (G-02): which build options it reads
	// and what one build may book at this deployment's tariff. Built from the SAME client the worker
	// is given below; wired only when the worker exists.
	var designThreedRoute *designgen.ThreedRoute
	// PLAYGROUND phase 3: the extend / inpaint route objects, built from the SAME fal client the
	// worker's Outpaint / Fill providers get (one value for the band, the door and the reserve).
	var designFalRoutes map[string]designgen.FalRoute

	// ─── THE AI LEDGER (B-07): one ai_usage_event row per physical provider call, opened BEFORE
	// the call. Built in BOTH branches below: the worker books every call it pays for and sweeps
	// stale `dispatching` rows on its tick; with generation off, the reserve sweeper does the sweep.
	//
	// The day key is the organisation's day (design_settings.budget_timezone) AS THE REGISTRY
	// CURRENTLY KNOWS IT: its snapshot carries the zone and its poller refreshes it every minute
	// (B-09), so a zone edited in the admin reaches the ledger without a redeploy. "" (no snapshot
	// yet — the boot Reload failed) falls back to the default zone, never to a boot error: the ledger
	// is an observer of spend, and a missing bookkeeping setting must not keep the paid features down.
	aiLedger := aiprov.NewLedger(a.db.AI(), func() string {
		if tz := a.aireg.BudgetTimezone(); tz != "" {
			return tz
		}
		return entity.DefaultBudgetTimezone
	})
	slog.Default().InfoContext(ctx, "ai ledger: every design provider call is booked to ai_usage_event",
		slog.String("budget_timezone", a.aireg.BudgetTimezone()))

	// ─── THE AI ROUTER (B-18): the chat door of every AI feature but the operations draft ─────────
	//
	// One route per purpose (admin → AI providers), read from the registry's live snapshot; the
	// candidates in order, a ledger row before every physical call, a fallback only where no money
	// moved. Transports: openrouter only in commit C (OpenAI / apibost arrive in commit E). A route
	// row with no model answers with today's env slugs (admin.AIRouterDefaults).
	aiRouter := router.New(a.aireg, aiLedger,
		map[string]aiprov.Chatter{entity.AIProviderOpenRouter: aiOpsClient.Transport()},
		admin.AIRouterDefaults(aiOpsClient), aiOpsClient.CompletionBase())
	// ⚠ ДВА ЧИСЛА, КОТОРЫЕ ОДНАЖДЫ РАЗОШЛИСЬ МОЛЧА, ТЕПЕРЬ ГОВОРЯТСЯ ВСЛУХ ОДИН РАЗ ЗА ЗАГРУЗКУ.
	//
	// Лиза строки черновика идеи обязана переживать платную цепочку, а длину цепочки задают БАЗЫ
	// бюджета транспортов её кандидатов — OPENROUTER_HTTP_TIMEOUT у openrouter. Сойтись они больше не
	// могут: лизу считает store/design.HandlerLeaseFor ИЗ router.ChainBudget — потолок цепочки (два
	// вызова) по самой долгой из тех же баз (хендлер приносит её в DesignRunStart и ограничивает ею
	// вызов), поэтому отказывать на старте нечему.
	//
	// Строка стоит ради другого: утверждение «на бете переменная не задана» было ЗАМЕРОМ РУКАМИ на
	// платформе — спек беты в .gitignore, — и протух бы этот замер в ту минуту, когда переменную
	// поставят. Теперь тот же вопрос отвечается журналом живого процесса, без доступа к панели.
	slog.Default().InfoContext(ctx, "design idea draft: the paid call's budget base and the row's lease",
		slog.String("flag", "OPENROUTER_HTTP_TIMEOUT"),
		slog.Duration("completion_base", aiOpsClient.CompletionBase()),
		slog.Duration("chain_budget", aiRouter.ChainBudget(entity.AIPurposeDesignDraftIdea,
			entity.DesignDraftLongestAnswerCeiling())),
		slog.Duration("handler_lease", designstore.HandlerLeaseFor(aiRouter.ChainBudget(
			entity.AIPurposeDesignDraftIdea, entity.DesignDraftLongestAnswerCeiling()))),
	)
	// Ask the provider once, in the background, whether every slug the routes will call on
	// openrouter is still served. It returns immediately, refuses nothing and can only write a log
	// line — see WarnIfRetired. It is here because the alternative is how the last outage was found:
	// by a person pressing a button weeks later.
	aiOpsClient.WarnIfRetired(ctx, aiRouter.OpenRouterSlugs())

	if designCfg.Enabled {
		// ─── WHICH 3D ROUTE GETS PAID, DECIDED BY A WORD SOMEBODY WROTE DOWN ────────────────────
		//
		// DESIGN_THREED_PROVIDER, defaulting to `fal` — the provider the owner named. It is NOT
		// inferred from which key happens to be present: that rule would move the owner's money
		// between two vendors as a side effect of typing a key into a dashboard. designgen's
		// applyDefaults has already normalised an unknown word to the default; this line is where
		// the effective choice is SAID OUT LOUD, once per boot, so «which vendor is this bill from»
		// is answerable from the logs and not only from an attempt row.
		//
		// Neither client is asked for a key here. A route with no credentials is a route that
		// refuses AT THE DOOR, in words, naming its variable (PreflightKind → MissingCredential) —
		// which is what lets the owner tell «I have not set the key yet» from «the service is
		// busy».
		falThreed := fal.New(a.c.Fal)
		// ⚠ BOTH 3D ROUTES ARE CONSTRUCTED, AND THE ONE NOT ROUTED GOES TO Providers.Also (B-13). A job
		// the other vendor ACCEPTED before a switch of DESIGN_THREED_PROVIDER is paid and collectable
		// for free — by that vendor only: the worker collects with the provider the accepted attempt
		// names. Constructing it asks nothing of it (meshy.New is a struct; keyless = Enabled false, and
		// such a job WAITS as `paid_collect_waiting` until its key is back).
		falThreedRoute := designgen.NewFalThreedProvider(falThreed)
		meshyThreedRoute := designgen.NewThreedProvider(meshy.New(a.c.Meshy))
		threed, unroutedThreed := falThreedRoute, meshyThreedRoute
		if designCfg.ThreedProvider == designgen.ThreedProviderMeshy {
			threed, unroutedThreed = meshyThreedRoute, falThreedRoute
		}
		// THE SAME EXPRESSION THE WORKER ASKS BEFORE EVERY FRESH SUBMIT (ThreedRouteOf the wired
		// provider at DESIGN_THREED_PBR), so the door and the pickup cannot read two routes.
		route := designgen.ThreedRouteOf(threed, designCfg.ThreedPBR)
		designThreedRoute = route
		slog.Default().InfoContext(ctx, "design generation: 3D route wired",
			slog.String("provider", designCfg.ThreedProvider),
			slog.String("flag", designgen.EnvThreedProvider),
			slog.Any("build_options", route.Options),
			slog.Bool("pbr", designCfg.ThreedPBR), slog.String("pbr_flag", designgen.EnvThreedPBR))
		if why := route.Unbounded(); why != "" {
			slog.Default().WarnContext(ctx, "design generation: the 3D door is closed — "+why)
		}

		// PLAYGROUND phase 3 — tile 9 (extend → fal outpaint) and tile 10's mask route (inpaint → fal
		// fill): the SAME FAL_KEY, their own slugs (FAL_MODEL_OUTPAINT / FAL_MODEL_FILL) and tariffs.
		// A tariff set without its units ceiling closes the kind at the door, in words.
		falRoutes := fal.New(a.c.Fal)
		designFalRoutes = map[string]designgen.FalRoute{}
		for _, kind := range []string{entity.DesignRunKindExtend, entity.DesignRunKindInpaint} {
			r, _ := designgen.FalRouteOf(falRoutes, kind)
			designFalRoutes[kind] = r
			slog.Default().InfoContext(ctx, "design generation: fal route wired",
				slog.String("kind", kind), slog.String("model", r.Model),
				slog.String("reserve_usd", r.Ceiling.String()), slog.Bool("bounded", r.Bounded))
			if !r.Bounded {
				slog.Default().WarnContext(ctx, "design generation: the "+kind+" door is closed — "+r.Unbounded)
			}
		}

		a.dgw, err = designgen.New(&designCfg, a.db, a.b, designgen.Providers{
			// flat, render, recolor, pattern and freeform — the raster route. They differ by prompt and
			// by which pictures go into which paid call, both of which live inside designgen. Since B-13
			// the slot is the ROUTE (admin → AI providers, image.generate): each pass pays one
			// candidate of it, a candidate that failed without money moving hands the run to the next
			// one on a fresh attempt. Transports: openrouter only (commit F adds OpenAI).
			Image: designgen.NewRoutedImageProvider(a.aireg,
				map[string]designgen.ImageTransport{entity.AIProviderOpenRouter: designImages}, designImages.Model()),
			// vector — Recraft's vector model, reached through the SAME image endpoint (owner rule
			// P-5); the direct Recraft transport is the fallback and is chosen by RECRAFT_ROUTE.
			Vector: designgen.NewVectorProvider(recraft.New(a.c.Recraft, recraft.NewOpenRouterGenerator(designImages))),
			// threed — fal.ai's queue by default (K-10), Meshy's own API behind the same slot on
			// request. Which MODEL the fal route asks for is FAL_MODEL_3D / fal.DefaultModel3D,
			// today `meshy/v7/multi-image-to-3d`. Both are reached DIRECTLY, because OpenRouter has
			// no 3D modality to route to.
			Threed: threed,
			// cutout — background removal, the SAME fal client and the SAME FAL_KEY as the 3D
			// route, and a different slug (FAL_MODEL_CUTOUT / fal.DefaultModelCutout, today
			// `fal-ai/birefnet/v2`) with a tariff of its own (FAL_UNIT_USD_CUTOUT). It is a route
			// rather than a fifth kind on Image because it is a different paid endpoint with a
			// different unit of money — and because it sends no words at all.
			Cutout: designgen.NewFalCutoutProvider(fal.New(a.c.Fal)),
			// extend — tile 9 «Extend Image», fal's outpaint route (FAL_MODEL_OUTPAINT, default
			// fal-ai/flux-2-pro/outpaint; fallback fal-ai/bria/expand).
			Outpaint: designgen.NewFalOutpaintProvider(falRoutes),
			// inpaint — tile 10's mask route, fal's fill route (FAL_MODEL_FILL, default
			// fal-ai/flux-pro/v1/fill); the composite goes through OUR mask only.
			Fill: designgen.NewFalFillProvider(falRoutes),
			// The 3D route DESIGN_THREED_PROVIDER did not pick — never chosen for a fresh run, kept so
			// it can collect what it accepted before the switch (see above).
			Also: []designgen.Provider{unroutedThreed},
		}, designgen.WithLedger(aiLedger))
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
	} else {
		// ВЫКЛЮЧЕНО — ЗНАЧИТ НЕ ТРАТИТ, А НЕ «НЕ УБИРАЕТ». Воркер выше не построен вовсе, и вместе
		// с очередью замолкает ReviveExpiredRuns — единственный путь, которым брошенная и
		// отменённая строка доходит до терминала и ОТПУСКАЕТ РЕЗЕРВ. Без этой ветки выключение
		// флага деплоем оставляет сирот с занятыми деньгами дня, и снять их некому никогда.
		//
		// Подметальщик не получает ни одного провайдера: потратить он не может физически.
		//
		// WithRunTimeout: the ledger sweep cuts where the worker's does (RunTimeout + finish slack) —
		// on a rolling enabled→disabled deploy the old instance may still be inside a paid call whose
		// row this one would otherwise call `unknown` (Codex A4 #4). designCfg is Normalize()d above.
		a.dgs, err = designgen.NewSweeper(a.db, designgen.WithLedger(aiLedger),
			designgen.WithRunTimeout(designCfg.RunTimeout))
		if err != nil {
			slog.Default().ErrorContext(ctx, "couldn't construct design reserve sweeper",
				slog.String("err", err.Error()),
			)
			return err
		}
		if err = a.dgs.Start(ctx); err != nil {
			slog.Default().ErrorContext(ctx, "couldn't start design reserve sweeper",
				slog.String("err", err.Error()),
			)
			return err
		}
	}

	adminS, err := admin.New(a.db, a.b, a.ma, stripeMain, stripeTest, a.re, reservationMgr, ga4mpClient, adminPwHasher, labelProvider, shipFrom, a.c.Security.HeroEmbedAllowedHosts, a.c.Mailer.TestRecipients, jpk.Taxpayer{
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
	if designThreedRoute != nil {
		adminS.SetDesignThreedRoute(*designThreedRoute)
	}
	if designFalRoutes != nil {
		adminS.SetDesignFalRoutes(designFalRoutes)
	}
	// The engine table the worker resolves params.image with (designCfg.Engines above — the SAME
	// function): the door validates and prices against it, the band advertises it. A table, not a
	// gate — it spends nothing, and the money flag above has already closed every paid verb when it
	// is off.
	adminS.SetDesignEngines(designCfg.Engines)
	// admin → AI providers: the SAME registry every client reads its key through (a write reloads it
	// here at once) and the SAME ring it opens stored keys with (a key sealed by another master would
	// read back "unreadable"). The recraft route is asked of recraft itself — RECRAFT_ROUTE's parse,
	// typo fallback included, lives in one place.
	// admin → the chat doors: the router built above, next to the registry and the ledger.
	adminS.SetAIRouter(aiRouter)
	adminS.SetAIProviders(admin.AIProvidersWiring{
		Registry:             a.aireg,
		KeyRing:              aiKeyRing,
		RecraftViaOpenRouter: recraft.New(a.c.Recraft, nil).Route() == recraft.RouteOpenRouter,
	})
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

	// Terminate the in-memory rate-limiter cleanup goroutines (frontend + auth + admin).
	// They are effectively singletons living the whole process, but stopping them
	// keeps lifecycle discipline consistent with the other background components.
	if a.frontendS != nil {
		a.frontendS.StopRateLimiter()
	}
	if a.authS != nil {
		a.authS.StopRateLimiter()
	}
	if a.adminS != nil {
		a.adminS.StopRateLimiter()
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
	if a.aireg != nil {
		_ = a.aireg.Stop()
	}
	// Inside the workers block, i.e. ABOVE a.db.Close(): a pass that has already paid a provider
	// finishes writing the charge and the picture on a context that ignores cancellation, and Stop
	// waits for it. Moving this below the close would turn a redeploy landing mid-generation into
	// a purchase with no record of it.
	if a.dgw != nil {
		_ = a.dgw.Stop()
	}
	// По тому же доводу, что и выше: подметание, уже снявшее резерв, обязано дописать это в живой
	// пул, поэтому останов идёт ДО закрытия базы.
	if a.dgs != nil {
		_ = a.dgs.Stop()
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
	if a.aireg != nil {
		addWorker(a.aireg)
	}
	if a.dgw != nil {
		addWorker(a.dgw)
	}
	if a.dgs != nil {
		addWorker(a.dgs)
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
