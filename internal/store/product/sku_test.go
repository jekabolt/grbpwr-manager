package product

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestBuildBaseSKU(t *testing.T) {
	tests := []struct {
		name string
		in   SKUSegments
		want string
	}{
		{
			name: "spec example — code colour",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 21, ColorCode: "BLK"},
			want: "SS26-00021-BLK",
		},
		{
			name: "styled colourways share model, differ by colour",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 8, ColorCode: "RED"},
			want: "SS26-00008-RED",
		},
		{
			name: "off-white code kept to 3",
			in:   SKUSegments{Season: entity.SeasonRC, Year: 2027, ModelNo: 99999, ColorCode: "OFW"},
			want: "RC27-99999-OFW",
		},
		{
			name: "year floor boundary",
			in:   SKUSegments{Season: entity.SeasonPF, Year: 2000, ModelNo: 1, ColorCode: "WHT"},
			want: "PF00-00001-WHT",
		},
		{
			name: "model floor boundary",
			in:   SKUSegments{Season: entity.SeasonFW, Year: 2026, ModelNo: 1, ColorCode: "BLK"},
			want: "FW26-00001-BLK",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildBaseSKU(tt.in)
			require.NoError(t, err)
			if got != tt.want {
				t.Fatalf("BuildBaseSKU = %q, want %q", got, tt.want)
			}
			if len(got) != BaseSKULen {
				t.Errorf("base SKU %q length = %d, want %d", got, len(got), BaseSKULen)
			}
			// stability: same input -> same output
			again, err := BuildBaseSKU(tt.in)
			require.NoError(t, err)
			if again != got {
				t.Errorf("BuildBaseSKU not stable: %q != %q", again, got)
			}
		})
	}
}

