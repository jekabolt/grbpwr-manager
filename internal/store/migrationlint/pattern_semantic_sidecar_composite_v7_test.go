package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const compositeV7SidecarMigration = "0476_pattern_semantic_sidecar_composite_v7_media.sql"

func TestPatternSemanticSidecarCompositeV7MigrationWidensOnlyTheMediaCheck(t *testing.T) {
	body := readCompositeV7Migration(t)
	up, down, found := strings.Cut(body, "-- +migrate Down")
	if !found {
		t.Fatal("migration has no Down section")
	}
	wantV6 := []string{
		"application/vnd.grbpwr.pattern-dxf-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v3+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v6+json",
	}
	assertMediaAllowlist(t, up, append(append([]string(nil), wantV6...),
		"application/vnd.grbpwr.pattern-dxf-product-composite.v7+json"))
	narrowingAt := strings.Index(down, "SET @composite_v7_rows")
	if narrowingAt < 0 {
		t.Fatal("rollback has no V7-row refusal gate")
	}
	assertMediaAllowlist(t, down[narrowingAt:], wantV6)

	for _, required := range []string{
		"information_schema.TABLE_CONSTRAINTS",
		"information_schema.CHECK_CONSTRAINTS",
		"CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'",
		"DROP CHECK chk_pattern_semantic_sidecar_media_type",
		"ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK",
		"migrationlint:allow-retroactive",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("Composite V7 Up migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE tech_card_size_pattern", "UPDATE tech_card_pattern_semantic_sidecar",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("Composite V7 migration must only widen the sidecar media CHECK: found %q", forbidden)
		}
	}
}

func TestPatternSemanticSidecarCompositeV7RollbackFailsClosedAndPreservesV6(t *testing.T) {
	body := readCompositeV7Migration(t)
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
	if !strings.Contains(down, "pattern-dxf-product-composite.v6+json") {
		t.Error("rollback must preserve Composite V6 media")
	}
	if strings.Contains(down, "DELETE FROM tech_card_pattern_semantic_sidecar") {
		t.Error("rollback must not silently destroy Composite V7 Full Assembly state")
	}
	if got := strings.Count(down, "information_schema.TABLE_CONSTRAINTS"); got != 2 {
		t.Errorf("rollback must audit recovery and atomic narrowing state; got %d guards", got)
	}
	atomicNarrowing := "DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK"
	if !strings.Contains(down, atomicNarrowing) {
		t.Error("rollback must replace the V7 CHECK with the V6 CHECK in one atomic ALTER")
	}
	recovery := strings.Index(down, "@has_media_check = 0")
	refusalGate := strings.Index(down, "SET @composite_v7_rows")
	if recovery < 0 || refusalGate < 0 || recovery >= refusalGate {
		t.Fatalf("rollback recovery/refusal order is unsafe: recovery=%d refusal=%d", recovery, refusalGate)
	}
	if !strings.Contains(down[recovery:refusalGate], "pattern-dxf-product-composite.v7+json") {
		t.Error("half-applied rollback recovery must restore the permissive V7 CHECK")
	}
}

func readCompositeV7Migration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "sql", compositeV7SidecarMigration))
	if err != nil {
		t.Fatalf("read %s: %v", compositeV7SidecarMigration, err)
	}
	return string(body)
}
