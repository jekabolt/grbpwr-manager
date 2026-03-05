package bigquery

import (
	"math"
)

// ClampInt64 ensures count-like values are non-negative.
// BigQuery can return negative values from data quality issues or query bugs.
func ClampInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// SanitizeFloat64 replaces NaN/Inf with 0 and clamps negatives to 0.
// Used for durations (seconds), averages, and other non-rate metrics.
func SanitizeFloat64(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	return v
}

// SanitizeRate clamps float64 to [0, 1] and replaces NaN/Inf with 0.
// Used for conversion_rate, cart_rate, abandonment_rate, etc.
func SanitizeRate(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// SanitizeFloat64ForDecimal sanitizes a float before converting to decimal.Decimal.
// Prevents NaN/Inf from producing invalid decimal values.
func SanitizeFloat64ForDecimal(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	// Allow negative for revenue/failed value (refunds, etc.)
	return v
}
