package techcard

import (
	"os"
	"strings"
	"testing"
)

// THE SEAMS WRITES SERIALISE WITH THE CARD SAVE BY STATEMENT ORDER, WHICH NO VALUE CAN SHOW.
//
// A seam write checks that every anchor names a piece of the card and that the card is not released;
// the card save may delete a piece or release the card. In REPEATABLE READ the first plain read
// opens the snapshot, so the card row must be taken FOR UPDATE as the very first statement — any
// read before it judges the pieces and the release state as they were BEFORE a concurrent save.
// The same invariant piece_area_lock_test.go keeps for the areas, kept here for the seams.
//
// And the seams never bump lock_version: the form's autosave must not ABORT because somebody
// confirmed a seam. A refactor that «helpfully» adds a bump breaks the card editor silently.
func TestSeamWritesLockTheCardRowFirstAndNeverBumpLockVersion(t *testing.T) {
	for _, sig := range []string{
		"func (s *Store) UpsertTechCardSeams",
		"func (s *Store) DeleteTechCardSeams",
	} {
		t.Run(sig, func(t *testing.T) {
			body := funcBody(t, "seams.go", sig)
			lock := strings.Index(body, "lockTechCardRow(")
			if lock < 0 {
				t.Fatal("the seam write no longer locks the card row")
			}
			for _, after := range []string{
				"RequireMutableTechCard(", "storeutil.Query", "storeutil.Exec", "listTechCardSeams(", "loadSeamSources(",
			} {
				at := strings.Index(body, after)
				if at < 0 && after == "RequireMutableTechCard(" {
					t.Fatal("the seam write no longer refuses a released card")
				}
				if at >= 0 && at < lock {
					t.Fatalf("%q runs before lockTechCardRow: the snapshot would open before the lock", after)
				}
			}
		})
	}

	src, err := os.ReadFile("seams.go")
	if err != nil {
		t.Fatal(err)
	}
	// Only executable SQL matters; the file's comments are allowed to say the words.
	for _, line := range strings.Split(string(src), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "//") {
			continue
		}
		if strings.Contains(code, "lock_version") {
			t.Fatalf("seams.go touches lock_version (%q): seam decisions must never bump the card's optimistic lock", code)
		}
	}
}
