package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMergeCountryDemand exercises the GA4→DB demand merge: GA4 country NAMES are mapped to ISO-2 and
// folded into the DB rows, conversion is orders/sessions, an unmapped GA4 name lands in "(unmatched)",
// and a period beyond the GA4 window suppresses sessions with a caveat.
func TestMergeCountryDemand(t *testing.T) {
	base := []entity.CountryDemandRow{
		{Country: "DE", Orders: 5},
		{Country: "US", Orders: 2},
		{Country: "FR", Orders: 1}, // has orders but no GA4 sessions
	}
	sessions := []entity.GeographySessionMetric{
		{Country: "Germany", Sessions: 100},
		{Country: "United States", Sessions: 200},
		{Country: "Nowhereland", Sessions: 7}, // no ISO mapping → unmatched
		{Country: "Atlantis", Sessions: 0},     // zero sessions ignored
	}

	rows := mergeCountryDemand(base, sessions, true)

	byC := map[string]entity.CountryDemandRow{}
	for _, r := range rows {
		byC[r.Country] = r
	}

	de := byC["DE"]
	require.Equal(t, 100, de.Sessions, "Germany sessions mapped to DE")
	require.Equal(t, 5.0, de.ConversionRatePct, "DE conversion = 5/100 × 100")
	require.Contains(t, de.Caveat, "directional")

	us := byC["US"]
	require.Equal(t, 200, us.Sessions, "United States → US")
	require.Equal(t, 1.0, us.ConversionRatePct, "US conversion = 2/200 × 100")

	fr := byC["FR"]
	require.Equal(t, 0, fr.Sessions, "FR has no GA4 sessions")
	require.Zero(t, fr.ConversionRatePct, "no sessions → conversion 0, not divide-by-zero")
	require.Contains(t, fr.Caveat, "No GA4 sessions")

	um, ok := byC["(unmatched)"]
	require.True(t, ok, "an unmatched-name bucket is emitted")
	require.Equal(t, 7, um.Sessions, "Nowhereland sessions land in (unmatched)")
	require.Zero(t, um.Orders)
}

// TestMergeCountryDemandWindowExceeded suppresses conversion when the period is longer than the GA4
// cache window: sessions are treated as unavailable and every row carries the window caveat.
func TestMergeCountryDemandWindowExceeded(t *testing.T) {
	base := []entity.CountryDemandRow{{Country: "DE", Orders: 5}}
	sessions := []entity.GeographySessionMetric{{Country: "Germany", Sessions: 100}}

	rows := mergeCountryDemand(base, sessions, false) // window NOT ok

	require.Len(t, rows, 1, "no unmatched bucket when sessions are suppressed")
	require.Equal(t, 0, rows[0].Sessions, "sessions unavailable beyond the GA4 window")
	require.Zero(t, rows[0].ConversionRatePct)
	require.Contains(t, rows[0].Caveat, "90-day")
}

// TestApplyGeographyGrowth checks the per-country growth rate: a country present in both periods gets
// (value − compare)/compare × 100; a country new this period keeps ChangePct 0 with no CompareValue so
// the frontend can detect it via compare_count.
func TestApplyGeographyGrowth(t *testing.T) {
	d := func(n int64) decimal.Decimal { return decimal.NewFromInt(n) }
	current := []entity.GeographyMetric{
		{Country: "DE", Value: d(150), Count: 3}, // was 100 → +50%
		{Country: "US", Value: d(80), Count: 2},  // new country (absent from compare)
	}
	compare := []entity.GeographyMetric{
		{Country: "DE", Value: d(100), Count: 2},
	}
	applyGeographyGrowth(current, compare)

	require.Equal(t, 50.0, current[0].ChangePct, "DE 100→150 = +50%")
	require.NotNil(t, current[0].CompareValue)
	require.Equal(t, "100", current[0].CompareValue.String())
	require.NotNil(t, current[0].CompareCount)
	require.Equal(t, 2, *current[0].CompareCount)

	require.Zero(t, current[1].ChangePct, "a new country has no growth rate")
	require.Nil(t, current[1].CompareValue, "new country has no compare value (frontend: new country)")
	require.Nil(t, current[1].CompareCount)
}

// TestApplyGeographyGrowthNoCompare is a no-op when there is no compare period.
func TestApplyGeographyGrowthNoCompare(t *testing.T) {
	current := []entity.GeographyMetric{{Country: "DE", Value: decimal.NewFromInt(150)}}
	applyGeographyGrowth(current, nil)
	require.Zero(t, current[0].ChangePct)
	require.Nil(t, current[0].CompareValue)
}

// TestCountryNameToISO2 checks the dictionary resolves the names GA4 emits, including the aliases the
// merge relies on, and rejects an unknown name.
func TestCountryNameToISO2(t *testing.T) {
	cases := map[string]string{
		"Germany":                  "DE",
		"United States":            "US",
		"United States of America": "US",
		"United Kingdom":           "GB",
		"Czechia":                  "CZ",
		"Türkiye":                  "TR",
		"Turkey":                   "TR",
		"South Korea":              "KR",
		"  germany  ":              "DE", // normalization: trim + lowercase
	}
	for name, want := range cases {
		got, ok := entity.CountryNameToISO2(name)
		require.Truef(t, ok, "%q should resolve", name)
		require.Equalf(t, want, got, "%q → ISO-2", name)
	}
	_, ok := entity.CountryNameToISO2("Nowhereland")
	require.False(t, ok, "an unknown country name does not resolve")
}
