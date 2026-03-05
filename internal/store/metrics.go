package store

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type metricsStore struct {
	*MYSQLStore
}

// Metrics returns an object implementing the Metrics interface.
func (ms *MYSQLStore) Metrics() dependency.Metrics {
	return &metricsStore{
		MYSQLStore: ms,
	}
}

func (m *metricsStore) GetCohortRetention(ctx context.Context, from, to time.Time) ([]entity.CohortRetentionRow, error) {
	return m.Retention().GetCohortRetention(ctx, from, to)
}

func (m *metricsStore) GetOrderSequenceMetrics(ctx context.Context, from, to time.Time) ([]entity.OrderSequenceMetric, error) {
	return m.Retention().GetOrderSequenceMetrics(ctx, from, to)
}

func (m *metricsStore) GetEntryProducts(ctx context.Context, from, to time.Time, limit int) ([]entity.EntryProductMetric, error) {
	return m.Retention().GetEntryProducts(ctx, from, to, limit)
}

func (m *metricsStore) GetRevenuePareto(ctx context.Context, from, to time.Time, limit int) ([]entity.RevenueParetoRow, error) {
	return m.Retention().GetRevenuePareto(ctx, from, to, limit)
}

func (m *metricsStore) GetCustomerSpendingCurve(ctx context.Context, from, to time.Time) ([]entity.SpendingCurvePoint, error) {
	return m.Retention().GetCustomerSpendingCurve(ctx, from, to)
}

func (m *metricsStore) GetCategoryLoyalty(ctx context.Context, from, to time.Time) ([]entity.CategoryLoyaltyRow, error) {
	return m.Retention().GetCategoryLoyalty(ctx, from, to)
}

func (m *metricsStore) GetInventoryHealth(ctx context.Context, from, to time.Time, limit int) ([]entity.InventoryHealthRow, error) {
	return m.Inventory().GetInventoryHealth(ctx, from, to, limit)
}

func (m *metricsStore) GetSizeRunEfficiency(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeRunEfficiencyRow, error) {
	return m.Inventory().GetSizeRunEfficiency(ctx, from, to, limit)
}

func (m *metricsStore) GetSlowMovers(ctx context.Context, from, to time.Time, limit int) ([]entity.SlowMoverRow, error) {
	return m.Analytics().GetSlowMovers(ctx, from, to, limit)
}

func (m *metricsStore) GetReturnByProduct(ctx context.Context, from, to time.Time, limit int) ([]entity.ReturnByProductRow, error) {
	return m.Analytics().GetReturnByProduct(ctx, from, to, limit)
}

func (m *metricsStore) GetReturnBySize(ctx context.Context, from, to time.Time) ([]entity.ReturnBySizeRow, error) {
	return m.Analytics().GetReturnBySize(ctx, from, to)
}

func (m *metricsStore) GetSizeAnalytics(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeAnalyticsRow, error) {
	return m.Analytics().GetSizeAnalytics(ctx, from, to, limit)
}

func (m *metricsStore) GetDeadStock(ctx context.Context, from, to time.Time, limit int) ([]entity.DeadStockRow, error) {
	return m.Analytics().GetDeadStock(ctx, from, to, limit)
}

func (m *metricsStore) GetProductTrend(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductTrendRow, error) {
	return m.Analytics().GetProductTrend(ctx, from, to, limit)
}