// TestBuildBaseSKURejectsInvalidSegments is the negative counterpart of TestBuildBaseSKU (problem
// 045/R7): the strict builder must error instead of silently substituting a fallback for an invalid
// season, an out-of-range year/model, or a non-canonical colour code.
func TestBuildBaseSKURejectsInvalidSegments(t *testing.T) {
	valid := SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 21, ColorCode: "BLK"}

	tests := []struct {
		name string
		in   SKUSegments
	}{
		{"unknown season code", withSeason(valid, "XX")},
		{"empty season", withSeason(valid, "")},
		{"year below 2000", withYear(valid, 1999)},
		{"year above 2099", withYear(valid, 2100)},
		{"model zero", withModel(valid, 0)},
		{"model negative", withModel(valid, -1)},
		{"model above 99999", withModel(valid, 100000)},
		{"color too short", withColor(valid, "BL")},
		{"color too long", withColor(valid, "BLACK")},
		{"color lowercase", withColor(valid, "blk")},
		{"color non-alphanumeric", withColor(valid, "B-K")},
		{"color empty (unresolved dictionary code)", withColor(valid, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildBaseSKU(tt.in)
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
}

func withSeason(s SKUSegments, season entity.SeasonEnum) SKUSegments {
	s.Season = season
	return s
}

func withYear(s SKUSegments, year int) SKUSegments {
	s.Year = year
	return s
}

func withModel(s SKUSegments, modelNo int) SKUSegments {
	s.ModelNo = modelNo
	return s
}

func withColor(s SKUSegments, code string) SKUSegments {
	s.ColorCode = code
	return s
}

func TestBuildVariantSKU(t *testing.T) {
	base := "SS26-00021-BLK"
	tests := []struct {
		name string
		ord  int
		want string
	}{
		{"apparel M (ord 25, canon v1)", 25, "SS26-00021-BLK-25"},
		{"apparel OS (ord 5)", 5, "SS26-00021-BLK-05"},
		{"shoe 35 (ord 50)", 50, "SS26-00021-BLK-50"},
		{"shoe 48 (ord 76, canon v1)", 76, "SS26-00021-BLK-76"},
		{"composite (ord 70)", 70, "SS26-00021-BLK-70"},
		{"ordinal ceiling boundary (99)", 99, "SS26-00021-BLK-99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildVariantSKU(base, tt.ord)
			require.NoError(t, err)
			if got != tt.want {
				t.Fatalf("BuildVariantSKU = %q, want %q", got, tt.want)
			}
			if len(got) != VariantSKULen {
				t.Errorf("variant SKU %q length = %d, want %d", got, len(got), VariantSKULen)
			}
		})
	}
}

// TestBuildVariantSKURejectsInvalidSegments is the negative counterpart of TestBuildVariantSKU: an
// out-of-range ordinal or a base that is not already a canonical fixed-14 string must error.
func TestBuildVariantSKURejectsInvalidSegments(t *testing.T) {
	base := "SS26-00021-BLK"
	tests := []struct {
		name string
		base string
		ord  int
	}{
		{"ordinal zero", base, 0},
		{"ordinal negative", base, -1},
		{"ordinal above 99", base, 100},
		{"base too short", "SS26-00021-BL", 25},
		{"base too long (already a variant)", "SS26-00021-BLK-05", 25},
		{"base empty", "", 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildVariantSKU(tt.base, tt.ord)
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
}

func TestBuildBaseSKUPermissive(t *testing.T) {
	valid := SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 21, ColorCode: "BLK"}

	t.Run("valid input never applies a fallback", func(t *testing.T) {
		got, report := BuildBaseSKUPermissive(valid)
		require.Equal(t, "SS26-00021-BLK", got)
		require.False(t, report.Applied())
	})

	t.Run("invalid season falls back to SS and is reported", func(t *testing.T) {
		got, report := BuildBaseSKUPermissive(withSeason(valid, "XX"))
		require.Equal(t, "SS26-00021-BLK", got)
		require.True(t, report.SeasonFallback)
		require.True(t, report.Applied())
	})

	t.Run("model out of range clamps to the nearest bound and is reported", func(t *testing.T) {
		got, report := BuildBaseSKUPermissive(withModel(valid, 0))
		require.Equal(t, "SS26-00001-BLK", got)
		require.True(t, report.ModelFallback)

		got, report = BuildBaseSKUPermissive(withModel(valid, 100000))
		require.Equal(t, "SS26-99999-BLK", got)
		require.True(t, report.ModelFallback)
	})

	t.Run("non-canonical color falls back to UNK and is reported", func(t *testing.T) {
		got, report := BuildBaseSKUPermissive(withColor(valid, "black"))
		require.Equal(t, "SS26-00021-UNK", got)
		require.True(t, report.ColorFallback)
	})

	t.Run("output is always the fixed base length", func(t *testing.T) {
		got, _ := BuildBaseSKUPermissive(SKUSegments{})
		require.Len(t, got, BaseSKULen)
	})
}

func TestBuildVariantSKUPermissive(t *testing.T) {
	base := "SS26-00021-BLK"

	t.Run("valid input never applies a fallback", func(t *testing.T) {
		got, report := BuildVariantSKUPermissive(base, 25)
		require.Equal(t, "SS26-00021-BLK-25", got)
		require.False(t, report.Applied())
	})

	t.Run("ordinal out of range clamps to the nearest bound and is reported", func(t *testing.T) {
		got, report := BuildVariantSKUPermissive(base, 0)
		require.Equal(t, "SS26-00021-BLK-01", got)
		require.True(t, report.OrdinalFallback)

		got, report = BuildVariantSKUPermissive(base, 100)
		require.Equal(t, "SS26-00021-BLK-99", got)
		require.True(t, report.OrdinalFallback)
	})

	t.Run("non-canonical base shape is reported but still renders", func(t *testing.T) {
		got, report := BuildVariantSKUPermissive("SS26-00021-BL", 25)
		require.Equal(t, "SS26-00021-BL-25", got)
		require.True(t, report.BaseShapeInvalid)
	})
}

func TestClassifyModelNoCeiling(t *testing.T) {
	tests := []struct {
		modelNo int
		want    ModelNoCeilingLevel
	}{
		{1, ModelNoCeilingOK},
		{89999, ModelNoCeilingOK},
		{90000, ModelNoCeilingWarn},
		{98999, ModelNoCeilingWarn},
		{99000, ModelNoCeilingCritical},
		{99999, ModelNoCeilingCritical},
	}
	for _, tt := range tests {
		if got := ClassifyModelNoCeiling(tt.modelNo); got != tt.want {
			t.Errorf("ClassifyModelNoCeiling(%d) = %v, want %v", tt.modelNo, got, tt.want)
		}
	}
}

func TestValidateSizeOrdinalFacts(t *testing.T) {
	tests := []struct {
		name    string
		facts   []sizeOrdinalFact
		wantErr string
	}{
		{
			name: "valid single system",
			facts: []sizeOrdinalFact{
				{SizeID: 1, SKUOrd: 5, SKUSystem: string(entity.SizeSKUSystemApparel)},
				{SizeID: 2, SKUOrd: 25, SKUSystem: string(entity.SizeSKUSystemApparel)},
			},
		},
		{name: "zero ordinal", facts: []sizeOrdinalFact{{SizeID: 1, SKUOrd: 0, SKUSystem: string(entity.SizeSKUSystemApparel)}}, wantErr: "must be 1-99"},
		{name: "ordinal over width", facts: []sizeOrdinalFact{{SizeID: 1, SKUOrd: 100, SKUSystem: string(entity.SizeSKUSystemApparel)}}, wantErr: "must be 1-99"},
		{name: "missing system", facts: []sizeOrdinalFact{{SizeID: 1, SKUOrd: 10}}, wantErr: "invalid SKU system"},
		{name: "unknown system", facts: []sizeOrdinalFact{{SizeID: 1, SKUOrd: 10, SKUSystem: "free-text"}}, wantErr: "invalid SKU system"},
		{
			name: "mixed systems",
			facts: []sizeOrdinalFact{
				{SizeID: 1, SKUOrd: 10, SKUSystem: string(entity.SizeSKUSystemApparel)},
				{SizeID: 2, SKUOrd: 50, SKUSystem: string(entity.SizeSKUSystemShoe)},
			},
			wantErr: "mixes size SKU systems",
		},
		{
			name: "duplicate ordinal",
			facts: []sizeOrdinalFact{
				{SizeID: 1, SKUOrd: 10, SKUSystem: string(entity.SizeSKUSystemApparel)},
				{SizeID: 2, SKUOrd: 10, SKUSystem: string(entity.SizeSKUSystemApparel)},
			},
			wantErr: "share SKU ordinal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSizeOrdinalFacts(42, tt.facts)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateSKUSeasonRejectsFallbackInputs(t *testing.T) {
	require.NoError(t, validateSKUSeason(entity.SeasonSS, 2026))
	require.Error(t, validateSKUSeason("", 2026))
	require.Error(t, validateSKUSeason(entity.SeasonFW, 1999))
	require.Error(t, validateSKUSeason(entity.SeasonFW, 2100))
}

func TestStrictColorSegment(t *testing.T) {
	tests := []struct {
		code    string
		wantErr bool
	}{
		{"BLK", false},
		{"A1B", false},
		{"", true},
		{"xy", true},
		{"black", true},
		{"bLk", true},
		{"B-K", true},
	}
	for _, tt := range tests {
		got, err := strictColorSegment(tt.code)
		if tt.wantErr {
			require.Errorf(t, err, "strictColorSegment(%q) should error", tt.code)
			continue
		}
		require.NoError(t, err)
		if got != tt.code {
			t.Errorf("strictColorSegment(%q) = %q, want %q", tt.code, got, tt.code)
		}
	}
}

func TestStrictSeasonSegment(t *testing.T) {
	tests := []struct {
		season  entity.SeasonEnum
		year    int
		want    string
		wantErr bool
	}{
		{entity.SeasonSS, 2026, "SS26", false},
		{entity.SeasonFW, 2025, "FW25", false},
		{entity.SeasonPF, 2030, "PF30", false},
		{entity.SeasonSS, 2000, "SS00", false},
		{"", 2026, "", true},
		{"XX", 2026, "", true},
		{entity.SeasonSS, 1999, "", true},
		{entity.SeasonSS, 2100, "", true},
	}
	for _, tt := range tests {
		got, err := strictSeasonSegment(tt.season, tt.year)
		if tt.wantErr {
			require.Errorf(t, err, "strictSeasonSegment(%q,%d) should error", tt.season, tt.year)
			continue
		}
		require.NoError(t, err)
		if got != tt.want {
			t.Errorf("strictSeasonSegment(%q,%d) = %q, want %q", tt.season, tt.year, got, tt.want)
		}
	}
}
