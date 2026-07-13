package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
	"strings"
)

// UpsertOpexEntries writes fixed-cost (OPEX) journal lines, one amount per (month, category),
// upserting on the UNIQUE (month, category) key (task 22). Callers pass base-currency amounts;
// category validity is enforced in dto before this point.
//
// NF-08: the dashboard now reads opex_line, so each aggregate is mirrored there as a base-currency
// line labelled '(aggregate)' (the same label migration 0112 backfilled). opex_entry is kept in sync
// for rollback safety. The '(aggregate)' label keeps this old lump API disjoint from per-item lines
// entered via UpsertOpexLines — the two never collide on the (month, category, label) key.
func (s *Store) UpsertOpexEntries(ctx context.Context, rows []entity.OpexEntry) error {
	lines := make([]entity.OpexLineInsert, 0, len(rows))
	for _, r := range rows {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO opex_entry (month, category, amount, note)
			VALUES (:month, :category, :amount, :note)
			ON DUPLICATE KEY UPDATE amount = VALUES(amount), note = VALUES(note)`,
			map[string]any{
				"month":    r.Month.Format("2006-01-02"),
				"category": r.Category,
				"amount":   r.Amount,
				"note":     r.Note,
			}); err != nil {
			return fmt.Errorf("upsert opex %s/%s: %w", r.Month.Format("2006-01"), r.Category, err)
		}
		lines = append(lines, entity.OpexLineInsert{
			Month:      r.Month,
			Category:   r.Category,
			Label:      opexAggregateLabel,
			Amount:     r.Amount,
			Currency:   strings.ToUpper(cache.GetBaseCurrency()),
			AmountBase: decimal.NullDecimal{Decimal: r.Amount, Valid: true},
			Note:       r.Note,
		})
	}
	if err := s.UpsertOpexLines(ctx, lines); err != nil {
		return fmt.Errorf("mirror opex aggregate to opex_line: %w", err)
	}
	return nil
}

// opexAggregateLabel is the reserved label for base-currency aggregates written by the legacy
// UpsertOpexEntries API (and backfilled from opex_entry by migration 0112).
const opexAggregateLabel = "(aggregate)"

// getOpexForPeriod returns the OPEX attributable to [from, to), day-pro-rated per month, and
// whether the period is FULLY covered — i.e. every calendar month the period overlaps has at least
// one costed OPEX line AND no line in those months is uncosted (an amount whose currency had no FX
// rate, so amount_base is NULL). `complete=false` makes the dashboard flag the operating result as
// incomplete: a period straddling two months with only one month entered would otherwise silently
// treat the missing month's fixed costs as zero and look complete, and an uncosted line would be
// dropped from the total with no warning. OPEX is summed per calendar month from opex_line (NF-08),
// each amount in base currency (amount_base); a period covering only part of a month is charged that
// month's share by day overlap, so a rolling 30-day window across two months gets the right fraction
// of each. All month arithmetic is in UTC so the month-floor filter matches the overlap math (a
// from.Location()-based floor could exclude a month the UTC overlap wants).
func (s *Store) getOpexForPeriod(ctx context.Context, from, to time.Time) (decimal.Decimal, bool, error) {
	fromUTC, toUTC := from.UTC(), to.UTC()
	// The earliest month that can overlap [from, to) is the month containing `from` (UTC).
	monthFloor := time.Date(fromUTC.Year(), fromUTC.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := storeutil.QueryListNamed[struct {
		Month    time.Time       `db:"month"`
		Amount   decimal.Decimal `db:"amount"`
		Uncosted int             `db:"uncosted"`
	}](ctx, s.DB, `
		SELECT month,
		       COALESCE(SUM(amount_base), 0) AS amount,
		       SUM(CASE WHEN amount_base IS NULL THEN 1 ELSE 0 END) AS uncosted
		FROM opex_line
		WHERE month >= :monthFloor AND month < :to
		GROUP BY month`,
		map[string]any{"monthFloor": monthFloor.Format("2006-01-02"), "to": toUTC})
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("get opex for period: %w", err)
	}

	monthKey := func(t time.Time) string { return fmt.Sprintf("%04d-%02d", t.Year(), int(t.Month())) }
	present := make(map[string]bool, len(rows))
	total := decimal.Zero
	anyUncosted := false
	for _, r := range rows {
		present[monthKey(r.Month)] = true
		if r.Uncosted > 0 {
			anyUncosted = true
		}
		monthStart := time.Date(r.Month.Year(), r.Month.Month(), 1, 0, 0, 0, 0, time.UTC)
		monthEnd := monthStart.AddDate(0, 1, 0)
		ovStart := maxTime(monthStart, fromUTC)
		ovEnd := minTime(monthEnd, toUTC)
		if !ovEnd.After(ovStart) {
			continue
		}
		daysInMonth := monthEnd.Sub(monthStart).Hours() / 24
		ovDays := ovEnd.Sub(ovStart).Hours() / 24
		frac := ovDays / daysInMonth // 0..1
		total = total.Add(r.Amount.Mul(decimal.NewFromFloat(frac)))
	}

	// Complete iff every month the period overlaps (monthFloor .. the month containing to-ε) has an
	// entry and none of the entered lines is uncosted. Iterating month starts while < to enumerates
	// exactly those overlapping months.
	complete := !anyUncosted
	for m := monthFloor; complete && m.Before(toUTC); m = m.AddDate(0, 1, 0) {
		if !present[monthKey(m)] {
			complete = false
		}
	}
	return total.Round(2), complete, nil
}

// getChannelSpendTotal sums marketing spend (channel_spend, base currency) over the period,
// using the same inclusive DATE bounds as the ROAS report (GetChannelSpendByCampaign) so the
// figure subtracted in the operating result matches the spend shown there.
func (s *Store) getChannelSpendTotal(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	row, err := storeutil.QueryNamedOne[struct {
		Total decimal.Decimal `db:"total"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(amount), 0) AS total
		FROM channel_spend
		WHERE date >= :from AND date <= :to AND UPPER(currency) = :baseCurrency`,
		map[string]any{
			"from":         from.Format("2006-01-02"),
			"to":           to.Format("2006-01-02"),
			"baseCurrency": baseCurrency,
		})
	if err != nil {
		return decimal.Zero, fmt.Errorf("get channel spend total: %w", err)
	}
	return row.Total, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
