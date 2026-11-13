package migrationlint

import (
	"strings"
	"testing"
)

// 0468 adds the piece's fusing base key as two nullable columns in one INSTANT ALTER (every existing
// piece reads as "no key"). Lock the column shapes, the binary collation of the signature, the retry
// gate, a Down that drops both, and no double-quoted literal (ANSI_QUOTES reads it as an identifier).
func TestTechCardPieceFusingBaseMigration(t *testing.T) {
	const file = "0468_tech_card_piece_fusing_base.sql"
	sql := readMigrationFile(t, file)
	up, down, ok := strings.Cut(sql, "-- +migrate Down")
	if !ok {
		t.Fatal("0468 has no Down section")
	}
	for _, col := range []string{
		"ADD COLUMN fusing_base_size_id INT NULL ",
		"ADD COLUMN fusing_base_signature VARCHAR(64) COLLATE utf8mb4_bin NULL ",
	} {
		if strings.Count(up, col) != 1 {
			t.Errorf("0468 Up must add exactly once: %q", col)
		}
	}
	if !strings.Contains(up, "ALGORITHM=INSTANT'") {
		t.Error("0468 Up must add the columns with ALGORITHM=INSTANT")
	}
	if !strings.Contains(up, "IF(@has_col = 0, 'ALTER TABLE tech_card_piece ADD COLUMN fusing_base_size_id") ||
		!strings.Contains(up, "TABLE_NAME = 'tech_card_piece' AND COLUMN_NAME = 'fusing_base_size_id');") {
		t.Error("0468 Up must gate the ALTER on information_schema so a retry is a no-op")
	}
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if upper := strings.ToUpper(line); strings.Contains(upper, "FOREIGN KEY") || strings.Contains(upper, "CHECK") {
			t.Errorf("0468 adds no FK and no CHECK (the key is opaque; a deleted size must not block anything): %s", line)
		}
	}
	if !strings.Contains(down, "IF(@has_col > 0, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_base_size_id, DROP COLUMN fusing_base_signature'") {
		t.Error("0468 Down must drop both columns behind the same gate")
	}
	for i, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, `"`) {
			t.Errorf("0468 line %d has a double quote, which ANSI_QUOTES reads as an identifier: %s", i+1, line)
		}
	}
}
