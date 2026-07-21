package product

import (
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// categoryIDAssignPrefix is the recognisable head of the derived category_id assignment.
const categoryIDAssignPrefix = "category_id = COALESCE(NULLIF("

// countCategoryIDAssignments counts how many times the SET clause assigns category_id. It must never
// exceed one -- three category levels can be masked together, and emitting the derivation per level
// would assign the same column three times in a single UPDATE.
func countCategoryIDAssignments(columns string) int {
	return strings.Count(columns, categoryIDAssignPrefix)
}

// TestStyleSetColumnsDerivesCategoryID pins the symmetric half of the taxonomy invariant: whenever
// UpdateStyle writes any level of the top/sub/type triple it must also re-derive category_id from
// it, so the row's two representations of a style's category cannot drift apart. The unmasked
// full-replace path matters as much as the masked one -- internal/betaseed calls UpdateStyle with no
// UpdateMask at all, so the whole beta dataset is seeded through it.
func TestStyleSetColumnsDerivesCategoryID(t *testing.T) {
	tests := []struct {
		name         string
		fields       []string
		wantCategory bool
		wantSeason   bool
	}{
		{
			name:         "unmasked full replace derives category_id",
			fields:       nil,
			wantCategory: true,
			wantSeason:   true,
		},
		{
			name:         "all three levels emit the derivation exactly once",
			fields:       []string{"topCategoryId", "subCategoryId", "typeId"},
			wantCategory: true,
		},
		{
			name:         "snake_case mask paths are normalized",
			fields:       []string{"top_category_id", "sub_category_id", "type_id"},
			wantCategory: true,
		},
		{
			// The tech card's own partial save: it must not touch the category at all.
			name:         "fit only leaves category_id alone",
			fields:       []string{"fit"},
			wantCategory: false,
		},
		{
			// The colourway card's partial save.
			name:         "model wears only leaves category_id alone",
			fields:       []string{"modelWearsHeightCm", "modelWearsSizeId"},
			wantCategory: false,
		},
		{
			name:         "season only leaves category_id alone",
			fields:       []string{"season"},
			wantCategory: false,
			wantSeason:   true,
		},
		{
			name:         "unknown field writes nothing",
			fields:       []string{"nonsense"},
			wantCategory: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns, seasonWritten := styleSetColumns(tt.fields)

			got := countCategoryIDAssignments(columns)
			want := 0
			if tt.wantCategory {
				want = 1
			}
			if got != want {
				t.Errorf("category_id assignments = %d, want %d\nSET: %s", got, want, columns)
			}
			if !tt.wantCategory && strings.Contains(columns, "category_id = ") {
				t.Errorf("a mask touching no category level must not write category_id\nSET: %s", columns)
			}
			if seasonWritten != tt.wantSeason {
				t.Errorf("seasonWritten = %v, want %v", seasonWritten, tt.wantSeason)
			}
		})
	}
}

// TestValidateStyleCategoryMaskRejectsPartialMask pins the rule that makes the derivation coherent
// under a field mask. The three levels are one path through the category tree (parent(type) = sub,
// parent(sub) = top), not three independent columns. A mask naming a strict subset writes a path that
// can violate that -- re-pointing sub from `tshirts` to `shirts` while leaving type `crop`, a child of
// `tshirts`, in place -- and no derivation can repair it, because the level that would have to change
// is the one the mask excludes. Worse, the next UpdateTechCard re-derives the triple from category_id
// and silently reverts the edit. So it is refused, loudly, rather than half-applied.
func TestValidateStyleCategoryMaskRejectsPartialMask(t *testing.T) {
	tests := []struct {
		name       string
		fields     []string
		wantReject bool
	}{
		{"full replace names every level", nil, false},
		{"no category level at all", []string{"fit", "modelWearsSizeId"}, false},
		{"all three levels together", []string{"topCategoryId", "subCategoryId", "typeId"}, false},
		{"all three plus unrelated fields", []string{"fit", "typeId", "topCategoryId", "subCategoryId"}, false},
		{"snake_case triple is normalized", []string{"top_category_id", "sub_category_id", "type_id"}, false},
		{"top alone", []string{"topCategoryId"}, true},
		{"sub alone", []string{"subCategoryId"}, true},
		{"type alone", []string{"typeId"}, true},
		{"top and sub without type", []string{"topCategoryId", "subCategoryId"}, true},
		{"sub and type without top", []string{"subCategoryId", "typeId"}, true},
		{"partial mixed with unrelated fields", []string{"brand", "subCategoryId"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStyleCategoryMask(tt.fields)
			if !tt.wantReject {
				if err != nil {
					t.Fatalf("expected the mask to be accepted, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a partial category mask to be rejected")
			}
			// Must be field-tagged so apisrv maps it to InvalidArgument rather than a 500.
			var ve *entity.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected a *entity.ValidationError so UpdateStyle returns InvalidArgument, got %T: %v", err, err)
			}
			if ve.Field != "update_mask" {
				t.Errorf("violation field = %q, want %q", ve.Field, "update_mask")
			}
		})
	}
}

