package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// DeleteMediaByIdIfUnused — the delete a losing import is allowed to make.
//
// THE TRACE THESE TESTS ARE ABOUT. Media is de-duplicated by content hash. Import A uploads a
// picture and mints media row M. Import B, running against the same archive, finds M by its
// content hash, plans a REUSE (it uploads nothing), wins the claim on the import row and commits
// a tech card whose callout points at M. Import A then loses, gets ErrImportAlreadyCommitted, and
// compensates — it deletes "its own" M. Nothing about M looks different from A's side.
//
// WHY THE FOREIGN KEY DOES NOT SAVE THE WINNER. tech_card_callout.media_id is
// ON DELETE SET NULL (migration 0067). A DELETE against a row a callout points at therefore does
// not fail: it SUCCEEDS, and the winner's callout silently becomes unanchored. Then A goes on to
// delete the bucket objects, and the picture is gone from a live card. TestDeleteMediaByIdBlanks…
// below is that mechanism in the open, so nobody has to take the claim on trust.
//
// WHAT THE FIX HAS TO BE. Asking "is it used?" and then deleting is two acts, and B can commit
// between them. The check and the delete happen in one transaction, on the parent row's lock —
// TestDeleteMediaIfUnusedDecidesUnderTheLock… holds an adopter open and proves the decision waits
// for it instead of racing it.
//
// Integration tests: real MySQL only (TestMain connects + migrates). Every row is cleaned up.
// ─────────────────────────────────────────────────────────────────────────────

// mdiuStyle inserts a bare tech card to hang callouts off, and registers its cleanup.
func mdiuStyle(ctx context.Context, t *testing.T, tag string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO tech_card (style_number, name) VALUES (CONCAT(?, '-', UUID_SHORT()), ?)`, tag, tag)
	require.NoError(t, err)
	id64, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM tech_card WHERE id = ?`, id64)
	})
	return int(id64)
}

// mdiuCallout pins a callout to a media row — the reference of the trace, and the one an FK
// would not have refused. Cleanup is by tech_card cascade, so nothing to register here.
func mdiuCallout(ctx context.Context, t *testing.T, styleID, mediaID int) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO tech_card_callout (tech_card_id, callout_number, media_id) VALUES (?, 1, ?)`,
		styleID, mediaID)
	require.NoError(t, err)
	id64, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id64)
}

// mdiuCalloutMedia reads the callout's media_id back as a nullable, because NULL is exactly the
// state under test: a callout that got SET NULL still exists and still looks fine in a list.
func mdiuCalloutMedia(ctx context.Context, t *testing.T, calloutID int) sql.NullInt64 {
	t.Helper()
	var got sql.NullInt64
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT media_id FROM tech_card_callout WHERE id = ?`, calloutID).Scan(&got))
	return got
}

