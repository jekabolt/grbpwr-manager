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
//  1. mint a number from the style_model_no_allocation AUTO_INCREMENT table with an
//     INSERT ... ON DUPLICATE KEY UPDATE model_no = LAST_INSERT_ID(model_no) — a fresh style_id inserts
//     a new number, a re-run re-selects the existing one instead of failing on UNIQUE(style_id);
//  2. persist the number onto tech_card.model_no, but only while it is still NULL;
//  3. re-read and return the persisted winner.
//
// Every product of a style shares this single number — there is no standalone product model number
// anymore. Must run inside the caller's transaction so the allocation commits or rolls back with the
// SKU it numbers.
//
// # Why there is no longer a `SELECT id FROM tech_card ... FOR UPDATE` first
//
// The old step 1 took an exclusive row lock on the style "so concurrent minters serialise". It was
// both redundant and actively harmful:
//
//   - Redundant, because UNIQUE(style_id) on style_model_no_allocation plus the single-statement
//     INSERT ... ON DUPLICATE KEY UPDATE already *is* the serialisation point. Two transactions
//     allocating for the same style contend on that one unique-index record: the first inserts and
//     holds it; the second blocks inside its own INSERT and, once the first commits, takes the
//     duplicate branch and reads back the SAME model_no via LAST_INSERT_ID(). If the first rolls
//     back instead, the second inserts and gets a number of its own. Either way exactly one number
//     ends up attached to the style, and two different styles never collide because they touch
//     different index records (insert-intention locks in distinct gaps are compatible).
//
//   - Harmful, because every caller reaches this function through MintProductSKUs →
//     resolveSegments → ensureStyleModelNo, i.e. AFTER loadProductSKUFacts has already read the row
//     (`JOIN tech_card sty`). The enclosing transaction is SERIALIZABLE (store.Tx), where InnoDB
//     silently promotes plain SELECTs to shared (S) locks — so the transaction already held S on
//     exactly the row this statement then requested in X mode. Two such transactions deadlock on the
//     mutual S→X upgrade *before doing any work at all*, and any unrelated SERIALIZABLE reader of
//     that style row was enough to make it wait. store.Tx retries the whole closure on 1213, which
//     re-runs the allocation — and an InnoDB AUTO_INCREMENT counter is never rolled back, so each
//     wasted attempt walked the hard 5-digit model_no segment one step closer to its ceiling.
//
// Removing it leaves exactly one exclusive lock request on tech_card (step 2's UPDATE), taken at the
// moment the write genuinely happens instead of twice per allocation. That residual upgrade cannot be
// removed here — persisting model_no IS a write to a row the caller has already read — and it is
// harmless in practice: the allocator only runs on the cold path (tech_card.model_no still NULL), and
// the dominant caller (product.AddProduct → writeStyleFields) has already taken the style row in X
// mode before minting, so no upgrade happens at all there. Hoisting an explicit X lock into every
// caller's transaction preamble would reverse the product→style lock order those callers use today
// and trade this narrow window for a wider one.
//
// # Residual AUTO_INCREMENT burn
//
// InnoDB never rolls the AUTO_INCREMENT counter back, so a transaction that allocates and then aborts
// for any reason still consumes one number. That is inherent to minting from an AUTO_INCREMENT and
// cannot be fixed by reshaping this statement: an INSERT ... SELECT ... WHERE NOT EXISTS would burn
// identically on abort, while under SERIALIZABLE its self-referencing NOT EXISTS probe takes a shared
// gap lock on the very gap the INSERT then needs an insert-intention lock in — reintroducing, as a
// new deadlock class, precisely what the change above removes. So the fix here is to abort less: with
// the guaranteed upgrade-deadlock gone, retries (and therefore burnt numbers) become the exception
// rather than the rule. product.ClassifyModelNoCeiling still watches the remaining headroom.
func AllocateStyleModelNo(ctx context.Context, conn dependency.DB, styleID int) (int, error) {
	// 1) Idempotent mint AND the serialisation point: fresh style_id -> new AUTO_INCREMENT;
	// concurrent/duplicate attempt -> the existing allocation, read back through LAST_INSERT_ID.
	if err := ExecNamed(ctx, conn,
		`INSERT INTO style_model_no_allocation (style_id) VALUES (:id)
		 ON DUPLICATE KEY UPDATE model_no = LAST_INSERT_ID(model_no)`,
		map[string]any{"id": styleID}); err != nil {
		return 0, fmt.Errorf("allocate style %d model_no: %w", styleID, err)
	}

	// 2) Persist onto the style, never overwriting a number a prior run already set.
	if err := ExecNamed(ctx, conn,
		`UPDATE tech_card t JOIN style_model_no_allocation a ON a.style_id = t.id
		 SET t.model_no = a.model_no WHERE t.id = :id AND t.model_no IS NULL`,
		map[string]any{"id": styleID}); err != nil {
		return 0, fmt.Errorf("persist style %d model_no: %w", styleID, err)
	}

	// 3) Re-read the persisted winner (covers a value set by a prior/concurrent run).
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
