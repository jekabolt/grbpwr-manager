package design

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// PROBES THAT NEED ROWS. They are written and they are NOT RUN here: they wait for a disposable
// MySQL container started with CI=1.
//
// ⚠ HOW THEY REFUSE TO TOUCH PRODUCTION. The store package's own harness reads
// config/config.toml when CI is unset — and that file points at PRODUCTION, which is why
// `go test ./internal/store/...` drops production tables. This file has NO code path that can
// read a config file at all: the DSN is assembled from the CI environment variables and from
// nothing else, and without CI=1 every probe here SKIPS before opening a connection. That is a
// property of the code, not a habit of the runner.
//
// Start them with:
//
//	CI=1 MYSQL_HOST=127.0.0.1 MYSQL_PORT=3306 MYSQL_USER=root \
//	MYSQL_PASSWORD=... MYSQL_DATABASE=grbpwr_probe \
//	go test -count=1 -run TestDesignDB ./internal/store/design/

func designProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("CI") != "1" {
		t.Skip("design band DB probes run only against a disposable container (CI=1)")
	}
	host, port := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT")
	user, pass := os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD")
	name := os.Getenv("MYSQL_DATABASE")
	if host == "" || port == "" || user == "" || name == "" {
		t.Skip("MYSQL_* of the disposable container are not set")
	}
	// A LAST GATE ON THE NAME. Even under CI=1 a database whose name does not say it is a probe
	// database is refused: the cost of being wrong here is the production schema.
	if name == "grbpwr" || name == "grbpwr_beta" {
		t.Fatalf("refusing to run destructive probes against %q", name)
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		user, pass, host, port, name)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// designProbeStore builds a Store over the probe database with the same transaction factories the
// real one gets, so the probes exercise the real isolation levels rather than a stand-in.
//
// It is left unimplemented until the container exists, because wiring it means constructing a
// dependency.Repository over the probe handle — the harness for that is the runner's to provide,
// and inventing a second one here is how two harnesses drift apart.
func designProbeStore(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	t.Skip("the probe harness constructs the repository; see the runbook in this file's header")
	return nil
}

// TestDesignDBLazySlotBirthRaceHasExactlyOneWinner is probe 1's live half.
//
// TWO concurrent SetBenchSlot calls put a plate on the same EMPTY `front`. Exactly one wins; the
// other gets slot_rev_mismatch and NEVER a bare 1062 — 1062 is in no taxonomy and the client will
// not roll back on it, because what it waits for is Aborted.
//
// MUTATION: replace the upsert with «SELECT, no row, INSERT». THIS probe must go red — and it
// must go red with a duplicate-key error surfacing, not with a timeout.
func TestDesignDBLazySlotBirthRaceHasExactlyOneWinner(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, picA, picB := designProbeCard(t, db)

	var wg sync.WaitGroup
	results := make([]error, 2)
	slots := make([]*entity.DesignBenchSlot, 2)
	pics := []int{picA, picB}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots[i], results[i] = s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
				TechCardId:      card,
				Slot:            entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
				PictureId:       pics[i],
				ExpectedSlotRev: 0,
				Actor:           fmt.Sprintf("probe-%d", i),
			})
		}(i)
	}
	wg.Wait()

	won := 0
	for i, err := range results {
		if err == nil {
			won++
			require.Equal(t, 1, slots[i].SlotRev, "the winner's slot is born at rev 1")
			continue
		}
		require.ErrorIs(t, err, entity.ErrDesignSlotRevMismatch,
			"the loser must get slot_rev_mismatch, never a bare 1062 the client cannot undo")
		require.NotContains(t, err.Error(), "1062")
		require.NotNil(t, slots[i], "the refusal carries the slot's current state")
	}
	require.Equal(t, 1, won, "exactly one placement wins")
}

