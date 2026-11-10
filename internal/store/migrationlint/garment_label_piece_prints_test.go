package migrationlint

import (
	"strings"
	"testing"
)

// 0467 adds the piece columns (every existing label reads as "no piece"), a nullable FK to the BOM
// line, and the prints table. Lock the column shapes, the FK actions, the retry gates, a Down that
// undoes all three, and no double-quoted literal (ANSI_QUOTES reads it as an identifier).
func TestGarmentLabelPiecePrintsMigration(t *testing.T) {
	const file = "0467_tech_card_garment_label_piece_prints.sql"
	sql := readMigrationFile(t, file)
	up, down, ok := strings.Cut(sql, "-- +migrate Down")
	if !ok {
		t.Fatal("0467 has no Down section")
	}
	for _, col := range []string{
		"ADD COLUMN piece_bom_item_id INT NULL ",
		"ADD COLUMN piece_width_mm DECIMAL(7,2) NOT NULL DEFAULT 0 ",
		"ADD COLUMN piece_height_mm DECIMAL(7,2) NOT NULL DEFAULT 0 ",
		"ADD COLUMN piece_seam_mm DECIMAL(5,2) NOT NULL DEFAULT 0 ",
		"ADD COLUMN label_x_mm DECIMAL(7,2) NOT NULL DEFAULT 0 ",
		"ADD COLUMN label_y_mm DECIMAL(7,2) NOT NULL DEFAULT 0 ",
		"ADD COLUMN label_on_piece VARCHAR(16) NOT NULL DEFAULT '''' ",
	} {
		if strings.Count(up, col) != 1 {
			t.Errorf("0467 Up must add exactly once: %q", col)
		}
	}
	if !strings.Contains(up, "ALGORITHM=INSTANT'") {
		t.Error("0467 Up must add the columns with ALGORITHM=INSTANT")
	}
	if !strings.Contains(up, "IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN") ||
		!strings.Contains(up, "COLUMN_NAME = 'piece_width_mm');") {
		t.Error("0467 Up must gate the column ALTER on information_schema so a retry is a no-op")
	}
	if !strings.Contains(up, "IF(@has_fk = 0, 'ALTER TABLE tech_card_garment_label ADD CONSTRAINT fk_tc_garment_label_piece_bom FOREIGN KEY (piece_bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL'") {
		t.Error("0467 Up must add the piece FK in its own gated ALTER, ON DELETE SET NULL")
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS tech_card_garment_label_print (",
		"FOREIGN KEY (label_id) REFERENCES tech_card_garment_label(id) ON DELETE CASCADE",
		"FOREIGN KEY (library_file_id) REFERENCES library_file(id) ON DELETE RESTRICT",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("0467 Up is missing %q", want)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS tech_card_garment_label_print;",
		"IF(@has_fk > 0, 'ALTER TABLE tech_card_garment_label DROP FOREIGN KEY fk_tc_garment_label_piece_bom'",
		"IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN piece_bom_item_id,",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("0467 Down is missing %q", want)
		}
	}
	for i, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, `"`) {
			t.Errorf("0467 line %d has a double quote, which ANSI_QUOTES reads as an identifier: %s", i+1, line)
		}
	}
}
