package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type assemblyClockRow struct {
	LineageID string `db:"lineage_id"`
	Revision  uint64 `db:"revision"`
}

func createTechCardAssemblyClock(ctx context.Context, db dependency.DB, techCardID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_assembly_clock (tech_card_id, lineage_id, revision)
		VALUES (:id, LOWER(REPLACE(UUID(), '-', '')), 1)`, map[string]any{"id": techCardID}); err != nil {
		return fmt.Errorf("create assembly clock for tech card %d: %w", techCardID, err)
	}
	return nil
}

// bumpTechCardAssemblyClock runs only under the card-row lock. Revision changes in the same
// transaction as the operation/seam mutation, so no reader can observe a mixed clock and graph.
func bumpTechCardAssemblyClock(ctx context.Context, db dependency.DB, techCardID int) error {
	rows, err := storeutil.ExecNamedRows(ctx, db, `
		INSERT INTO tech_card_assembly_clock (tech_card_id, lineage_id, revision)
		VALUES (:id, LOWER(REPLACE(UUID(), '-', '')), 1)
		ON DUPLICATE KEY UPDATE revision = revision + 1`,
		map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("bump assembly clock for tech card %d: %w", techCardID, err)
	}
	// MySQL reports one affected row for the self-healing insert and two for the duplicate-key
	// update. Accept both: direct card creators outside this package get a valid lineage/revision 1,
	// while an existing authoritative clock advances exactly once.
	if rows < 1 || rows > 2 {
		return fmt.Errorf("unexpected assembly clock write count %d for tech card %d", rows, techCardID)
	}
	return nil
}

func readTechCardAssemblyClock(ctx context.Context, db dependency.DB, techCardID int) (assemblyClockRow, error) {
	row, err := storeutil.QueryNamedOne[assemblyClockRow](ctx, db, `
		SELECT lineage_id, revision FROM tech_card_assembly_clock WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return assemblyClockRow{}, fmt.Errorf("read assembly clock for tech card %d: %w", techCardID, err)
	}
	return row, nil
}

func (r assemblyClockRow) Token() string {
	return fmt.Sprintf("asm:v1:%s:%d", r.LineageID, r.Revision)
}
