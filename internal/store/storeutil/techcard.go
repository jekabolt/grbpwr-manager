package storeutil

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// RequireMutableTechCard rejects content writes to a released card. Callers invoke it inside their
// write transaction so the SERIALIZABLE transaction keeps the checked approval state stable until
// the content mutation commits.
func RequireMutableTechCard(ctx context.Context, db dependency.DB, styleID int) error {
	row, err := QueryNamedOne[struct {
		ApprovalState string `db:"approval_state"`
	}](ctx, db, `SELECT approval_state FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return fmt.Errorf("load tech card %d approval state: %w", styleID, err)
	}
	if row.ApprovalState == string(entity.TechCardApprovalReleased) {
		return entity.ErrTechCardReleased
	}
	return nil
}
