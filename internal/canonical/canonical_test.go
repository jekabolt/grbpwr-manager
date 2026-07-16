package canonical

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type tr struct {
	lang int
	name string
}

func langID(t tr) int { return t.lang }

// TestSelect is the acceptance test for problem 030: canonical translation selection is deterministic
// and order-independent — default language wins (smallest id among several defaults), else the
// smallest language id; a nil/empty default set falls back to the smallest id.
func TestSelect(t *testing.T) {
	langs := func(defaults ...int) func(int) bool {
		set := make([]entity.Language, 0)
		for _, id := range defaults {
			set = append(set, entity.Language{Id: id, IsDefault: true})
		}
		return IsDefaultFunc(set)
	}

	cases := []struct {
		name     string
		items    []tr
		isDef    func(int) bool
		wantName string
		wantOK   bool
	}{
		{
			name:     "default id not the minimal one",
			items:    []tr{{1, "en"}, {5, "de"}, {9, "fr"}},
			isDef:    langs(5), // default is language 5, not the smallest
			wantName: "de",
			wantOK:   true,
		},
		{
			name:     "no default -> smallest language id",
			items:    []tr{{9, "fr"}, {3, "es"}, {7, "it"}},
			isDef:    langs(), // none default
			wantName: "es",
			wantOK:   true,
		},
		{
			name:     "several defaults -> smallest default id",
			items:    []tr{{9, "fr"}, {4, "es"}, {7, "it"}},
			isDef:    langs(7, 4, 9), // all default (data error) -> smallest id wins
			wantName: "es",
			wantOK:   true,
		},
		{
			name:     "empty -> not ok",
			items:    nil,
			isDef:    langs(1),
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "nil predicate -> smallest id",
			items:    []tr{{8, "x"}, {2, "y"}},
			isDef:    nil,
			wantName: "y",
			wantOK:   true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Select(c.items, langID, c.isDef)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if got.name != c.wantName {
				t.Fatalf("name = %q, want %q", got.name, c.wantName)
			}
		})
	}
}

// TestSelectOrderIndependent asserts the same set of translations yields the same choice regardless of
// slice order (different SQL row orders must not change the canonical URL).
func TestSelectOrderIndependent(t *testing.T) {
	isDef := IsDefaultFunc([]entity.Language{{Id: 5, IsDefault: true}})
	orders := [][]tr{
		{{1, "en"}, {5, "de"}, {9, "fr"}},
		{{9, "fr"}, {1, "en"}, {5, "de"}},
		{{5, "de"}, {9, "fr"}, {1, "en"}},
	}
	for i, items := range orders {
		got, ok := Select(items, langID, isDef)
		if !ok || got.name != "de" {
			t.Fatalf("order %d: got %q ok=%v, want de", i, got.name, ok)
		}
	}
}
