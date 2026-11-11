package techcard

import (
	"os"
	"strings"
	"testing"
)

// THE FOLD WRITES KEEP THE SEAMS' TWO INVARIANTS (seams_lock_test.go explains both): the card row is
// taken FOR UPDATE before any read, and lock_version is never touched — the form's autosave must
// not abort because somebody drew a fold.
func TestPieceFoldWritesLockTheCardRowFirstAndNeverBumpLockVersion(t *testing.T) {
	for _, sig := range []string{
		"func (s *Store) UpsertTechCardPieceFolds",
		"func (s *Store) DeleteTechCardPieceFolds",
		"func (s *Store) ReplaceTechCardPieceFolds",
	} {
		t.Run(sig, func(t *testing.T) {
			body := funcBody(t, "piece_folds.go", sig)
			lock := strings.Index(body, "lockTechCardRow(")
			if lock < 0 {
				t.Fatal("the fold write no longer locks the card row")
			}
			for _, after := range []string{
				"RequireMutableTechCard(", "storeutil.Query", "storeutil.Exec", "listTechCardPieceFolds(",
				"loadSeamSources(", "writeTechCardPieceFoldsTx(",
			} {
				at := strings.Index(body, after)
				if at < 0 && after == "RequireMutableTechCard(" {
					t.Fatal("the fold write no longer refuses a released card")
				}
				if at >= 0 && at < lock {
					t.Fatalf("%q runs before lockTechCardRow: the snapshot would open before the lock", after)
				}
			}
		})
	}

	src, err := os.ReadFile("piece_folds.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "//") {
			continue
		}
		if strings.Contains(code, "lock_version") {
			t.Fatalf("piece_folds.go touches lock_version (%q): folds must never bump the card's optimistic lock", code)
		}
	}
}
