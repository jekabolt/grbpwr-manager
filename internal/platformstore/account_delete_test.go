package platformstore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Delete my account under concurrency, and a team whose people all deleted their accounts (P0 §3.5).

func execAll(t *testing.T, st *platformstore.Store, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// isLockRace: a deadlock or lock wait the caller retries (a member change does not map it).
func isLockRace(err error) bool {
	return err != nil && (errors.Is(err, platformstore.ErrTryAgain) || strings.Contains(err.Error(), "Deadlock") ||
		strings.Contains(err.Error(), "Lock wait timeout"))
}

func deleteParams(uid int64, handle string, now time.Time) platformstore.DeleteAccountParams {
	return platformstore.DeleteAccountParams{UserID: uid, Confirm: handle, Now: now, Retention: platformstore.DeleteRetention,
		RestoreLink: "https://x.test/restore"}
}

// Two owners of a team with a member delete their accounts at once: one goes, the other is the
// only owner left and is refused. Never both (the team would be left with members and no owner).
func TestDeleteAccountTwoOwnersAtOnce(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	execAll(t, st, `UPDATE team_member SET role = 'owner' WHERE team_id = 2 AND user_id = 102`,
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (104, 'cat@x.io', 'cat', 'h', 'active')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (2, 104, 'member', 'active')`,
		// ann's team in review would close with her: out of the way, this is about acme.
		`DELETE FROM team_member WHERE team_id = 3`)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for round := range 6 {
		var (
			wg   sync.WaitGroup
			errs [2]error
		)
		start := make(chan struct{})
		for i, who := range []struct {
			id     int64
			handle string
		}{{101, "ann"}, {102, "bob"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for {
					_, errs[i] = st.DeleteAccount(ctx, deleteParams(who.id, who.handle, now))
					if !errors.Is(errs[i], platformstore.ErrTryAgain) {
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		ok, sole := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, platformstore.ErrSoleOwner):
				sole++
			default:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		var owners int
		if err := st.DB().Get(&owners, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND role = 'owner' AND status = 'active'`); err != nil {
			t.Fatal(err)
		}
		if ok != 1 || sole != 1 || owners != 1 {
			t.Fatalf("round %d: deleted %d, refused %d, owners left %d", round, ok, sole, owners)
		}
		execAll(t, st, `UPDATE platform_user SET status = 'active', deleted_at = NULL, purge_at = NULL WHERE id IN (101, 102)`,
			`UPDATE team_member SET status = 'active' WHERE team_id = 2`)
	}
}

// A role change racing the delete: the delete either sees bob as an owner (ann goes) or not (ann is
// refused); ann never goes while bob's demotion leaves the team without an owner.
func TestDeleteAccountRacesDemotion(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	execAll(t, st, `DELETE FROM team_member WHERE team_id = 3`)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for round := range 6 {
		execAll(t, st, `UPDATE platform_user SET status = 'active', deleted_at = NULL, purge_at = NULL WHERE id = 101`,
			`UPDATE team_member SET status = 'active', role = 'owner' WHERE team_id = 2`)
		var (
			wg         sync.WaitGroup
			delErr     error
			demoteErr  error
			start      = make(chan struct{})
			bobMember  = "member"
			annDeleted bool
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for {
				_, delErr = st.DeleteAccount(ctx, deleteParams(101, "ann", now))
				if !errors.Is(delErr, platformstore.ErrTryAgain) {
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for {
				_, demoteErr = st.SetMemberRole(ctx, platformstore.MemberChangeParams{TeamID: 2, UserID: 102,
					Actor: platformstore.Actor{UserID: 103, Operator: true}, MirrorOwners: []int64{101, 102}, Now: now}, bobMember)
				if !isLockRace(demoteErr) {
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		annDeleted = delErr == nil
		if delErr != nil && !errors.Is(delErr, platformstore.ErrSoleOwner) {
			t.Fatalf("round %d delete: %v", round, delErr)
		}
		if demoteErr != nil && !errors.Is(demoteErr, platformstore.ErrLastOwner) {
			t.Fatalf("round %d demote: %v", round, demoteErr)
		}
		if annDeleted == (demoteErr == nil) {
			t.Fatalf("round %d: ann deleted %v, bob demoted %v (exactly one)", round, annDeleted, demoteErr == nil)
		}
		var owners int
		if err := st.DB().Get(&owners, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND role = 'owner' AND status = 'active'`); err != nil {
			t.Fatal(err)
		}
		if owners == 0 {
			t.Fatalf("round %d: acme has no owner (ann deleted %v, demote %v)", round, annDeleted, demoteErr)
		}
	}
}

// acme's two owners delete their accounts one after the other: the second closes acme with it (the
// first is frozen, not a member any more). The console cannot reopen a team of frozen people; once
// ann restores her account it can, and bob's later restore finds it open.
func TestTeamOfDeletedAccounts(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	execAll(t, st, `UPDATE team_member SET role = 'owner' WHERE team_id = 2 AND user_id = 102`, `DELETE FROM team_member WHERE team_id = 3`)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := st.DeleteAccount(ctx, deleteParams(101, "@ann", now)); err != nil {
		t.Fatal(err)
	}
	del, err := st.DeleteAccount(ctx, deleteParams(102, "bob", now))
	if err != nil {
		t.Fatal(err)
	}
	if len(del.Closed) != 1 || del.Closed[0].ID != 2 || len(del.Frozen) != 0 {
		t.Fatalf("bob's delete %+v", del)
	}
	if _, err := st.RestoreTeam(ctx, 2, now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("console restore of a team of deleted accounts: %v", err)
	}
	execAll(t, st, `INSERT INTO email_token (user_id, purpose, token_hash, expires_at) VALUES
 (101, 'restore', REPEAT('e', 64), '2099-01-01'), (102, 'restore', REPEAT('f', 64), '2099-01-01')`)
	ann, err := st.RestoreByLink(ctx, strings.Repeat("e", 64), now, "sannrestore000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(ann.Unfrozen) != 1 || ann.Unfrozen[0] != 2 || len(ann.Reopened) != 0 {
		t.Fatalf("ann's restore %+v (acme closed with bob, not her)", ann)
	}
	if _, err := st.RestoreTeam(ctx, 2, now); err != nil {
		t.Fatalf("console restore with ann back: %v", err)
	}
	bob, err := st.RestoreByLink(ctx, strings.Repeat("f", 64), now, "sbobrestore000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(bob.Unfrozen) != 1 || len(bob.Reopened) != 0 || bob.User.Status != platformstore.UserActive {
		t.Fatalf("bob's restore %+v", bob)
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND role = 'owner' AND status = 'active'`); err != nil || n != 2 {
		t.Fatalf("owners back %d %v", n, err)
	}
	// Spent links do nothing.
	if _, err := st.RestoreByLink(ctx, strings.Repeat("e", 64), now, "sannrestore100000000000000000000"); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("spent link: %v", err)
	}
}
