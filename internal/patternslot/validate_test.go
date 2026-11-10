package patternslot

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestValidate(t *testing.T) {
	pattern := func(purpose, bomLineKey sql.NullString) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{FabricPurpose: purpose, BomLineKey: bomLineKey}
	}
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

	tests := []struct {
		name       string
		patterns   []entity.TechCardSizePattern
		lines      []entity.RollGoodsLine
		wantField  string
		wantReason string
	}{
		{
			name: "distinct purpose slots",
			patterns: []entity.TechCardSizePattern{
				pattern(present("main"), present("LINE-A")),
				pattern(present("lining"), present("LINE-A")),
			},
		},
		{
			name: "unbound rows are not limited",
			patterns: []entity.TechCardSizePattern{
				pattern(sql.NullString{}, sql.NullString{}),
				pattern(present("  "), present("  ")),
			},
		},
		{
			name: "purpose is normalized and authoritative",
			patterns: []entity.TechCardSizePattern{
				pattern(present(" Main "), present("LINE-A")),
				pattern(present("MAIN"), present("LINE-B")),
			},
			wantField:  "patterns[1].fabric_purpose",
			wantReason: `material slot binding "main"`,
		},
		{
			name: "legacy BOM line binding is normalized",
			patterns: []entity.TechCardSizePattern{
				pattern(sql.NullString{}, present(" line-a ")),
				pattern(present(""), present("LINE-A")),
			},
			wantField:  "patterns[1].bom_line_key",
			wantReason: `material slot binding "LINE-A"`,
		},
		// №203: the legacy line L1 was sorted into «main»; a sheet still bound to L1 and a sheet bound
		// to «main» are two sheets on one cloth. Before, the raw keys differed and both were stored.
		{
			name: "legacy line and the purpose it was sorted into are one slot",
			patterns: []entity.TechCardSizePattern{
				pattern(sql.NullString{}, present("LINE-A")),
				pattern(present("main"), sql.NullString{}),
			},
			lines:      []entity.RollGoodsLine{{LineKey: "LINE-A", Purpose: "main", Section: "fabric"}},
			wantField:  "patterns[1].fabric_purpose",
			wantReason: `material slot binding "main"`,
		},
		{
			name: "a line that has a different purpose is another slot",
			patterns: []entity.TechCardSizePattern{
				pattern(sql.NullString{}, present("LINE-A")),
				pattern(present("main"), sql.NullString{}),
			},
			lines: []entity.RollGoodsLine{
				{LineKey: "LINE-A", Purpose: "lining", Section: "lining"},
				{LineKey: "LINE-B", Purpose: "main", Section: "fabric"},
			},
		},
		// Each fusible line is its own cloth even when its sheet echoes the shared purpose.
		{
			name: "two fusibles echoing one purpose are two slots",
			patterns: []entity.TechCardSizePattern{
				pattern(present("other"), present("FUSE-A")),
				pattern(present("other"), present("FUSE-B")),
			},
			lines: []entity.RollGoodsLine{
				{LineKey: "FUSE-A", Purpose: "other", Section: entity.BomSectionInterlining},
				{LineKey: "FUSE-B", Purpose: "other", Section: entity.BomSectionInterlining},
			},
		},
		{
			name: "one fusible line twice is one slot",
			patterns: []entity.TechCardSizePattern{
				pattern(present("other"), present("FUSE-A")),
				pattern(sql.NullString{}, present("fuse-a")),
			},
			lines: []entity.RollGoodsLine{
				{LineKey: "FUSE-A", Purpose: "other", Section: entity.BomSectionInterlining},
				{LineKey: "FUSE-B", Purpose: "other", Section: entity.BomSectionInterlining},
			},
			wantField:  "patterns[1].bom_line_key",
			wantReason: `material slot binding "FUSE-A"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.patterns, tt.lines)
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			var violation *entity.ValidationError
			if !errors.As(err, &violation) {
				t.Fatalf("Validate() error = %T %v, want *entity.ValidationError", err, err)
			}
			if violation.Field != tt.wantField {
				t.Errorf("Field = %q, want %q", violation.Field, tt.wantField)
			}
			if !strings.Contains(violation.Message, tt.wantReason) {
				t.Errorf("Message = %q, want it to name %q", violation.Message, tt.wantReason)
			}
		})
	}
}
