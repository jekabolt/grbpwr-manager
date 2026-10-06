package admin

import (
	"context"
	"fmt"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/singleflight"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fileaccess"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	"github.com/jekabolt/grbpwr-manager/internal/patternaccess"
	"github.com/jekabolt/grbpwr-manager/internal/runpackaccess"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxConcurrentRevalidations bounds how many async storefront revalidations may run
// at once. Admin writes call revalidateAsync, which previously spawned an unbounded
// goroutine per request; a burst during a Vercel slowdown could spawn unbounded
// goroutines. The counting semaphore caps concurrency and queues the excess instead.
const maxConcurrentRevalidations = 4
const maxConcurrentCampaignTestSends = 2

// maxConcurrentNoteFormats bounds how many markdown-assistant calls may be in flight at once
// (FormatLibraryNoteMarkdown). Same shape as maxConcurrentCampaignTestSends, and for a stronger
// reason: that RPC is an unbounded proxy to a paid third party. Everybody who works with the
// library holds files:write, each call may carry 12 000 runes and parks a goroutine on an upstream
// request for up to 60 s, and nothing else in the path costs the caller anything. Four is a real
// team formatting notes at the same time; a fifth waits a moment rather than the process growing
// goroutines and the account growing a bill.
const maxConcurrentNoteFormats = 4

// maxConcurrentEnhance bounds how many EnhanceText calls may be in flight at once. Same shape and
// same reason as maxConcurrentNoteFormats: every call parks a goroutine on a paid third party, and
// the button sits on every free-text field of every tech card. A fifth concurrent press is refused
// at once (ResourceExhausted) rather than queued — «try again in a moment» beats a silent minute.
const maxConcurrentEnhance = 4

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo   dependency.Repository
	bucket dependency.FileStore
	// patternURLs mints output-only view_url/download_url on pattern messages (Ф7).
	// Nil-safe by design — tests construct Servers without it and reads then simply
	// omit the tokenized urls.
	patternURLs        *patternaccess.Service
	patternURLsBaseURL string
	// runPackTokens mints the output-only run_pack_token on a run read (/api/rp). Nil-safe
	// for the same reason patternURLs is.
	runPackTokens *runpackaccess.Service
	// fileLinks mints the public /api/f/{token} url shown in a file's access block (Ф7).
	// Nil-safe like the two above: без сервиса блок доступа приезжает без url, а не падает.
	fileLinks       *fileaccess.Service
	mailer          dependency.Mailer
	renderer        *campaignrender.Renderer
	campaignTestSem chan struct{}
	// campaignTestRecipientAllowlist contains lower-cased, trimmed addresses
	// permitted for admin campaign test sends (MAILER_TEST_RECIPIENTS). Empty is
	// fail-closed: test sends are refused until it is configured. Suppression-list
	// checks remain mandatory on top.
	campaignTestRecipientAllowlist map[string]struct{}
	stripePayment                  dependency.Invoicer
	stripePaymentTest              dependency.Invoicer
	re                             dependency.RevalidationService
	reservationMgr                 dependency.StockReservationManager
	ga4mp                          *ga4mp.Client
	// labelProvider generates carrier shipping labels (AfterShip Shipping); a disabled no-op
	// when unconfigured, so GenerateShippingLabel reports labels-not-configured. shipFrom is the
	// warehouse origin address (from config) stamped on every generated label.
	labelProvider dependency.LabelProvider
	shipFrom      entity.LabelAddress
	// pwhash hashes passwords for admin-account management RPCs (create / reset).
	pwhash *pwhash.PasswordHasher
	// revalidateSem is a counting semaphore bounding concurrent async revalidations
	// spawned by revalidateAsync. Buffered to maxConcurrentRevalidations.
	revalidateSem chan struct{}
	// revalCtx is the server-scoped lifecycle context for async revalidations. It
	// is cancelled by StopRevalidation (from App.Stop) so in-flight best-effort
	// Vercel calls stop retrying at shutdown instead of outliving the process;
	// revalWG tracks the detached admin side effects — revalidations plus waitlist
	// notification, which runs uncancelled — so shutdown can wait for them (bounded).
	revalCtx    context.Context
	revalCancel context.CancelFunc
	// defectNormalLossRate is the P1 expected-waste threshold (config accounting), used by the
	// receipt command's final valuation so cost_price mirrors the ledger's abnormal write-off.
	defectNormalLossRate decimal.Decimal
	revalWG              sync.WaitGroup
	// embedAllowedHosts restricts the hosts allowed as hero EMBED iframe sources.
	// Empty means any https host is accepted (scheme/format validation still applies).
	embedAllowedHosts []string
	// ai is THE chat door (internal/aiprov/router): the route of each purpose, the fallback where no
	// money moved, one ledger row per physical call. Set by SetAIRouter (ai_router.go); nil is a
	// disabled router — every door then answers «not configured», never panics.
	ai *router.Router
	// analysisRuns is the spend fence in front of AnalyzeTechCardConstruction: who is running what,
	// when they last ran it, and how many runs this account has bought in the last hour. Its zero
	// value works — see analysisRunGuard for why that is deliberate rather than lazy.
	analysisRuns analysisRunGuard
	// noteFormatSem bounds concurrent markdown-assistant calls. NOT nil-safe on purpose: a nil
	// channel makes the acquire fall to its default branch, so a Server built without this field
	// refuses the RPC loudly instead of silently running with no ceiling at all.
	noteFormatSem chan struct{}
	// enhanceSem bounds concurrent EnhanceText calls (maxConcurrentEnhance). NOT nil-safe, on purpose
	// and for the same reason as noteFormatSem: a Server built without it refuses the RPC loudly
	// instead of running with no ceiling.
	enhanceSem chan struct{}
	// enhanceRuns is the per-admin hourly spend window in front of EnhanceText. Its zero value works
	// (lazy limiter), like analysisRuns: a fence that bounds SPEND must not depend on New() having run.
	enhanceRuns enhanceTextGuard
	// suggestCache holds SuggestPrompts answers for ten minutes (design_suggest.go). Zero value works.
	suggestCache suggestPromptsCache
	// suggestFlight coalesces identical SuggestPrompts misses in flight (G-03, Codex 11). Zero value works.
	suggestFlight singleflight.Group
	// quizFlight coalesces GenerateDesignQuiz presses of ONE card in flight (design_quiz.go): a double
	// click pays once and both presses get the same questions. Zero value works.
	quizFlight singleflight.Group
	// boardLabels — the per-card moodboard label sync (design_board_label.go, 101). Zero value works.
	boardLabels designBoardLabeller
	// partsFlight coalesces SuggestDesignParts presses of ONE (card, view, flat, cut) in flight
	// (design_parts.go). Zero value works.
	partsFlight singleflight.Group
	// calloutCache / calloutFlight — SuggestCallouts answers for ten minutes and identical presses
	// coalesced in flight (callout_suggest.go). Zero values work.
	calloutCache  calloutSuggestCache
	calloutFlight singleflight.Group
	// partsCardFlight coalesces SuggestDesignPartsCard presses of ONE (card, sides+flats, cut) in
	// flight (design_parts_card.go). Zero value works.
	partsCardFlight singleflight.Group
	// joinsFlight coalesces GenerateDesignJoins presses of ONE (card, source) in flight
	// (design_joins.go). Zero value works.
	joinsFlight singleflight.Group
	// designImageRunCap — the worker's wall-clock cap of an image run (SetDesignImageRunCap).
	designImageRunCap time.Duration
	// jpkTaxpayer is the Polish taxpayer identity (from JPK_* config) stamped into JPK_V7M exports.
	// Zero (unconfigured) → ExportJpkV7M returns FailedPrecondition instead of an invalid filing.
	jpkTaxpayer jpk.Taxpayer
	// designGenerationEnabled gates the PAID half of the DESIGN band (DESIGN_GENERATION_ENABLED).
	//
	// ⚠ THE ZERO VALUE IS «OFF», AND THAT IS THE WHOLE POINT. A Server built without
	// SetDesignGenerationEnabled refuses StartDesignRun and DraftDesignIdea in plain words rather
	// than opening a run that no worker exists to pick up — a run that would hold a budget
	// reservation and sit in `pending` until midnight while the screen says «generating».
	designGenerationEnabled bool
	// designKindGate answers, for one run kind, whether the generation pass would refuse it for
	// FREE — no route, no credentials, or an output the media store cannot keep. It is a function
	// rather than a value because the answer is COMPUTED (the route's Produces() crossed with the
	// sink's Accepts()); a stored list of "kinds that do not work" is exactly the thing that goes
	// stale silently on the day one of the two sides changes.
	//
	// Nil = not wired, and the door then lets the kind through. That is safe by construction and
	// not by hope: app.go sets this beside SetDesignGenerationEnabled, from the same `if enabled`
	// block that builds the worker, so a Server without the gate is a Server whose money flag is
	// off and which therefore refuses every paid verb one check earlier.
	designKindGate func(kind string) error
	// designEngines is the per-run engine table (designgen.EngineTable, PLAYGROUND phase 2): the
	// door validates and prices params.image against it, the band advertises it. Nil = no engine
	// is offered, and the door refuses every params.image.
	designEngines func() []designgen.Engine
	// designThreedRoute is the LIVE 3D route as the door reads it (B-24: the panel's `threed` route,
	// designgen.NewRoutedThreedProvider(...).View, app.go): which build options its head reads, the most
	// one build may book anywhere on the chain at this deployment's tariff, and whether the door is
	// closed. A function, asked afresh by every reader. Nil = not wired (the generation worker is off, or
	// a test): the band then advertises no build option and the door refuses a non-default one, and the
	// reserve keeps the static table.
	designThreedRoute func() designgen.ThreedRouteView
	// designFalRoutes are the fal JSON routes of kind extend / inpaint (PLAYGROUND phase 3; B-24: at the
	// route row's model, designgen.FalRoutesFunc over the live registry, app.go): the band, the door and
	// the reserve read one object, asked afresh by every reader. Nil, or a kind with no entry, is CLOSED
	// (fail closed: nothing on the door knows what the worker would book).
	designFalRoutes func() map[string]designgen.FalRoute
	// designVideoRoute — the live video route (B-32): the `video.generate` row's Kling slug and the
	// reserve per clip. nil = no video route wired (the table's default reserve, Kling's default slug).
	designVideoRoute func() designgen.VideoRoute
	// aiReg, aiKeyRing, aiProbeClient, aiReconcile — the admin → AI providers panel
	// (ai_providers.go, SetAIProviders). aiReg nil = not wired: the five RPCs refuse with
	// FailedPrecondition. A nil/disabled aiKeyRing refuses to store a key and says which variable
	// is missing.
	aiReg         *registry.Registry
	aiKeyRing     *keyring.Ring
	aiProbeClient *http.Client
	// aiReconcile starts one provider's cost fetch after an accepted admin-key save. Nil means the
	// worker is intentionally disabled; the handler then has no detached side effect.
	aiReconcile func(context.Context, string)
}

