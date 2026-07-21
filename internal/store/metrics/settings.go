package metrics

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

const (
	alertKeyCoverageWarnPct        = "coverage_warn_pct"
	alertKeyRefundRateWarnPct      = "refund_rate_warn_pct"
	alertKeyRateFloorN             = "rate_floor_n"
	alertKeyContributionTrustPct   = "contribution_trust_pct"
	alertKeyGA4CoverageWarnPct     = "ga4_coverage_warn_pct"
	alertKeyProductionRunStaleDays = "production_run_stale_days"
	alertKeyAcctPostingLagHours    = "acct_posting_lag_hours"
)

// GetAlertThresholds loads the dashboard alert thresholds from alert_setting, falling back to
// entity.DefaultAlertThresholds for any key that is missing — so a partially-seeded (or
// not-yet-migrated) table still yields sane, unchanged behaviour.
func (s *Store) GetAlertThresholds(ctx context.Context) (entity.AlertThresholds, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Key   string          `db:"setting_key"`
		Value decimal.Decimal `db:"value"`
	}](ctx, s.DB, `SELECT setting_key, value FROM alert_setting`, nil)
	if err != nil {
		return entity.AlertThresholds{}, fmt.Errorf("get alert thresholds: %w", err)
	}
	t := entity.DefaultAlertThresholds()
	for _, r := range rows {
		switch r.Key {
		case alertKeyCoverageWarnPct:
			t.CoverageWarnPct = r.Value.InexactFloat64()
		case alertKeyRefundRateWarnPct:
			t.RefundRateWarnPct = r.Value.InexactFloat64()
		case alertKeyRateFloorN:
			t.RateFloorN = int(r.Value.IntPart())
		case alertKeyContributionTrustPct:
			t.ContributionTrustPct = r.Value.InexactFloat64()
		case alertKeyGA4CoverageWarnPct:
			t.GA4CoverageWarnPct = r.Value.InexactFloat64()
		case alertKeyProductionRunStaleDays:
			t.ProductionRunStaleDays = int(r.Value.IntPart())
		case alertKeyAcctPostingLagHours:
			t.AcctPostingLagHours = int(r.Value.IntPart())
		}
	}
	return t, nil
}

// UpsertAlertThresholds writes all thresholds back to alert_setting.
func (s *Store) UpsertAlertThresholds(ctx context.Context, t entity.AlertThresholds) error {
	vals := []struct {
		key string
		val decimal.Decimal
	}{
		{alertKeyCoverageWarnPct, decimal.NewFromFloat(t.CoverageWarnPct)},
		{alertKeyRefundRateWarnPct, decimal.NewFromFloat(t.RefundRateWarnPct)},
		{alertKeyRateFloorN, decimal.NewFromInt(int64(t.RateFloorN))},
		{alertKeyContributionTrustPct, decimal.NewFromFloat(t.ContributionTrustPct)},
		{alertKeyGA4CoverageWarnPct, decimal.NewFromFloat(t.GA4CoverageWarnPct)},
		{alertKeyProductionRunStaleDays, decimal.NewFromInt(int64(t.ProductionRunStaleDays))},
		{alertKeyAcctPostingLagHours, decimal.NewFromInt(int64(t.AcctPostingLagHours))},
	}
	for _, v := range vals {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO alert_setting (setting_key, value) VALUES (:k, :v)
			ON DUPLICATE KEY UPDATE value = VALUES(value)`,
			map[string]any{"k": v.key, "v": v.val}); err != nil {
			return fmt.Errorf("upsert alert threshold %s: %w", v.key, err)
		}
	}
	return nil
}
