package admin

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
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

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo              dependency.Repository
	bucket            dependency.FileStore
	mailer            dependency.Mailer
	stripePayment     dependency.Invoicer
	stripePaymentTest dependency.Invoicer
	re                dependency.RevalidationService
	reservationMgr    dependency.StockReservationManager
	ga4mp             *ga4mp.Client
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
	// revalWG tracks those goroutines so shutdown can wait for them (bounded).
	revalCtx    context.Context
	revalCancel context.CancelFunc
	revalWG     sync.WaitGroup
	// embedAllowedHosts restricts the hosts allowed as hero EMBED iframe sources.
	// Empty means any https host is accepted (scheme/format validation still applies).
	embedAllowedHosts []string
	// aiOps drafts tech-card sewing operations from a plain-language description via
	// OpenRouter (#66). It is nil-safe/disabled when OPENROUTER_API_KEY is unset, so
	// GenerateTechCardOperations degrades to a clear FailedPrecondition instead of failing.
	aiOps *openrouter.Client
	// jpkTaxpayer is the Polish taxpayer identity (from JPK_* config) stamped into JPK_V7M exports.
	// Zero (unconfigured) → ExportJpkV7M returns FailedPrecondition instead of an invalid filing.
	jpkTaxpayer jpk.Taxpayer
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
	aiOps *openrouter.Client,
	jpkTaxpayer jpk.Taxpayer,
) *Server {
	revalCtx, revalCancel := context.WithCancel(context.Background())
	return &Server{
		repo:              r,
		bucket:            b,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		reservationMgr:    reservationMgr,
		ga4mp:             ga4mpClient,
		pwhash:            ph,
		labelProvider:     labelProvider,
		shipFrom:          shipFrom,
		revalidateSem:     make(chan struct{}, maxConcurrentRevalidations),
		revalCtx:          revalCtx,
		revalCancel:       revalCancel,
		embedAllowedHosts: parseEmbedAllowedHosts(embedAllowedHosts),
		aiOps:             aiOps,
		jpkTaxpayer:       jpkTaxpayer,
	}
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
// by ctx, for in-flight async revalidations to return. App.Stop calls it after the
// HTTP listener has drained — so no new revalidateAsync can be spawned — ensuring
// best-effort Vercel ISR calls don't keep retrying after the process is meant to be
// down. RevalidateAll touches no DB, so this is ordering-independent of the DB close.
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
