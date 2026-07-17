package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestWS6SampleRoundSpineAndCarryOver exercises the WS6 round spine (Q7): sample round auto-assignment
// and the iteration chain, the sample optimistic lock (S25), dev-time substitutions (§2.7, Q2), and the
// S26 structured-remark carry-over (dedicated CRUD + ListOpenFittingChangeRequests, acceptance E.15).
// Integration test: runs only against a real MySQL (TestMain connects); it is here to compile-check the
// full WS6 store surface and document the intended behaviour.
func TestWS6SampleRoundSpineAndCarryOver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)

	var techCardID int
	var sampleIDs, fittingIDs []int
	defer func() {
		for _, id := range fittingIDs {
			_ = s.Fittings().DeleteFitting(ctx, id)
		}
		for _, id := range sampleIDs {
			_ = s.Samples().DeleteSample(ctx, id)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
	}()

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "WS6-ROUND-1", Valid: true},
		Name:            "ws6",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	mkSample := func(purpose string, prev int) *entity.SampleInsert {
		ins := &entity.SampleInsert{
			TechCardId:   techCardID,
			Purpose:      purpose,
			Status:       entity.SampleStatusPlanned,
			FabricSource: entity.SampleFabricSample,
			CreatedBy:    "tester",
			UpdatedBy:    "tester",
		}
		if prev != 0 {
			ins.PreviousSampleId = sql.NullInt32{Int32: int32(prev), Valid: true}
		}
		return ins
	}

	// Round 1: round_number auto-assigns to 1.
	s1, err := s.Samples().AddSample(ctx, mkSample(entity.SamplePurposeProto, 0))
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s1)
	sm1, err := s.Samples().GetSampleById(ctx, s1)
	require.NoError(t, err)
	require.True(t, sm1.RoundNumber.Valid && sm1.RoundNumber.Int32 == 1, "round 1 auto-assigned, got %+v", sm1.RoundNumber)

	// Round 2: chains to round 1, round_number auto-assigns to 2.
	s2, err := s.Samples().AddSample(ctx, mkSample(entity.SamplePurposeFit, s1))
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s2)
	sm2, err := s.Samples().GetSampleById(ctx, s2)
	require.NoError(t, err)
	require.True(t, sm2.RoundNumber.Valid && sm2.RoundNumber.Int32 == 2, "round 2 auto-assigned")
	require.True(t, sm2.PreviousSampleId.Valid && sm2.PreviousSampleId.Int32 == int32(s1), "chain link to round 1")

	// A previous-sample from a different style is rejected (chain integrity).
	otherID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "WS6-OTHER", Valid: true}, Name: "other",
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm, SizeIds: []int{4},
	})
	require.NoError(t, err)
	defer func() { _ = s.TechCards().DeleteTechCard(ctx, otherID) }()
	_, err = s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: otherID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusPlanned,
		FabricSource:     entity.SampleFabricSample,
		PreviousSampleId: sql.NullInt32{Int32: int32(s1), Valid: true}, // s1 belongs to techCardID, not otherID
		CreatedBy:        "tester", UpdatedBy: "tester",
	})
	require.ErrorIs(t, err, entity.ErrSamplePreviousForeign)

	// Optimistic lock (S25): a stale expected version aborts.
	require.ErrorIs(t, s.Samples().UpdateSample(ctx, s1, mkSample(entity.SamplePurposeProto, 0), 99), entity.ErrSampleConflict)
	// The correct version (0) succeeds and bumps the lock.
	require.NoError(t, s.Samples().UpdateSample(ctx, s1, mkSample(entity.SamplePurposeProto, 0), 0))

	// Substitution on round 1 (Q2: dev-only, never COGS).
	subID, err := s.Samples().AddSampleSubstitution(ctx, &entity.SampleSubstitutionInsert{
		SampleId:  s1,
		Reason:    sql.NullString{String: "out of stock", Valid: true},
		CreatedBy: "tester",
	})
	require.NoError(t, err)
	subs, err := s.Samples().ListSampleSubstitutions(ctx, s1)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.NoError(t, s.Samples().DeleteSampleSubstitution(ctx, subID))

	// Round 1 fitting on the sample, with an open structured remark (S26).
	f1, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		SampleId:    sql.NullInt32{Int32: int32(s1), Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		CreatedBy:   "tester",
		UpdatedBy:   "tester",
	})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, f1)
	crID, err := s.Fittings().AddFittingChangeRequest(ctx, &entity.FittingChangeRequest{
		FittingId: f1, Target: "pattern", Note: "raise hem 2cm",
		Zone: sql.NullString{String: "outer", Valid: true}, Status: entity.FittingChangeStatusOpen, CreatedBy: "tester",
	})
	require.NoError(t, err)

	// Carry-over: the open round-1 remark is visible when opening round 2 (E.15).
	open, err := s.Fittings().ListOpenFittingChangeRequests(ctx, techCardID, 2)
	require.NoError(t, err)
	require.Len(t, open, 1)
	require.Equal(t, crID, open[0].Id)
	require.True(t, open[0].RoundNumber.Valid && open[0].RoundNumber.Int32 == 1, "carry-over carries the source round")

	// Resolving it removes it from the carry-over view.
	resolved := open[0]
	resolved.Status = entity.FittingChangeStatusResolved
	require.NoError(t, s.Fittings().UpdateFittingChangeRequest(ctx, crID, &resolved))
	openAfter, err := s.Fittings().ListOpenFittingChangeRequests(ctx, techCardID, 0)
	require.NoError(t, err)
	require.Empty(t, openAfter, "resolved remark is no longer an open carry-over tip")
}
