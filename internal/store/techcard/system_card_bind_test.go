package techcard

import (
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The system-card filter binds its pattern: the prefix holds a ':', and a literal 'system:%' in the
// SQL text would be read by sqlx as an empty bind name, failing ListTechCards, the pipeline and
// DeleteTechCard at request time. No database needed — sqlx.Named alone reproduces it.
func TestTechCardNotSystemFilterBindsItsPattern(t *testing.T) {
	q, args, err := sqlx.Named(`SELECT COUNT(*) FROM tech_card WHERE id = :id AND NOT `+techCardNotSystemSQL,
		map[string]any{"id": 1, techCardSystemKeyPatternParam: techCardSystemKeyPattern})
	if err != nil {
		t.Fatalf("system-card filter does not bind: %v", err)
	}
	if len(args) != 2 || args[1] != "system:%" {
		t.Fatalf("args = %v, want [1 system:%%]", args)
	}
	if strings.Contains(q, "system") {
		t.Fatalf("the pattern must be bound, not spelled in the SQL: %s", q)
	}
}
