package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const compositeV6SidecarMigration = "0475_pattern_semantic_sidecar_composite_v6_media.sql"

func TestPatternSemanticSidecarCompositeV6MigrationWidensOnlyTheMediaCheck(t *testing.T) {
	body := readCompositeV6Migration(t)
	up, down, found := strings.Cut(body, "-- +migrate Down")
	if !found {
		t.Fatal("migration has no Down section")
	}
	wantV5 := []string{
		"application/vnd.grbpwr.pattern-dxf-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v3+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json",
	}
	assertMediaAllowlist(t, up, append(append([]string(nil), wantV5...),
		"application/vnd.grbpwr.pattern-dxf-product-composite.v6+json"))
	narrowingAt := strings.Index(down, "SET @composite_v6_rows")
	if narrowingAt < 0 {
		t.Fatal("rollback has no V6-row refusal gate")
	}
	assertMediaAllowlist(t, down[narrowingAt:], wantV5)

	for _, required := range []string{
		"information_schema.TABLE_CONSTRAINTS",
		"information_schema.CHECK_CONSTRAINTS",
		"CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'",
		"DROP CHECK chk_pattern_semantic_sidecar_media_type",
		"ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK",
		"migrationlint:allow-retroactive",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("Composite V6 Up migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE tech_card_size_pattern", "UPDATE tech_card_pattern_semantic_sidecar",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("Composite V6 migration must only widen the sidecar media CHECK: found %q", forbidden)
		}
	}
}

func TestPatternSemanticSidecarCompositeV6RollbackFailsClosedAndPreservesV5(t *testing.T) {
	body := readCompositeV6Migration(t)
	_, down, found := strings.Cut(body, "-- +migrate Down")
	if !found {
		t.Fatal("migration has no Down section")
	}
	refusal := strings.Index(down, "SIGNAL SQLSTATE ''45000''")
	dropCheck := strings.Index(down, "DROP CHECK chk_pattern_semantic_sidecar_media_type")
	addOldCheck := -1
	if dropCheck >= 0 {
		if relative := strings.Index(down[dropCheck:], "ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK"); relative >= 0 {
			addOldCheck = dropCheck + relative
		}
	}
	if refusal < 0 || dropCheck < 0 || addOldCheck < 0 || refusal >= dropCheck || dropCheck >= addOldCheck {
		t.Fatalf("rollback refusal/drop/add order is unsafe: refusal=%d drop=%d add=%d", refusal, dropCheck, addOldCheck)
	}
	if !strings.Contains(down, "pattern-dxf-product-composite.v5+json") {
		t.Error("rollback must preserve the Composite V5 media admitted before this migration")
	}
	if strings.Contains(down, "DELETE FROM tech_card_pattern_semantic_sidecar") {
		t.Error("rollback must not silently destroy Composite V6 arrangement state")
	}
	if got := strings.Count(down, "information_schema.TABLE_CONSTRAINTS"); got != 2 {
		t.Errorf("rollback must audit recovery and atomic narrowing state; got %d guards", got)
	}
	atomicNarrowing := "DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK"
	if !strings.Contains(down, atomicNarrowing) {
		t.Error("rollback must replace the V6 CHECK with the V5 CHECK in one atomic ALTER")
	}
	standaloneDrop := "DROP CHECK chk_pattern_semantic_sidecar_media_type',"
	if strings.Contains(down, standaloneDrop) {
		t.Error("rollback must never commit a standalone DROP CHECK before adding its replacement")
	}
	recovery := strings.Index(down, "@has_media_check = 0")
	refusalGate := strings.Index(down, "SET @composite_v6_rows")
	if recovery < 0 || refusalGate < 0 || recovery >= refusalGate {
		t.Fatalf("rollback must restore an absent V6 CHECK before counting/refusing rows: recovery=%d refusal=%d", recovery, refusalGate)
	}
	recoverySection := down[recovery:refusalGate]
	if !strings.Contains(recoverySection, "pattern-dxf-product-composite.v6+json") {
		t.Error("half-applied rollback recovery must restore the permissive V6 CHECK before narrowing")
	}
}

func readCompositeV6Migration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "sql", compositeV6SidecarMigration))
	if err != nil {
		t.Fatalf("read %s: %v", compositeV6SidecarMigration, err)
	}
	return string(body)
}
