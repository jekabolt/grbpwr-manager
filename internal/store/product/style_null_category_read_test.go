package product

import (
	"os"
	"regexp"
	"testing"
)

// TestProductReadsCoalesceNullTopCategory pins the fix for colourways of a style with no category
// yet (style.top_category_id NULL): the product reads in query.go scan top_category_id into an int,
// so every SELECT there must COALESCE it to 0 ("no category"), or GetColorwayByID answers 500.
func TestProductReadsCoalesceNullTopCategory(t *testing.T) {
	bare := regexp.MustCompile(`(?m)SELECT\s+sty\.top_category_id\b|^\s*sty\.top_category_id,\s*sty\.sub_category_id`)
	src, err := os.ReadFile("query.go")
	if err != nil {
		t.Fatal(err)
	}
	if loc := bare.FindIndex(src); loc != nil {
		t.Errorf("query.go: bare sty.top_category_id in a product read at byte %d; wrap it in COALESCE(..., 0)", loc[0])
	}
}

// TestVariantSizePolicyKeepsNullTopCategory pins the opposite for variant.go: the size check scans
// into StyleCategoryPath (sql.NullInt32), where NULL means "no category yet → any size system".
// COALESCE(…, 0) there turns NULL into category 0 → one-size only, and CreateVariant refuses apparel
// sizes on an uncategorised style (backlog 291).
func TestVariantSizePolicyKeepsNullTopCategory(t *testing.T) {
	src, err := os.ReadFile("variant.go")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`COALESCE\(\s*sty\.top_category_id`).Match(src) {
		t.Error("variant.go: top_category_id must stay NULL for the size policy; drop the COALESCE")
	}
}