// TestStyleFieldsSetDerivesCategoryID pins the derivation on the COLOURWAY paths. styleFieldsSet is
// the SET clause behind writeStyleFields (colourway create) and updateProduct (colourway edit), and
// both write the top/sub/type triple. If they wrote it without re-deriving category_id, a colourway
// saved from a stale form would re-write the OLD triple while category_id kept the newer value the
// tech card set — leaving the row's two representations of its category permanently inconsistent,
// with neither writer able to detect it. The invariant has to hold for all three writers or it is
// not an invariant.
func TestStyleFieldsSetDerivesCategoryID(t *testing.T) {
	if got := countCategoryIDAssignments(styleFieldsSet); got != 1 {
		t.Errorf("styleFieldsSet must derive category_id exactly once, got %d\nSET: %s", got, styleFieldsSet)
	}
	// The triple itself must still be written — the derivation supplements it, never replaces it.
	for _, want := range []string{
		"top_category_id = :topCategoryId",
		"sub_category_id = :subCategoryId",
		"type_id = :typeId",
	} {
		if !strings.Contains(styleFieldsSet, want) {
			t.Errorf("styleFieldsSet must still write the triple, missing %q", want)
		}
	}
	// The fragment is concatenated onto the const, so a dropped comma between the last column and it
	// would produce invalid SQL that no unit test exercising the Go side would otherwise catch — it
	// would only surface as a syntax error on a live colourway save.
	assertWellFormedSetClause(t, "styleFieldsSet", styleFieldsSet)
}

// assertWellFormedSetClause checks a SET clause is syntactically plausible. The load-bearing check is
// the last one: the derivation is CONCATENATED onto the const, so the separator between the previous
// column and it is the thing most likely to be dropped by an edit. Counting commas cannot detect that
// (COALESCE/CONCAT argument commas swamp the count), so look at the separator directly.
func assertWellFormedSetClause(t *testing.T, name, clause string) {
	t.Helper()
	if strings.Contains(clause, ",,") {
		t.Errorf("%s has a doubled comma: %s", name, clause)
	}
	if strings.HasSuffix(strings.TrimSpace(clause), ",") {
		t.Errorf("%s ends with a dangling comma: %s", name, clause)
	}
	idx := strings.Index(clause, categoryIDAssignPrefix)
	if idx <= 0 {
		t.Fatalf("%s does not contain the category_id derivation", name)
	}
	if before := strings.TrimRight(clause[:idx], " \t\n"); !strings.HasSuffix(before, ",") {
		t.Errorf("%s is missing the comma before the category_id derivation, which would be a SQL "+
			"syntax error at runtime: %s", name, clause)
	}
}

// TestUnmaskedStyleSetColumnsDoesNotDoubleAssign guards the specific way this can regress: the
// fragment now lives inside styleFieldsSet, so the unmasked branch of styleSetColumns must NOT
// append it a second time. Two assignments to category_id in one SET is a silent MySQL-accepted
// footgun (last-writer-wins), not a syntax error, so nothing else would catch it.
func TestUnmaskedStyleSetColumnsDoesNotDoubleAssign(t *testing.T) {
	columns, _ := styleSetColumns(nil)
	if got := countCategoryIDAssignments(columns); got != 1 {
		t.Errorf("unmasked SET must assign category_id exactly once, got %d\nSET: %s", got, columns)
	}
}

// TestStyleCategoryIDFragmentFallsBackToStoredValue pins the "never un-set a category" rule on the
// UpdateStyle side. A mask naming only typeId with typeId unset means "clear the type", not "this
// style has no category" -- without the trailing stored-value fallback the COALESCE would collapse
// to NULL and blank category_id while top_category_id kept its value, creating exactly the
// divergence the derivation exists to prevent.
func TestStyleCategoryIDFragmentFallsBackToStoredValue(t *testing.T) {
	if !strings.HasSuffix(styleCategoryIDFragment, ", category_id)") {
		t.Errorf("styleCategoryIDFragment must fall back to the stored category_id, got: %s", styleCategoryIDFragment)
	}
	// Most-specific-first ordering: type beats sub beats top.
	typeIdx := strings.Index(styleCategoryIDFragment, ":typeId")
	subIdx := strings.Index(styleCategoryIDFragment, ":subCategoryId")
	topIdx := strings.Index(styleCategoryIDFragment, ":topCategoryId")
	if !(typeIdx >= 0 && typeIdx < subIdx && subIdx < topIdx) {
		t.Errorf("category_id must be derived most-specific-first (type, sub, top), got: %s", styleCategoryIDFragment)
	}
}
