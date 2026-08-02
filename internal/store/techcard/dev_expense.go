package techcard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// devExpenseDedupeWindowSeconds is how long an already-recorded expense shadows a byte-identical
// one. The journal deliberately has no natural key — the same card can honestly be charged the same
// amount for the same kind twice — so a UNIQUE index is the wrong tool: it would forbid the honest
// repeat forever, could not even see the note-less rows (MySQL counts each NULL as distinct), and
// adding it would have to dedupe rows of real money already in the journal. Time-boxing instead
// catches what actually doubles the spend (a retry after a timeout, a double-clicked Add — both
// arrive within seconds) while leaving the deliberate repeat representable a minute later, or
// immediately with a note, another date or another amount.
const devExpenseDedupeWindowSeconds = 60

// devExpenseDuplicateWhere matches an existing journal row that is the SAME expense as the one being
// inserted: every operator-entered field equal — NULL-safe, so note-less and unlinked rows still
// collide — and stamped inside the dedupe window. amount_base is deliberately excluded: it is folded
// from the FX rather than entered, so a retry that converted at a newer rate is still the same
// expense. The window is measured against the DB clock (created_at is DB-stamped), never the app's.
const devExpenseDuplicateWhere = `d.tech_card_id = :tech_card_id
			AND d.kind = :kind
			AND d.description <=> :description
			AND d.amount = :amount
			AND d.currency = :currency
			AND d.fitting_id <=> :fitting_id
			AND d.sample_id <=> :sample_id
			AND d.incurred_at <=> :incurred_at
			AND d.created_at > NOW() - INTERVAL :window SECOND`

const (
	// devExpenseInsertRetries caps how often the dedupe INSERT is re-run after losing an InnoDB lock
	// race with a simultaneous submission of the same expense. Contention is between two clicks of one
	// button, so a couple of attempts settle it; beyond that the lock error is surfaced honestly.
	devExpenseInsertRetries = 2
	// devExpenseRetryDelay is the base backoff between those attempts (grows linearly).
	devExpenseRetryDelay = 15 * time.Millisecond
)

// isLockContention reports whether err is InnoDB lock contention (deadlock or lock-wait timeout) —
// the shape a genuinely simultaneous submission takes, since the dedupe INSERT makes the racers
// serialise on the same index range. The root store retries transactions on exactly these codes.
func isLockContention(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205)
}

