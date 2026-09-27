package techcard

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// T45 (27.09): the «apply to slots» statements bind without a database (see the colon note in
// marker_composition_bind_test.go).
func TestPaletteApplyQueriesBind(t *testing.T) {
	cases := []struct {
		name  string
		query string
		args  map[string]any
		want  int
	}{
		{"bom", paletteApplyBomQuery, map[string]any{"id": 1}, 1},
		{"max order", paletteApplyMaxOrderQuery, map[string]any{"cw": 1}, 1},
		{"count", paletteApplyCountQuery, map[string]any{"cw": 1, "bom": 2, "color": sql.NullString{}, "pantone": sql.NullString{}}, 2},
		{"update", paletteApplyUpdateQuery, map[string]any{"cw": 1, "bom": 2, "color": sql.NullString{}, "pantone": sql.NullString{}}, 4},
		{"insert", paletteApplyInsertQuery, map[string]any{"cw": 1, "bom": 2, "color": sql.NullString{},
			"pantone": sql.NullString{}, "source": "manual", "display_order": 3}, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, args, err := sqlx.Named(c.query, c.args)
			if err != nil {
				t.Fatalf("%s does not bind: %v", c.name, err)
			}
			if len(args) != c.want {
				t.Fatalf("%s: %d bound args, want %d", c.name, len(args), c.want)
			}
		})
	}
}

// Owner's decision 7 in SQL: only GARMENT-LEVEL rows (no piece by id NOR by the legacy position)
// take the palette colour, and only color / pantone move. MUTATION: drop «piece_id IS NULL» — every
// per-piece row of the slot is recoloured too; SET consumption — the norm moves with the colour.
func TestPaletteApplyTouchesOnlyGarmentLevelColour(t *testing.T) {
	for name, q := range map[string]string{"count": paletteApplyCountQuery, "update": paletteApplyUpdateQuery} {
		if !strings.Contains(q, "piece_id IS NULL AND piece_index IS NULL") {
			t.Errorf("%s must address garment-level rows only", name)
		}
	}
	set := paletteApplyUpdateQuery[strings.Index(paletteApplyUpdateQuery, "SET"):strings.Index(paletteApplyUpdateQuery, "WHERE")]
	if strings.TrimSpace(set) != "SET color = :color, pantone = :pantone" {
		t.Errorf("the recolour writes color and pantone and nothing else: %q", set)
	}
}
