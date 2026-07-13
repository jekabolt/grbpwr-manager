package stripe

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestAmountToSmallestUnit(t *testing.T) {
	cases := []struct {
		amount string
		cur    string
		want   int64
	}{
		{"10.50", "EUR", 1050},
		{"10.50", "USD", 1050},
		{"0.30", "GBP", 30},
		{"1000", "JPY", 1000}, // zero-decimal: no x100
		{"1000", "KRW", 1000},
		{"10.500", "KWD", 10500}, // three-decimal: x1000, not x100
		{"10.999", "EUR", 1100},  // rounded to currency precision first
	}
	for _, c := range cases {
		amt := decimal.RequireFromString(c.amount)
		if got := AmountToSmallestUnit(amt, c.cur); got != c.want {
			t.Errorf("AmountToSmallestUnit(%s, %s) = %d, want %d", c.amount, c.cur, got, c.want)
		}
	}
}

func TestAmountFromSmallestUnitRoundTrip(t *testing.T) {
	cases := []struct {
		minor int64
		cur   string
		want  string
	}{
		{1050, "EUR", "10.5"},
		{1000, "JPY", "1000"},
		{10500, "KWD", "10.5"},
	}
	for _, c := range cases {
		got := AmountFromSmallestUnit(c.minor, c.cur)
		if !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("AmountFromSmallestUnit(%d, %s) = %s, want %s", c.minor, c.cur, got, c.want)
		}
	}
}
