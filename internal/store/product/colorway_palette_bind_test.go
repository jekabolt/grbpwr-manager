package product

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// T45 (27.09): every statement of colorway_palette.go binds without a database. sqlx reads EVERY
// ':' as a named parameter — one inside a comment or a literal fails the statement at request time
// with «could not find name  in map», and nothing but a MySQL-backed run would otherwise see it.
// The IN (:ids) reads also have to survive sqlx.In's slice expansion, which is what
// storeutil.QueryListNamed runs next.
func TestColorwayPaletteQueriesBind(t *testing.T) {
	cases := []struct {
		name  string
		query string
		args  map[string]any
		want  int
	}{
		{"style tokens", styleSkuTokensQuery, map[string]any{"sid": 1, "exclude": 2}, 2},
		{"pin legacy token", pinLegacySkuTokenQuery, map[string]any{"token": "BLK", "id": 1}, 2},
		{"has palette", colorwayHasPaletteQuery, map[string]any{"id": 1}, 1},
		{"clear palette", clearColorwayPaletteQuery, map[string]any{"id": 1}, 1},
		{"mirror main colour", mirrorMainColourQuery, map[string]any{"id": 1, "pantone": sql.NullString{},
			"pantone_system": sql.NullString{}, "dev_hex": sql.NullString{}}, 4},
		{"known languages", knownLanguagesQuery, map[string]any{"ids": []int{1, 2, 3}}, 3},
		{"delete name", deleteColorwayNameI18nQuery, map[string]any{"id": 1, "lang": 2}, 2},
		{"upsert name", upsertColorwayNameI18nQuery, map[string]any{"id": 1, "lang": 2, "name": "Nuit"}, 3},
		{"display name", refreshColorwayDisplayNameQuery, map[string]any{"id": 1}, 1},
		{"palettes", colorwayPaletteQuery, map[string]any{"ids": []int{4, 5}}, 2},
		{"names", colorwayNameI18nQuery, map[string]any{"ids": []int{4, 5}}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, args, err := sqlx.Named(c.query, c.args)
			if err != nil {
				t.Fatalf("%s does not bind: %v", c.name, err)
			}
			if _, args, err = sqlx.In(q, args...); err != nil {
				t.Fatalf("%s does not expand: %v", c.name, err)
			}
			if len(args) != c.want {
				t.Fatalf("%s: %d bound args, want %d", c.name, len(args), c.want)
			}
		})
	}
	// The parameterless read goes to the driver untouched (storeutil.makeQuery) — it must carry none.
	if strings.Contains(dictionaryColourCodesQuery, ":") {
		t.Fatal("the dictionary read is parameterless and must stay colon-free")
	}
}

// The palette read is ordered by position — the main colour first is the whole contract — and the
// token read counts ARCHIVED colourways too (a frozen SKU keeps its token), so it must not filter
// on lifecycle_status. MUTATION: drop «ORDER BY product_id, position», or add a lifecycle filter.
func TestColorwayPaletteQueriesKeepTheirContract(t *testing.T) {
	if !strings.Contains(colorwayPaletteQuery, "ORDER BY product_id, position") {
		t.Fatal("the palette must read main colour first")
	}
	if strings.Contains(styleSkuTokensQuery, "lifecycle_status") {
		t.Fatal("an archived colourway still holds its token")
	}
	if !strings.Contains(styleSkuTokensQuery, "COALESCE(sku_color_token, color_code)") {
		t.Fatal("a row an older binary inserted holds its color_code as its token")
	}
	if !strings.Contains(pinLegacySkuTokenQuery, "sku_color_token IS NULL") {
		t.Fatal("the pin writes only a row that has no token — a token is immutable")
	}
}
