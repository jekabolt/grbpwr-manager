package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestDeleteTechCardBlockedByStyleAssembly is the acceptance test for P4-flyover M2
// (04-MAZE-FLYOVER.md): deleting a tech card that is referenced as an auxiliary component in another
// style's assembly bill (style_assembly.component_tech_card_id -> tech_card ON DELETE RESTRICT, 0174)
// must fail with a readable field-tagged error, not the raw `Error 1451` -> Internal 500 regression the
// review found (S24). Exercises the store guard and the handler-level status mapping (apierr).
func TestDeleteTechCardBlockedByStyleAssembly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	mkCard := func(name, styleNo string, purpose entity.TechCardPurpose, aux sql.NullString) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(styleNo),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: purpose, AuxSubtype: aux,
		})
		require.NoError(t, err)
		return id
	}

	styleID := mkCard("M2 Garment", "M2-DEL-GARMENT", entity.TechCardPurposeSellable, sql.NullString{})
	compID := mkCard("M2 Woven Brand Label", "M2-DEL-AUX", entity.TechCardPurposeAuxiliary,
		ns(string(entity.AuxSubtypeBrandLabel)))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testDB.ExecContext(bg, "DELETE FROM style_assembly WHERE style_id = ?", styleID)
		for _, id := range []int{styleID, compID} {
			_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", id)
		}
	})

	require.NoError(t, T.UpsertStyleAssembly(ctx, styleID, []entity.StyleAssemblyInsert{{
		ComponentTechCardId: compID, Qty: decimal.RequireFromString("1"), Active: true,
	}}, "tester"))

	// The referenced component cannot be deleted; the error is field-tagged, not a raw 1451/Internal.
	err = T.DeleteTechCard(ctx, compID)
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve, "guard must return a field-tagged ValidationError, not a raw DB error")
	require.Equal(t, "tech_card_id", ve.Field)
	require.Contains(t, ve.Error(), "assembly")

	// The handler-level mapping (apierr.FailedPrecondition) turns it into a readable gRPC status: the
	// S24 contract (field + reason + how-to-fix), FailedPrecondition rather than Internal.
	grpcErr := apierr.FailedPrecondition(ve)
	st, ok := status.FromError(grpcErr)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	var fieldViolationFound bool
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, fv := range br.GetFieldViolations() {
				if fv.GetField() == "tech_card_id" {
					fieldViolationFound = true
				}
			}
		}
	}
	require.True(t, fieldViolationFound, "status must carry a BadRequest field violation for tech_card_id")

	// Confirm the component still exists (delete was refused, not partially applied).
	_, err = T.GetTechCardById(ctx, compID)
	require.NoError(t, err)

	// Clearing the assembly bill unblocks the delete.
	require.NoError(t, T.UpsertStyleAssembly(ctx, styleID, nil, "tester"))
	require.NoError(t, T.DeleteTechCard(ctx, compID))
}