func mdiuMediaExists(ctx context.Context, t *testing.T, mediaID int) bool {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media WHERE id = ?`, mediaID).Scan(&n))
	return n == 1
}

// THE DEFECT, DEMONSTRATED. Before asserting that the new delete protects an adopted row, prove
// that the old one does not — otherwise the protection is a guard over a danger nobody verified.
//
// This is the exact call compensation used to make, against a media row a live tech card's callout
// points at. It does not return an error. The row goes, the callout stays, and its picture is now
// NULL — which is why the objects would have been deleted next.
func TestDeleteMediaByIdBlanksALiveCalloutInsteadOfRefusing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mediaID := insertTestMedia(t, "mdiu-fk-"+suffix)
	styleID := mdiuStyle(ctx, t, "MDIU-FK")
	calloutID := mdiuCallout(ctx, t, styleID, mediaID)

	require.NoError(t, s.Media().DeleteMediaById(ctx, mediaID),
		"ON DELETE SET NULL means the FK does NOT refuse this; if it ever starts refusing, the "+
			"whole premise of DeleteMediaByIdIfUnused has changed and this test is where to notice")

	assert.False(t, mdiuMediaExists(ctx, t, mediaID), "the row is gone")
	got := mdiuCalloutMedia(ctx, t, calloutID)
	assert.False(t, got.Valid,
		"and the live card's callout has quietly lost its picture — the loss compensation used to cause")
}

// THE TRACE. A mints M; B adopts M through a callout and commits; A compensates. M must survive,
// and A must be told who kept it — so that the caller knows the objects behind M are not its to
// delete either.
func TestDeleteMediaIfUnusedKeepsARowAdoptedThroughASetNullReference(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// adopted: minted by the losing import, then reused by the winner.
	// orphan:  minted by the losing import and never touched by anybody.
	adopted := insertTestMedia(t, "mdiu-adopted-"+suffix)
	orphan := insertTestMedia(t, "mdiu-orphan-"+suffix)
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range []int{adopted, orphan} {
			_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, id)
		}
	})

	styleID := mdiuStyle(ctx, t, "MDIU-WINNER")
	calloutID := mdiuCallout(ctx, t, styleID, adopted)

	deleted, refs, err := s.Media().DeleteMediaByIdIfUnused(ctx, adopted)
	require.NoError(t, err, "an adopted row is not an error — it is a row that is no longer ours")
	assert.False(t, deleted, "the winner's picture must survive the loser's compensation")
	require.Len(t, refs, 1, "and the loser must be told what kept it")
	assert.Equal(t, "tech_card", refs[0].Kind)
	assert.Equal(t, styleID, refs[0].EntityId, "the operator is sent to the card, not to the callout row")
	assert.Equal(t, "callout", refs[0].Slot)

	assert.True(t, mdiuMediaExists(ctx, t, adopted), "the media row is still there")
	got := mdiuCalloutMedia(ctx, t, calloutID)
	require.True(t, got.Valid, "and the callout still points at it, un-blanked")
	assert.EqualValues(t, adopted, got.Int64)

	// THE COUNTER-CHECK. Compensation that never deletes anything is not compensation. A row
	// nobody took must still go — otherwise this fix has traded one silent leak for another.
	deleted, refs, err = s.Media().DeleteMediaByIdIfUnused(ctx, orphan)
	require.NoError(t, err)
	assert.True(t, deleted, "an unreferenced row is exactly what compensation exists to take back")
	assert.Empty(t, refs)
	assert.False(t, mdiuMediaExists(ctx, t, orphan))

	// And a row that is already gone reads as deleted: the caller's next step (dropping the
	// objects that backed it) is right either way, and a repeated compensation must not stall.
	deleted, refs, err = s.Media().DeleteMediaByIdIfUnused(ctx, orphan)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Empty(t, refs)
}

// THE INTERLEAVING ITSELF. The two acts — "is it used?" and "delete it" — are only safe if nothing
// can adopt the row between them. This holds an adopter open across exactly that window: the
// callout INSERT has happened but has NOT committed when the delete is asked for.
//
// A delete that merely asked first and deleted second would see no reference (the insert is
// invisible until it commits), delete the row, and the adopter's COMMIT would then blank its own
// callout — the same loss, one moment later. So the assertion is not only «the row survived»: it
// is that the decision BLOCKED until the adopter finished. A verdict reached while the window was
// open would be a verdict about the past.
func TestDeleteMediaIfUnusedDecidesUnderTheLockNotBeforeIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mediaID := insertTestMedia(t, "mdiu-race-"+suffix)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM media WHERE id = ?`, mediaID)
	})
	styleID := mdiuStyle(ctx, t, "MDIU-RACE")

	// The winner's transaction: the reference exists, and is not yet visible to anybody else.
	adopter, err := testDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.NoError(t, err)
	committed := false
	defer func() {
		if !committed {
			_ = adopter.Rollback()
		}
	}()
	_, err = adopter.ExecContext(ctx,
		`INSERT INTO tech_card_callout (tech_card_id, callout_number, media_id) VALUES (?, 1, ?)`,
		styleID, mediaID)
	require.NoError(t, err)

	type verdict struct {
		deleted bool
		refs    int
		err     error
	}
	done := make(chan verdict, 1)
	go func() {
		d, refs, err := s.Media().DeleteMediaByIdIfUnused(ctx, mediaID)
		done <- verdict{deleted: d, refs: len(refs), err: err}
	}()

	// The window is open. Anything decided here is decided against a database that is about to
	// change, so nothing may be decided here at all.
	select {
	case v := <-done:
		t.Fatalf("the delete reached a verdict (deleted=%v, refs=%d, err=%v) while an uncommitted "+
			"adopter held the row — the check and the delete are not serialised against adoption",
			v.deleted, v.refs, v.err)
	case <-time.After(750 * time.Millisecond):
	}

	require.NoError(t, adopter.Commit())
	committed = true

	select {
	case v := <-done:
		require.NoError(t, v.err)
		assert.False(t, v.deleted, "once the adopter is visible, the row is not the loser's to take")
		assert.Equal(t, 1, v.refs, "and the reference that kept it is reported")
	case <-time.After(30 * time.Second):
		t.Fatal("the delete never finished after the adopter committed — it is stuck on a lock it " +
			"should have been granted")
	}

	assert.True(t, mdiuMediaExists(ctx, t, mediaID))
	var anchored int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_callout WHERE tech_card_id = ? AND media_id = ?`,
		styleID, mediaID).Scan(&anchored))
	assert.Equal(t, 1, anchored, "the winner's callout still holds its picture")
}
