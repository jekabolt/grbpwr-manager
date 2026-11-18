package airollup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSpikeRule(t *testing.T) {
	abs := d("20")
	cases := []struct {
		name          string
		today, median string
		want          bool
	}{
		{"above the absolute line", "20.01", "50", true},
		{"at the absolute line", "20", "50", false},
		{"three times the median", "3.01", "1", true},
		{"exactly three times", "3", "1", false},
		{"ratio below the floor", "0.99", "0.01", false},
		{"first dollar on a zero history", "1", "0", true},
		{"usual day", "5", "4", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, IsSpike(d(c.today), d(c.median), abs))
		})
	}
}

func TestSpikeMedianCountsMissingDaysAsZero(t *testing.T) {
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	byDay := map[string]decimal.Decimal{
		"2026-11-17": d("10"), "2026-11-16": d("10"), "2026-11-15": d("10"),
		"2026-11-14": d("2"), "2026-11-10": d("99"), // the 10th is outside the 7 days
	}
	// 10 10 10 2 0 0 0 → sorted 0 0 0 2 10 10 10 → median 2
	require.True(t, medianOfPrevious(today, byDay).Equal(d("2")))
}

// ───────────────────────── fakes ─────────────────────────

type fakeDir struct{ teams []tenant.Team }

func (f fakeDir) ByID(_ context.Context, id int64) (tenant.Team, error) {
	for _, t := range f.teams {
		if t.ID == id {
			return t, nil
		}
	}
	return tenant.Team{}, tenant.ErrNotFound
}
func (fakeDir) BySlug(context.Context, string) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (f fakeDir) ListActive(context.Context) ([]tenant.Team, error) { return f.teams, nil }
func (fakeDir) Invalidate(int64)                                    {}

// ledgers answers the usage query from the team the ctx carries.
type ledgers struct {
	mu    sync.Mutex
	rows  map[int64][]dayUsage
	fail  map[int64]error
	since map[int64][]string
}

func (l *ledgers) SelectContext(ctx context.Context, dest any, query string, args ...any) error {
	t, ok := tenant.From(ctx)
	if !ok {
		return tenant.ErrNoTenant
	}
	if !strings.Contains(query, "FROM ai_usage_event") {
		return errors.New("unexpected query")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.since == nil {
		l.since = map[int64][]string{}
	}
	l.since[t.ID] = append(l.since[t.ID], args[0].(string))
	if err := l.fail[t.ID]; err != nil {
		return err
	}
	*(dest.(*[]dayUsage)) = append([]dayUsage(nil), l.rows[t.ID]...)
	return nil
}

type sinkKey struct {
	team int64
	day  string
}

type fakeSink struct {
	mu   sync.Mutex
	rows map[sinkKey]platformstore.AISpend
}

func (s *fakeSink) UpsertAISpend(_ context.Context, teamID int64, day time.Time, v platformstore.AISpend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[sinkKey]platformstore.AISpend{}
	}
	s.rows[sinkKey{teamID, day.Format(time.DateOnly)}] = v
	return nil
}

func (s *fakeSink) AISpendSince(_ context.Context, day time.Time) (map[int64]platformstore.AISpend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]platformstore.AISpend{}
	for k, v := range s.rows {
		if k.day >= day.Format(time.DateOnly) {
			cur := out[k.team]
			cur.CostUSD = cur.CostUSD.Add(v.CostUSD)
			cur.Calls += v.Calls
			cur.Unpriced += v.Unpriced
			out[k.team] = cur
		}
	}
	return out, nil
}

