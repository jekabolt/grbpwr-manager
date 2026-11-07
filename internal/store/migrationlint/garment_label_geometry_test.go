package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The free-note migration is intentionally conservative: only these unambiguous spellings move.
// Lock the complete mapping so a later edit cannot silently reinterpret historical text.
func TestGarmentLabelFoldBackfillVocabulary(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(migrationsDir, "0454_tech_card_garment_label_geometry.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	want := map[string][]string{
		"flat":  {"flat", "flat fold", "no fold"},
		"half":  {"half", "half fold", "centre fold", "center fold", "folded in half", "centerfold", "centrefold"},
		"ends":  {"ends", "end fold", "ends folded", "end folded", "folded ends"},
		"mitre": {"mitre", "miter", "mitre fold", "miter fold"},
		"loop":  {"loop", "loop fold", "looped"},
	}
	for fold, legacy := range want {
		quoted := make([]string, 0, len(legacy))
		for _, value := range legacy {
			quoted = append(quoted, "'"+value+"'")
		}
		statement := "SET fold = '" + fold + "', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN (" +
			strings.Join(quoted, ", ") + ");"
		if strings.Count(sql, statement) != 1 {
			t.Errorf("0454 must contain exactly one %s backfill with the frozen vocabulary; looking for %q", fold, statement)
		}
	}
	if got := strings.Count(sql, "LOWER(TRIM(folding)) IN ("); got != len(want) {
		t.Errorf("0454 carries %d fold backfills, want exactly %d", got, len(want))
	}
}
