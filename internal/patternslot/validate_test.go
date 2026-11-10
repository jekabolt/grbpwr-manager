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
			wantReason: `material slot "main" already holds a pattern file`,
		},
		{
			name: "legacy BOM line binding is normalized",
			patterns: []entity.TechCardSizePattern{
				pattern(sql.NullString{}, present(" line-a ")),
				pattern(present(""), present("LINE-A")),
			},
			wantField:  "patterns[1].bom_line_key",
			wantReason: `material slot of a fabric line no longer on the BOM`,
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
			wantReason: `material slot "main" already holds a pattern file`,
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
				{LineKey: "FUSE-A", Purpose: "other", Section: entity.BomSectionInterlining, Name: "Fusible G-400"},
				{LineKey: "FUSE-B", Purpose: "other", Section: entity.BomSectionInterlining},
			},
			wantField:  "patterns[1].bom_line_key",
			wantReason: `material slot "Fusible G-400" already holds a pattern file`,
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
			// A line key is an internal id: the operator reads the purpose word or the line's name.
			for _, raw := range []string{"LINE-A", "LINE-B", "FUSE-A", "FUSE-B"} {
				if strings.Contains(violation.Message, raw) {
					t.Errorf("Message = %q prints the internal line key %q", violation.Message, raw)
				}
			}
		})
	}
}

// A card that already holds two files in one slot (beta card 58: two sheets on one unsorted fabric
// line) must stay writable: an unrelated save and removing one of the two pass; a third file, or a
// second file in another slot, is refused; a card with nothing stored still refuses the second file.
func TestValidateAgainstStoredDuplicate(t *testing.T) {
	present := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	sheet := func(lineKey, url, bomLine, purpose string) entity.TechCardSizePattern {
		p := entity.TechCardSizePattern{LineKey: lineKey, URL: url, Name: present(url)}
		if bomLine != "" {
			p.BomLineKey = present(bomLine)
		}
		if purpose != "" {
			p.FabricPurpose = present(purpose)
		}
		return p
	}
	lines := []entity.RollGoodsLine{
		{LineKey: "01UNSORTED", Section: entity.BomSectionFabric, Name: "Cotton twill"},
		{LineKey: "01LINING", Purpose: "lining", Section: entity.BomSectionLining, Name: "Cupro"},
	}
	a := sheet("PL-A", "allsizes.dxf", "01UNSORTED", "")
	b := sheet("PL-B", "import.dxf", "01UNSORTED", "")
	lining := sheet("PL-L", "lining.dxf", "", "lining")
	stored := []entity.TechCardSizePattern{a, b, lining}

	t.Run("unrelated save of the stored duplicate passes", func(t *testing.T) {
		if err := ValidateAgainst(stored, lines, stored, lines); err != nil {
			t.Fatalf("ValidateAgainst() = %v", err)
		}
	})
	t.Run("a replaced file keeps its line key and passes", func(t *testing.T) {
		replaced := sheet("PL-B", "import-v2.dxf", "01UNSORTED", "")
		if err := ValidateAgainst(stored, lines, []entity.TechCardSizePattern{a, replaced, lining}, lines); err != nil {
			t.Fatalf("ValidateAgainst() = %v", err)
		}
	})
	t.Run("a keyless client matched by size and url passes", func(t *testing.T) {
		keyless := []entity.TechCardSizePattern{
			sheet("", "allsizes.dxf", "01UNSORTED", ""), sheet("", "import.dxf", "01UNSORTED", ""), lining,
		}
		if err := ValidateAgainst(stored, lines, keyless, lines); err != nil {
			t.Fatalf("ValidateAgainst() = %v", err)
		}
	})
	t.Run("removing one of the two passes", func(t *testing.T) {
		if err := ValidateAgainst(stored, lines, []entity.TechCardSizePattern{a, lining}, lines); err != nil {
			t.Fatalf("ValidateAgainst() = %v", err)
		}
	})
	t.Run("a third file in the doubled slot is refused", func(t *testing.T) {
		third := sheet("", "third.dxf", "01UNSORTED", "")
		err := ValidateAgainst(stored, lines, []entity.TechCardSizePattern{a, b, lining, third}, lines)
		var violation *entity.ValidationError
		if !errors.As(err, &violation) {
			t.Fatalf("ValidateAgainst() = %v, want a refusal", err)
		}
		if violation.Field != "patterns[3].bom_line_key" {
			t.Errorf("Field = %q", violation.Field)
		}
		if !strings.Contains(violation.Message, `material slot "Cotton twill" already holds a pattern file`) ||
			strings.Contains(violation.Message, "01UNSORTED") {
			t.Errorf("Message = %q, want the line named by its material, never its key", violation.Message)
		}
	})
	t.Run("a second file in another slot is refused", func(t *testing.T) {
		second := sheet("", "lining-2.dxf", "", "lining")
		err := ValidateAgainst(stored, lines, []entity.TechCardSizePattern{a, b, lining, second}, lines)
		if err == nil || !strings.Contains(err.Error(), `material slot "lining" already holds a pattern file`) {
			t.Fatalf("ValidateAgainst() = %v, want the lining slot refused", err)
		}
	})
	t.Run("moving a single file into the doubled slot is refused", func(t *testing.T) {
		moved := sheet("PL-L", "lining.dxf", "01UNSORTED", "")
		if err := ValidateAgainst(stored, lines, []entity.TechCardSizePattern{a, b, moved}, lines); err == nil {
			t.Fatal("ValidateAgainst() = nil, want the moved file refused")
		}
	})
	t.Run("a card with nothing stored still refuses the second file", func(t *testing.T) {
		if err := ValidateAgainst(nil, nil, []entity.TechCardSizePattern{a, b}, lines); err == nil {
			t.Fatal("ValidateAgainst() = nil, want a refusal")
		}
	})
}
