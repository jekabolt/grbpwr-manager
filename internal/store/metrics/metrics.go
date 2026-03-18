// Package metrics implements business metrics, retention, inventory, and analytics.
package metrics

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.Metrics (Retention + Inventory + Analytics + GetBusinessMetrics).
// repo is used for cross-domain calls (GA4Data, BQCache, SyncStatus, Subscribers).
type Store struct {
	storeutil.Base
	repo dependency.Repository
}

// New creates a new metrics store.
func New(base storeutil.Base, repo dependency.Repository) *Store {
	return &Store{Base: base, repo: repo}
}

// Retention returns dependency.Retention (same store, implements via embedding).
func (s *Store) Retention() dependency.Retention {
	return s.retention()
}

// Inventory returns dependency.Inventory (same store, implements via embedding).
func (s *Store) Inventory() dependency.Inventory {
	return s.inventory()
}

// Analytics returns dependency.Analytics (same store, implements via embedding).
func (s *Store) Analytics() dependency.Analytics {
	return s.analytics()
}

// --- Metrics interface: Retention methods ---

func (s *Store) GetCohortRetention(ctx context.Context, from, to time.Time) ([]entity.CohortRetentionRow, error) {
	return s.retention().GetCohortRetention(ctx, from, to)
}

func (s *Store) GetOrderSequenceMetrics(ctx context.Context, from, to time.Time) ([]entity.OrderSequenceMetric, error) {
	return s.retention().GetOrderSequenceMetrics(ctx, from, to)
}

func (s *Store) GetEntryProducts(ctx context.Context, from, to time.Time, limit int) ([]entity.EntryProductMetric, error) {
	return s.retention().GetEntryProducts(ctx, from, to, limit)
}

func (s *Store) GetRevenuePareto(ctx context.Context, from, to time.Time, limit int) ([]entity.RevenueParetoRow, error) {
	return s.retention().GetRevenuePareto(ctx, from, to, limit)
}

func (s *Store) GetCustomerSpendingCurve(ctx context.Context, from, to time.Time) ([]entity.SpendingCurvePoint, error) {
	return s.retention().GetCustomerSpendingCurve(ctx, from, to)
}

func (s *Store) GetCategoryLoyalty(ctx context.Context, from, to time.Time) ([]entity.CategoryLoyaltyRow, error) {
	return s.retention().GetCategoryLoyalty(ctx, from, to)
}

// --- Metrics interface: Inventory methods ---

func (s *Store) GetInventoryHealth(ctx context.Context, from, to time.Time, limit int) ([]entity.InventoryHealthRow, error) {
	return s.inventory().GetInventoryHealth(ctx, from, to, limit)
}

func (s *Store) GetSizeRunEfficiency(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeRunEfficiencyRow, error) {
	return s.inventory().GetSizeRunEfficiency(ctx, from, to, limit)
}

// --- Metrics interface: Analytics methods ---

func (s *Store) GetSlowMovers(ctx context.Context, from, to time.Time, limit int) ([]entity.SlowMoverRow, error) {
	return s.analytics().GetSlowMovers(ctx, from, to, limit)
}

func (s *Store) GetReturnByProduct(ctx context.Context, from, to time.Time, limit int) ([]entity.ReturnByProductRow, error) {
	return s.analytics().GetReturnByProduct(ctx, from, to, limit)
}

func (s *Store) GetReturnBySize(ctx context.Context, from, to time.Time) ([]entity.ReturnBySizeRow, error) {
	return s.analytics().GetReturnBySize(ctx, from, to)
}

func (s *Store) GetSizeAnalytics(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeAnalyticsRow, error) {
	return s.analytics().GetSizeAnalytics(ctx, from, to, limit)
}

func (s *Store) GetDeadStock(ctx context.Context, from, to time.Time, limit int) ([]entity.DeadStockRow, error) {
	return s.analytics().GetDeadStock(ctx, from, to, limit)
}

func (s *Store) GetProductTrend(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductTrendRow, error) {
	return s.analytics().GetProductTrend(ctx, from, to, limit)
}
