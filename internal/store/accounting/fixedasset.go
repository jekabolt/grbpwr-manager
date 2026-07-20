package accounting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	storeutil "github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// CreateFixedAsset adds a capitalised asset to the register (depreciated straight-line from AcquiredOn
// over UsefulLifeMonths). It does not post the acquisition itself — record the purchase (Dr 1220 / Cr
// cash/payable) separately; the register drives the depreciation charge only.
func (s *Store) CreateFixedAsset(ctx context.Context, in entity.FixedAssetInsert) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
		INSERT INTO fixed_asset (name, cost_base, acquired_on, useful_life_months)
		VALUES (:name, :cost, :acquired, :life)`,
		map[string]any{
			"name":     in.Name,
			"cost":     in.CostBase,
			"acquired": in.AcquiredOn.UTC().Format(dateLayout),
			"life":     in.UsefulLifeMonths,
		})
	if err != nil {
		return 0, fmt.Errorf("accounting: create fixed asset: %w", err)
	}
	return id, nil
}

// ListFixedAssets returns the register, newest first.
func (s *Store) ListFixedAssets(ctx context.Context) ([]entity.FixedAsset, error) {
	rows, err := storeutil.QueryListNamed[entity.FixedAsset](ctx, s.DB,
		`SELECT id, name, cost_base, acquired_on, useful_life_months, disposed_on, created_at
		 FROM fixed_asset ORDER BY id DESC`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("accounting: list fixed assets: %w", err)
	}
	return rows, nil
}

// PostDepreciationDue posts every not-yet-posted monthly straight-line depreciation charge for every
// asset, up to and including the month `upTo` falls in. Each month is one entry (Dr 6370 / Cr 1225)
// keyed "asset:<id>:<YYYY-MM>", so re-running only fills gaps (the posting primitive is idempotent on
// source_key). Charges use cumulative rounding so the total depreciation equals cost exactly at the
// end of the useful life.
//
// A month whose accounting period is already closed cannot be posted into; because each charge is
// computed independently (not from prior posted state) such a month would otherwise be dropped
// permanently — under-depreciating the asset with no signal. This is realistic when a back-dated
// asset is added after its early months are closed. So closed months are NOT silently swallowed:
// `skipped` counts them, and the caller surfaces it ("posted N, skipped M — closed periods") so the
// operator can reopen the period (or post a manual catch-up) rather than trusting an incomplete run.
// Returns the number of charges newly posted and the number skipped because their period was closed.
func (s *Store) PostDepreciationDue(ctx context.Context, upTo time.Time) (int, int, error) {
	assets, err := s.ListFixedAssets(ctx)
	if err != nil {
		return 0, 0, err
	}
	upToMonth := firstOfMonthUTC(upTo)
	posted, skipped := 0, 0

	for _, a := range assets {
		start := firstOfMonthUTC(a.AcquiredOn)
		cost := a.CostBase
		life := a.UsefulLifeMonths
		// Iterate month indices 1..life; the charge for month i is cumDepr(i) − cumDepr(i−1).
		for i := 1; i <= life; i++ {
			month := start.AddDate(0, i-1, 0)
			if month.After(upToMonth) {
				break
			}
			if a.DisposedOn.Valid && !month.Before(firstOfMonthUTC(a.DisposedOn.Time)) {
				break // stop at disposal
			}
			charge := cumDepr(cost, i, life).Sub(cumDepr(cost, i-1, life))
			if charge.LessThanOrEqual(decimal.Zero) {
				continue
			}
			monthEnd := month.AddDate(0, 1, -1)
			key := fmt.Sprintf("asset:%d:%s", a.ID, month.Format("2006-01"))
			_, dup, perr := s.CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
				OccurredAt:  monthEnd,
				Description: fmt.Sprintf("Depreciation — %s (%s)", a.Name, month.Format("2006-01")),
				SourceType:  entity.AcctSourceDepreciation,
				SourceKey:   key,
				Lines: []entity.AcctJournalLineInsert{
					{AccountCode: "6370", Side: entity.AcctSideDebit, Amount: charge},
					{AccountCode: "1225", Side: entity.AcctSideCredit, Amount: charge},
				},
			})
			if perr != nil {
				if errors.Is(perr, entity.ErrAcctPeriodClosed) {
					skipped++ // month already closed — surfaced to the caller, not silently dropped
					continue
				}
				return posted, skipped, fmt.Errorf("accounting: post depreciation asset %d %s: %w", a.ID, key, perr)
			}
			if !dup {
				posted++
			}
		}
	}
	return posted, skipped, nil
}

// cumDepr is the cumulative straight-line depreciation after i months of a life-month asset, rounded to
// 2 dp. Rounding the cumulative (not the per-month) figure makes the charges sum to cost exactly at
// i == life (cost * life / life = cost).
func cumDepr(cost decimal.Decimal, i, life int) decimal.Decimal {
	if i <= 0 {
		return decimal.Zero
	}
	if i >= life {
		return cost
	}
	return cost.Mul(decimal.NewFromInt(int64(i))).Div(decimal.NewFromInt(int64(life))).Round(2)
}
