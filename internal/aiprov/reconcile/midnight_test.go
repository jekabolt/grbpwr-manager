package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// B-30 — OpenRouter's days are differences of its cumulative counter read at LOCAL midnights (D-17).
// Every test names the ONE mutation measured red → restored green against it. The clock is a fake; the
// loop's clocks are the newTicker / newMidnightTimer knobs; the store and the base are recorders.

// at is a wall-clock instant in Europe/Warsaw.
func at(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, warsaw)
}

// openRouterWorker is a worker with only OpenRouter's key, on a rig whose counter reads usd, at clk.
func openRouterWorker(t *testing.T, usd string, sn *recSnaps, clk *clock) (*Worker, *rig, *recStore) {
	t.Helper()
	r := newRig(t)
	r.answer(openRouterPath, usageAnswer(usd))
	st := newRecStore()
	return newWorkerWith(r, st, sn, only(entity.AIProviderOpenRouter), clk.now), r, st
}

// rowsOf flattens every write into "day=usd@zone".
func rowsOf(st *recStore) []string {
	var out []string
	for _, c := range st.written() {
		for _, row := range c {
			out = append(out, row.Day+"="+row.AmountUSD.String()+"@"+row.BucketTZ)
		}
	}
	return out
}

// baseOf renders a base as "usd@day/zone".
func baseOf(s entity.AIUsageSnapshot) string {
	return s.UsageUSD.String() + "@" + s.Day + "/" + s.BucketTZ
}

// TestOpenRouterFirstReadingIsABaseNotADay — no base yet: the reading is Put as today's base (the
// local day, the zone, taken_at = the reading's instant) and NO day is written — there is nothing to
// diff it against. The base is read once, for openrouter; the tick is clean.
//
// MUTATION (measured red → restored green): settle's `base == nil` case sets rebase=false — the first
// reading is never kept, every later reading finds no base again, and OpenRouter never gets a number.
func TestOpenRouterFirstReadingIsABaseNotADay(t *testing.T) {
	sn := newSnaps()
	w, r, st := openRouterWorker(t, "42.1", sn, newClock(fixedNow))

	require.True(t, w.runOnce(context.Background()), "a base is not a failure")
	require.Len(t, r.requests(), 1)
	require.Empty(t, st.written(), "a base, not a number")
	gets, puts := sn.recorded()
	require.Equal(t, []string{"openrouter"}, gets)
	require.Len(t, puts, 1)
	require.Equal(t, entity.AIUsageSnapshot{ProviderKey: "openrouter", UsageUSD: puts[0].UsageUSD, Day: "2026-09-28",
		BucketTZ: "Europe/Warsaw", TakenAt: fixedNow}, puts[0])
	require.Equal(t, "42.1", puts[0].UsageUSD.String())
	require.Empty(t, w.LastError())
}

// TestOpenRouterHourlyPartialIsTodayAgainstTheBase — base 10 opened today: the counter at 12.5 writes
// today's row 2.5, at 13.75 re-writes it as 3.75, under the LOCAL day with bucket_tz Europe/Warsaw;
// the base is never moved by a partial.
//
// MUTATION (measured red → restored green): the partial writes the raw counter (`r.usage` instead of
// `r.usage.Sub(base.UsageUSD)`) → 12.5 and 13.75: their number becomes the key's lifetime spend.
func TestOpenRouterHourlyPartialIsTodayAgainstTheBase(t *testing.T) {
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	clk := newClock(at(2026, 9, 28, 11, 0, 0))
	w, r, st := openRouterWorker(t, "12.5", sn, clk)

	require.True(t, w.runOnce(context.Background()))
	clk.set(at(2026, 9, 28, 12, 0, 0))
	r.answer(openRouterPath, usageAnswer("13.75"))
	require.True(t, w.runOnce(context.Background()))

	require.Equal(t, []string{"2026-09-28=2.5@Europe/Warsaw", "2026-09-28=3.75@Europe/Warsaw"}, rowsOf(st))
	_, puts := sn.recorded()
	require.Empty(t, puts, "a partial does not move the base")
	require.Equal(t, clk.now().UTC(), st.written()[1][0].FetchedAt)
}

