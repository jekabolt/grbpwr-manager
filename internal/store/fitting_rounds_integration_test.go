package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFittingsShareARound covers the post-WS6 round model (§2.7): a sample is the OBJECT of a
// development round and a fitting is an EVENT on it, so a round can host MORE THAN ONE fitting —
// the same sample tried on twice, or two samples (sizes/colourways) of the same round. The 0102
// UNIQUE (tech_card_id, round_number) predated that model and rejected the second fitting with a
// driver 1062 the API surfaced as a bare "can't add fitting"; 0300 replaces it with a plain index.
//
// Integration test: runs only against a real MySQL (TestMain connects).
func TestFittingsShareARound(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

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
		StyleNumber:     sql.NullString{String: "FIT-ROUND-1", Valid: true},
		Name:            "round sharing",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	mkSample := func() *entity.SampleInsert {
		return &entity.SampleInsert{
			TechCardId:   techCardID,
			Purpose:      entity.SamplePurposeFit,
			Status:       entity.SampleStatusPlanned,
			FabricSource: entity.SampleFabricSample,
			CreatedBy:    "tester",
			UpdatedBy:    "tester",
		}
	}
	sampleA, err := s.Samples().AddSample(ctx, mkSample())
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, sampleA)

	addFitting := func(sampleID int, round sql.NullInt32) (int, error) {
		return s.Fittings().AddFitting(ctx, &entity.FittingInsert{
			TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
			SampleId:    sql.NullInt32{Int32: int32(sampleID), Valid: true},
			RoundNumber: round,
			FittingDate: time.Now().UTC(),
			Status:      entity.FittingPlanned,
			Verdict:     entity.FittingPending,
			CreatedBy:   "tester",
			UpdatedBy:   "tester",
		})
	}

	// The admin client mirrors the SAMPLE's round into the fitting it creates from the tech card,
	// so both try-ons of the same sample carry round 1 — this is the reported failure.
	f1, err := addFitting(sampleA, sql.NullInt32{Int32: 1, Valid: true})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, f1)

	f2, err := addFitting(sampleA, sql.NullInt32{Int32: 1, Valid: true})
	require.NoError(t, err, "a second fitting on the same sample must be allowed")
	fittingIDs = append(fittingIDs, f2)

	// A sample-linked fitting that sends no round inherits the SAMPLE's round rather than taking
	// MAX+1: the authoritative round is the sample's, and the per-card try-on counter would have
	// invented a round the style never had.
	f3, err := addFitting(sampleA, sql.NullInt32{})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, f3)
	got, err := s.Fittings().GetFittingById(ctx, f3)
	require.NoError(t, err)
	require.True(t, got.RoundNumber.Valid && got.RoundNumber.Int32 == 1,
		"sample round inherited, got %+v", got.RoundNumber)

	// A second sample of the same round (two sizes sewn for round 1) also gets tried on.
	sampleB, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: techCardID, Purpose: entity.SamplePurposeFit, Status: entity.SampleStatusPlanned,
		FabricSource: entity.SampleFabricSample, RoundNumber: sql.NullInt32{Int32: 1, Valid: true},
		CreatedBy: "tester", UpdatedBy: "tester",
	})
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, sampleB)
	f4, err := addFitting(sampleB, sql.NullInt32{})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, f4)

	// A card fitting with no sample keeps the per-card try-on counter (MAX+1 over the card).
	f5, err := s.Fittings().AddFitting(ctx, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		CreatedBy:   "tester",
		UpdatedBy:   "tester",
	})
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, f5)
	got5, err := s.Fittings().GetFittingById(ctx, f5)
	require.NoError(t, err)
	require.True(t, got5.RoundNumber.Valid && got5.RoundNumber.Int32 == 2,
		"sample-less fitting auto-numbers to MAX+1, got %+v", got5.RoundNumber)

	// Editing a fitting onto an already-tried-on round is allowed too (the same constraint used to
	// reject it on the update path).
	cur, err := s.Fittings().GetFittingById(ctx, f5)
	require.NoError(t, err)
	require.NoError(t, s.Fittings().UpdateFitting(ctx, f5, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		SampleId:    sql.NullInt32{Int32: int32(sampleA), Valid: true},
		RoundNumber: sql.NullInt32{Int32: 1, Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		UpdatedBy:   "tester",
	}, cur.LockVersion))
}
