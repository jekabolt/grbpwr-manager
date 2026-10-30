package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SEAM DECISIONS (tech_card_seam_decision, 0439): an append-only log of what the technologist did
// with each seam guess. NOT A LEG OF UpdateTechCard and not card content: no card row lock, no
// lock_version, no released-card refusal, and clone / archive / restore do not carry it.

var seamDecisionColumns = []string{
	"tech_card_id", "operation_number", "action", "seam_key", "tier", "shown", "client_version", "created_by",
}

// AddTechCardSeamDecisions appends the decisions in ONE multi-row INSERT (≤ 50 rows, one batch), so
// the call is all-or-nothing without a transaction. The card's existence is checked first so an
// unknown card is sql.ErrNoRows like the other per-card writes; a delete racing the insert fails
// the FK instead, which the handler maps to NotFound too.
func (s *Store) AddTechCardSeamDecisions(ctx context.Context, in entity.TechCardSeamDecisionsWrite) (int, error) {
	// Re-validated here and not only trusted from the handler: a second caller (seeder) must not
	// bypass the shape rules.
	if err := entity.ValidateTechCardSeamDecisionsWrite(&in); err != nil {
		return 0, err
	}
	if _, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, s.DB, `SELECT id FROM tech_card WHERE id = :id`,
		map[string]any{"id": in.TechCardId}); err != nil {
		return 0, fmt.Errorf("tech card %d: %w", in.TechCardId, err)
	}
	rows := make([][]any, 0, len(in.Decisions))
	for _, d := range in.Decisions {
		var shown any // NULL when the client sent none
		if d.Shown != "" {
			shown = d.Shown
		}
		rows = append(rows, []any{
			in.TechCardId, d.OperationNumber, string(d.Action), d.SeamKey, string(d.Tier), shown, d.ClientVersion, in.By,
		})
	}
	if err := storeutil.BulkInsertRows(ctx, s.DB, "tech_card_seam_decision", seamDecisionColumns, rows); err != nil {
		return 0, fmt.Errorf("failed to add seam decisions to tech card %d: %w", in.TechCardId, err)
	}
	return len(rows), nil
}
