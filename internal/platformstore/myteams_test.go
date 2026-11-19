package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// `new team`, the root list and the team logo in the store (teams O §7, §11, §12).

func countRows(t *testing.T, st *platformstore.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().Get(&n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func newTeam(user int64, slug string, now time.Time) platformstore.CreateTeamParams {
	return platformstore.CreateTeamParams{UserID: user, RootSID: rootSID(user), RootNotBefore: now.Add(-30 * 24 * time.Hour),
		Name: strings.ToUpper(slug[:1]) + slug[1:], Slug: slug, Now: now,
		TermsVersion: "2026-11", TermsAccepted: true}
}

func rootSID(user int64) string { return fmt.Sprintf("r%031d", user) }

func seedCreateTeamSessions(t *testing.T, st *platformstore.Store) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES
 (?, 101, NULL, 'root'), (?, 102, NULL, 'root')`, rootSID(101), rootSID(102)); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTeam(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	seedCreateTeamSessions(t, st)
	db := st.DB()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := func() string {
		return fmt.Sprint(countRows(t, st, `SELECT COUNT(*) FROM team`), countRows(t, st, `SELECT COUNT(*) FROM team_member`),
			countRows(t, st, `SELECT COUNT(*) FROM team_logo`), countRows(t, st, `SELECT COUNT(*) FROM platform_email`),
			countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE terms_version IS NOT NULL`))
	}

	// Terms not accepted and not carried: refused, nothing written.
	before := snapshot()
	p := newTeam(101, "newco", now)
	p.TermsAccepted = false
	if _, err := st.CreateTeam(ctx, p); !errors.Is(err, platformstore.ErrTermsRequired) {
		t.Fatalf("no terms: %v, want ErrTermsRequired", err)
	}
	if got := snapshot(); got != before {
		t.Fatalf("refused CreateTeam wrote rows: %s → %s", before, got)
	}

	// Approval on: review, the owner membership, the logo, the terms, one team_request letter per
	// active operator and none to the person.
	p = newTeam(101, "newco", now)
	p.Review = platformstore.Review{On: true, ConsoleLink: "https://console.example.test/teams"}
	p.Logo = &platformstore.TeamLogo{PNG: []byte("png-newco"), SHA256: hex64('1')}
	created, err := st.CreateTeam(ctx, p)
	if err != nil || created.Status != "review" || created.Slug != "newco" || created.TeamID <= 0 {
		t.Fatalf("review on: %+v %v", created, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team WHERE id = ? AND status = 'review' AND created_by = 101 AND db_name IS NULL`, created.TeamID) != 1 {
		t.Fatal("team row not in review / not created by ann")
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_member WHERE team_id = ? AND user_id = 101 AND role = 'owner' AND status = 'active'`, created.TeamID) != 1 {
		t.Fatal("no owner membership")
	}
	if sha, err := st.TeamLogoSHA(ctx, created.TeamID); err != nil || sha != hex64('1') {
		t.Fatalf("logo sha %q %v", sha, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE id = 101 AND terms_version = '2026-11' AND terms_accepted_at IS NOT NULL`) != 1 {
		t.Fatal("terms not stored")
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_email WHERE template = 'team_request' AND to_email = 'op@x.io'`) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM platform_email`) != 1 {
		t.Fatal("want exactly one team_request letter, to the operator")
	}

	// Approval off: provisioning, no letter; terms already accepted, so the request need not carry them.
	p = newTeam(101, "second", now)
	p.TermsAccepted = false
	created, err = st.CreateTeam(ctx, p)
	if err != nil || created.Status != "provisioning" {
		t.Fatalf("review off: %+v %v", created, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_email`) != 1 {
		t.Fatal("review off sent a letter")
	}
	if sha, _ := st.TeamLogoSHA(ctx, created.TeamID); sha != "" {
		t.Fatalf("team without a logo has sha %q", sha)
	}

	// No cap on own teams: ann owned two before, now four, and two more go through.
	for _, slug := range []string{"third", "fourth"} {
		if _, err := st.CreateTeam(ctx, newTeam(101, slug, now)); err != nil {
			t.Fatalf("own team %s: %v", slug, err)
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_member WHERE user_id = 101 AND role = 'owner' AND status = 'active'`); n != 6 {
		t.Fatalf("ann owns %d teams, want 6", n)
	}

	// Held addresses: a team's (any status), a live redirect, a pending team not yet expired.
	// An expired redirect frees its address.
	for _, q := range []string{
		`INSERT INTO team_slug_history (slug, team_id, retired_at, redirect_until) VALUES
 ('acme-old', 2, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3) + INTERVAL 1 DAY),
 ('acme-ancient', 2, UTC_TIMESTAMP(3) - INTERVAL 40 DAY, UTC_TIMESTAMP(3) - INTERVAL 10 DAY)`,
		`INSERT INTO team (id, slug, name, status, created_by, pending_expires_at) VALUES
 (80, 'fresh', 'Fresh', 'pending', 102, UTC_TIMESTAMP(3) + INTERVAL 1 DAY),
 (81, 'stale', 'Stale', 'pending', 102, UTC_TIMESTAMP(3) - INTERVAL 1 DAY)`,
		`INSERT INTO team_logo (team_id, png, sha256) VALUES (80, 'p8', REPEAT('8', 64)), (81, 'p9', REPEAT('9', 64))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, slug := range []string{"acme", "wait", "acme-old", "fresh"} {
		if _, err := st.CreateTeam(ctx, newTeam(102, slug, now)); !errors.Is(err, platformstore.ErrSlugTaken) {
			t.Errorf("held address %s: %v, want ErrSlugTaken", slug, err)
		}
	}
	if _, err := st.CreateTeam(ctx, newTeam(102, "acme-ancient", now)); err != nil {
		t.Fatalf("expired redirect: %v", err)
	}
	// The expired pending team that held "stale" goes with its logo; the fresh one keeps both.
	if _, err := st.CreateTeam(ctx, newTeam(102, "stale", now)); err != nil {
		t.Fatalf("address of an expired pending team: %v", err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team WHERE id = 81`) != 0 || countRows(t, st, `SELECT COUNT(*) FROM team_logo WHERE team_id = 81`) != 0 {
		t.Fatal("expired pending team or its logo left behind")
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_logo WHERE team_id = 80`) != 1 {
		t.Fatal("a live pending team lost its logo")
	}

	// Not an active account: refused.
	if _, err := db.Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (104, 'dis@x.io', 'dis', 'h', 'disabled'), (105, 'pen@x.io', 'pen', 'h', 'pending')`); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{104, 105, 999} {
		if _, err := st.CreateTeam(ctx, newTeam(uid, fmt.Sprintf("u%d", uid), now)); !errors.Is(err, platformstore.ErrTokenInvalid) {
			t.Errorf("account %d: %v, want ErrTokenInvalid", uid, err)
		}
	}
}

func TestCreateTeamRechecksRootSession(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	seedCreateTeamSessions(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	sid := rootSID(101)
	if live, err := st.RootSessionLive(ctx, sid, 101, now.Add(-30*24*time.Hour)); err != nil || !live {
		t.Fatalf("preflight root session = %v, %v", live, err)
	}
	if _, err := st.SignOutRoot(ctx, sid, 101, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTeam(ctx, newTeam(101, "after-signout", now)); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("CreateTeam after root sign-out = %v, want ErrTokenInvalid", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team WHERE slug = 'after-signout'`); n != 0 {
		t.Fatalf("CreateTeam after root sign-out wrote %d team rows", n)
	}
}

// The root list: review, provisioning, active and suspended teams with an active membership, each
// with its logo; never a deleted or pending team, never a removed membership.
func TestListMyTeams(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, db_name, status) VALUES
 (10, 'gone', 'Gone', 'lk_t_gone0001', 'deleted'), (11, 'pend', 'Pend', NULL, 'pending'),
 (12, 'left', 'Left', 'lk_t_left0001', 'active'), (13, 'paus', 'Paus', 'lk_t_paus0001', 'suspended')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES
 (4, 101, 'member', 'active'), (10, 101, 'owner', 'active'), (11, 101, 'owner', 'active'),
 (12, 101, 'member', 'removed'), (13, 101, 'member', 'active')`,
		`INSERT INTO team_logo (team_id, png, sha256) VALUES (2, 'p2', REPEAT('2', 64)), (10, 'p10', REPEAT('a', 64))`,
		`UPDATE platform_user SET terms_version = '2026-11' WHERE id = 101`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListMyTeams(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "ann@x.io" || got.Status != platformstore.UserActive || got.IsOperator || got.TermsVersion != "2026-11" {
		t.Fatalf("account %+v", got)
	}
	var list []string
	for _, m := range got.Teams {
		list = append(list, fmt.Sprintf("%s:%s:%s:%d", m.Slug, m.Status, m.Role, len(m.LogoSHA)))
		if m.Slug == "acme" && m.LogoSHA != hex64('2') {
			t.Errorf("acme logo sha %q", m.LogoSHA)
		}
	}
	want := "acme:active:owner:64,paus:suspended:member:0,prov:provisioning:member:0,wait:review:owner:0"
	if strings.Join(list, ",") != want {
		t.Fatalf("root list\n got %s\nwant %s", strings.Join(list, ","), want)
	}
	if op, err := st.ListMyTeams(ctx, 103); err != nil || !op.IsOperator || len(op.Teams) != 0 {
		t.Fatalf("operator %+v %v", op, err)
	}
	if _, err := st.ListMyTeams(ctx, 999); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

// The logo: set, replace, remove; the public read is the same "none" for a deleted or pending team;
// it goes with its team on the pending sweep and on a declined review.
func TestTeamLogo(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	set := func(teamID, userID int64, sid string, logo *platformstore.TeamLogo) error {
		return st.SetTeamLogo(ctx, platformstore.SetTeamLogoParams{TeamID: teamID, UserID: userID, SID: sid, Logo: logo})
	}
	read := func(slug string) (string, string, bool) {
		t.Helper()
		l, _, ok, err := st.TeamLogoBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		return string(l.PNG), l.SHA256, ok
	}
	if _, _, ok := read("acme"); ok {
		t.Fatal("logo before any was set")
	}
	if err := set(2, 102, "sbob0000000000000000000000000000", nil); !errors.Is(err, platformstore.ErrNotOwner) {
		t.Fatalf("member logo write: %v, want ErrNotOwner", err)
	}
	if err := set(2, 101, "revoked-or-unknown", nil); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("dead-session logo write: %v, want ErrTokenInvalid", err)
	}
	if err := st.SetTeamReadOnly(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := set(2, 101, "sann0000000000000000000000000000", nil); !errors.Is(err, platformstore.ErrTeamReadOnly) {
		t.Fatalf("suspended-team logo write: %v, want ErrTeamReadOnly", err)
	}
	if err := st.SetTeamReadOnly(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if err := set(2, 101, "sann0000000000000000000000000000", &platformstore.TeamLogo{PNG: []byte("one"), SHA256: hex64('1')}); err != nil {
		t.Fatal(err)
	}
	if png, sha, ok := read("acme"); !ok || png != "one" || sha != hex64('1') {
		t.Fatalf("set: %q %q %v", png, sha, ok)
	}
	meta, ok, err := st.TeamLogoMetaBySlug(ctx, "acme")
	if err != nil || !ok || meta.SHA256 != hex64('1') || meta.Size != int64(len("one")) {
		t.Fatalf("logo metadata = %+v, %v, %v", meta, ok, err)
	}
	if png, ok, err := st.TeamLogoPNGBySlug(ctx, "acme", meta.SHA256); err != nil || !ok || string(png) != "one" {
		t.Fatalf("logo content = %q, %v, %v", png, ok, err)
	}
	if _, ok, err := st.TeamLogoPNGBySlug(ctx, "acme", hex64('f')); err != nil || ok {
		t.Fatalf("stale logo content = %v, %v", ok, err)
	}
	if err := set(2, 101, "sann0000000000000000000000000000", &platformstore.TeamLogo{PNG: []byte("two"), SHA256: hex64('2')}); err != nil {
		t.Fatal(err)
	}
	if png, sha, ok := read("acme"); !ok || png != "two" || sha != hex64('2') {
		t.Fatalf("replace: %q %q %v", png, sha, ok)
	}
	if shas, err := st.TeamLogoSHAs(ctx, []int64{2, 3}); err != nil || len(shas) != 1 || shas[2] != hex64('2') {
		t.Fatalf("shas %v %v", shas, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_logo`) != 1 {
		t.Fatal("replace kept two rows")
	}
	if err := set(2, 101, "sann0000000000000000000000000000", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := read("acme"); ok {
		t.Fatal("removed logo still served")
	}
	if sha, err := st.TeamLogoSHA(ctx, 2); err != nil || sha != "" {
		t.Fatalf("removed logo sha %q %v", sha, err)
	}

	// Logos created with a team remain public in review and provisioning, but cannot be mutated by a
	// team session before the team is active.
	if _, err := st.DB().Exec(`INSERT INTO team_logo (team_id, png, sha256) VALUES
 (3, 'x', ?), (4, 'x', ?)`, hex64('3'), hex64('3')); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3, 4} {
		if err := set(id, 101, "sann0000000000000000000000000000", nil); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
			t.Fatalf("team %d logo mutation: %v, want ErrWrongTeamStatus", id, err)
		}
	}
	if _, _, ok := read("wait"); !ok {
		t.Fatal("a team in review has no public logo")
	}

	// Deleted: not served, not settable; the row waits for the wipe and is back on restore.
	if err := set(2, 101, "sann0000000000000000000000000000", &platformstore.TeamLogo{PNG: []byte("three"), SHA256: hex64('4')}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := read("acme"); ok {
		t.Fatal("a deleted team's logo is served")
	}
	if err := set(2, 101, "sann0000000000000000000000000000", nil); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("logo of a deleted team: %v, want ErrWrongTeamStatus", err)
	}
	if _, err := st.RestoreTeam(ctx, 2, now); err != nil {
		t.Fatal(err)
	}
	if png, _, ok := read("acme"); !ok || png != "three" {
		t.Fatal("restored team lost its logo")
	}

	// Pending: not served, not settable; swept with its team. Unknown team: ErrTeamNotFound.
	if _, err := st.DB().Exec(`INSERT INTO team (id, slug, name, status, created_by, pending_expires_at) VALUES
 (8, 'pend', 'Pend', 'pending', 101, UTC_TIMESTAMP(3) - INTERVAL 1 HOUR)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO team_logo (team_id, png, sha256) VALUES (8, 'p8', REPEAT('8', 64))`); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := read("pend"); ok {
		t.Fatal("a pending team's logo is served")
	}
	if err := set(8, 101, "sann0000000000000000000000000000", &platformstore.TeamLogo{PNG: []byte("x"), SHA256: hex64('5')}); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("logo of a pending team: %v, want ErrWrongTeamStatus", err)
	}
	if err := set(999, 101, "sann0000000000000000000000000000", nil); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("logo of no team: %v, want ErrTeamNotFound", err)
	}
	if n, err := st.SweepPendingTeams(ctx, now, 10); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_logo WHERE team_id = 8`) != 0 {
		t.Fatal("swept pending team left its logo")
	}
	// A declined review takes its logo along.
	if err := st.DeclineTeam(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_logo WHERE team_id = 3`) != 0 {
		t.Fatal("declined team left its logo")
	}
}

