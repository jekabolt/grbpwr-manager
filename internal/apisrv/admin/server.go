package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
	// pwhash hashes passwords for admin-account management RPCs (create / reset).
	pwhash *pwhash.PasswordHasher
	// revalidateSem is a counting semaphore bounding concurrent async revalidations
	// spawned by revalidateAsync. Buffered to maxConcurrentRevalidations.
	revalidateSem chan struct{}
	// embedAllowedHosts restricts the hosts allowed as hero EMBED iframe sources.
	// Empty means any https host is accepted (scheme/format validation still applies).
	embedAllowedHosts []string
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
	embedAllowedHosts string,
) *Server {
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
		revalidateSem:     make(chan struct{}, maxConcurrentRevalidations),
		embedAllowedHosts: parseEmbedAllowedHosts(embedAllowedHosts),
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
// TODO: the goroutine uses context.Background() so the in-flight revalidation survives
// the request returning, but it also means these goroutines outlive process shutdown.
// The admin Server has no cancellable lifecycle context to derive from; wiring one would
// require changes in app/app.go (out of scope for this fix).
func (s *Server) revalidateAsync(data *dto.RevalidationData) {
	go func() {
		ctx := context.Background()
		// Acquire a semaphore slot, queuing if maxConcurrentRevalidations are already
		// in flight, so concurrency stays bounded.
		s.revalidateSem <- struct{}{}
		defer func() { <-s.revalidateSem }()
		if err := s.re.RevalidateAll(ctx, data); err != nil {
			slog.Default().ErrorContext(ctx, "async storefront revalidation failed",
				slog.String("err", err.Error()),
			)
		}
	}()
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
