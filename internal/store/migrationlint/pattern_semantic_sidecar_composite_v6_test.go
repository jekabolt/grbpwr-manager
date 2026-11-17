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
	assertMediaAllowlist(t, down, wantV5)

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
	addOldCheck := strings.Index(down, "ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK")
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
		t.Errorf("rollback must guard both DROP and ADD for partial retry; got %d guards", got)
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
