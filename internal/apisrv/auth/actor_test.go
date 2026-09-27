package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// countingAdmins is the one Admin method the interceptor may call, counted per username. Every
// other method panics on the nil embedded interface: the interceptor must not grow a second read.
type countingAdmins struct {
	dependency.Admin

	mu    sync.Mutex
	calls map[string]int
	ids   map[string]int
	err   error
}

func (f *countingAdmins) GetAdminByUsername(ctx context.Context, username string) (*entity.Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[username]++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	id, ok := f.ids[username]
	if !ok {
		return nil, errors.New("sql: no rows in result set")
	}
	return &entity.Admin{Id: id, Username: username}, nil
}

func (f *countingAdmins) callsFor(username string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[username]
}

func (f *countingAdmins) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// actorHarness is an auth server over countingAdmins with a settable clock, and a way to push one
// admin RPC through the interceptor and read the actor the handler saw.
type actorHarness struct {
	srv   *Server
	store *countingAdmins
	now   time.Time
}

func newActorHarness(t *testing.T) *actorHarness {
	t.Helper()
	store := &countingAdmins{calls: map[string]int{}, ids: map[string]int{"alice": 42}}
	srv, err := New(&Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 1000,
		JWTTTL:                   "60m",
	}, store)
	require.NoError(t, err)
	t.Cleanup(srv.StopRateLimiter)
	h := &actorHarness{srv: srv, store: store, now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	srv.now = func() time.Time { return h.now }
	return h
}

func (h *actorHarness) call(t *testing.T, ctx context.Context, username, method string) (aiprov.Actor, bool) {
	t.Helper()
	tok, err := authjwt.NewAdminToken(h.srv.JwtAuth, time.Hour, username, true, nil, nil)
	require.NoError(t, err)
	ctx = metadata.NewIncomingContext(ctx, metadata.New(map[string]string{
		strings.ToLower(AuthMetadataKey): "Bearer " + tok,
	}))
	var seen aiprov.Actor
	called := false
	_, err = h.srv.UnaryAdminAuthInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, _ any) (any, error) {
			called = true
			seen = aiprov.ActorFrom(ctx)
			require.Equal(t, username, GetAdminUsername(ctx), "the username door is unchanged")
			return "ok", nil
		})
	require.NoError(t, err, "the actor lookup must never fail the RPC")
	return seen, called
}

const adminMethod = "/admin.AdminService/GetProduct"

// TestInterceptorPutsTheAIActor — an admin RPC carries the AI actor: the JWT username and the
// admins.id, looked up ONCE per username per five minutes.
//
// MUTATION: adminIDTTL = 0 in New (the cache window shrunk to nothing) → red: the second call hits
// the store again.
// MUTATION: drop the WithActor line → red (actor "unknown", no id).
func TestInterceptorPutsTheAIActor(t *testing.T) {
	h := newActorHarness(t)

	a, called := h.call(t, context.Background(), "alice", adminMethod)
	require.True(t, called)
	require.Equal(t, "alice", a.Username)
	require.NotNil(t, a.AdminID)
	require.Equal(t, 42, *a.AdminID)
	require.Equal(t, 1, h.store.callsFor("alice"))

	// Inside the window: the same actor, no second DB call.
	h.now = h.now.Add(adminIDCacheTTL - time.Second)
	a, _ = h.call(t, context.Background(), "alice", adminMethod)
	require.Equal(t, 42, *a.AdminID)
	require.Equal(t, 1, h.store.callsFor("alice"), "no extra DB call within the cache window")

	// Past it: one fresh lookup.
	h.now = h.now.Add(2 * time.Second)
	a, _ = h.call(t, context.Background(), "alice", adminMethod)
	require.Equal(t, 42, *a.AdminID)
	require.Equal(t, 2, h.store.callsFor("alice"))
}

// TestInterceptorActorSurvivesALookupFailure — a store error gives the actor with a nil id, the RPC
// goes on, the failure is cached for the window and warned about once.
//
// MUTATION: return the lookup error from the interceptor → red (the RPC fails).
// MUTATION: skip s.adminIDs.Store on the error path → red (the second call looks up and warns again).
func TestInterceptorActorSurvivesALookupFailure(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newActorHarness(t)
	h.store.err = errors.New("db is down")

	for range 2 {
		a, called := h.call(t, context.Background(), "bob", adminMethod)
		require.True(t, called)
		require.Equal(t, "bob", a.Username)
		require.Nil(t, a.AdminID)
	}
	require.Equal(t, 1, h.store.callsFor("bob"))
	require.Equal(t, 1, strings.Count(logs.String(), "ai actor: admin id lookup failed"), logs.String())

	// An unknown username (no row) reads the same way: nil id, no failure.
	h.store.err = nil
	a, _ := h.call(t, context.Background(), "carol", adminMethod)
	require.Equal(t, "carol", a.Username)
	require.Nil(t, a.AdminID)
}

// TestInterceptorActorLookupOnlyOnAdminRPCs — a storefront RPC and a refused token look nothing up;
// an RPC whose own context is already cancelled does not poison the cache for the next one.
func TestInterceptorActorLookupOnlyOnAdminRPCs(t *testing.T) {
	h := newActorHarness(t)

	_, err := h.srv.UnaryAdminAuthInterceptor()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/frontend.FrontendService/GetProduct"},
		func(ctx context.Context, _ any) (any, error) {
			require.Equal(t, aiprov.ActorUnknown, aiprov.ActorFrom(ctx).Username)
			return "ok", nil
		})
	require.NoError(t, err)

	refused := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		strings.ToLower(AuthMetadataKey): "Bearer not-a-token",
	}))
	_, err = h.srv.UnaryAdminAuthInterceptor()(refused, nil, &grpc.UnaryServerInfo{FullMethod: adminMethod},
		func(context.Context, any) (any, error) { return "ok", nil })
	require.Error(t, err)
	require.Equal(t, 0, h.store.total(), "no lookup before the token is verified and authorized")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	a, _ := h.call(t, cancelled, "alice", adminMethod)
	require.Nil(t, a.AdminID)
	a, _ = h.call(t, context.Background(), "alice", adminMethod)
	require.NotNil(t, a.AdminID, "a cancelled request must not cache a nil id for the next one")
	require.Equal(t, 42, *a.AdminID)
}