// TestOpenRouterMidnightClosesYesterdayExactly — the whole day through the running worker: a base of
// 10 read at 28.09 00:00:30, partials during the day, and the MIDNIGHT TIMER's reading at 29.09
// 00:00:30 (counter 30) writes 28.09 = 20 with bucket_tz Europe/Warsaw — exactly the local day — and
// makes 30 the base of 29.09; the timer is re-armed a local day later, and 29.09's first partial is
// measured from the new base.
//
// MUTATION (measured red → restored green): the midnight row filed under the reading's own day
// (`day: today` instead of `day: yesterday` in the closing case) → 29.09 = 20: the number lands on the
// day it did not happen in, and 28.09 keeps its 18:00 partial.
func TestOpenRouterMidnightClosesYesterdayExactly(t *testing.T) {
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	clk := newClock(at(2026, 9, 28, 9, 0, 0))
	w, r, st := openRouterWorker(t, "12.5", sn, clk)
	ticks := make(chan time.Time)
	w.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	arms := make(chan time.Duration, 8)
	fire := make(chan time.Time)
	w.newMidnightTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		arms <- d
		return fire, func() bool { return true }
	}
	waitWrite := func(want string) {
		t.Helper()
		select {
		case <-st.notify:
		case <-time.After(5 * time.Second):
			t.Fatalf("the write of %s never came", want)
		}
		rows := rowsOf(st)
		require.Equal(t, want, rows[len(rows)-1])
	}
	arm := func(want time.Duration) {
		t.Helper()
		select {
		case d := <-arms:
			require.Equal(t, want, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("the midnight timer was not armed (%s)", want)
		}
	}

	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(func() { _ = w.Stop() })
	waitWrite("2026-09-28=2.5@Europe/Warsaw") // the startup tick at 09:00
	arm(15*time.Hour + 30*time.Second)        // 09:00 → 29.09 00:00:30

	clk.set(at(2026, 9, 28, 18, 0, 0))
	r.answer(openRouterPath, usageAnswer("20"))
	ticks <- clk.now()
	waitWrite("2026-09-28=10@Europe/Warsaw")

	clk.set(at(2026, 9, 29, 0, 0, 30))
	r.answer(openRouterPath, usageAnswer("30"))
	fire <- clk.now()
	waitWrite("2026-09-28=20@Europe/Warsaw") // closed: exactly the local day 28.09
	select {
	case b := <-sn.notify:
		require.Equal(t, "30@2026-09-29/Europe/Warsaw", baseOf(b))
		require.Equal(t, at(2026, 9, 29, 0, 0, 30).UTC(), b.TakenAt)
	case <-time.After(5 * time.Second):
		t.Fatal("the midnight reading was not kept as the new base")
	}
	arm(24 * time.Hour) // re-armed for 30.09 00:00:30

	clk.set(at(2026, 9, 29, 9, 0, 0))
	r.answer(openRouterPath, usageAnswer("31.25"))
	ticks <- clk.now()
	waitWrite("2026-09-29=1.25@Europe/Warsaw")
	require.Equal(t, []string{
		"2026-09-28=2.5@Europe/Warsaw", "2026-09-28=10@Europe/Warsaw", "2026-09-28=20@Europe/Warsaw",
		"2026-09-29=1.25@Europe/Warsaw",
	}, rowsOf(st))
}