// New creates a new server with admin handlers.
func New(
	r dependency.Repository,
	b dependency.FileStore,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr dependency.StockReservationManager,
	ga4mpClient *ga4mp.Client,
	ph *pwhash.PasswordHasher,
	labelProvider dependency.LabelProvider,
	shipFrom entity.LabelAddress,
	embedAllowedHosts string,
	campaignTestRecipients string,
	jpkTaxpayer jpk.Taxpayer,
	defectNormalLossRate decimal.Decimal,
) (*Server, error) {
	renderer, err := campaignrender.New()
	if err != nil {
		return nil, fmt.Errorf("create campaign renderer: %w", err)
	}
	revalCtx, revalCancel := context.WithCancel(context.Background())
	return &Server{
		repo:            r,
		bucket:          b,
		mailer:          m,
		renderer:        renderer,
		campaignTestSem: make(chan struct{}, maxConcurrentCampaignTestSends),
		campaignTestRecipientAllowlist: parseCampaignTestRecipientAllowlist(
			campaignTestRecipients,
		),
		stripePayment:        stripePayment,
		stripePaymentTest:    stripePaymentTest,
		re:                   re,
		reservationMgr:       reservationMgr,
		ga4mp:                ga4mpClient,
		pwhash:               ph,
		labelProvider:        labelProvider,
		shipFrom:             shipFrom,
		revalidateSem:        make(chan struct{}, maxConcurrentRevalidations),
		revalCtx:             revalCtx,
		revalCancel:          revalCancel,
		defectNormalLossRate: defectNormalLossRate,
		embedAllowedHosts:    parseEmbedAllowedHosts(embedAllowedHosts),
		noteFormatSem:        make(chan struct{}, maxConcurrentNoteFormats),
		enhanceSem:           make(chan struct{}, maxConcurrentEnhance),
		jpkTaxpayer:          jpkTaxpayer,
		boardLabels:          designBoardLabeller{live: true},
	}, nil
}

