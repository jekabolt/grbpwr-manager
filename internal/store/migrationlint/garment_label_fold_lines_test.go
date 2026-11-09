package migrationlint

import (
	"strings"
	"testing"
)

// 0460 adds one column that every existing label must read as «no lines». Lock its shape: NOT NULL
// with the empty default (the read path scans it into a plain string), gated for a retry, and no
// double-quoted literal, which the cluster's ANSI_QUOTES would read as an identifier.
func TestGarmentLabelFoldLinesColumn(t *testing.T) {
	sql := readMigrationFile(t, "0460_tech_card_garment_label_fold_lines.sql")
	up, down, ok := strings.Cut(sql, "-- +migrate Down")
	if !ok {
		t.Fatal("0460 has no Down section")
	}
	add := "'ALTER TABLE tech_card_garment_label ADD COLUMN fold_lines_mm VARCHAR(255) NOT NULL DEFAULT '''' "
	if strings.Count(up, add) != 1 {
		t.Errorf("0460 Up must add fold_lines_mm exactly once as VARCHAR(255) NOT NULL DEFAULT ''; looking for %q", add)
	}
	if !strings.Contains(up, "COLUMN_NAME = 'fold_lines_mm');") || !strings.Contains(up, "IF(@has_col = 0, ") {
		t.Error("0460 Up must gate the ADD on information_schema so a retry is a no-op")
	}
	if !strings.Contains(down, "IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN fold_lines_mm'") {
		t.Error("0460 Down must drop fold_lines_mm behind the same gate")
	}
	for i, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, `"`) {
			t.Errorf("0460 line %d has a double quote, which ANSI_QUOTES reads as an identifier: %s", i+1, line)
		}
	}
}
