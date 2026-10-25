package safety

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLintDirsFlagsHistoryValidatingChangesCaseInsensitively(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, baseDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32), quantity INT NULL);
`)
	copyMigrations(t, baseDir, headDir)
	writeMigration(t, headDir, "0002_harden_widgets.sql", `
-- +migrate Up
alter table WIDGETS add constraint chk_quantity CHECK (quantity >= 0);
AlTeR TaBlE widgets ADD CONSTRAINT uq_widgets_code UNIQUE KEY (code);
ALTER TABLE widgets MODIFY COLUMN quantity BIGINT NULL;
ALTER TABLE widgets CHANGE COLUMN code code VARCHAR(16) NOT NULL;
ALTER TABLE widgets ALTER COLUMN quantity SET NOT NULL;
CREATE UNIQUE INDEX uq_widgets_quantity ON widgets (quantity);
ALTER TABLE widgets ADD COLUMN external_id VARCHAR(32) UNIQUE;
ALTER TABLE widgets ADD COLUMN score INT CHECK (score >= 0);
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.NewFiles) != 1 || report.NewFiles[0] != "0002_harden_widgets.sql" {
		t.Fatalf("new files = %v, want 0002_harden_widgets.sql", report.NewFiles)
	}

	want := map[Risk]int{
		RiskCheckConstraint: 2,
		RiskUnique:          3,
		RiskModifyColumn:    2,
		RiskNotNull:         1,
	}
	got := make(map[Risk]int)
	for _, finding := range report.Findings {
		got[finding.Risk]++
		if finding.Table != "widgets" {
			t.Errorf("finding table = %q, want widgets", finding.Table)
		}
	}
	for risk, count := range want {
		if got[risk] != count {
			t.Errorf("%s findings = %d, want %d (all findings: %#v)", risk, got[risk], count, report.Findings)
		}
	}
}

func TestLintDirsAcceptsDataCheckedAnnotationWithExplanation(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, baseDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, quantity INT NULL);
`)
	copyMigrations(t, baseDir, headDir)
	writeMigration(t, headDir, "0002_harden_widgets.sql", `
-- +migrate Up
-- data-checked: SELECT COUNT(*) FROM widgets WHERE quantity IS NULL returned 0 on beta.
ALTER TABLE widgets MODIFY COLUMN quantity BIGINT NOT NULL;
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("annotated migration findings = %#v, want none", report.Findings)
	}
}

func TestLintDirsRejectsEmptyDataCheckedAnnotation(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, baseDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, quantity INT NULL);
`)
	copyMigrations(t, baseDir, headDir)
	writeMigration(t, headDir, "0002_harden_widgets.sql", `
-- +migrate Up
-- data-checked:
ALTER TABLE widgets MODIFY COLUMN quantity BIGINT NOT NULL;
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("empty annotation findings = %#v, want one", report.Findings)
	}
}

func TestLintDirsDoesNotFlagTableCreatedInSameMigration(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, headDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32));
ALTER TABLE widgets ADD CONSTRAINT chk_code CHECK (code <> '');
ALTER TABLE widgets ADD UNIQUE KEY uq_widgets_code (code);
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("same-file table findings = %#v, want none", report.Findings)
	}
}

func TestLintDirsFlagsTableCreatedByEarlierNewMigration(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, headDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32));
`)
	writeMigration(t, headDir, "0002_widgets_unique.sql", `
-- +migrate Up
ALTER TABLE widgets ADD UNIQUE KEY uq_widgets_code (code);
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Risk != RiskUnique {
		t.Fatalf("later new migration findings = %#v, want one UNIQUE finding", report.Findings)
	}
}

func TestLintDirsTreatsEveryBaseTableAsAlreadyDeployed(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, baseDir, "0002_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32));
`)
	copyMigrations(t, baseDir, headDir)
	// A misnumbered new migration is applied after the complete base chain even
	// though its filename sorts first. Numbering lint rejects it separately; the
	// safety lint must not silently miss the old table.
	writeMigration(t, headDir, "0001_widgets_unique.sql", `
-- +migrate Up
ALTER TABLE widgets ADD UNIQUE KEY uq_widgets_code (code);
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Table != "widgets" {
		t.Fatalf("misnumbered migration findings = %#v, want old widgets table", report.Findings)
	}
}

func TestLintDirsIgnoresChangesToExistingMigrationFiles(t *testing.T) {
	baseDir, headDir := migrationPair(t)
	writeMigration(t, baseDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32));
`)
	writeMigration(t, headDir, "0001_widgets.sql", `
-- +migrate Up
CREATE TABLE widgets (id INT, code VARCHAR(32));
ALTER TABLE widgets ADD UNIQUE KEY uq_widgets_code (code);
`)

	report, err := LintDirs(baseDir, headDir)
	if err != nil {
		t.Fatalf("LintDirs: %v", err)
	}
	if len(report.NewFiles) != 0 || len(report.Findings) != 0 {
		t.Fatalf("existing-file report = %#v, want no new files or findings", report)
	}
}

func TestCompactStatementKeepsUTF8Valid(t *testing.T) {
	got := compactStatement("ALTER TABLE widgets COMMENT '" + strings.Repeat("проверено ", 80) + "';")
	if !utf8.ValidString(got) {
		t.Fatalf("compact statement is invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("compact statement was not truncated: %q", got)
	}
}

func migrationPair(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	headDir := filepath.Join(root, "head")
	for _, dir := range []string{baseDir, headDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return baseDir, headDir
}

func writeMigration(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func copyMigrations(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), body, 0o644); err != nil {
			t.Fatalf("copy %s: %v", entry.Name(), err)
		}
	}
}
