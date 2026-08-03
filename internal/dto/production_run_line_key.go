package dto

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// mintProductionRunLineKey creates the stable 26-character identity of a production-run plan line
// (migration 0230), the same shape and for the same reason as mintTechCardLineKey does for BOM lines
// and cut-pieces: the store diffs the submitted grid by this key and UPDATEs matched rows in place,
// so a line's database id survives an edit of the run.
//
// The admin client mints a real Crockford ULID; this fallback only covers payloads that arrive
// without one (a client that predates the field, or a line just added). Standard base32 of 128
// random bits is 26 characters of [A-Z2-7] — inside the accepted charset, unique for all practical
// purposes, and never colliding with the 'LEGACY'-prefixed keys the migration backfilled.
func mintProductionRunLineKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read randomness for production run line key: %w", err)
	}
	key := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	if !entity.IsValidProductionRunLineKey(key) { // unreachable; guards a future encoder swap
		return "", fmt.Errorf("minted production run line key %q is not a valid key", key)
	}
	return key, nil
}
