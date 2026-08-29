package design

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// THE PAGE TOKEN OF THE BAND'S HISTORY.
//
// This is the FIRST cursor-paginated read in the admin service; everything around it pages by
// limit/offset. That is deliberate and must not be "fixed": no other paged list in the admin grows
// at its HEAD while the reader is looking at it, and an offset page would duplicate and skip rows
// exactly while somebody is uploading.
//
// IT LIVES IN THE STORE, next to the keyset it addresses, because what it encodes is a fact about
// the query — which rows were passed and under which predicate — not a fact about transport.
//
// ⚠ THE TOKEN CARRIES THE FILTER, NOT JUST THE POSITION, and that closes a real defect. The band
// returns archived rows (it ships everything with its flags and the client filters), so a cursor
// minted there and then continued by a ListDesignRuns call with include_archived=false would
// change the row set MID-PAGINATION and skip rows in silence. Putting the flag inside the token
// turns "the client will pass the same flag" from a hope about the caller into a property of the
// server.
//
// TWO CURSORS, because the page carries two lists that grow independently — generation runs and
// upload shelves. One cursor over both would drag the quiet list along behind the busy one, and
// with the generative machine cut from this wave the busy list is the shelves.
const pageTokenPrefix = "db1"

// EncodePageToken builds the opaque cursor. An empty string means the listing ends here — both
// lists are exhausted.
func EncodePageToken(runCursor, batchCursor int, includeArchived bool) string {
	if runCursor == 0 && batchCursor == 0 {
		return ""
	}
	flag := "n"
	if includeArchived {
		flag = "a"
	}
	raw := fmt.Sprintf("%s:%d:%d:%s", pageTokenPrefix, runCursor, batchCursor, flag)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodePageToken reads a cursor back. ok is false for anything this list did not mint — a token
// from another listing must be refused, not silently read as "start from the top", which would
// hand the caller page one while it believed it was on page four.
func DecodePageToken(token string) (runCursor, batchCursor int, includeArchived, ok bool) {
	if token == "" {
		return 0, 0, false, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, 0, false, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 4 || parts[0] != pageTokenPrefix {
		return 0, 0, false, false
	}
	rc, err1 := strconv.Atoi(parts[1])
	bc, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || rc < 0 || bc < 0 {
		return 0, 0, false, false
	}
	return rc, bc, parts[3] == "a", true
}
