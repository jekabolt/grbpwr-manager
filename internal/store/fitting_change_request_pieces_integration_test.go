package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFittingChangeRequestPieceSet covers the 0256 change: a fitting change request points at a SET of
// cut-pieces (fitting_change_request_piece) instead of the single piece_id column, across every path
// that reads or writes one — the embedded batch on AddFitting, the dedicated CRUD, the fitting read
// projection, and the cross-round carry-over list.
//
// Integration test: runs only against a real MySQL (TestMain connects).
func TestFittingChangeRequestPieceSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)

	var techCardID int
	var fittingIDs []int
	defer func() {
		for _, id := range fittingIDs {
			_ = s.Fittings().DeleteFitting(ctx, id)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
	}()

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "FCRP-0001", Valid: true},
		Name:            "piece set",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
		Pieces: []entity.TechCardPiece{
			{Name: "полочка", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{Name: "рукав", PiecesPerGarment: 2, Mirrored: true, Grainline: "lengthwise"},
		},
	})
	require.NoError(t, err)

	card, err := s.TechCards().GetTechCardById(ctx, techCardID)
	require.NoError(t, err)
	require.Len(t, card.Pieces, 3)
	front, back, sleeve := card.Pieces[0].Id, card.Pieces[1].Id, card.Pieces[2].Id

	// AddFitting's embedded initial batch carries a multi-piece remark and a single-piece one.
	fittingID, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		CreatedBy:   "tester",
		UpdatedBy:   "tester",
		ChangeRequests: []entity.FittingChangeRequest{
			{
				Target: "construction", Note: "низ бейкой как на всём изделии",
				Zone:     sql.NullString{String: "hem", Valid: true},
				Status:   entity.FittingChangeStatusOpen,
				PieceIds: []int{front, back},
			},
			{
				Target: "pattern", Note: "убрать 4 см",
				Status:   entity.FittingChangeStatusOpen,
				PieceIds: []int{sleeve},
			},
		},
	})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, fittingID)

	got, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	require.Len(t, got.ChangeRequests, 2)
	// Selection order survives the round trip — the chips read back the way they were picked.
	require.Equal(t, []int{front, back}, got.ChangeRequests[0].PieceIds)
	require.Equal(t, "hem", got.ChangeRequests[0].Zone.String, "garment-area zone token stored as-is")
	require.Equal(t, []int{sleeve}, got.ChangeRequests[1].PieceIds)

	// Dedicated CRUD: add with a set, then read it back off the fitting.
	crID, err := s.Fittings().AddFittingChangeRequest(ctx, &entity.FittingChangeRequest{
		FittingId: fittingID, Target: "material", Note: "сменить подклад",
		Zone:      sql.NullString{String: "lining", Valid: true},
		Status:    entity.FittingChangeStatusOpen,
		PieceIds:  []int{back, sleeve},
		CreatedBy: "tester",
	})
	require.NoError(t, err)

	reload := func() entity.FittingChangeRequest {
		f, err := s.Fittings().GetFittingById(ctx, fittingID)
		require.NoError(t, err)
		for _, cr := range f.ChangeRequests {
			if cr.Id == crID {
				return cr
			}
		}
		t.Fatalf("change request %d not found on the fitting", crID)
		return entity.FittingChangeRequest{}
	}
	require.Equal(t, []int{back, sleeve}, reload().PieceIds)

	// THE REGRESSION THIS PINS: an edit that touches ONLY the piece set leaves every column on the
	// fitting_change_request row identical. MySQL is not connected with CLIENT_FOUND_ROWS, so the
	// UPDATE reports 0 affected rows — reading existence off that count would fail the call with
	// sql.ErrNoRows and roll the piece edit back.
	piecesOnly := reload()
	piecesOnly.PieceIds = []int{front}
	require.NoError(t, s.Fittings().UpdateFittingChangeRequest(ctx, crID, &piecesOnly))
	require.Equal(t, []int{front}, reload().PieceIds, "pieces-only edit must persist")

	// Clearing the set is a real state, not a no-op.
	cleared := reload()
	cleared.PieceIds = nil
	require.NoError(t, s.Fittings().UpdateFittingChangeRequest(ctx, crID, &cleared))
	require.Empty(t, reload().PieceIds)

	// A missing id still reports ErrNoRows rather than silently succeeding.
	require.ErrorIs(t, s.Fittings().UpdateFittingChangeRequest(ctx, 0, &cleared), sql.ErrNoRows)

	// Deleting a piece drops that pin and leaves the remark (and its other pins) standing — the
	// set-shaped analogue of the old column's ON DELETE SET NULL.
	multi := got.ChangeRequests[0]
	require.Equal(t, []int{front, back}, multi.PieceIds)
	card.Pieces = card.Pieces[1:] // drop «полочка» from the card
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, techCardID, &card.TechCardInsert, card.LockVersion))

	after, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	var reloadedMulti entity.FittingChangeRequest
	for _, cr := range after.ChangeRequests {
		if cr.Id == multi.Id {
			reloadedMulti = cr
		}
	}
	require.Equal(t, multi.Id, reloadedMulti.Id, "the remark survives its piece being deleted")
	require.Equal(t, []int{back}, reloadedMulti.PieceIds, "only the deleted piece's pin is gone")
}
