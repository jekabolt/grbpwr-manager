package teamfair

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BusyCode is the status message of a refusal: one machine word the client can match.
const BusyCode = "team_busy"

// ShopBusyCode is the refusal of a storefront call: the team's shop share is used up.
const ShopBusyCode = "shop_busy"

// uploadReadTimeout bounds how long an upload's body may take to arrive (the server has no
// ReadTimeout: it also carries long streams). 256 MiB at ~4 Mbit/s fits.
const uploadReadTimeout = 10 * time.Minute

// warnEvery bounds the WARN log of refusals: at most one line per key per minute.
const warnEvery = time.Minute

// Edge adapts a Gate to the gRPC chain and to plain HTTP handlers.
type Edge struct {
	g *Gate
	// shop reports a storefront method: it takes the Shop lane (its team's shop share), never the
	// team's admin share. nil = none.
	shop func(fullMethod string) bool

	mu   sync.Mutex
	warn map[warnKey]time.Time
}

// warnKey throttles refusal logs per team and per share.
type warnKey struct {
	k    Key
	shop bool
}

// NewEdge wraps g (nil g = pass-through). shop marks storefront methods, which take the Shop lane.
func NewEdge(g *Gate, shop func(fullMethod string) bool) *Edge {
	return &Edge{g: g, shop: shop, warn: map[warnKey]time.Time{}}
}

// Gate returns the gate behind the edge.
func (e *Edge) Gate() *Gate {
	if e == nil {
		return nil
	}
	return e.g
}

func (e *Edge) logBusy(ctx context.Context, k Key, lane Lane, err error) {
	wk := warnKey{k: k, shop: lane == Shop}
	e.mu.Lock()
	last, seen := e.warn[wk]
	now := time.Now()
	if seen && now.Sub(last) < warnEvery {
		e.mu.Unlock()
		return
	}
	e.warn[wk] = now
	e.mu.Unlock()
	slog.Default().WarnContext(ctx, "team share used up; call refused",
		slog.String("key", k.String()), slog.String("lane", lane.String()), slog.String("err", err.Error()))
}

// refusal maps an Acquire error of lane to the gRPC answer.
func refusal(lane Lane, err error) error {
	if IsBusy(err) {
		if lane == Shop {
			return status.Error(codes.ResourceExhausted, ShopBusyCode)
		}
		return status.Error(codes.ResourceExhausted, BusyCode)
	}
	return status.FromContextError(err).Err()
}

// Unary gates a unary call by the team in its context. Put it after the admin auth interceptor:
// in teams mode the team of an admin call is bound there. No team = not gated. A storefront call
// takes the Shop lane of the shop's team (bound by its host).
func (e *Edge) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if e == nil || e.g == nil || e.g.cfg.Off {
			return handler(ctx, req)
		}
		t, ok := tenant.From(ctx)
		if !ok {
			return handler(ctx, req)
		}
		k := Key{TeamID: t.ID}
		lane := LaneOf(info.FullMethod)
		if e.shop != nil && e.shop(info.FullMethod) {
			lane = Shop
		}
		release, err := e.g.Acquire(ctx, k, lane)
		if err != nil {
			if IsBusy(err) {
				e.logBusy(ctx, k, lane, err)
			}
			return nil, refusal(lane, err)
		}
		defer release()
		return handler(ctx, req)
	}
}

// HTTP gates a plain HTTP handler (an admin upload) as one call of lane. Wrap it INSIDE the auth
// wrapper, which binds the team. Busy = 429 {"error":"team_busy"}. For Upload the body read gets a
// deadline (uploadReadTimeout) once the slot is taken, so a stalled client frees the slot.
func (e *Edge) HTTP(lane Lane, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e == nil || e.g == nil || e.g.cfg.Off {
			next.ServeHTTP(w, r)
			return
		}
		t, ok := tenant.From(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		k := Key{TeamID: t.ID}
		release, err := e.g.Acquire(r.Context(), k, lane)
		if err != nil {
			if !IsBusy(err) {
				return // the client went away while waiting
			}
			e.logBusy(r.Context(), k, lane, err)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"` + BusyCode + `"}`))
			return
		}
		defer release()
		if lane == Upload {
			// Best effort: a writer that cannot set deadlines (a test recorder) is left as is.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadTimeout))
		}
		next.ServeHTTP(w, r)
	})
}