// holdTx runs stmts in an open transaction and keeps it (and its locks) until the test ends or the
// returned func is called.
func holdTx(t *testing.T, st *platformstore.Store, stmts ...string) func() {
	t.Helper()
	tx, err := st.DB().BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range stmts {
		if strings.HasPrefix(q, "SELECT") {
			var out []string
			if err := tx.Select(&out, q); err != nil {
				_ = tx.Rollback()
				t.Fatalf("%s: %v", q, err)
			}
			continue
		}
		if _, err := tx.Exec(q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", q, err)
		}
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback() }) }
	t.Cleanup(release)
	return release
}

// within runs f and fails the test when it does not finish in d (it would be waiting on a lock).
func within(t *testing.T, d time.Duration, what string, f func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(d + 2*time.Second):
		t.Fatalf("%s did not finish in %v: it waits on a lock the other transaction holds", what, d)
	}
}

// CreateTeam of a person never waits for a DeleteTeam of another team of the same person, and the
// other way round (lock order at DeleteTeam): neither holds a row the other asks for.
func TestCreateTeamDoesNotWaitForDeleteTeam(t *testing.T) {
	st := openFresh(t)
	seedDelete(t, st)
	seedCreateTeamSessions(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)

	// DeleteTeam(acme) as far as its last statement, held open.
	release := holdTx(t, st,
		`SELECT slug FROM team WHERE id = 2 FOR UPDATE`,
		`UPDATE team SET status = 'deleted', deleted_at = UTC_TIMESTAMP(3), purge_at = UTC_TIMESTAMP(3) + INTERVAL 30 DAY, deleted_from = 'active' WHERE id = 2`,
		`SELECT sid FROM user_session WHERE team_id = 2 AND revoked_at IS NULL FOR UPDATE`,
		`UPDATE user_session SET revoked_at = UTC_TIMESTAMP(3) WHERE team_id = 2 AND revoked_at IS NULL`,
		`UPDATE team_invite SET revoked_at = UTC_TIMESTAMP(3) WHERE team_id = 2 AND accepted_at IS NULL AND revoked_at IS NULL`,
		`UPDATE email_token SET used_at = UTC_TIMESTAMP(3) WHERE team_id = 2 AND purpose = 'handoff' AND used_at IS NULL`,
	)
	within(t, 5*time.Second, "CreateTeam while acme's delete is open", func(ctx context.Context) error {
		p := newTeam(101, "while-delete", now)
		p.Review = platformstore.Review{On: true}
		p.Logo = &platformstore.TeamLogo{PNG: []byte("x"), SHA256: hex64('1')}
		_, err := st.CreateTeam(ctx, p)
		return err
	})
	release()

	// CreateTeam of ann as far as its last statement, held open: the address check, the account FOR
	// UPDATE, the new team, its owner, its logo, the operator letter.
	release = holdTx(t, st,
		`SELECT id FROM team WHERE slug = 'held-create' AND status = 'pending' AND pending_expires_at < UTC_TIMESTAMP(3) FOR UPDATE`,
		`SELECT email FROM platform_user WHERE id = 101 FOR UPDATE`,
		`INSERT INTO team (id, slug, name, status, created_by) VALUES (50, 'held-create', 'Held', 'review', 101)`,
		`INSERT INTO team_logo (team_id, png, sha256) VALUES (50, 'x', REPEAT('5', 64))`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (50, 101, 'owner', 'active')`,
		`UPDATE platform_user SET terms_version = '2026-11' WHERE id = 101`,
		`INSERT INTO platform_email (to_email, template, payload, status) VALUES ('op@x.io', 'team_request', '{}', 'pending')`,
	)
	within(t, 5*time.Second, "DeleteTeam(acme) while ann's CreateTeam is open", func(ctx context.Context) error {
		_, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention)
		return err
	})
	release()
}

// The same pair for real, concurrently, many times: no deadlock victim, no lock-wait timeout.
func TestCreateTeamRacesDeleteTeam(t *testing.T) {
	st := openFresh(t)
	seedDelete(t, st)
	seedCreateTeamSessions(t, st)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	const rounds = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
				errs <- fmt.Errorf("delete #%d: %w", i, err)
				return
			}
			if _, err := st.RestoreTeam(ctx, 2, now); err != nil {
				errs <- fmt.Errorf("restore #%d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			p := newTeam(101, fmt.Sprintf("race%02d", i), now)
			p.Review = platformstore.Review{On: i%2 == 0}
			p.Logo = &platformstore.TeamLogo{PNG: []byte("x"), SHA256: hex64('1')}
			if _, err := st.CreateTeam(ctx, p); err != nil {
				errs <- fmt.Errorf("create #%d: %w", i, err)
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if errors.Is(err, platformstore.ErrTryAgain) {
			t.Errorf("lock conflict between CreateTeam and DeleteTeam: %v", err)
		} else {
			t.Error(err)
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team WHERE slug LIKE 'race%'`); n != rounds {
		t.Fatalf("created %d teams, want %d", n, rounds)
	}
}
