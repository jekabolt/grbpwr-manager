package patterntoken

import (
	"strings"
	"testing"
)

func TestMintParseRoundtrip(t *testing.T) {
	m, err := NewMinter("test-pepper")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []Scope{ScopeInternal, ScopePrint} {
		tok := m.Mint(scope, 12345, 7)
		gotScope, id, epoch, err := m.Parse(tok)
		if err != nil {
			t.Fatalf("scope %c: parse: %v", scope, err)
		}
		if gotScope != scope || id != 12345 || epoch != 7 {
			t.Fatalf("scope %c: got (%c,%d,%d)", scope, gotScope, id, epoch)
		}
	}
}

func TestMintDeterministic(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	if m.Mint(ScopeInternal, 42, 0) != m.Mint(ScopeInternal, 42, 0) {
		t.Fatal("mint must be deterministic")
	}
	if m.Mint(ScopeInternal, 42, 0) == m.Mint(ScopePrint, 42, 0) {
		t.Fatal("scopes must sign differently")
	}
	if m.Mint(ScopeInternal, 42, 0) == m.Mint(ScopeInternal, 42, 1) {
		t.Fatal("epochs must sign differently")
	}
}

func TestParseRejectsForgery(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	other, _ := NewMinter("other-pepper")
	cases := map[string]string{
		"empty":          "",
		"one char":       "i",
		"bad scope":      "x" + m.Mint(ScopeInternal, 1, 0)[1:],
		"missing parts":  "i12.0",
		"extra parts":    m.Mint(ScopeInternal, 1, 0) + ".zzz",
		"zero id":        "i0.0.AAAAAAAAAAAAAAAAAAAAAA",
		"negative id":    "i-1.0.AAAAAAAAAAAAAAAAAAAAAA",
		"bad base36":     "i!!.0.AAAAAAAAAAAAAAAAAAAAAA",
		"short sig":      "i1.0.AAAA",
		"foreign pepper": other.Mint(ScopeInternal, 1, 0),
		"tampered id": func() string {
			tok := m.Mint(ScopeInternal, 1, 0)
			return string(tok[0]) + "2" + tok[2:]
		}(),
	}
	for name, tok := range cases {
		if _, _, _, err := m.Parse(tok); err == nil {
			t.Errorf("%s: forged token %q accepted", name, tok)
		}
	}
}

func TestEpochMismatchIsCallersJob(t *testing.T) {
	// Parse only verifies the signature; a token minted at epoch 0 still PARSES after the
	// row moved to epoch 1 — the handler compares epochs. Locked here so nobody "fixes"
	// Parse into doing DB lookups.
	m, _ := NewMinter("test-pepper")
	tok := m.Mint(ScopePrint, 9, 0)
	_, _, epoch, err := m.Parse(tok)
	if err != nil || epoch != 0 {
		t.Fatalf("expected epoch 0 claim, got %d err %v", epoch, err)
	}
}

func TestEmptyPepperFailsClosed(t *testing.T) {
	if _, err := NewMinter("  "); err == nil {
		t.Fatal("empty pepper must be rejected")
	}
}

func TestTokenShape(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	tok := m.Mint(ScopePrint, 1234567, 3)
	if len(tok) > 40 {
		t.Fatalf("token unexpectedly long (%d): %s", len(tok), tok)
	}
	if strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token must be url-safe: %s", tok)
	}
}

// TestNonCanonicalSpellingsAreRejected locks the canonicality check. base36 parsing
// accepts leading zeros, '+' and upper case, and RawURLEncoding ignores the unused
// trailing bits of the final character — so without the re-mint comparison one valid
// token would have thousands of spellings that all pass the signature check. They are not
// forgeries, but anything keying state on the token STRING (rate limiting, caches) would
// treat each as new, which is exactly how a per-token budget gets bypassed.
func TestNonCanonicalSpellingsAreRejected(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	const id, epoch = 1, 0
	canonical := m.Mint(ScopeInternal, id, epoch)
	if _, _, _, err := m.Parse(canonical); err != nil {
		t.Fatalf("canonical token must parse: %v", err)
	}
	sig := canonical[strings.Index(canonical, ".")+1:] // "epoch36.sig"
	sig = sig[strings.Index(sig, ".")+1:]

	variants := map[string]string{
		"leading zero id":    "i01.0." + sig,
		"many zeros id":      "i0001.0." + sig,
		"plus signed id":     "i+1.0." + sig,
		"upper case id":      "i1.0." + strings.ToUpper(sig[:1]) + sig[1:],
		"leading zero epoch": "i1.00." + sig,
		"plus signed epoch":  "i1.+0." + sig,
		"negative zero":      "i1.-0." + sig,
	}
	for name, tok := range variants {
		if tok == canonical {
			continue // upper-casing a digit that has no letter form
		}
		if _, _, _, err := m.Parse(tok); err == nil {
			t.Errorf("%s: non-canonical token %q must be rejected", name, tok)
		}
	}

	// Trailing-bit malleability: a 22-char base64url encoding of 16 bytes leaves 4 bits
	// unused, so 16 distinct strings decode to the same signature bytes.
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := canonical[len(canonical)-1]
	rejected := 0
	for _, c := range alphabet {
		alt := canonical[:len(canonical)-1] + string(c)
		if alt == canonical {
			continue
		}
		_, _, _, err := m.Parse(alt)
		if err == nil {
			t.Errorf("trailing-bit variant %q must be rejected (last char %q)", alt, string(last))
		} else {
			rejected++
		}
	}
	if rejected != len(alphabet)-1 {
		t.Fatalf("expected every variant rejected, got %d of %d", rejected, len(alphabet)-1)
	}
}
