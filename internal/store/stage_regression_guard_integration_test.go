package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardStageRegressionGuard is the acceptance test for the stage-regression guard: a tech
// card's lifecycle stage may advance freely, but once downstream artifacts exist (a sample, a
// release snapshot, or a colourway product.style_id) it may NOT move back to an earlier stage —
// that would desync the committed artifacts from the card's declared maturity. The block is a
// field-tagged ValidationError on `stage` (apisrv maps it to a 400), the write is rolled back, and
// forward moves stay allowed even with downstream artifacts present.
func TestTechCardStageRegressionGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	// mkCard creates a draft, sellable tech card at the given stage and registers cleanup (samples
	// cascade on tech_card delete, so removing the card is enough to clean the whole fixture).
	mkCard := func(name, styleNo string, stage entity.TechCardStage) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: stage, StyleNumber: ns(styleNo),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id)
		})
		return id
	}

	addSample := func(tcID int) {
		_, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
			TechCardId: tcID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusPlanned,
			FabricSource: entity.SampleFabricSample, CreatedBy: "tester", UpdatedBy: "tester",
		})
		require.NoError(t, err)
	}

	// setStage issues an UpdateTechCard that only moves the stage, echoing the card's current header
	// facts and optimistic-lock version (the minimal well-formed update payload).
	setStage := func(tcID int, stage entity.TechCardStage) error {
		card, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return T.UpdateTechCard(ctx, tcID, &entity.TechCardInsert{
			Name: card.Name, Stage: stage, StyleNumber: card.StyleNumber,
			MeasurementUnit: card.MeasurementUnit, ApprovalState: card.ApprovalState,
			UpdatedBy: "tester",
		}, card.LockVersion)
	}

	stageOf := func(tcID int) entity.TechCardStage {
		card, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return card.Stage
	}

	// (a) Regress (proto → idea) with a sample present → blocked by the field-tagged guard, and the
	// write is rolled back (stage stays proto).
	t.Run("regress_blocked_when_sample_exists", func(t *testing.T) {
		tcID := mkCard("SR Guard A", "SR-GUARD-A", entity.TechCardStageProto)
		addSample(tcID)

		err := setStage(tcID, entity.TechCardStageIdea)
		require.Error(t, err, "regressing proto→idea with a sample present must be blocked")
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve, "guard must return a field-tagged ValidationError")
		require.Equal(t, "stage", ve.Field)
		require.Contains(t, ve.Error(), "sample", "the message must name the blocking artifact")
		require.Contains(t, ve.Error(), "idea", "the message must name the target stage")

		require.Equal(t, entity.TechCardStageProto, stageOf(tcID), "the blocked update must roll back")
	})

	// (b) Regress (proto → idea) with nothing downstream → allowed.
	t.Run("regress_allowed_when_nothing_downstream", func(t *testing.T) {
		tcID := mkCard("SR Guard B", "SR-GUARD-B", entity.TechCardStageProto)

		require.NoError(t, setStage(tcID, entity.TechCardStageIdea),
			"regressing with no downstream artifacts must succeed")
		require.Equal(t, entity.TechCardStageIdea, stageOf(tcID))
	})

	// (c) Forward (proto → fit) is always allowed, even with a sample present.
	t.Run("forward_always_allowed_even_with_downstream", func(t *testing.T) {
		tcID := mkCard("SR Guard C", "SR-GUARD-C", entity.TechCardStageProto)
		addSample(tcID)

		require.NoError(t, setStage(tcID, entity.TechCardStageFit),
			"a forward stage move must be allowed regardless of downstream artifacts")
		require.Equal(t, entity.TechCardStageFit, stageOf(tcID))
	})
}
