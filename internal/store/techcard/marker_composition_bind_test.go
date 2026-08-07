package techcard

import (
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The СОСТАВ queries run on the card read and inside every marker save, and a bind error would take
// both down at request time with nothing but a MySQL-backed test to catch it. sqlx.Named reproduces
// the failure without a database: sqlx reads EVERY ':' as a named parameter — including one inside a
// `--` comment, which is why the rationale for these queries lives in Go comments above them.
func TestMarkerCompositionQueriesBind(t *testing.T) {
	cases := []struct {
		name  string
		query string
		args  map[string]any
		want  int
	}{
		{"read", markerCompositionQuery, map[string]any{"ids": []int{1, 2}}, 1},
		{"card sizes", cardSizeMembershipQuery, map[string]any{"card": 1, "sizes": []int{2, 3}}, 2},
		{"delete", markerCompositionDeleteQuery, map[string]any{"marker_id": 1}, 1},
		{"insert", markerCompositionInsertQuery, map[string]any{"marker_id": 1, "size_id": 2, "quantity": 3}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, args, err := sqlx.Named(c.query, c.args)
			if err != nil {
				t.Fatalf("%s query does not bind: %v", c.name, err)
			}
			if len(args) != c.want {
				t.Fatalf("bound args = %d, want %d", len(args), c.want)
			}
		})
	}
}

// The emitted состав is read verbatim into a list row, so its order must be a function of the data.
// Without the ORDER BY, InnoDB may hand back the same unchanged marker in a different order between
// two reads, and a summary that reshuffles itself reads as data having changed.
func TestMarkerCompositionQueryIsOrdered(t *testing.T) {
	if !strings.Contains(markerCompositionQuery, "ORDER BY marker_id, size_id") {
		t.Fatal("the composition read must be ordered by (marker_id, size_id)")
	}
}
