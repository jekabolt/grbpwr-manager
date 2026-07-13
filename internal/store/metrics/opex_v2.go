package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// NF-08 OPEX v2 — the line-item journal (opex_line) + recurring templates (opex_recurring). This
// supersedes the single-aggregate-per-(month,category) opex_entry table: lines carry their own
// currency (folded to base on write) and a free-text label, so an operator can enter individual
// costs («Adobe — 60 USD», «зарплата швеи — 1200 EUR») instead of one lumped monthly figure. The
// dashboard operating result reads opex_line (see getOpexForPeriod). opex_entry is kept in sync by
// UpsertOpexEntries for rollback safety; its drop is a later migration.

// firstOfMonthUTC snaps t to the 1st of its month at 00:00 UTC — the canonical month key used
// throughout OPEX (matches the DATE column and the pro-rating math in getOpexForPeriod).
func firstOfMonthUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// foldOpexToBase converts amount in `currency` to the base currency using the manual costing FX
// rates (UPPERCASE ISO → base units per 1 unit). It returns an invalid NullDecimal — meaning
// "uncosted" — when no base currency is configured or the currency has no rate; such a line is
// excluded from the operating result and flagged, mirroring the fold used for production/dev costs.
func foldOpexToBase(amount decimal.Decimal, currency, base string, rates map[string]decimal.Decimal) decimal.NullDecimal {
	if base == "" {
		return decimal.NullDecimal{}
	}
	if currency == "" || strings.EqualFold(currency, base) {
		return decimal.NullDecimal{Decimal: amount.Round(2), Valid: true}
	}
	r, ok := rates[strings.ToUpper(currency)]
	if !ok {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: amount.Mul(r).Round(2), Valid: true}
}

// UpsertOpexLines writes operating-expense line items, upserting on the UNIQUE (month, category,
// label) key so a re-send with the same key updates in place (used both by the admin editor and by
// the aggregate-compat path). Callers fold AmountBase before this point (it is left NULL only when
// the line's currency has no FX rate); category validity is enforced in dto. Month is normalised to
// the 1st of the month here defensively.
func (s *Store) UpsertOpexLines(ctx context.Context, rows []entity.OpexLineInsert) error {
	for _, r := range rows {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO opex_line (month, category, label, amount, currency, amount_base, recurring_id, note)
			VALUES (:month, :category, :label, :amount, :currency, :amount_base, :recurring_id, :note)
			ON DUPLICATE KEY UPDATE
				amount = VALUES(amount),
				currency = VALUES(currency),
				amount_base = VALUES(amount_base),
				recurring_id = VALUES(recurring_id),
				note = VALUES(note)`,
			map[string]any{
				"month":        firstOfMonthUTC(r.Month).Format("2006-01-02"),
				"category":     r.Category,
				"label":        r.Label,
				"amount":       r.Amount,
				"currency":     strings.ToUpper(r.Currency),
				"amount_base":  r.AmountBase,
				"recurring_id": r.RecurringId,
				"note":         r.Note,
			}); err != nil {
			return fmt.Errorf("upsert opex line %s/%s/%s: %w",
				r.Month.Format("2006-01"), r.Category, r.Label, err)
		}
	}
	return nil
}

// DeleteOpexLine removes a single OPEX line by id. Deleting a materialised line (recurring_id set)
// only removes that month's booking; the worker re-materialises it on its next tick unless the
// template is archived, so to stop a recurring cost archive the template rather than deleting lines.
func (s *Store) DeleteOpexLine(ctx context.Context, id int) error {
	return storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM opex_line WHERE id = :id`, map[string]any{"id": id})
}

// ListOpexLines returns OPEX lines within the (inclusive, 1st-of-month) month bounds, optionally
// filtered to one category, newest month first then by label for a stable order.
func (s *Store) ListOpexLines(ctx context.Context, f entity.OpexLineFilter) ([]entity.OpexLine, error) {
	params := map[string]any{
		"from": firstOfMonthUTC(f.MonthFrom).Format("2006-01-02"),
		"to":   firstOfMonthUTC(f.MonthTo).Format("2006-01-02"),
	}
	catClause := ""
	if strings.TrimSpace(f.Category) != "" {
		catClause = " AND category = :category"
		params["category"] = f.Category
	}
	rows, err := storeutil.QueryListNamed[entity.OpexLine](ctx, s.DB, `
		SELECT id, month, category, label, amount, currency, amount_base, recurring_id, note,
		       created_at, updated_at
		FROM opex_line
		WHERE month >= :from AND month <= :to`+catClause+`
		ORDER BY month DESC, category, label, id`, params)
	if err != nil {
		return nil, fmt.Errorf("list opex lines: %w", err)
	}
	return rows, nil
}

