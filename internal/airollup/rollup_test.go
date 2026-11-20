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

	// Every run re-reads from min(month start, today − 7): the 1st here, the 11st being later.
	require.Equal(t, []string{"2026-11-01"}, l.since[2])
	w.RunOnce(context.Background())
	require.Equal(t, []string{"2026-11-01", "2026-11-01"}, l.since[2], "no marker: the second run re-reads the month too")
	require.Equal(t, 1, strings.Count(logs.String(), "msg="+EventSpike), "once per team per day")
}

// A first pass cut off half way (the control plane already holds one day of the month) must not be
// taken for a complete month: the next run copies the older days it missed.
func TestRollupRecoversAPartialFirstPass(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s := &fakeSink{rows: map[sinkKey]platformstore.AISpend{{2, "2026-11-01"}: {CostUSD: d("1"), Calls: 1}}}
	l := &ledgers{rows: map[int64][]dayUsage{2: {
		{Day: "2026-11-01", CostUSD: d("1"), Calls: 1},
		{Day: "2026-11-02", CostUSD: d("2"), Calls: 2},
		{Day: "2026-11-12", CostUSD: d("3"), Calls: 3},
	}}}
	w, _ := newTestWorker(t, l, s, now)
	w.RunOnce(context.Background())
	require.Equal(t, []string{"2026-11-01"}, l.since[2], "partial plane data: still the whole month")
	require.Equal(t, int64(2), s.rows[sinkKey{2, "2026-11-02"}].Calls, "the 2nd, older than 7 days, is copied")

	// Early in a month the 7-day history reaches into the previous month.
	l2 := &ledgers{}
	w2, _ := newTestWorker(t, l2, &fakeSink{}, time.Date(2026, 12, 3, 12, 0, 0, 0, time.UTC))
	w2.RunOnce(context.Background())
	require.Equal(t, []string{"2026-11-26"}, l2.since[2])
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
		case teamOstra.ID:
			// ai is off by default since X7: ostra is the team an operator switched it on for
			return map[string]bool{string(feature.AI): true}, nil
		}
		return nil, nil
	}, 1)
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
	require.NoError(t, WorkLoop(dir, nil, 0)(context.Background(), func(ctx context.Context) error {
		tm, _ := tenant.From(ctx)
		got = append(got, tm.ID)
		return nil
	}))
	require.Equal(t, []int64{1}, got)
}

// Cleanup serves every listed team — suspended and `ai`-off included — whatever the work loop says.
func TestCleanupLoopServesSuspendedAndAIOffTeams(t *testing.T) {
	off := tenant.Team{ID: 4, Slug: "noai", Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	var mu sync.Mutex
	var got []string
	require.NoError(t, CleanupLoop(fakeDir{teams: []tenant.Team{teamOne, teamSusp, off}}, 1)(context.Background(), func(ctx context.Context) error {
		tm, _ := tenant.From(ctx)
		mu.Lock()
		got = append(got, tm.Slug)
		mu.Unlock()
		return nil
	}))
	require.Equal(t, []string{"grbpwr", "sleepy", "noai"}, got)
}

// A slow team holds one slot only: with parallel 2, the teams after it are served while it hangs,
// and never more than two run at once.
func TestLoopSlowTeamDoesNotBlockTheOthers(t *testing.T) {
	teams := []tenant.Team{teamOne, teamOstra, teamSusp, {ID: 4, Slug: "four", Status: tenant.StatusActive}}
	release := make(chan struct{})
	var (
		mu        sync.Mutex
		inFlight  int
		maxFlight int
		others    = make(chan string, 4)
	)
	done := make(chan error, 1)
	go func() {
		done <- CleanupLoop(fakeDir{teams: teams}, 2)(context.Background(), func(ctx context.Context) error {
			tm, _ := tenant.From(ctx)
			mu.Lock()
			inFlight++
			if inFlight > maxFlight {
				maxFlight = inFlight
			}
			mu.Unlock()
			defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
			if tm.ID == 1 {
				<-release // team #1 hangs
				return nil
			}
			others <- tm.Slug
			return nil
		})
	}()
	for range 3 {
		select {
		case <-others:
		case <-time.After(2 * time.Second):
			t.Fatal("a later team waited for the slow team #1")
		}
	}
	close(release)
	require.NoError(t, <-done)
	require.LessOrEqual(t, maxFlight, 2)
	require.Equal(t, 2, ClampParallel(0))
	require.Equal(t, MaxParallel, ClampParallel(100))
}
