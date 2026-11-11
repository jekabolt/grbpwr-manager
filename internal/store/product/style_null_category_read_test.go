package product

import (
	"os"
	"regexp"
	"testing"
)

// TestProductReadsCoalesceNullTopCategory pins the fix for colourways of a style with no category
// yet (style.top_category_id NULL): the product reads scan top_category_id into an int, so every
// SELECT that feeds them must COALESCE it to 0 ("no category"), or GetColorwayByID answers 500.
func TestProductReadsCoalesceNullTopCategory(t *testing.T) {
	bare := regexp.MustCompile(`(?m)SELECT\s+sty\.top_category_id\b|^\s*sty\.top_category_id,\s*sty\.sub_category_id`)
	for _, f := range []string{"query.go", "variant.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := bare.FindIndex(src); loc != nil {
			t.Errorf("%s: bare sty.top_category_id in a product read at byte %d; wrap it in COALESCE(..., 0)", f, loc[0])
		}
	}
}