// AddTechCardDevExpense appends one development-cost row to a tech card's journal and returns the
// stored row (with its id and server-stamped created_at). The row is a one-off record (never
// full-replaced); AmountBase is pre-folded by the caller (apisrv) via the costing FX, or left NULL
// when the currency has no rate. A re-submission of an expense just recorded (client retry after a
// timeout, double-clicked Add) is rejected with ErrDuplicateDevExpense rather than appended twice —
// a doubled row silently inflates net_after_dev in style economics.
func (s *Store) AddTechCardDevExpense(ctx context.Context, e entity.TechCardDevExpense) (entity.TechCardDevExpense, error) {
	// A linked sample must belong to this expense's tech card — otherwise one style's spend would land
	// in another style's sample cost AND its own dev-cost total (double attribution) — NF-04.
	if e.SampleId.Valid {
		n, err := storeutil.QueryCountNamed(ctx, s.DB,
			`SELECT COUNT(*) FROM sample WHERE id = :s AND tech_card_id = :tc`,
			map[string]any{"s": e.SampleId.Int32, "tc": e.TechCardId})
		if err != nil {
			return entity.TechCardDevExpense{}, fmt.Errorf("check dev-expense sample: %w", err)
		}
		if n == 0 {
			return entity.TechCardDevExpense{}, entity.ErrSampleForeignToCard
		}
	}
	// A linked fitting must belong to this expense's tech card — anchored on it directly OR via the
	// product it fitted (a colourway's style_id) — so a round's R&D spend is never attributed to the
	// wrong style (S20/Q8 — this is the attribution the frontend used to dead-code to fitting_id 0).
	if e.FittingId.Valid {
		n, err := storeutil.QueryCountNamed(ctx, s.DB,
			`SELECT COUNT(*) FROM fitting f
			 LEFT JOIN product p ON p.id = f.product_id
			 WHERE f.id = :f AND (f.tech_card_id = :tc OR p.style_id = :tc)`,
			map[string]any{"f": e.FittingId.Int32, "tc": e.TechCardId})
		if err != nil {
			return entity.TechCardDevExpense{}, fmt.Errorf("check dev-expense fitting: %w", err)
		}
		if n == 0 {
			return entity.TechCardDevExpense{}, entity.ErrFittingForeignToCard
		}
	}
	params := map[string]any{
		"tech_card_id": e.TechCardId,
		"kind":         strings.ToLower(strings.TrimSpace(e.Kind)),
		"description":  e.Description,
		"amount":       e.Amount,
		"currency":     strings.ToUpper(strings.TrimSpace(e.Currency)),
		"amount_base":  e.AmountBase,
		"fitting_id":   e.FittingId,
		"sample_id":    e.SampleId,
		"incurred_at":  e.IncurredAt,
		"window":       devExpenseDedupeWindowSeconds,
	}
	// The dedupe guard is the INSERT's own WHERE NOT EXISTS rather than a pre-read: a double-clicked
	// Add fires two requests in parallel and a check-then-insert would let both of them through.
	// Zero rows written (no AUTO_INCREMENT id back) means an identical row is already there — see
	// devExpenseDuplicateWhere. Truly simultaneous submissions never both land either: InnoDB
	// serialises them on the index range this statement reads, and the loser fails with 1213 instead
	// of writing a second row. Re-running it then finds the committed row and reports the honest
	// duplicate, so "already recorded" is never dressed up as a server error the operator resends.
	var id int
	for attempt := 0; ; attempt++ {
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO tech_card_dev_expense
				(tech_card_id, kind, description, amount, currency, amount_base, fitting_id, sample_id, incurred_at)
			SELECT :tech_card_id, :kind, :description, :amount, :currency, :amount_base, :fitting_id, :sample_id, :incurred_at
			FROM DUAL
			WHERE NOT EXISTS (SELECT 1 FROM tech_card_dev_expense d WHERE `+devExpenseDuplicateWhere+`)`, params)
		if err == nil {
			break
		}
		if attempt >= devExpenseInsertRetries || !isLockContention(err) {
			return entity.TechCardDevExpense{}, fmt.Errorf("add tech card dev expense for %d: %w", e.TechCardId, err)
		}
		select {
		case <-ctx.Done():
			return entity.TechCardDevExpense{}, fmt.Errorf("add tech card dev expense for %d: %w", e.TechCardId, ctx.Err())
		case <-time.After(time.Duration(attempt+1) * devExpenseRetryDelay):
		}
	}
	if id == 0 {
		// Nothing was inserted, so no AUTO_INCREMENT value came back. Name the row that blocked it so
		// the operator can tell "already recorded" from "failed" without diffing the journal.
		dupID, derr := storeutil.QueryCountNamed(ctx, s.DB,
			`SELECT COALESCE(MAX(d.id), 0) FROM tech_card_dev_expense d WHERE `+devExpenseDuplicateWhere, params)
		if derr != nil {
			return entity.TechCardDevExpense{}, fmt.Errorf("find duplicate tech card dev expense for %d: %w", e.TechCardId, derr)
		}
		ref := "an identical expense"
		if dupID > 0 {
			ref = fmt.Sprintf("identical expense #%d", dupID)
		}
		return entity.TechCardDevExpense{}, fmt.Errorf(
			"%w: %s was recorded on this card within the last %ds; add a note, date or different amount to record a second, distinct expense",
			entity.ErrDuplicateDevExpense, ref, devExpenseDedupeWindowSeconds)
	}
	row, err := storeutil.QueryNamedOne[entity.TechCardDevExpense](ctx, s.DB, `
		SELECT id, tech_card_id, kind, description, amount, currency, amount_base, fitting_id, sample_id, incurred_at, created_at
		FROM tech_card_dev_expense WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return entity.TechCardDevExpense{}, fmt.Errorf("reload tech card dev expense %d: %w", id, err)
	}
	return row, nil
}

// DeleteTechCardDevExpense removes a single development-cost row by id.
func (s *Store) DeleteTechCardDevExpense(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM tech_card_dev_expense WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("delete tech card dev expense %d: %w", id, err)
	}
	return nil
}

// ListTechCardDevExpenses returns a tech card's development-cost journal, newest first.
func (s *Store) ListTechCardDevExpenses(ctx context.Context, techCardID int) ([]entity.TechCardDevExpense, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardDevExpense](ctx, s.DB, `
		SELECT id, tech_card_id, kind, description, amount, currency, amount_base, fitting_id, sample_id, incurred_at, created_at
		FROM tech_card_dev_expense
		WHERE tech_card_id = :tc
		ORDER BY COALESCE(incurred_at, DATE(created_at)) DESC, id DESC`,
		map[string]any{"tc": techCardID})
	if err != nil {
		return nil, fmt.Errorf("list tech card dev expenses for %d: %w", techCardID, err)
	}
	return rows, nil
}
