package auth

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// teamAdmins answers from the team in ctx: two teams, the same username, different rows.
type teamAdmins struct {
	dependency.Admin
	rows map[int64]map[string]entity.Admin
	// park, when set, holds team parkTeam's lookups until it is closed.
	park     chan struct{}
	parkTeam int64
}

func (f *teamAdmins) GetAdminByUsername(ctx context.Context, username string) (*entity.Admin, error) {
	t, _ := tenant.From(ctx)
	if f.park != nil && t.ID == f.parkTeam {
		<-f.park
	}
	a, ok := f.rows[t.ID][username]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &a, nil
}

func newTeamsAuth(t *testing.T, store *teamAdmins) *Server {
	t.Helper()
	srv, err := New(&Config{
		JWTSecret: jwtSecret, MasterPassword: masterPassword,
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "60m",
	}, store)
	require.NoError(t, err)
	t.Cleanup(srv.StopRateLimiter)
	return srv
}

var (
	teamA = tenant.Team{ID: 2, Slug: "alpha", Status: tenant.StatusActive}
	teamB = tenant.Team{ID: 3, Slug: "bravo", Status: tenant.StatusActive}
)

// Two teams, one username "anna": each team gets its own admin id and its own account state, and a
// lookup in flight for one team is never joined by the other.
func TestAuthCachesAreKeyedByTeamAndUsername(t *testing.T) {
	store := &teamAdmins{rows: map[int64]map[string]entity.Admin{
		2: {"anna": {Id: 42, Username: "anna", Disabled: true}},
		3: {"anna": {Id: 77, Username: "anna"}},
	}}
	srv := newTeamsAuth(t, store)
	ctxA, ctxB := tenant.With(context.Background(), teamA), tenant.With(context.Background(), teamB)

	idA, idB := srv.adminIDFor(ctxA, "anna"), srv.adminIDFor(ctxB, "anna")
	require.NotNil(t, idA)
	require.NotNil(t, idB)
	require.Equal(t, 42, *idA)
	require.Equal(t, 77, *idB, "team B must not read team A's cached id")

	require.Error(t, srv.checkAdminAccountState(ctxA, "anna"), "anna is disabled in team A")
	require.NoError(t, srv.checkAdminAccountState(ctxB, "anna"), "team A's disabled anna must not lock out team B's")

	// A's invalidation leaves B's cached answer alone; B's anna stays enabled.
	srv.InvalidateAdminAccount(ctxA, "anna")
	_, ok := srv.cachedAdminAccountState(tenant.Key(ctxB, "anna"))
	require.True(t, ok)

	// Flights: team A's lookup is parked; team B's own lookup still answers at once.
	parked := &teamAdmins{rows: store.rows, park: make(chan struct{}), parkTeam: 2}
	srv2 := newTeamsAuth(t, parked)
	done := make(chan *int, 1)
	go func() { done <- srv2.adminIDFor(ctxA, "anna") }()
	time.Sleep(20 * time.Millisecond) // let A start its flight
	gotB := make(chan *int, 1)
	go func() { gotB <- srv2.adminIDFor(ctxB, "anna") }()
	select {
	case id := <-gotB:
		require.NotNil(t, id)
		require.Equal(t, 77, *id, "team B started its own flight")
	case <-time.After(2 * time.Second):
		t.Fatal("team B joined team A's parked flight")
	}
	close(parked.park)
	require.Equal(t, 42, *<-done)
}
