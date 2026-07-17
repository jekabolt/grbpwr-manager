package techcard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AddTechCardDevExpense appends one development-cost row to a tech card's journal and returns the
// stored row (with its id and server-stamped created_at). The row is a one-off record (never
// full-replaced); AmountBase is pre-folded by the caller (apisrv) via the costing FX, or left NULL
// when the currency has no rate.
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
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
		INSERT INTO tech_card_dev_expense
			(tech_card_id, kind, description, amount, currency, amount_base, fitting_id, sample_id, incurred_at)
		VALUES (:tech_card_id, :kind, :description, :amount, :currency, :amount_base, :fitting_id, :sample_id, :incurred_at)`,
		map[string]any{
			"tech_card_id": e.TechCardId,
			"kind":         strings.ToLower(strings.TrimSpace(e.Kind)),
			"description":  e.Description,
			"amount":       e.Amount,
			"currency":     strings.ToUpper(strings.TrimSpace(e.Currency)),
			"amount_base":  e.AmountBase,
			"fitting_id":   e.FittingId,
			"sample_id":    e.SampleId,
			"incurred_at":  e.IncurredAt,
		})
	if err != nil {
		return entity.TechCardDevExpense{}, fmt.Errorf("add tech card dev expense for %d: %w", e.TechCardId, err)
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
