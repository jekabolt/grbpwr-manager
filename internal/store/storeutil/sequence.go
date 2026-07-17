package storeutil

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// AllocateStyleModelNo assigns (or returns the already-assigned) 5-digit model number for a style
// (tech_card). It implements the crash-idempotent allocation from contract decision R10:
//
//  1. lock the style row FOR UPDATE so concurrent minters for the same style serialise;
//  2. mint a number from the style_model_no_allocation AUTO_INCREMENT table with an
//     INSERT ... ON DUPLICATE KEY UPDATE model_no = LAST_INSERT_ID(model_no) — a fresh style_id inserts
//     a new number, a retry re-selects the existing one instead of failing on UNIQUE(style_id);
//  3. persist the number onto tech_card.model_no, but only while it is still NULL;
//  4. re-read and return the persisted winner.
//
// A boot that dies between (2) and (3) therefore reuses the same number on retry rather than burning a
// second one (fixes problem 037). Every product of a style shares this single number — there is no
// standalone product model number anymore. Must run inside the caller's transaction so the allocation
// commits or rolls back with the SKU it numbers.
func AllocateStyleModelNo(ctx context.Context, conn dependency.DB, styleID int) (int, error) {
	// 1) Serialise concurrent allocation for this style.
	if _, err := QueryNamedOne[struct {
		ID int `db:"id"`
	}](ctx, conn, `SELECT id FROM tech_card WHERE id = :id FOR UPDATE`, map[string]any{"id": styleID}); err != nil {
		return 0, fmt.Errorf("lock style %d for model_no allocation: %w", styleID, err)
	}

	// 2) Idempotent mint: fresh style_id -> new AUTO_INCREMENT; retry -> the existing allocation.
	if err := ExecNamed(ctx, conn,
		`INSERT INTO style_model_no_allocation (style_id) VALUES (:id)
		 ON DUPLICATE KEY UPDATE model_no = LAST_INSERT_ID(model_no)`,
		map[string]any{"id": styleID}); err != nil {
		return 0, fmt.Errorf("allocate style %d model_no: %w", styleID, err)
	}

	// 3) Persist onto the style, never overwriting a number a prior run already set.
	if err := ExecNamed(ctx, conn,
		`UPDATE tech_card t JOIN style_model_no_allocation a ON a.style_id = t.id
		 SET t.model_no = a.model_no WHERE t.id = :id AND t.model_no IS NULL`,
		map[string]any{"id": styleID}); err != nil {
		return 0, fmt.Errorf("persist style %d model_no: %w", styleID, err)
	}

	// 4) Re-read the persisted winner (covers a value set by a prior/concurrent run).
	row, err := QueryNamedOne[struct {
		ModelNo sql.NullInt32 `db:"model_no"`
	}](ctx, conn, `SELECT model_no FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return 0, fmt.Errorf("reread style %d model_no: %w", styleID, err)
	}
	if !row.ModelNo.Valid {
		return 0, fmt.Errorf("style %d has no model_no after allocation", styleID)
	}
	return int(row.ModelNo.Int32), nil
}
