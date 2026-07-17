package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// styleCompositionDisplayRow is one fibre share of a style's structured composition, resolved with its
// dictionary name for display. The json tags define the wire shape written into TechCard.Composition —
// kept in sync with styleCompositionSelect (internal/store/product/query.go), which projects the same
// shape server-side in SQL for the colourway/product read paths.
type styleCompositionDisplayRow struct {
	FiberCode string          `db:"fiber_code" json:"fiber_code"`
	Name      string          `db:"name" json:"name"`
	Percent   decimal.Decimal `db:"percent" json:"percent"`
}

// applyStructuredComposition overlays tc.Composition with the structured style_composition rows
// (S17/WS3-Ф5, P4-flyover M1 04-MAZE-FLYOVER.md) when any exist for this style, falling back to the
// legacy free-text tech_card.composition value already loaded onto tc (by the `SELECT *` in
// GetTechCardById) when there are none. The legacy column is not dropped here — that is a later guarded
// M3, after every style is backfilled.
func applyStructuredComposition(ctx context.Context, db dependency.DB, tc *entity.TechCard) error {
	rows, err := storeutil.QueryListNamed[styleCompositionDisplayRow](ctx, db, `
		SELECT sc.fiber_code, COALESCE(f.name, sc.fiber_code) AS name, sc.percent
		FROM style_composition sc LEFT JOIN fiber f ON f.code = sc.fiber_code
		WHERE sc.tech_card_id = :id
		ORDER BY sc.percent DESC, sc.fiber_code`,
		map[string]any{"id": tc.Id})
	if err != nil {
		return fmt.Errorf("load tech card %d structured composition: %w", tc.Id, err)
	}
	if len(rows) == 0 {
		return nil
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("encode tech card %d structured composition: %w", tc.Id, err)
	}
	tc.Composition = sql.NullString{String: string(encoded), Valid: true}
	return nil
}
