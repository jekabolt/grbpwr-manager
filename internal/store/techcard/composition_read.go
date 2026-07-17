package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// loadStructuredComposition populates tc.CompositionEntries from the structured style_composition
// rows (S17/WS3-Ф5) for this style, resolved with their dictionary display name. tc.Composition (the
// legacy free-text column, already loaded by the `SELECT *` in GetTechCardById) is NEVER touched here
// (M1 fix): composition used to be silently overloaded — applyStructuredComposition (P4-flyover M1,
// 04-MAZE-FLYOVER.md) JSON-encoded these same rows INTO tc.Composition once style_composition gained
// any, switching the wire shape of a plain-text field by data, not by version. That JSON-transition is
// cancelled: the typed CompositionEntries field is the replacement, composition on the wire is legacy
// plain-text ONLY, always. The legacy column is not dropped here — that is a later guarded M3, after
// every style is backfilled.
func loadStructuredComposition(ctx context.Context, db dependency.DB, tc *entity.TechCard) error {
	rows, err := storeutil.QueryListNamed[entity.CompositionEntry](ctx, db, `
		SELECT sc.fiber_code, COALESCE(f.name, sc.fiber_code) AS name, sc.percent
		FROM style_composition sc LEFT JOIN fiber f ON f.code = sc.fiber_code
		WHERE sc.tech_card_id = :id
		ORDER BY sc.percent DESC, sc.fiber_code`,
		map[string]any{"id": tc.Id})
	if err != nil {
		return fmt.Errorf("load tech card %d structured composition: %w", tc.Id, err)
	}
	tc.CompositionEntries = rows
	return nil
}
