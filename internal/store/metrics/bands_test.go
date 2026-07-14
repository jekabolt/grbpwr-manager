package metrics

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestOrderValueBandIndex(t *testing.T) {
	bands := orderValueBandDefs()
	cases := []struct {
		rev       string
		wantIdx   int
		wantLabel string
	}{
		{"0", 0, "€0–50"},        // lower edge inclusive
		{"49.99", 0, "€0–50"},    // just below first upper edge
		{"50", 1, "€50–100"},     // edge lands in the higher band ([from,to))
		{"100", 2, "€100–150"},
		{"199.99", 3, "€150–200"},
		{"200", 4, "€200–300"},
		{"300", 5, "€300–500"},
		{"499.99", 5, "€300–500"},
		{"500", 6, "€500+"},      // open-ended band
		{"9999.99", 6, "€500+"},
		{"-5", 0, "€0–50"},       // heavily-refunded (negative) net revenue → first band
	}
	for _, c := range cases {
		i := orderValueBandIndex(decimal.RequireFromString(c.rev), bands)
		assert.Equal(t, c.wantIdx, i, "rev %s → index", c.rev)
		assert.Equal(t, c.wantLabel, bands[i].label, "rev %s → label", c.rev)
	}
}