// TestDesignDBStaleCASIsRefusedAndChangesNothing is probe 2.
func TestDesignDBStaleCASIsRefusedAndChangesNothing(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, picA, picB := designProbeCard(t, db)

	first, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picA, ExpectedSlotRev: 0, Actor: "first",
	})
	require.NoError(t, err)
	require.Equal(t, 1, first.SlotRev)

	// Somebody who last read rev 0 tries to overwrite what is now rev 1.
	stale, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picB, ExpectedSlotRev: 0, Actor: "stale",
	})
	require.ErrorIs(t, err, entity.ErrDesignSlotRevMismatch)
	require.NotNil(t, stale, "the refusal carries the slot as it stands")
	require.Equal(t, 1, stale.SlotRev, "the revision did not move")
	require.Equal(t, int32(picA), stale.PictureId.Int32, "the plate did not move")
	require.NotNil(t, stale.Picture, "and the refusal shows WHICH plate, not only its id")

	// And the byline belongs to the writer that actually won — the assignment-order defect in the
	// plan's printed upsert leaves it at the previous author while the picture does move.
	require.Equal(t, "first", stale.SetBy)
}

// TestDesignDBForeignCardPlateIsRefused is probe 3. The schema cannot express this — a composite
// FK would have to cascade, and a detail slot must outlive its plate — so Go checks it in the same
// transaction.
//
// MUTATION: remove the tech_card_id comparison in setBenchSlotTx. THIS probe must go red.
func TestDesignDBForeignCardPlateIsRefused(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	cardA, _, _ := designProbeCard(t, db)
	_, foreignPic, _ := designProbeCard(t, db)

	_, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: cardA, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: foreignPic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignCardPlate)
}

