package techcard

import (
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TestExpireStaleImportsQueryBinds is the bind half, in the shape this package already uses for
// the import write path: sqlx's named binder is where a stray ':' or a typo'd parameter turns into
// a runtime error nobody sees until the worker has been failing for an hour.
func TestExpireStaleImportsQueryBinds(t *testing.T) {
	_, args, err := storeutil.MakeQuery(expireStaleTechCardImportsQuery, map[string]any{
		"expired":   entity.TechCardImportStatusExpired,
		"uploaded":  entity.TechCardImportStatusUploaded,
		"olderThan": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("bound %d args, want 3: %v", len(args), args)
	}
}

// TestExpireStaleImportsMovesOnlyUncommittedUploads guards the two conditions the statement cannot
// lose.
//
// WITHOUT THE STATUS TEST the sweep repaints EVERY import row older than a week as 'expired',
// including the 'committed' ones — which are the only record of where an imported card came from,
// are read long after the archive's bytes are gone, and would come back saying the card's own
// archive expired before it was ever used. Without the age test it does that to all of them at
// once. Neither failure raises an error, neither is visible on the screen that reads these rows,
// and neither can be undone: the prior status is not stored anywhere else.
//
// A live-database test would say this better, and there is no live database here. So the guard is
// on the statement itself, which is at least the thing that runs (the query is a named constant so
// that this cannot drift into checking a copy).
func TestExpireStaleImportsMovesOnlyUncommittedUploads(t *testing.T) {
	q := strings.Join(strings.Fields(expireStaleTechCardImportsQuery), " ")

	for _, want := range []string{
		"UPDATE tech_card_import",
		"SET status = :expired",
		// The status test, spelled out: only an upload nobody committed may move.
		"WHERE status = :uploaded",
		// The age test: strictly older than the cutoff the caller passed. `<=` would be
		// harmless; `>` or a missing bound would expire everything.
		"created_at < :olderThan",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("the expiry statement no longer says %q — it now reads: %s", want, q)
		}
	}

	// The statuses are entity constants, not literals, because the commit path CLAIMS a row by
	// matching the exact word: two spellings of one status is a row nobody picks up.
	if entity.TechCardImportStatusUploaded != "uploaded" || entity.TechCardImportStatusExpired != "expired" {
		t.Fatalf("status vocabulary moved: uploaded=%q expired=%q",
			entity.TechCardImportStatusUploaded, entity.TechCardImportStatusExpired)
	}
	// Nothing may repaint a committed or failed import: the first is a card's provenance, the
	// second is the record of a commit that did not survive its transaction.
	for _, forbidden := range []string{
		entity.TechCardImportStatusCommitted,
		entity.TechCardImportStatusFailed,
	} {
		if strings.Contains(q, forbidden) {
			t.Fatalf("the expiry statement names %q; it must touch neither", forbidden)
		}
	}
}
