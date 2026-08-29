package design

import "testing"

// TestPageTokenPinsTheFilterItWasMintedUnder is the running probe for the correction that the
// band's continuation must not silently change the row set.
//
// MUTATION IT CATCHES: dropping the flag from the token (encoding only the two cursors). The
// round trip then reports include_archived=false for a token the band minted, the continuation
// runs under a narrower predicate than the cursor was taken with, and rows vanish out of the
// middle of a pagination with nothing to show for it.
func TestPageTokenPinsTheFilterItWasMintedUnder(t *testing.T) {
	// A token the band minted: archived rows were IN the row set the cursor was taken over.
	tok := EncodePageToken(41, 17, true)
	if tok == "" {
		t.Fatal("a page with more rows must mint a token")
	}
	rc, bc, archived, ok := DecodePageToken(tok)
	if !ok {
		t.Fatal("our own token must decode")
	}
	if rc != 41 || bc != 17 {
		t.Fatalf("cursors = %d/%d, want 41/17", rc, bc)
	}
	if !archived {
		t.Fatal("the token must remember that it was minted over the UNFILTERED list: " +
			"continuing it under include_archived=false skips rows mid-pagination")
	}

	// And the other direction, so the flag is really carried rather than hard-coded true.
	_, _, archived, ok = DecodePageToken(EncodePageToken(5, 0, false))
	if !ok || archived {
		t.Fatal("a token minted over the filtered list must decode as filtered")
	}
}

// TestPageTokenIsOpaqueAndRefusesForeignCursors. A token this list did not mint must be REFUSED,
// never read as «start from the top»: that would hand the caller page one while it believed it
// was on page four, and the duplicate rows would look like a server bug rather than a bad token.
func TestPageTokenIsOpaqueAndRefusesForeignCursors(t *testing.T) {
	if tok := EncodePageToken(0, 0, true); tok != "" {
		t.Fatalf("an exhausted listing must mint no token, got %q", tok)
	}
	if _, _, _, ok := DecodePageToken(""); !ok {
		t.Fatal("an empty token starts a new listing and is not an error")
	}
	for _, bad := range []string{
		"not base64 !!",
		"YWJj",           // "abc" — no prefix
		"ZGIxOjE6Mg",     // db1:1:2 — a field short
		"eDE6MTowOmE",    // x1:1:0:a — a foreign list's prefix
		"ZGIxOi0xOjA6YQ", // db1:-1:0:a — a negative cursor
	} {
		if _, _, _, ok := DecodePageToken(bad); ok {
			t.Fatalf("token %q must be refused", bad)
		}
	}
}
