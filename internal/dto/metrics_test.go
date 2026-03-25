package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	shopspring "github.com/shopspring/decimal"
)

func TestComputeChangePct(t *testing.T) {
	tests := []struct {
		name     string
		current  float64
		previous float64
		want     float64
	}{
		{"revenue +24.9%", 20419.91, 16350.97, 24.88},
		{"avg order value -23.7%", 1134.44, 1486.45, -23.62},
		{"items per order +3.4%", 1.22, 1.18, 3.39},
		{"revenue vs prior period (inexact float operands)", 110, 5665.96, -98.06},
		{"1 vs 1 should be 0%", 1.0, 1.0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeChangePct(
				shopspring.NewFromFloat(tt.current),
				shopspring.NewFromFloat(tt.previous),
			)
			if got == nil {
				t.Fatal("computeChangePct returned nil")
			}
			diff := *got - tt.want
			if diff < -0.1 || diff > 0.1 {
				t.Errorf("computeChangePct() = %v, want ~%v", *got, tt.want)
			}
		})
	}
}

func TestMetricWithComparisonToPb_ChangePct(t *testing.T) {
	// AvgOrderValue: 1134.44 vs 1486.45 should yield ~-23.6%
	m := entity.MetricWithComparison{
		Value:        shopspring.NewFromFloat(1134.44),
		CompareValue: ptr(shopspring.NewFromFloat(1486.45)),
		ChangePct:    nil, // store didn't set it
	}
	pb := metricWithComparisonToPb(m)
	if pb == nil {
		t.Fatal("metricWithComparisonToPb returned nil")
	}
	if pb.ChangePct > -23 || pb.ChangePct < -24 {
		t.Errorf("ChangePct = %v, want ~-23.6", pb.ChangePct)
	}
}

func TestMetricWithComparisonToPb_ItemsPerOrder_1vs1(t *testing.T) {
	// When both display as "1" (e.g. 1.14 vs 1.0 rounded to int), change should be 0%
	m := entity.MetricWithComparison{
		Value:        shopspring.NewFromFloat(1.14),
		CompareValue: ptr(shopspring.NewFromFloat(1.0)),
		ChangePct:    nil,
	}
	pb := metricWithComparisonToPb(m, false, true) // roundToInt=true for items_per_order
	if pb == nil {
		t.Fatal("metricWithComparisonToPb returned nil")
	}
	if pb.ChangePct != 0 {
		t.Errorf("ItemsPerOrder 1.14 vs 1.0 (rounded to int) ChangePct = %v, want 0", pb.ChangePct)
	}
}

func ptr(d shopspring.Decimal) *shopspring.Decimal {
	return &d
}
