package bigquery

import (
	"math"
	"testing"
)

func TestClampInt64(t *testing.T) {
	tests := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{1, 1},
		{100, 100},
		{-1, 0},
		{-999, 0},
	}
	for _, tt := range tests {
		if got := ClampInt64(tt.in); got != tt.want {
			t.Errorf("ClampInt64(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeFloat64(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{1.5, 1.5},
		{100.0, 100.0},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
		{-1.0, 0},
		{-0.5, 0},
	}
	for _, tt := range tests {
		got := SanitizeFloat64(tt.in)
		if tt.want == 0 && got == 0 {
			continue
		}
		if got != tt.want {
			t.Errorf("SanitizeFloat64(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeRate(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{0.5, 0.5},
		{1.0, 1.0},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{-0.1, 0},
		{1.5, 1.0},
		{2.0, 1.0},
	}
	for _, tt := range tests {
		got := SanitizeRate(tt.in)
		if tt.want == 0 && got == 0 {
			continue
		}
		if got != tt.want {
			t.Errorf("SanitizeRate(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeFloat64ForDecimal(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{99.99, 99.99},
		{-50.0, -50.0},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
	}
	for _, tt := range tests {
		got := SanitizeFloat64ForDecimal(tt.in)
		if tt.want == 0 && got == 0 {
			continue
		}
		if got != tt.want {
			t.Errorf("SanitizeFloat64ForDecimal(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
