package migrationlint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFiberLabelSeedMatchesJSON — сид переводов волокон в миграции *_fiber_label_translation.sql
// печатает scripts/care-labels/seed-from-json.mjs из scripts/care-labels/fiber-translations.json.
// Правка одной стороны без перегенерации другой молча расходит «истину» и базу: тест требует, чтобы
// каждая пара (волокно, язык) из JSON стояла в миграции ровно той строкой, которую печатает скрипт,
// и чтобы строк сида было ровно столько же.
func TestFiberLabelSeedMatchesJSON(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join(migrationsDir, "*_fiber_label_translation.sql"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one *_fiber_label_translation.sql migration, got %v (%v)", matches, err)
	}
	sql := readMigrationFile(t, filepath.Base(matches[0]))

	raw, err := os.ReadFile(filepath.Join(migrationsDir, "..", "..", "..", "scripts", "care-labels", "fiber-translations.json"))
	if err != nil {
		t.Fatalf("read seed JSON: %v", err)
	}
	var data struct {
		Languages []string `json:"languages"`
		Fibers    []struct {
			Code  string            `json:"code"`
			Names map[string]string `json:"names"`
		} `json:"fibers"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse seed JSON: %v", err)
	}

	want := 0
	for _, f := range data.Fibers {
		for _, lang := range data.Languages {
			name, ok := f.Names[lang]
			if !ok {
				continue
			}
			want++
			line := "SELECT f.code, '" + lang + "', '" + strings.ReplaceAll(name, "'", "''") + "' FROM fiber f WHERE f.code = '" + f.Code + "';"
			if !strings.Contains(sql, line) {
				t.Errorf("seed row missing for %s/%s: %q", f.Code, lang, line)
			}
		}
	}
	if got := strings.Count(sql, "INSERT IGNORE INTO fiber_label_translation"); got != want {
		t.Errorf("seed rows in migration = %d, JSON pairs = %d", got, want)
	}
}
