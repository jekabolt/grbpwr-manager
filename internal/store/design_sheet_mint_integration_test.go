package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestDesignSheetMintEndToEnd RUNS THE SQL HALF OF THE ATOMIC MINT. Everything under
// internal/store/design was written, compiled and covered by pure-logic probes, but not one line
// of its SQL had ever been executed: the package's own probes cannot import internal/store, so
// their fixtures skip. This probe lives in `store`, where NewForTest hands out a real repository,
// and drives MintSheetVersion through a real MySQL.
//
// EIGHT THINGS ARE PROVEN BY EXECUTION, not by reading:
//  1. a card with plates in bench slots mints v1 — the version row, its plate composition and its
//     frozen callouts are all written. The note freezes as the COMPOSED line, with BOTH part names
//     (which only the `parts` JSON parse in storedCallouts can supply), and the geometry beside it
//     carries no second copy of that note;
//  2. a repeat with the SAME client_request_id returns THAT version, not a phantom v2 (this is the
//     UNIQUE key the whole idempotency argument rests on, and it had never been executed);
//  3. a second mint takes MAX+1 == 2 — not 1 (a fresh count) and not 3 (an off-by-one) — and v1's
//     frozen composition is untouched by it;
//  4. a stale expected_lock_version is refused with the SAME conflict a plain save speaks, and the
//     document does not move;
//  5. a stale expected_plates (the bench moved between the read and the mint) refuses with
//     bench_moved, naming the slot;
//  6. plates absent from the frozen document trip the plates_not_in_document belt;
//  7. THE ROLLBACK, ACTUALLY EXECUTED: a refusal that fires AFTER UpdateTechCardTx has rewritten
//     the card undoes that write, lock-version bump and all. Points 4-6 all refuse BEFORE the
//     document is written, so none of them would have executed a rollback at all;
//  8. the sheet minimum is enforced against the real bench, not a payload.
//
// Every one of the eight has a negative control: breaking exactly the mechanism it guards (the
// idempotency branch, MAX+1, the bench CAS, the belt, the freeze, the unrepinned refusal, the
// `parts` parse, the empty annotation text, BOTH document lock belts, and committing the document
// write instead of rolling it back) reddens that probe and no other. The lock has TWO belts — an in-Go check and a load-bearing WHERE guard —
// so disabling only the first proves nothing.
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestDesignSheetMintEndToEnd(t *testing.T) {
	// Only run in CI (which points MYSQL_* at a container) or when the DSN explicitly targets a local
	// container. Otherwise skip — a bare local `go test ./internal/store/...` uses config.toml's prod
	// DSN, this test runs Automigrate and DELETEs rows, and this suite's TestMain drops all tables on
	// cleanup (see mysql_test.go / project memory).
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	T := s.TechCards()
	D := s.Design()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	// ─── FIXTURE: a card, three media, three pictures ───
	mediaFront := insertTestMedia(t, "mint-front-"+suffix)
	mediaBack := insertTestMedia(t, "mint-back-"+suffix)
	mediaSide := insertTestMedia(t, "mint-side-"+suffix)

	const cardFit = "regular"
	base := func() *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name:            "Design Mint Style",
			Stage:           entity.TechCardStageProto,
			StyleNumber:     ns("MINT-" + suffix),
			MeasurementUnit: entity.TechCardUnitMm,
			ApprovalState:   entity.TechCardApprovalDraft,
			Fit:             ns(cardFit),
		}
	}
	cardID, err := T.AddTechCard(ctx, base())
	require.NoError(t, err)
	// `tech_card.fit` IS NOT WRITTEN BY THE CARD SAVE. Since PR6 P2 the garment-level catalogue
	// fields live on the style and are written by the masked style editor (store/product/style.go)
	// or by the ZIP importer — never by AddTechCard/UpdateTechCard, which do not list the column.
	// The probe therefore seeds it the way the style editor does, so the mint's fit gate and fit
	// stamp have a value to see; without it every plate would be stamped with an empty fit.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET fit = ? WHERE id = ?", cardFit, cardID)
	require.NoError(t, err)
	t.Cleanup(func() {
		// design_sheet_version_plate/callout hold media with ON DELETE RESTRICT, so the version
		// rows must go before the card's media can ever be reclaimed; the card cascade takes the
		// versions, the bench slots and the pictures with it.
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
		for _, m := range []int{mediaFront, mediaBack, mediaSide} {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", m)
		}
	})

	// PICTURES ARE FILED BY THE PRODUCTION WRITER, not by a hand-rolled INSERT. RegisterBatch is
	// what a real upload calls, so the rows the mint later reads have exactly the column values
	// production has — including the ones nothing ever writes.
	batch, err := D.RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId:      cardID,
		ClientRequestId: uuid.NewString(),
		Items: []entity.DesignUploadItem{
			{MediaId: mediaFront, GhostView: entity.DesignViewFront},
			{MediaId: mediaBack, GhostView: entity.DesignViewBack},
			{MediaId: mediaSide, GhostView: entity.DesignViewSideL},
		},
		Actor: "probe",
	})
	require.NoError(t, err)
	require.Len(t, batch.Pictures, 3)
	picFront, picBack, picSide := batch.Pictures[0].Id, batch.Pictures[1].Id, batch.Pictures[2].Id

	// ─── FIXTURE: the two plates the sheet minimum demands, placed through the real writer ───
	frontSlot, err := D.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: cardID, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picFront, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)
	backSlot, err := D.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: cardID, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewBack},
		PictureId: picBack, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)

	// technical() is what the HANDLER injects (injectBenchPlatesAsTechnicalMedia). The store-level
	// probe must do it itself, because the belt inside the transaction reads tc.Media and nothing
	// else.
	// The kind is per-media and NOT optional: tech_card_media carries chk_tech_card_media_kind, so
	// an item with an empty kind is a raw 3819 rather than a refusal anyone can read.
	plateKinds := map[int]entity.TechCardMediaKind{
		mediaFront: entity.TechCardMediaFront,
		mediaBack:  entity.TechCardMediaBack,
		mediaSide:  entity.TechCardMediaSideL,
	}
	technical := func(ids ...int) []entity.TechCardMediaItem {
		out := make([]entity.TechCardMediaItem, 0, len(ids))
		for _, id := range ids {
			kind, ok := plateKinds[id]
			require.True(t, ok, "the probe must name a media kind for %d", id)
			out = append(out, entity.TechCardMediaItem{
				MediaId: id, Category: entity.TechCardMediaCategoryTechnical, Kind: kind,
			})
		}
		return out
	}
	// ONE CALLOUT WITH EVERY TEXT FIELD FILLED, AND TWO PARTS. All three text fields matter: the
	// frozen note is the COMPOSED line (part(s) / description / dimensions), and a callout that
	// only ever filled `description` in the fixtures is exactly how a dimension-only callout came
	// to freeze as a numbered tag with no text under it.
	//
	// TWO PARTS ARE THE POINT OF THE SECOND NAME. `parts` is its own JSON column (0310) and the
	// composer lists them all; it is written only when there is more than one (the single name
	// already lives in `part`). This is the pairing that a memory-only probe cannot reach: it hands
	// `Parts` to the composer directly and so never executes the SQL read that has to fill it.
	const wantFrozenText = "полочка, спинка: обтачка (6 мм)"
	calloutOnFront := func(desc string) entity.TechCardCallout {
		return entity.TechCardCallout{
			Number:      1,
			Part:        ns("полочка"),
			Parts:       []string{"полочка", "спинка"},
			Description: ns(desc),
			Dimensions:  ns("6 мм"),
			MediaId:     sql.NullInt32{Int32: int32(mediaFront), Valid: true},
			Kind:        entity.AnnotationKindPin,
			Color:       entity.AnnotationColorRed,
			Points: []entity.TechCardAnnotationPoint{
				{X: decimal.RequireFromString("0.25"), Y: decimal.RequireFromString("0.40")},
			},
		}
	}
	doc := func(name string, media []entity.TechCardMediaItem, callouts ...entity.TechCardCallout) *entity.TechCardInsert {
		tc := base()
		tc.Name = name
		tc.Media = media
		tc.Callouts = callouts
		return tc
	}

	read := func() *entity.TechCard {
		t.Helper()
		c, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		return c
	}
	versionCount := func() int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM design_sheet_version WHERE tech_card_id = ?`, cardID).Scan(&n))
		return n
	}

	// THE LOCK VERSION IS READ FRESH IN EVERY SUBTEST, never carried between them: a carried value
	// makes one failure cascade into five look-alike failures downstream, and the count of executed
	// outcomes stops meaning anything.
	lockNow := func() int { return read().LockVersion }

	var (
		v1RequestID = uuid.NewString()
		v1ID        int
	)

	// ─── 1. MINT v1 ───
	t.Run("mint v1 writes the version, its composition and its frozen callouts", func(t *testing.T) {
		lockBefore := lockNow()
		full, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: v1RequestID,
			TechCard:        doc("Design Mint Style v1", technical(mediaFront, mediaBack), calloutOnFront("обтачка")),
			// The bench is at the revision SetBenchSlot just wrote — CAS must agree.
			ExpectedLockVersion: lockBefore,
			ExpectedPlates: []entity.DesignExpectedPlate{
				{Slot: entity.DesignSlotRef{SlotId: frontSlot.Id}, SlotRev: frontSlot.SlotRev},
				{Slot: entity.DesignSlotRef{SlotId: backSlot.Id}, SlotRev: backSlot.SlotRev},
			},
			UploadedFitConfirm: true,
			MintedVia:          entity.DesignMintedViaCallout,
			Actor:              "probe",
		})
		require.NoError(t, err, "the first mint must succeed")
		require.NotNil(t, full)
		require.False(t, full.Idempotent, "a first mint is not a replay")
		require.Equal(t, 1, full.Version.VersionNumber, "the first version of a card is v1")
		require.Equal(t, v1RequestID, full.Version.ClientRequestId)
		require.Equal(t, entity.DesignMintedViaCallout, full.Version.MintedVia)
		v1ID = full.Version.Id
		require.NotZero(t, v1ID)

		// COMPOSITION — canonical order, front then back, both stamped with the card's fit.
		require.Len(t, full.Version.Plates, 2, "both plates were frozen")
		require.Equal(t, entity.DesignViewFront, full.Version.Plates[0].ViewKey)
		require.Equal(t, entity.DesignViewBack, full.Version.Plates[1].ViewKey)
		require.Equal(t, mediaFront, full.Version.Plates[0].MediaId)
		require.Equal(t, mediaBack, full.Version.Plates[1].MediaId)
		require.Equal(t, cardFit, full.Version.Plates[0].FitStamp.String,
			"a plate whose run declares no fit is stamped with the card's")

		// CALLOUTS — frozen from the COLUMNS the same transaction wrote, geometry and all.
		require.Len(t, full.Version.Callouts, 1, "the callout on a plate was frozen")
		require.Equal(t, 1, full.Version.Callouts[0].Number)
		require.Equal(t, mediaFront, full.Version.Callouts[0].MediaId)
		// THE FROZEN TEXT IS THE COMPOSED PRINT LINE, not the bare description: a card callout's
		// note is parts / description / dimensions, and it is the composed line that goes on paper
		// (entity.TechCardCalloutPrintedLine). A dimension-only callout — parts set, description
		// empty — would otherwise freeze as a numbered tag with no text under it, irreversibly.
		//
		// BOTH PART NAMES HAVE TO BE THERE. `Parts` is filled only by parsing the `parts` JSON
		// column in storedCallouts; without that parse the composer silently falls back to the
		// single `part` and the sheet prints «полочка: …», losing the second piece forever.
		require.Equal(t, wantFrozenText, full.Version.Callouts[0].Text.String)

		// READ IT STRAIGHT OUT OF THE TABLE TOO, not only through the loader: the assertion above
		// would also pass if loadSheetVersion composed the line on read, and the whole point of a
		// frozen version is that the bytes are ON THE ROW.
		var storedText string
		var storedAnn []byte
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT c.text, c.annotation FROM design_sheet_version_callout c
			 JOIN design_sheet_version v ON v.id = c.version_id
			 WHERE v.tech_card_id = ? AND v.version_number = 1`, cardID).Scan(&storedText, &storedAnn))
		require.Equal(t, wantFrozenText, storedText, "the composed line is what the column holds")
		require.NotEmpty(t, full.Version.Callouts[0].Annotation, "the geometry was frozen, not dropped")
		var ann map[string]any
		require.NoError(t, json.Unmarshal(full.Version.Callouts[0].Annotation, &ann))
		require.Equal(t, "TECH_CARD_ANNOTATION_KIND_PIN", ann["kind"],
			"the frozen geometry is the real wire converter's output, not a hand-rolled blob")
		pts, ok := ann["points"].([]any)
		require.True(t, ok, "the anchor survived the freeze: %s", full.Version.Callouts[0].Annotation)
		require.Len(t, pts, 1)
		require.Equal(t, "0.25", pts[0].(map[string]any)["x"].(map[string]any)["value"],
			"and it survived with its coordinate, not rounded to nothing")

		// THE ANNOTATION'S OWN `text` IS EMPTY, AND THE CONTRACT SAYS SO IN WORDS
		// (`DesignSheetCallout.annotation`: its own text field is left empty, the printed note
		// lives in `text`). Filling it would print the same note twice — once on the figure and
		// once in the legend. protojson omits an empty string, so the key must be ABSENT.
		require.NotContains(t, ann, "text",
			"the geometry must not carry a second copy of the note: %s", full.Version.Callouts[0].Annotation)
		var annOnRow map[string]any
		require.NoError(t, json.Unmarshal(storedAnn, &annOnRow))
		require.NotContains(t, annOnRow, "text")

		// JOURNAL — the `minted` line is born here and only here.
		var actions []string
		for _, is := range full.Issues {
			actions = append(actions, is.Action)
		}
		require.Equal(t, []string{entity.DesignIssueMinted}, actions)

		// And the DOCUMENT moved with it: same transaction, one lock bump.
		c := read()
		require.Equal(t, "Design Mint Style v1", c.Name)
		require.Equal(t, lockBefore+1, c.LockVersion, "the document write inside the mint bumped the lock exactly once")
	})

	// ─── 2. IDEMPOTENT REPLAY ───
	t.Run("a replay of the same client_request_id returns THAT version, not a phantom v2", func(t *testing.T) {
		before := versionCount()
		require.Equal(t, 1, before)
		lockBefore := read().LockVersion

		full, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: v1RequestID, // THE SAME KEY
			// A replay carries a DIFFERENT document on purpose: the idempotency check stands BEFORE
			// the document write, so if it fired the name below must never reach the card.
			TechCard:            doc("Design Mint Style REPLAYED", technical(mediaFront, mediaBack), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore,
			UploadedFitConfirm:  true,
			MintedVia:           entity.DesignMintedViaCallout,
			Actor:               "probe",
		})
		require.NoError(t, err, "a retry after a lost response must not be an error")
		require.True(t, full.Idempotent, "the store must SAY it is a replay — the handler skips post-commit work on it")
		require.Equal(t, 1, full.Version.VersionNumber, "the SAME version, not a second one")
		require.Equal(t, v1ID, full.Version.Id)
		require.Len(t, full.Version.Plates, 2)
		require.Equal(t, 1, versionCount(), "no phantom version row was created")

		c := read()
		require.Equal(t, "Design Mint Style v1", c.Name,
			"the replay wrote NOTHING: the idempotency check stands before the document write")
		require.Equal(t, lockBefore, c.LockVersion, "and it did not bump the lock version")
	})

	// ─── 3. MINT v2 — MAX+1 ───
	t.Run("a second mint takes MAX+1 == 2", func(t *testing.T) {
		// The bench grows a third plate first, so v2's composition is observably its own.
		sideSlot, err := D.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
			TechCardId: cardID, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewSideL},
			PictureId: picSide, ExpectedSlotRev: 0, Actor: "probe",
		})
		require.NoError(t, err)

		full, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			TechCard: doc("Design Mint Style v2",
				technical(mediaFront, mediaBack, mediaSide), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockNow(),
			ExpectedPlates: []entity.DesignExpectedPlate{
				{Slot: entity.DesignSlotRef{SlotId: sideSlot.Id}, SlotRev: sideSlot.SlotRev},
			},
			UploadedFitConfirm: true,
			MintedVia:          entity.DesignMintedViaPrint,
			Actor:              "probe",
		})
		require.NoError(t, err)
		require.False(t, full.Idempotent)
		require.Equal(t, 2, full.Version.VersionNumber, "MAX+1 — not 1 (a fresh count) and not 3 (an off-by-one)")
		require.NotEqual(t, v1ID, full.Version.Id)
		require.Len(t, full.Version.Plates, 3, "the grown bench is what v2 froze")
		require.Equal(t, entity.DesignViewSideL, full.Version.Plates[2].ViewKey)
		require.Equal(t, 2, versionCount())

		// v1 is untouched by v2 — the whole point of freezing.
		var v1Plates int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM design_sheet_version_plate WHERE version_id = ?`, v1ID).Scan(&v1Plates))
		require.Equal(t, 2, v1Plates, "v1 still holds the composition it was minted with")
	})

	// ─── 4. STALE LOCK VERSION — REFUSED, AND NOTHING MOVED ───
	t.Run("a stale expected_lock_version is refused and the document does not move", func(t *testing.T) {
		nameBefore := read().Name
		lockBefore := read().LockVersion

		_, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			TechCard: doc("Design Mint Style STALE-LOCK",
				technical(mediaFront, mediaBack, mediaSide), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore - 1, // somebody else saved under us
			UploadedFitConfirm:  true,
			MintedVia:           entity.DesignMintedViaPrint,
			Actor:               "probe",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrTechCardConflict,
			"the mint must speak the SAME conflict the plain save speaks")

		// THE ROLLBACK, EXECUTED. Everything the transaction touched before the refusal must be gone.
		c := read()
		require.Equal(t, nameBefore, c.Name, "the refused document did not land")
		require.Equal(t, lockBefore, c.LockVersion, "and the lock version did not move")
		require.Equal(t, 2, versionCount(), "no version was born by a refused mint")
	})

	// ─── 5. STALE EXPECTED PLATES — bench_moved ───
	t.Run("a bench that moved under the mint refuses with bench_moved", func(t *testing.T) {
		nameBefore := read().Name
		lockBefore := read().LockVersion

		_, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			TechCard: doc("Design Mint Style BENCH-MOVED",
				technical(mediaFront, mediaBack, mediaSide), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore,
			ExpectedPlates: []entity.DesignExpectedPlate{
				// The composer read the front slot at rev 1; it is not there any more.
				{Slot: entity.DesignSlotRef{SlotId: frontSlot.Id}, SlotRev: frontSlot.SlotRev + 7},
			},
			UploadedFitConfirm: true,
			MintedVia:          entity.DesignMintedViaPrint,
			Actor:              "probe",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrDesignBenchMoved)

		var refusal *entity.DesignMintRefusal
		require.ErrorAs(t, err, &refusal, "the refusal must name the slot, not merely announce a change")
		require.Equal(t, fmt.Sprintf("slot %d", frontSlot.Id), refusal.Metadata["slot"])

		c := read()
		require.Equal(t, nameBefore, c.Name, "the refused document did not land")
		require.Equal(t, lockBefore, c.LockVersion)
		require.Equal(t, 2, versionCount())
	})

	// ─── 6. PLATES NOT IN DOCUMENT — the belt ───
	t.Run("plates missing from the frozen document trip plates_not_in_document", func(t *testing.T) {
		nameBefore := read().Name
		lockBefore := read().LockVersion

		_, err := D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			// The side plate is on the bench but NOT listed as technical media — exactly the state
			// that would make every cutting piece detached and print an empty sketch.
			TechCard: doc("Design Mint Style NO-PLATES",
				technical(mediaFront, mediaBack), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore,
			UploadedFitConfirm:  true,
			MintedVia:           entity.DesignMintedViaPrint,
			Actor:               "probe",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrDesignPlatesNotInDocument)
		require.Contains(t, err.Error(), fmt.Sprintf("side_l=%d", mediaSide),
			"the refusal names WHICH plate is missing")

		c := read()
		require.Equal(t, nameBefore, c.Name)
		require.Equal(t, lockBefore, c.LockVersion)
		require.Equal(t, 2, versionCount())
	})

	// ─── 7. THE ROLLBACK, ACTUALLY EXECUTED ───
	//
	// EVERY REFUSAL ABOVE FIRES BEFORE THE DOCUMENT IS WRITTEN, so none of them proves the
	// transaction rolls anything back — «nothing moved» is trivially true when nothing was ever
	// written. `unrepinned_callouts` is the refusal that fires AFTER UpdateTechCardTx has already
	// rewritten the card inside this transaction (step 6 of the mint, step 5 being the document).
	// The lock version is the witness: the document write bumped it, and only a rollback puts it
	// back.
	t.Run("a refusal after the document write rolls the document back", func(t *testing.T) {
		// A NEW plate displaces the front one. The callout still stands on the OLD front picture —
		// which was a plate of v2 — so its address is the one this composition just replaced.
		mediaNew := insertTestMedia(t, "mint-new-"+suffix)
		plateKinds[mediaNew] = entity.TechCardMediaFront
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", mediaNew)
		})
		nb, err := D.RegisterBatch(ctx, entity.DesignBatchRegister{
			TechCardId: cardID, ClientRequestId: uuid.NewString(),
			Items: []entity.DesignUploadItem{{MediaId: mediaNew, GhostView: entity.DesignViewFront}},
			Actor: "probe",
		})
		require.NoError(t, err)
		require.Len(t, nb.Pictures, 1)

		cur, err := D.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
			TechCardId: cardID, Slot: entity.DesignSlotRef{SlotId: frontSlot.Id},
			PictureId: nb.Pictures[0].Id, ExpectedSlotRev: frontSlot.SlotRev, Actor: "probe",
		})
		require.NoError(t, err)
		require.Equal(t, int32(nb.Pictures[0].Id), cur.PictureId.Int32)

		nameBefore := read().Name
		lockBefore := lockNow()

		_, err = D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			TechCard: doc("Design Mint Style ROLLED-BACK",
				technical(mediaNew, mediaBack, mediaSide), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore,
			UploadedFitConfirm:  true,
			MintedVia:           entity.DesignMintedViaPrint,
			Actor:               "probe",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrDesignUnrepinnedCallouts,
			"the callout stands on the picture this composition replaced")

		// THE WITNESS. UpdateTechCardTx ran to completion inside this transaction before the freeze
		// refused: it rewrote the header and bumped lock_version. Both are gone.
		c := read()
		require.Equal(t, nameBefore, c.Name, "the document written inside the refused transaction was rolled back")
		require.Equal(t, lockBefore, c.LockVersion, "and so was its lock-version bump")
		require.Equal(t, 2, versionCount(), "no version was born")
		// The card's callouts survived too — the wipe-and-reinsert of the children rolled back with
		// everything else.
		var callouts int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tech_card_callout WHERE tech_card_id = ?`, cardID).Scan(&callouts))
		require.Equal(t, 1, callouts)
	})

	// ─── 8. THE SHEET MINIMUM — a card whose bench has no back plate cannot mint ───
	t.Run("the sheet minimum is enforced on the real bench", func(t *testing.T) {
		// Empty the back slot, then try: front alone is not a sheet.
		_, err := D.SetBenchSlot(ctx, entity.DesignBenchSlotSet{
			TechCardId: cardID, Slot: entity.DesignSlotRef{SlotId: backSlot.Id},
			PictureId: 0, ExpectedSlotRev: backSlot.SlotRev, Actor: "probe",
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = D.SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
				TechCardId: cardID, Slot: entity.DesignSlotRef{SlotId: backSlot.Id},
				PictureId: picBack, ExpectedSlotRev: backSlot.SlotRev + 1, Actor: "probe",
			})
		})

		lockBefore := read().LockVersion
		_, err = D.MintSheetVersion(ctx, entity.DesignSheetMint{
			TechCardId:      cardID,
			ClientRequestId: uuid.NewString(),
			TechCard: doc("Design Mint Style NO-BACK",
				technical(mediaFront, mediaBack, mediaSide), calloutOnFront("обтачка")),
			ExpectedLockVersion: lockBefore,
			UploadedFitConfirm:  true,
			MintedVia:           entity.DesignMintedViaPrint,
			Actor:               "probe",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrDesignSheetMinUnmet)
		require.Equal(t, 2, versionCount())
		require.Equal(t, lockBefore, read().LockVersion)
	})
}
