package accounting

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestOpexCategoryAccount(t *testing.T) {
	tests := []struct {
		category  string
		wantCode  string
		wantKnown bool
	}{
		{"salaries", Acc6330, true},
		{"rent", Acc6340, true},
		{"software", Acc6320, true},
		{"marketing_other", Acc6110, true},
		{"production_content", Acc6125, true},
		{"taxes", Acc6360, true},
		{"bank_fees", Acc6060, true},
		{"professional_services", Acc6350, true},
		{"logistics_office", Acc6010, true},
		{"other", Acc6390, true},
		{"totally_new_category", Acc6390, false},
		{"", Acc6390, false},
	}
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			code, known := OpexCategoryAccount(tt.category)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantKnown, known)
		})
	}
}

// TestOpexMappingCoversValidCategories guards against drift: every dto-valid OPEX category must have
// an explicit account (an unmapped one would silently fall to 6390 with a caveat). If this fails,
// entity.ValidOpexCategories gained a member that opexCategoryAccounts is missing.
func TestOpexMappingCoversValidCategories(t *testing.T) {
	for category := range entity.ValidOpexCategories {
		_, known := OpexCategoryAccount(category)
		assert.Truef(t, known, "opex category %q is not mapped to an account", category)
	}
}

// TestAccountCodesUniqueAndComplete checks the full chart of accounts is 37 distinct codes (the
// seed count: migration 0190's 34 + phase-2-wave-1 migration 0191's 2080 / 4310 / 4050 —
// 8 asset + 4 liability + 3 equity + 6 revenue + 5 cogs + 11 opex).
func TestAccountCodesUniqueAndComplete(t *testing.T) {
	codes := []string{
		Acc1010, Acc1030, Acc1040, Acc1110, Acc1120, Acc1130, Acc1210, Acc1220,
		Acc2010, Acc2030, Acc2070, Acc2080,
		Acc3010, Acc3020, Acc3030,
		Acc4010, Acc4020, Acc4040, Acc4050, Acc4110, Acc4310,
		Acc5010, Acc5040, Acc5050, Acc5090, Acc6210,
		Acc6010, Acc6050, Acc6060, Acc6110, Acc6125, Acc6320, Acc6330, Acc6340, Acc6350, Acc6360, Acc6390,
	}
	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		_, dup := seen[c]
		assert.Falsef(t, dup, "duplicate account code %q", c)
		seen[c] = struct{}{}
	}
	assert.Len(t, seen, 37)
}