// UpsertOpexRecurring inserts a recurring OPEX template when id == 0, otherwise updates the template
// with that id, returning its id. ActiveFrom/ActiveTo are normalised to the 1st of the month.
// Editing an existing template affects only months not yet materialised — the worker never rewrites
// an already-booked month (see MaterializeOpexRecurring).
func (s *Store) UpsertOpexRecurring(ctx context.Context, ins entity.OpexRecurringInsert, id int) (int, error) {
	activeTo := sqlNullMonth(ins.ActiveTo)
	params := map[string]any{
		"label":       ins.Label,
		"category":    ins.Category,
		"amount":      ins.Amount,
		"currency":    strings.ToUpper(ins.Currency),
		"active_from": firstOfMonthUTC(ins.ActiveFrom).Format("2006-01-02"),
		"active_to":   activeTo,
		"note":        ins.Note,
	}
	if id == 0 {
		newID, err := storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO opex_recurring (label, category, amount, currency, active_from, active_to, note)
			VALUES (:label, :category, :amount, :currency, :active_from, :active_to, :note)`, params)
		if err != nil {
			return 0, fmt.Errorf("insert opex recurring: %w", err)
		}
		return newID, nil
	}
	params["id"] = id
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE opex_recurring
		SET label = :label, category = :category, amount = :amount, currency = :currency,
		    active_from = :active_from, active_to = :active_to, note = :note
		WHERE id = :id`, params); err != nil {
		return 0, fmt.Errorf("update opex recurring %d: %w", id, err)
	}
	return id, nil
}

// ArchiveOpexRecurring marks a template archived so the worker stops materialising future months.
// Already-booked lines are left in place (past fixed costs stay on the operating result).
func (s *Store) ArchiveOpexRecurring(ctx context.Context, id int) error {
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE opex_recurring SET archived = TRUE WHERE id = :id`, map[string]any{"id": id})
}

// ListOpexRecurring returns recurring templates, active-only unless includeArchived, newest first.
func (s *Store) ListOpexRecurring(ctx context.Context, includeArchived bool) ([]entity.OpexRecurring, error) {
	where := "WHERE archived = FALSE"
	if includeArchived {
		where = ""
	}
	rows, err := storeutil.QueryListNamed[entity.OpexRecurring](ctx, s.DB, `
		SELECT id, label, category, amount, currency, active_from, active_to, note, archived,
		       created_at, updated_at
		FROM opex_recurring `+where+`
		ORDER BY active_from DESC, id DESC`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list opex recurring: %w", err)
	}
	return rows, nil
}

// MaterializeOpexRecurring books each active template into a monthly opex_line for every month from
// its ActiveFrom to min(upTo, ActiveTo), folding the amount to base with `rates`. It is INSERT-ONLY:
// a month already materialised (UNIQUE month,category,label) is never overwritten, so past bookings
// are immutable and editing a template's amount only affects months not yet reached. Idempotent —
// re-running books nothing new. Returns the number of lines newly created. `upTo` is the current
// time (worker) or a fixed month (tests); it is snapped to the 1st of its month.
func (s *Store) MaterializeOpexRecurring(ctx context.Context, upTo time.Time, rates map[string]decimal.Decimal) (int, error) {
	base := strings.ToUpper(cache.GetBaseCurrency())
	upToMonth := firstOfMonthUTC(upTo)

	templates, err := s.ListOpexRecurring(ctx, false)
	if err != nil {
		return 0, fmt.Errorf("materialize opex: load templates: %w", err)
	}

	created := 0
	for _, t := range templates {
		end := upToMonth
		if t.ActiveTo.Valid {
			if to := firstOfMonthUTC(t.ActiveTo.Time); to.Before(end) {
				end = to
			}
		}
		amountBase := foldOpexToBase(t.Amount, t.Currency, base, rates)
		for m := firstOfMonthUTC(t.ActiveFrom); !m.After(end); m = m.AddDate(0, 1, 0) {
			n, err := storeutil.ExecNamedRows(ctx, s.DB, `
				INSERT INTO opex_line (month, category, label, amount, currency, amount_base, recurring_id, note)
				VALUES (:month, :category, :label, :amount, :currency, :amount_base, :recurring_id, :note)
				ON DUPLICATE KEY UPDATE id = id`,
				map[string]any{
					"month":        m.Format("2006-01-02"),
					"category":     t.Category,
					"label":        t.Label,
					"amount":       t.Amount,
					"currency":     strings.ToUpper(t.Currency),
					"amount_base":  amountBase,
					"recurring_id": t.Id,
					"note":         t.Note,
				})
			if err != nil {
				return created, fmt.Errorf("materialize opex %s/%s (%s): %w",
					m.Format("2006-01"), t.Label, t.Category, err)
			}
			if n > 0 {
				created++
			}
		}
	}
	return created, nil
}

// sqlNullMonth normalises an optional ActiveTo bound to the 1st of the month for storage.
func sqlNullMonth(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return firstOfMonthUTC(t.Time).Format("2006-01-02")
}
