package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// teamsStore is the two-database fixture of the pin: one fakeStore per team, picked by the team the
// call's ctx carries — what the routing pool does with a real database. A call without a team fails
// like the strict pool (tenant.ErrNoTenant).
type teamsStore struct {
	dependency.AI // the methods the registry never calls stay nil and panic if called
	byTeam        map[int64]*fakeStore
}

func (s *teamsStore) pick(ctx context.Context) (*fakeStore, error) {
	t, ok := tenant.From(ctx)
	if !ok {
		return nil, tenant.ErrNoTenant
	}
	fs, ok := s.byTeam[t.ID]
	if !ok {
		return nil, fmt.Errorf("no database for team %d", t.ID)
	}
	return fs, nil
}

func (s *teamsStore) GetConfig(ctx context.Context) (*entity.AIConfig, error) {
	fs, err := s.pick(ctx)
	if err != nil {
		return nil, err
	}
	return fs.GetConfig(ctx)
}

func (s *teamsStore) ConfigVersion(ctx context.Context) (uint64, error) {
	fs, err := s.pick(ctx)
	if err != nil {
		return 0, err
	}
	return fs.ConfigVersion(ctx)
}

func (s *teamsStore) SetProviderKey(ctx context.Context, key string, kind entity.AIKeyKind, enc []byte, last4, by string) error {
	fs, err := s.pick(ctx)
	if err != nil {
		return err
	}
	return fs.SetProviderKey(ctx, key, kind, enc, last4, by)
}

type pinDir struct{ teams map[int64]tenant.Team }

func (d pinDir) ByID(_ context.Context, id int64) (tenant.Team, error) {
	t, ok := d.teams[id]
	if !ok {
		return tenant.Team{}, tenant.ErrNotFound
	}
	return t, nil
}
func (pinDir) BySlug(context.Context, string) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (d pinDir) ListActive(context.Context) ([]tenant.Team, error) {
	return []tenant.Team{d.teams[1], d.teams[2]}, nil
}
func (pinDir) Invalidate(int64) {}

func twoTeams(t *testing.T) (*teamsStore, pinDir, context.Context) {
	t.Helper()
	one := seedConfig()
	two := seedConfig()
	// Team 2's copy of the tables is inert: a different zone and version, openrouter switched off.
	two.BudgetTimezone = "Asia/Tokyo"
	two.Settings.ConfigVersion = 77
	provider(&two, entity.AIProviderOpenRouter).Enabled = false
	st := &teamsStore{byTeam: map[int64]*fakeStore{1: {cfg: one}, 2: {cfg: two}}}
	dir := pinDir{teams: map[int64]tenant.Team{
		1: tenant.TeamOne("team1db"),
		2: {ID: 2, Slug: "ostra", DBName: "lkb_t_aaaaaaaa", Profile: tenant.ProfileStandard, Status: tenant.StatusActive},
	}}
	return st, dir, tenant.With(context.Background(), dir.teams[2])
}

func pinTeamOne(dir tenant.Directory) Option {
	return WithPin(func(ctx context.Context) (context.Context, error) {
		return tenant.Pinned(ctx, dir, tenant.TeamOneID)
	})
}

func TestPinReadsTeamOneWhateverTheCtx(t *testing.T) {
	st, dir, team2 := twoTeams(t)
	r := New(st, testRing(t), EnvKeys{}, pinTeamOne(dir))

	require.NoError(t, r.Reload(team2))
	require.Equal(t, "Europe/Warsaw", r.BudgetTimezone(), "the zone is team #1's")
	require.Equal(t, uint64(1), r.Version(), "the version is team #1's")
	require.Equal(t, seedKeys[entity.AIProviderOpenRouter], r.KeyFunc(entity.AIProviderOpenRouter)(),
		"team #1's openrouter row answers; team 2's switched-off copy is inert")

	g1, _ := st.byTeam[1].calls()
	g2, v2 := st.byTeam[2].calls()
	require.Equal(t, 1, g1)
	require.Zero(t, g2+v2, "team 2's database is never read")

	// The poller on a team-2 ctx compares team #1's version (no reload when it did not move).
	require.True(t, r.pollOnce(team2))
	_, v1 := st.byTeam[1].calls()
	require.Equal(t, 1, v1)
	_, v2 = st.byTeam[2].calls()
	require.Zero(t, v2)
}

func TestPinImportWritesTeamOneOnly(t *testing.T) {
	st, dir, team2 := twoTeams(t)
	st.byTeam[1].cfg = entity.AIConfig{Providers: []entity.AIProvider{{Key: entity.AIProviderFal}}, Settings: entity.AISettings{ConfigVersion: 1}}
	r := New(st, testRing(t), EnvKeys{Fal: "env-fal-cccc"}, pinTeamOne(dir))

	imported, err := r.ImportEnvKeys(team2)
	require.NoError(t, err)
	require.Equal(t, []string{entity.AIProviderFal}, imported)
	require.Len(t, st.byTeam[1].writes(), 1, "the key lands in team #1's slot")
	require.Empty(t, st.byTeam[2].writes(), "never in another team's database")
}

func TestPinErrorKeepsTheLastSnapshot(t *testing.T) {
	st, dir, team2 := twoTeams(t)
	fail := false
	errPin := errors.New("control plane down")
	r := New(st, testRing(t), EnvKeys{}, WithPin(func(ctx context.Context) (context.Context, error) {
		if fail {
			return ctx, errPin
		}
		return tenant.Pinned(ctx, dir, tenant.TeamOneID)
	}))
	require.NoError(t, r.Reload(team2))

	fail = true
	err := r.Reload(team2)
	require.ErrorIs(t, err, errPin)
	require.Equal(t, "Europe/Warsaw", r.BudgetTimezone(), "the previous snapshot stays in force")
	require.False(t, r.pollOnce(team2), "a pin error is a failed poll")
	_, err = r.ImportEnvKeys(team2)
	require.ErrorIs(t, err, errPin)
}

func TestPinOffIsTheIdentity(t *testing.T) {
	st, _, team2 := twoTeams(t)
	r := New(st, testRing(t), EnvKeys{}, WithPin(nil))
	require.NoError(t, r.Reload(team2))
	require.Equal(t, "Asia/Tokyo", r.BudgetTimezone(), "without a pin the store sees the caller's ctx unchanged")
}
