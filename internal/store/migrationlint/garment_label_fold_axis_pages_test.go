package migrationlint

import (
	"strings"
	"testing"
)

// 0466 adds two columns every existing label must read as before: axis '' (along the height) and one
// page. Lock the shapes (the read path scans them into a plain string and int), the gates for a retry,
// and no double-quoted literal, which the cluster's ANSI_QUOTES would read as an identifier.
func TestGarmentLabelFoldAxisPagesColumns(t *testing.T) {
	const file = "0466_tech_card_garment_label_fold_axis_pages.sql"
	sql := readMigrationFile(t, file)
	up, down, ok := strings.Cut(sql, "-- +migrate Down")
	if !ok {
		t.Fatal("0466 has no Down section")
	}
	for _, add := range []string{
		"'ALTER TABLE tech_card_garment_label ADD COLUMN fold_axis VARCHAR(8) NOT NULL DEFAULT '''' ",
		"'ALTER TABLE tech_card_garment_label ADD COLUMN pages INT NOT NULL DEFAULT 1 ",
	} {
		if strings.Count(up, add) != 1 {
			t.Errorf("0466 Up must add the column exactly once; looking for %q", add)
		}
	}
	for _, col := range []string{"fold_axis", "pages"} {
		if !strings.Contains(up, "COLUMN_NAME = '"+col+"');") {
			t.Errorf("0466 Up must gate the ADD of %s on information_schema so a retry is a no-op", col)
		}
		if !strings.Contains(down, "IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN "+col+"'") {
			t.Errorf("0466 Down must drop %s behind the same gate", col)
		}
	}
	if strings.Count(up, "IF(@has_col = 0, ") != 2 {
		t.Error("0466 Up must gate both ADDs with IF(@has_col = 0, ...)")
	}
	for i, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, `"`) {
			t.Errorf("0466 line %d has a double quote, which ANSI_QUOTES reads as an identifier: %s", i+1, line)
		}
	}
}