const (
	adminListDefaultLimit = 50
	adminListMaxLimit     = 1000
)

// clampPagination bounds a client-supplied limit/offset for admin list endpoints
// so a huge limit can't force MySQL to materialize an entire (growing) table. The
// max is generous for admin bulk views while still capping pathological requests.
func clampPagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = adminListDefaultLimit
	}
	if limit > adminListMaxLimit {
		limit = adminListMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// revalidateAsync triggers storefront ISR revalidation in the background. Revalidation
// is a cache-freshness side effect, not part of the admin operation's success: blocking
// the RPC on it — and returning codes.Internal when Vercel is briefly unreachable — made
// successful admin writes look failed and could hang the admin UI for many seconds while
// RevalidateAll retried each deployment. Mirrors the frontend order-submit path.
//
// Concurrency is bounded by revalidateSem (capacity maxConcurrentRevalidations): the
// goroutine acquires a slot before running and releases it when done, so a burst of
// admin writes during a Vercel slowdown queues on the semaphore rather than spawning
// unbounded goroutines.
//
// The goroutine derives from s.revalCtx (a server-scoped lifecycle context) rather
// than the request context, so it survives the RPC returning but is still
// cancellable: StopRevalidation cancels revalCtx and waits on revalWG at shutdown,
// so best-effort Vercel calls stop retrying instead of outliving the process.
func (s *Server) revalidateAsync(data *dto.RevalidationData) {
	// Add before the goroutine starts so a concurrent StopRevalidation cannot
	// Wait() past an un-registered goroutine.
	s.revalWG.Add(1)
	go func() {
		defer s.revalWG.Done()
		// Best-effort background side effect: a panic in the revalidation path must
		// be logged with a stack and swallowed, never crash the whole process.
		defer saferun.Recover(s.revalCtx, "admin-revalidate")
		// Acquire a semaphore slot, queuing if maxConcurrentRevalidations are already
		// in flight, so concurrency stays bounded.
		s.revalidateSem <- struct{}{}
		defer func() { <-s.revalidateSem }()
		if err := s.re.RevalidateAll(s.revalCtx, data); err != nil {
			slog.Default().ErrorContext(s.revalCtx, "async storefront revalidation failed",
				slog.String("err", err.Error()),
			)
		}
	}()
}

// StopRevalidation cancels the server-scoped revalidation context and waits, bounded
// by ctx, for in-flight detached admin work to return. App.Stop calls it after the
// HTTP listener has drained — so no new revalidateAsync can be spawned — ensuring
// best-effort Vercel ISR calls don't keep retrying after the process is meant to be
// down. RevalidateAll touches no DB, but revalWG also tracks waitlist notification,
// which does — so calling this before the DB closes lets those mails finish queueing.
func (s *Server) StopRevalidation(ctx context.Context) {
	if s.revalCancel != nil {
		s.revalCancel()
	}
	done := make(chan struct{})
	go func() {
		s.revalWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Default().WarnContext(ctx, "timed out waiting for in-flight admin revalidations to drain")
	}
}

// StopRateLimiter ends the sweep goroutines of the admin's lazily built rate limiters — the per-admin
// hourly windows in front of EnhanceText and AnalyzeTechCardConstruction (review ENH-03). App.Stop
// calls it beside the frontend and auth StopRateLimiter, with the same contract: idempotent, and safe
// on a limiter that was never built. A press that races the drain still meets a working window.
func (s *Server) StopRateLimiter() {
	s.enhanceRuns.stop()
	s.analysisRuns.stop()
}

func (s *Server) getPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	switch pm {
	case entity.CARD:
		return s.stripePayment, nil
	case entity.CARD_TEST:
		return s.stripePaymentTest, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}

// SetPatternURLService wires the tokenized pattern url minter (Ф7). baseURL is this
// backend's external origin (no trailing slash); minted urls are absolute so <object>
// embeds and QR codes resolve against the backend, not the SPA origin.
func (s *Server) SetPatternURLService(svc *patternaccess.Service, baseURL string) {
	s.patternURLs = svc
	s.patternURLsBaseURL = baseURL
}

// SetRunPackTokenService wires the run-pack token minter (/api/rp). No base url here: the
// response carries the bare TOKEN and the admin builds the printed url from its own origin,
// exactly like pattern_viewer_token — the page that resolves it lives in the SPA.
func (s *Server) SetRunPackTokenService(svc *runpackaccess.Service) {
	s.runPackTokens = svc
}

// SetFileLinkService wires the public library-file link minter (/api/f, Ф7). Base url lives
// INSIDE the service (unlike the run pack above): эту ссылку копируют в мессенджер и открывают
// вне панели, поэтому она обязана быть абсолютной и собранной одним местом — тем же, что её
// потом разбирает.
func (s *Server) SetFileLinkService(svc *fileaccess.Service) {
	s.fileLinks = svc
}