var (
	teamOne   = tenant.TeamOne("team1db")
	teamOstra = tenant.Team{ID: 2, Slug: "ostra", DBName: "lkb_t_aaaaaaaa", Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	teamSusp  = tenant.Team{ID: 3, Slug: "sleepy", DBName: "lkb_t_bbbbbbbb", Profile: tenant.ProfileStandard, Status: tenant.StatusSuspended}
)

func newTestWorker(t *testing.T, l *ledgers, s *fakeSink, now time.Time) (*Worker, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := New(Config{
		Dir:   fakeDir{teams: []tenant.Team{teamOne, teamOstra, teamSusp}},
		Teams: l, Sink: s,
		TZ:  func() string { return "Europe/Warsaw" },
		Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	require.NoError(t, err)
	return w, &buf
}

func TestRollupPerTeamFailureAndSpike(t *testing.T) {
	// 23:30 UTC on the 17th is already the 18th in Warsaw: days are the owner's budget days.
	now := time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	l := &ledgers{
		rows: map[int64][]dayUsage{
			2: {{Day: "2026-11-18", CostUSD: d("25.5"), Unpriced: 2, Calls: 40}, {Day: "2026-11-17", CostUSD: d("1"), Calls: 3}},
			3: {{Day: "2026-11-02", CostUSD: d("4"), Calls: 9}},
		},
		fail: map[int64]error{1: errors.New("team #1 database down")},
	}
	s := &fakeSink{}
	w, logs := newTestWorker(t, l, s, now)

	w.RunOnce(context.Background())

	require.Equal(t, platformstore.AISpend{CostUSD: d("25.5"), Unpriced: 2, Calls: 40}, s.rows[sinkKey{2, "2026-11-18"}],
		"team 2's spend lands under team 2's id, on the owner's day")
	require.Equal(t, int64(9), s.rows[sinkKey{3, "2026-11-02"}].Calls, "a suspended team is still rolled up")
	for k := range s.rows {
		require.NotEqual(t, int64(1), k.team, "the failing team wrote nothing")
	}
	out := logs.String()
	require.Contains(t, out, EventFailed)
	require.Contains(t, out, "slug=grbpwr")
	require.Equal(t, 1, strings.Count(out, "msg="+EventSpike), "team 2 spent $25.50 > $20")
	require.Contains(t, out, "slug=ostra")

	// First sight = the whole month; afterwards the 7-day history only.
	require.Equal(t, []string{"2026-11-01"}, l.since[2])
	w.RunOnce(context.Background())
	require.Equal(t, []string{"2026-11-01", "2026-11-11"}, l.since[2])
	require.Equal(t, 1, strings.Count(logs.String(), "msg="+EventSpike), "once per team per day")
}

func TestRollupSkipsTheMonthForATeamTheControlPlaneKnows(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s := &fakeSink{rows: map[sinkKey]platformstore.AISpend{{2, "2026-11-03"}: {Calls: 1}}}
	l := &ledgers{}
	w, _ := newTestWorker(t, l, s, now)
	w.RunOnce(context.Background())
	require.Equal(t, []string{"2026-11-11"}, l.since[2], "known this month: no backfill after a restart")
	require.Equal(t, []string{"2026-11-01"}, l.since[3])
}

// ───────────────────────── team loops ─────────────────────────

func TestWorkLoopServesActiveTeamsWithAIOn(t *testing.T) {
	off := tenant.Team{ID: 4, Slug: "noai", DBName: "lkb_t_cccccccc", Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	dir := fakeDir{teams: []tenant.Team{teamOne, teamOstra, teamSusp, off}}
	broken := tenant.Team{ID: 5, Slug: "broken", Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	dir.teams = append(dir.teams, broken)
	loop := WorkLoop(dir, func(_ context.Context, id int64) (map[string]bool, error) {
		switch id {
		case 4:
			return map[string]bool{string(feature.AI): false}, nil
		case 5:
			return nil, errors.New("control plane hiccup")
		}
		return nil, nil
	})
	var got []string
	require.NoError(t, loop(context.Background(), func(ctx context.Context) error {
		tm, ok := tenant.From(ctx)
		require.True(t, ok)
		got = append(got, tm.Slug)
		return nil
	}))
	require.Equal(t, []string{"grbpwr", "ostra"}, got, "suspended, ai-off and unreadable teams get no work")
}

func TestWorkLoopTeamsModeOff(t *testing.T) {
	dir, err := tenant.NewStaticDirectory(teamOne)
	require.NoError(t, err)
	var got []int64
	require.NoError(t, WorkLoop(dir, nil)(context.Background(), func(ctx context.Context) error {
		tm, _ := tenant.From(ctx)
		got = append(got, tm.ID)
		return nil
	}))
	require.Equal(t, []int64{1}, got)
}

func TestSweepLoopServesSuspendedTeams(t *testing.T) {
	var got []string
	require.NoError(t, SweepLoop(fakeDir{teams: []tenant.Team{teamOne, teamSusp}})(context.Background(), func(ctx context.Context) error {
		tm, _ := tenant.From(ctx)
		got = append(got, tm.Slug)
		return nil
	}))
	require.Equal(t, []string{"grbpwr", "sleepy"}, got)
}