// TestOpenRouterMissedMidnightRebasesAndWritesNothing — the base is 28.09's and the next reading is
// 29.09 10:00 (the clock jumped a day: a restart across midnight, an outage). Nothing is written — the
// reading carries ten hours of 29.09 and is not split by a guess — 28.09 keeps its last partial, the
// reading becomes 29.09's base, one line says so, and the next reading that day is a partial with no
// second line.
//
// MUTATION (measured red → restored green): the closing window's late bound dropped (the closing case
// matches any reading of the next day) → 10:00's reading is written as 28.09 = 25, ten hours of 29.09
// presented as 28.09's number.
func TestOpenRouterMissedMidnightRebasesAndWritesNothing(t *testing.T) {
	logs := captureLogs(t)
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	clk := newClock(at(2026, 9, 29, 10, 0, 0))
	w, r, st := openRouterWorker(t, "35", sn, clk)

	require.True(t, w.runOnce(context.Background()), "a missed midnight is not a failure")
	require.Empty(t, st.written())
	_, puts := sn.recorded()
	require.Len(t, puts, 1)
	require.Equal(t, "35@2026-09-29/Europe/Warsaw", baseOf(puts[0]))
	require.Contains(t, logs.String(), "reconcile: openrouter midnight reading missed; 2026-09-28 keeps its last partial, a new base")

	clk.set(at(2026, 9, 29, 11, 0, 0))
	r.answer(openRouterPath, usageAnswer("36.5"))
	require.True(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-29=1.5@Europe/Warsaw"}, rowsOf(st))
	require.Equal(t, 1, strings.Count(logs.String(), "midnight reading missed"), "once per day")
}

// TestOpenRouterReadingInsideTheGraceLeavesTheDayToMidnight — an hourly tick that lands after local
// midnight but before the grace writes nothing and keeps the base: the closing reading comes after
// the grace. A reading inside the window closes the day (whoever takes it); one after the window is a
// missed midnight.
//
// MUTATION (measured red → restored green): the grace case removed from settle → the tick at 00:00:10
// falls into the closing case and closes 28.09 at 19 twenty seconds early: the day's last spend, still
// on its way to the provider's counter, lands in 29.09.
func TestOpenRouterReadingInsideTheGraceLeavesTheDayToMidnight(t *testing.T) {
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	clk := newClock(at(2026, 9, 29, 0, 0, 10))
	w, r, st := openRouterWorker(t, "29", sn, clk)

	require.True(t, w.runOnce(context.Background()))
	require.Empty(t, st.written())
	_, puts := sn.recorded()
	require.Empty(t, puts, "inside the grace the base stays")

	// An hourly tick inside the window is the closing reading.
	clk.set(at(2026, 9, 29, 0, 3, 0))
	r.answer(openRouterPath, usageAnswer("30"))
	require.True(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-28=20@Europe/Warsaw"}, rowsOf(st))
	_, puts = sn.recorded()
	require.Len(t, puts, 1)
	require.Equal(t, "30@2026-09-29/Europe/Warsaw", baseOf(puts[0]))

	// The same base, a reading past the window: nothing closes.
	late := newSnaps(openRouterBase("10", "2026-09-28"))
	w2, _, st2 := openRouterWorker(t, "30", late, newClock(at(2026, 9, 29, 0, 5, 31)))
	require.True(t, w2.runOnce(context.Background()))
	require.Empty(t, st2.written())
	_, puts = late.recorded()
	require.Equal(t, "30@2026-09-29/Europe/Warsaw", baseOf(puts[0]))
}

// TestOpenRouterCounterRestartRebasesAndLogs — the counter went DOWN (a rotated key restarts at zero):
// nothing is written — a negative day is not their number — the reading becomes today's base, and a
// WARN line says «openrouter usage restarted; a new base».
//
// MUTATION (measured red → restored green): the restart case removed from settle → today's row is
// written as 42.1 − 50 = −7.9.
func TestOpenRouterCounterRestartRebasesAndLogs(t *testing.T) {
	logs := captureLogs(t)
	sn := newSnaps(openRouterBase("50", "2026-09-28"))
	w, _, st := openRouterWorker(t, "42.1", sn, newClock(fixedNow))

	require.True(t, w.runOnce(context.Background()))
	require.Empty(t, st.written())
	_, puts := sn.recorded()
	require.Len(t, puts, 1)
	require.Equal(t, "42.1@2026-09-28/Europe/Warsaw", baseOf(puts[0]))
	require.Contains(t, logs.String(), `level=WARN msg="reconcile: openrouter usage restarted; a new base"`)
}

// TestOpenRouterAnotherKeyIsAnotherCounter — the key changes between two readings (a key saved in the
// panel, or a stored key cleared back to the env one) and the new key's counter is HIGHER: its history
// is not today's spend. The reading becomes the base; nothing is written; the next reading with the
// same key is a partial again.
//
// MUTATION (measured red → restored green): keyChanged never set (`known && last != fp` → `false`) →
// today's row is written as 500 − 10 = 490: the other key's lifetime spend presented as today's.
func TestOpenRouterAnotherKeyIsAnotherCounter(t *testing.T) {
	logs := captureLogs(t)
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	k := only(entity.AIProviderOpenRouter)
	r := newRig(t)
	r.answer(openRouterPath, usageAnswer("12"))
	st := newRecStore()
	w := newWorkerWith(r, st, sn, k, newClock(fixedNow).now)

	require.True(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-28=2@Europe/Warsaw"}, rowsOf(st))

	k.set(nil, map[string]string{entity.AIProviderOpenRouter: "sk-or-v1-ANOTHER-key-7788"})
	r.answer(openRouterPath, usageAnswer("500"))
	require.True(t, w.runOnce(context.Background()))
	require.Len(t, st.written(), 1, "the other key's history is not written")
	_, puts := sn.recorded()
	require.Len(t, puts, 1)
	require.Equal(t, "500@2026-09-28/Europe/Warsaw", baseOf(puts[0]))
	require.Contains(t, logs.String(), "reconcile: openrouter key changed; a new base")
	require.NotContains(t, logs.String(), "ANOTHER")

	r.answer(openRouterPath, usageAnswer("501.5"))
	require.True(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-28=2@Europe/Warsaw", "2026-09-28=1.5@Europe/Warsaw"}, rowsOf(st))
}

// TestOpenRouterZoneRules — the zone is the registry's budget zone as it is now. "" (no snapshot yet)
// is Europe/Warsaw, the zone the ledger stamps for the same blank; a name that does not load reads
// nothing and writes nothing, logs once, and is not a failure; a base read in another zone is replaced,
// not diffed against.
//
// MUTATION (measured red → restored green): "" not defaulted (`name = ""` in place of
// `name = entity.DefaultBudgetTimezone`) → time.LoadLocation("") answers UTC: the 01:00 reading is
// dated 28.09 by the UTC calendar and the Warsaw base is re-taken under a zone named "" (which the real
// store refuses — every reading would fail) — the «blank zone» subtest turns red.
func TestOpenRouterZoneRules(t *testing.T) {
	t.Run("blank zone", func(t *testing.T) {
		sn := newSnaps(openRouterBase("40", "2026-09-28"))
		k := only(entity.AIProviderOpenRouter)
		k.setZone("  ")
		r := newRig(t)
		st := newRecStore()
		w := newWorkerWith(r, st, sn, k, newClock(at(2026, 9, 29, 1, 0, 0)).now) // 28.09 23:00 UTC
		require.True(t, w.runOnce(context.Background()))
		require.Empty(t, st.written(), "01:00 local: the closing reading was missed, not a UTC partial of 28.09")
		_, puts := sn.recorded()
		require.Equal(t, "42.1@2026-09-29/Europe/Warsaw", baseOf(puts[0]))
	})
	t.Run("a zone that does not load", func(t *testing.T) {
		logs := captureLogs(t)
		sn := seededSnaps()
		k := only(entity.AIProviderOpenRouter)
		k.setZone("Mars/Olympus_Mons")
		r := newRig(t)
		st := newRecStore()
		w := newWorkerWith(r, st, sn, k, nil)
		require.True(t, w.runOnce(context.Background()), "as if there were no key: not a failure")
		require.True(t, w.runOnce(context.Background()))
		require.Empty(t, r.requests(), "nothing is read")
		require.Empty(t, st.written())
		gets, puts := sn.recorded()
		require.Empty(t, gets)
		require.Empty(t, puts)
		require.Equal(t, 1, strings.Count(logs.String(), "the budget zone does not load"), "logged once")
	})
	t.Run("a base of another zone", func(t *testing.T) {
		logs := captureLogs(t)
		b := openRouterBase("10", "2026-09-28")
		b.BucketTZ = "UTC"
		sn := newSnaps(b)
		w, _, st := openRouterWorker(t, "42.1", sn, newClock(fixedNow))
		require.True(t, w.runOnce(context.Background()))
		require.Empty(t, st.written())
		_, puts := sn.recorded()
		require.Equal(t, "42.1@2026-09-28/Europe/Warsaw", baseOf(puts[0]))
		require.Contains(t, logs.String(), "budget zone changed since the base was read")
	})
}

// TestOpenRouterBaseStoreFailures — a base that cannot be read writes nothing and is a failed tick
// (`snapshot read failed`); a closing reading whose new base cannot be written still writes the closed
// day, and the tick is failed (`snapshot write failed`).
//
// MUTATION (measured red → restored green): the Get error ignored (`base, _ :=` — read as «no base») →
// the reading is Put as a first base over a base that exists: nothing reaches the tracker and the
// real base is overwritten.
func TestOpenRouterBaseStoreFailures(t *testing.T) {
	logs := captureLogs(t)
	sn := seededSnaps()
	sn.getErr = errors.New("connection reset")
	w, _, st := openRouterWorker(t, "42.1", sn, newClock(fixedNow))
	require.False(t, w.runOnce(context.Background()))
	require.Empty(t, st.written())
	_, puts := sn.recorded()
	require.Empty(t, puts)
	require.Equal(t, "connection reset", w.LastError())
	require.Contains(t, logs.String(), "reconcile: openrouter snapshot read failed")

	closing := newSnaps(openRouterBase("10", "2026-09-28"))
	closing.putErr = errors.New("lock wait timeout")
	w, _, st = openRouterWorker(t, "30", closing, newClock(at(2026, 9, 29, 0, 0, 30)))
	require.False(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-28=20@Europe/Warsaw"}, rowsOf(st), "the closed day is written all the same")
	require.Contains(t, w.LastError(), "lock wait timeout")
	require.Contains(t, logs.String(), "reconcile: openrouter snapshot write failed")

	// No store at all: a failure, never a silent skip.
	r := newRig(t)
	w = New(Config{Enabled: true}, newRecStore(), nil, only(entity.AIProviderOpenRouter), WithHTTPClient(r.client()))
	require.False(t, w.runOnce(context.Background()))
	require.Empty(t, r.requests())
}

// TestMidnightTimerIsArmedForTheLocalMidnight — the fake timer records every delay the second clock
// asks for: from 23:10 CEST the next 00:00:30 is 50 min 30 s away; after it fires, a local day; across
// the October change (a 25-hour day) 25 h, across the March one (23 hours) 23 h — the local midnight,
// never «24 h later»; from inside the grace the SAME night's 00:00:30; with a zone that does not load,
// look again in an Interval.
//
// MUTATION (measured red → restored green): nextMidnight's next day counted as 24 h
// (`time.Date(y, m, d+1, …)` → `time.Date(y, m, d, …).Add(24 * time.Hour)`) → 24 h on both change
// nights: the closing reading drifts an hour off midnight for the whole season.
func TestMidnightTimerIsArmedForTheLocalMidnight(t *testing.T) {
	clk := newClock(time.Date(2026, 9, 28, 21, 10, 0, 0, time.UTC)) // 23:10 CEST
	k := &keys{}
	w := New(Config{Enabled: true}, newRecStore(), newSnaps(), k, WithClock(clk.now))
	w.newTicker = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }
	arms := make(chan time.Duration)
	fire := make(chan time.Time)
	w.newMidnightTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		arms <- d
		return fire, func() bool { return true }
	}
	next := func() time.Duration {
		t.Helper()
		select {
		case d := <-arms:
			return d
		case <-time.After(5 * time.Second):
			t.Fatal("the midnight timer was not armed")
			return 0
		}
	}
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(func() { _ = w.Stop() })

	require.Equal(t, 50*time.Minute+30*time.Second, next())
	for _, c := range []struct {
		name  string
		clock time.Time
		want  time.Duration
	}{
		{"an ordinary night", at(2026, 9, 29, 0, 0, 30), 24 * time.Hour},
		{"the October change (25 h)", at(2026, 10, 25, 0, 0, 30), 25 * time.Hour},
		{"the March change (23 h)", at(2026, 3, 29, 0, 0, 30), 23 * time.Hour},
		{"inside the grace", at(2026, 9, 30, 0, 0, 10), 20 * time.Second},
	} {
		clk.set(c.clock)
		fire <- c.clock
		require.Equal(t, c.want, next(), c.name)
	}
	k.setZone("Mars/Olympus_Mons")
	fire <- clk.now()
	require.Equal(t, time.Hour, next(), "no zone to count midnights in: look again in an Interval")
}

// TestOpenRouterClosingRowIsRetriedInPlace — Codex REVIEW-F2 P1-2: the closing write fails once (a deadlock,
// a lock wait) and lands on the second try, inside the same reading: the day is closed, the base advances,
// the tick is clean. When every try fails the base still advances (today must not lose its hours) and the
// tick is an error — the day keeps its last partial, as before.
//
// MUTATION (measured red→green): the retry loop removed → one upsert call, the tick fails, 28.09 is never
// closed.
func TestOpenRouterClosingRowIsRetriedInPlace(t *testing.T) {
	sn := newSnaps(openRouterBase("10", "2026-09-28"))
	w, _, st := openRouterWorker(t, "30", sn, newClock(at(2026, 9, 29, 0, 0, 30)))
	w.closingRetry = 0
	st.fail = func(n int) error {
		if n == 1 {
			return errors.New("deadlock found when trying to get lock")
		}
		return nil
	}
	require.True(t, w.runOnce(context.Background()))
	require.Equal(t, []string{"2026-09-28=20@Europe/Warsaw", "2026-09-28=20@Europe/Warsaw"}, rowsOf(st),
		"the same closing row, written on the second try")
	_, puts := sn.recorded()
	require.Len(t, puts, 1)
	require.Equal(t, "2026-09-29", puts[0].Day)
	require.Equal(t, "30", puts[0].UsageUSD.String())

	sn = newSnaps(openRouterBase("10", "2026-09-28"))
	w, _, st = openRouterWorker(t, "30", sn, newClock(at(2026, 9, 29, 0, 0, 30)))
	w.closingRetry = 0
	st.fail = func(int) error { return errors.New("deadlock found when trying to get lock") }
	require.False(t, w.runOnce(context.Background()))
	require.Len(t, st.written(), closingWriteTries, "every try, then the day is given up")
	_, puts = sn.recorded()
	require.Len(t, puts, 1, "the base still advances: today must not lose its hours")
	require.Contains(t, w.LastError(), "deadlock")

	// An hourly partial is NOT retried: the next hour re-writes it anyway.
	sn = newSnaps(openRouterBase("10", "2026-09-28"))
	w, _, st = openRouterWorker(t, "12", sn, newClock(at(2026, 9, 28, 9, 0, 0)))
	w.closingRetry = 0
	st.fail = func(int) error { return errors.New("deadlock found when trying to get lock") }
	require.False(t, w.runOnce(context.Background()))
	require.Len(t, st.written(), 1)
}