// TestDesignDBBatchIsIdempotentOnClientRequestId is probe 4.
//
// MUTATION: drop the ON DUPLICATE KEY UPDATE from the batch insert. THIS probe must go red — with
// a duplicate-key error, which is precisely the failure a person sees as «my upload vanished».
func TestDesignDBBatchIsIdempotentOnClientRequestId(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, _, _ := designProbeCard(t, db)
	mediaA := designProbeMedia(t, db)
	mediaB := designProbeMedia(t, db)

	req := entity.DesignBatchRegister{
		TechCardId:      card,
		ClientRequestId: uuid.NewString(),
		Items: []entity.DesignUploadItem{
			{MediaId: mediaA, GhostView: entity.DesignViewFront},
			{MediaId: mediaB, GhostView: entity.DesignViewBack},
		},
		Actor: "probe",
	}
	first, err := s.RegisterBatch(ctx, req)
	require.NoError(t, err)
	require.False(t, first.Idempotent)
	require.Len(t, first.Pictures, 2)

	second, err := s.RegisterBatch(ctx, req)
	require.NoError(t, err, "a retry after a network timeout must not be an error")
	require.True(t, second.Idempotent)
	require.Equal(t, first.Batch.Id, second.Batch.Id, "the SAME batch, not a second one")
	require.Len(t, second.Pictures, 2, "and the same pictures, not four")
	for i := range first.Pictures {
		require.Equal(t, first.Pictures[i].Id, second.Pictures[i].Id)
	}

	var batches int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM design_batch WHERE tech_card_id = ?`, card).Scan(&batches))
	require.Equal(t, 1, batches)
}

// TestDesignDBHeaderAggregatesSurvivePagination is probe 5's live half.
//
// A card with MORE runs than fit on a page: total_runs must count them all.
//
// MUTATION: compute the aggregate from the loaded page. THIS probe must go red, and the number it
// reports must be exactly the page size — which is the tell that the header was counting the
// screen rather than the card.
func TestDesignDBHeaderAggregatesSurvivePagination(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, _, _ := designProbeCard(t, db)

	const total = DefaultRunPageLimit + 7
	const archived = 3
	for i := 0; i < total; i++ {
		designProbeRun(t, db, card, i < archived)
	}

	band, err := s.GetBand(ctx, card, DefaultRunPageLimit)
	require.NoError(t, err)
	require.Len(t, band.Runs, DefaultRunPageLimit, "the PAGE is one page")
	require.Equal(t, total, band.TotalRuns, "the HEADER counts the whole card")
	require.Equal(t, archived, band.ArchivedRuns)
	require.NotZero(t, band.NextCursor, "and the page says there is more")
}

// TestDesignDBHidePictureRefusesAPlateInASlot is probe 6. The guard reads in the SAME transaction
// as the update; reading first and writing after is a TOCTOU with a nicer name.
func TestDesignDBHidePictureRefusesAPlateInASlot(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, db)

	_, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = s.HidePicture(ctx, pic, true, "probe")
	require.ErrorIs(t, err, entity.ErrDesignInSlot)

	// Un-hiding is never guarded, and hiding becomes legal again once the slot lets go.
	_, err = s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: 0, ExpectedSlotRev: 1, Actor: "probe",
	})
	require.NoError(t, err)
	hidden, err := s.HidePicture(ctx, pic, true, "probe")
	require.NoError(t, err)
	require.True(t, hidden.HiddenAt.Valid)
}

// TestDesignDBDetailSlotQuotedByAVersionCannotBeDeleted covers the refusal a foreign key cannot
// express, because slot and version both cascade from tech_card and a RESTRICT would kill
// DeleteTechCard with a 1451 the caller has nothing to fix.
func TestDesignDBDetailSlotQuotedByAVersionCannotBeDeleted(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, db)

	slot, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		PictureId: pic, ExpectedSlotRev: 0, NewDetailName: "pocket flap", Actor: "probe",
	})
	require.NoError(t, err)

	// Still holding a plate.
	require.ErrorIs(t, s.DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignSlotFilled)

	_, err = s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{SlotId: slot.Id},
		PictureId: 0, ExpectedSlotRev: slot.SlotRev, Actor: "probe",
	})
	require.NoError(t, err)
	designProbeVersionQuotingSlot(t, db, card, slot.Id)
	require.ErrorIs(t, s.DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignSlotInVersion)
}

// TestDesignDBSilhouetteSideIsNotADetailSlot pins the refusal the plan does not name.
func TestDesignDBSilhouetteSideIsNotADetailSlot(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, db)

	slot, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)
	_, err = s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{SlotId: slot.Id},
		PictureId: 0, ExpectedSlotRev: slot.SlotRev, Actor: "probe",
	})
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignNotADetailSlot)
}

// TestDesignDBPlateCannotStandInTwoSlots is the probe for the correctness half of
// picture_already_in_slot: without the Go pre-check, INSERT … ON DUPLICATE KEY UPDATE collides on
// uq_design_bench_picture and mutates the OTHER slot's row.
//
// MUTATION: remove the pre-check. THIS probe must go red — and the tell is that `back` changed.
func TestDesignDBPlateCannotStandInTwoSlots(t *testing.T) {
	db := designProbeDB(t)
	s := designProbeStore(t, db)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, db)

	back, err := s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewBack},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = s.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignPictureAlreadyInSlot)

	var rev int
	var holder sql.NullInt32
	require.NoError(t, db.QueryRow(
		`SELECT slot_rev, picture_id FROM design_bench_slot WHERE id = ?`, back.Id).Scan(&rev, &holder))
	require.Equal(t, back.SlotRev, rev, "the OTHER slot was not touched by the refused placement")
	require.Equal(t, int32(pic), holder.Int32)
}

// ─────────── fixtures, to be finished with the container harness ───────────

func designProbeCard(t *testing.T, db *sql.DB) (cardID, picA, picB int) {
	t.Helper()
	t.Skip("fixture needs the container harness")
	return 0, 0, 0
}

func designProbeMedia(t *testing.T, db *sql.DB) int {
	t.Helper()
	t.Skip("fixture needs the container harness")
	return 0
}

func designProbeRun(t *testing.T, db *sql.DB, cardID int, archived bool) int {
	t.Helper()
	arch := "NULL"
	if archived {
		arch = "UTC_TIMESTAMP(6)"
	}
	res, err := db.Exec(fmt.Sprintf(`
		INSERT INTO design_run
			(tech_card_id, kind, status, client_request_id, provider_idempotency_key, archived_at)
		VALUES (?, 'flat', 'done', ?, ?, %s)`, arch),
		cardID, uuid.NewString(), uuid.NewString())
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

func designProbeVersionQuotingSlot(t *testing.T, db *sql.DB, cardID, slotID int) {
	t.Helper()
	t.Skip("fixture needs the container harness")
}

var _ = time.Now
