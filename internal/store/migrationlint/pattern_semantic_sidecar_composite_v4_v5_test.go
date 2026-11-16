package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const compositeV4V5SidecarMigration = "0474_pattern_semantic_sidecar_composite_v4_v5_media.sql"

var mediaTypeLiteralRe = regexp.MustCompile(`application/vnd\.grbpwr\.[a-z0-9.+-]+`)

func TestPatternSemanticSidecarCompositeV4V5MigrationWidensOnlyTheMediaCheck(t *testing.T) {
	body := readCompositeV4V5Migration(t)
	up, down, found := strings.Cut(body, "-- +migrate Down")
	if !found {
		t.Fatal("migration has no Down section")
	}

	wantUp := []string{
		"application/vnd.grbpwr.pattern-dxf-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v3+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v4+json",
		"application/vnd.grbpwr.pattern-dxf-product-composite.v5+json",
	}
	wantDown := wantUp[:3]
	assertMediaAllowlist(t, up, wantUp)
	assertMediaAllowlist(t, down, wantDown)

	for _, required := range []string{
		"information_schema.TABLE_CONSTRAINTS",
		"information_schema.CHECK_CONSTRAINTS",
		"CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'",
		"DROP CHECK chk_pattern_semantic_sidecar_media_type",
		"ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK",
		"migrationlint:allow-retroactive",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("Composite V4/V5 Up migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE TABLE",
		"ALTER TABLE tech_card_size_pattern",
		"UPDATE tech_card_pattern_semantic_sidecar",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("Composite V4/V5 migration must only widen the sidecar media CHECK: found %q", forbidden)
		}
	}
}

func TestPatternSemanticSidecarCompositeV4V5RollbackFailsClosedBeforeNarrowing(t *testing.T) {
	body := readCompositeV4V5Migration(t)
	_, down, found := strings.Cut(body, "-- +migrate Down")
	if !found {
		t.Fatal("migration has no Down section")
	}

	refusal := strings.Index(down, "SIGNAL SQLSTATE ''45000''")
	dropCheck := strings.Index(down, "DROP CHECK chk_pattern_semantic_sidecar_media_type")
	addOldCheck := strings.Index(down, "ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK")
	if refusal < 0 || dropCheck < 0 || addOldCheck < 0 {
		t.Fatalf("rollback is missing refusal/drop/add sequence: refusal=%d drop=%d add=%d", refusal, dropCheck, addOldCheck)
	}
	if refusal >= dropCheck || dropCheck >= addOldCheck {
		t.Fatalf("rollback must refuse before narrowing the CHECK: refusal=%d drop=%d add=%d", refusal, dropCheck, addOldCheck)
	}
	if strings.Contains(down, "DELETE FROM tech_card_pattern_semantic_sidecar") {
		t.Error("rollback must not silently destroy V4/V5 annotation authority")
	}
	if got := strings.Count(down, "information_schema.TABLE_CONSTRAINTS"); got != 2 {
		t.Errorf("rollback must guard both DROP and ADD for partial retry; got %d guards", got)
	}
}

func readCompositeV4V5Migration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "sql", compositeV4V5SidecarMigration))
	if err != nil {
		t.Fatalf("read %s: %v", compositeV4V5SidecarMigration, err)
	}
	return string(body)
}

func assertMediaAllowlist(t *testing.T, section string, want []string) {
	t.Helper()
	add := strings.Index(section, "ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK")
	if add < 0 {
		t.Fatal("named media CHECK addition not found")
	}
	statement := section[add:]
	end := strings.Index(statement, "',\n  'SELECT 1'")
	if end < 0 {
		t.Fatal("end of guarded media CHECK DDL not found")
	}
	got := mediaTypeLiteralRe.FindAllString(statement[:end], -1)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("media allowlist = %q, want %q", got, want)
	}
}
