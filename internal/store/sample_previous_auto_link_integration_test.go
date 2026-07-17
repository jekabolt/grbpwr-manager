package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAddSampleAutoLinksPreviousSampleId is the M2 fix's contract test. Review finding M2:
// sample.proto documents `previous_sample_id = 0` as "server links to the latest prior sample", the
// same auto-assign contract already honoured for round_number (MAX+1 when unset) — but AddSample
// wrote NULL for an unset previous_sample_id instead of resolving it, silently breaking the
// iteration chain for any client relying on the documented 0-means-auto behaviour. Two AddSample
// calls in a row with no previous_sample_id must chain: the second sample's previous_sample_id must
// equal the first sample's id.
func TestAddSampleAutoLinksPreviousSampleId(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)

	techCardID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "M2-AUTOLINK-1", Valid: true},
		Name:            "m2 auto-link",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	var sampleIDs []int
	defer func() {
		for _, id := range sampleIDs {
			_ = s.Samples().DeleteSample(ctx, id)
		}
		_ = s.TechCards().DeleteTechCard(ctx, techCardID)
	}()

	base := func() *entity.SampleInsert {
		return &entity.SampleInsert{
			TechCardId:   techCardID,
			Purpose:      entity.SamplePurposeProto,
			Status:       entity.SampleStatusPlanned,
			FabricSource: entity.SampleFabricSample,
			CreatedBy:    "tester",
			UpdatedBy:    "tester",
			// PreviousSampleId left zero-value (Valid: false) -> the wire's previous_sample_id = 0.
		}
	}

	// First sample of the card: no prior sample exists, so previous_sample_id stays NULL/unset.
	s1, err := s.Samples().AddSample(ctx, base())
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s1)
	sm1, err := s.Samples().GetSampleById(ctx, s1)
	require.NoError(t, err)
	require.False(t, sm1.PreviousSampleId.Valid, "the card's first sample has no prior sample to link")

	// Second AddSample, again with previous_sample_id unset: the server must auto-link to the latest
	// prior sample of the same card (s1) per the documented proto contract.
	s2, err := s.Samples().AddSample(ctx, base())
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s2)
	sm2, err := s.Samples().GetSampleById(ctx, s2)
	require.NoError(t, err)
	require.True(t, sm2.PreviousSampleId.Valid, "previous_sample_id=0 must auto-link, not stay NULL")
	require.Equal(t, int32(s1), sm2.PreviousSampleId.Int32, "auto-link must point at the latest prior sample")

	// A third sample continues the chain onto the second.
	s3, err := s.Samples().AddSample(ctx, base())
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s3)
	sm3, err := s.Samples().GetSampleById(ctx, s3)
	require.NoError(t, err)
	require.True(t, sm3.PreviousSampleId.Valid)
	require.Equal(t, int32(s2), sm3.PreviousSampleId.Int32, "chain follows the most recently added sample")

	// An explicit previous_sample_id is still honoured as-is (not overridden by auto-link), even when
	// it points earlier in the chain than the latest sample.
	explicit := base()
	explicit.PreviousSampleId = sql.NullInt32{Int32: int32(s1), Valid: true}
	s4, err := s.Samples().AddSample(ctx, explicit)
	require.NoError(t, err)
	sampleIDs = append(sampleIDs, s4)
	sm4, err := s.Samples().GetSampleById(ctx, s4)
	require.NoError(t, err)
	require.True(t, sm4.PreviousSampleId.Valid)
	require.Equal(t, int32(s1), sm4.PreviousSampleId.Int32, "an explicit previous_sample_id is not overridden")
}
