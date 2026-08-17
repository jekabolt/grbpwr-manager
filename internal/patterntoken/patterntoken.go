// Package patterntoken mints and verifies the capability tokens behind /api/p/{token} —
// the stable, unauthenticated-but-unforgeable urls that serve private pattern objects
// (выкройки). A token names a pattern_object_access ROW (id) at a specific revocation
// epoch; bumping the row's epoch invalidates every previously minted token for the
// object without touching the object or the pepper.
//
// Format: {scope}{id36}.{epoch36}.{sig}
//   - scope is one byte: 'i' (internal — admin SPA), 'p' (print — tech-pack QR), 'c'
//     (card viewer — the id names a TECH CARD, not a pattern_object_access row), 'r'
//     (run pack — the id names a PRODUCTION RUN) or 'f' (library file — the id names a
//     LIBRARY FILE).
//     Scopes sign differently, so revoking a leaked paper tech-pack does not have to
//     break the admin UI and vice versa (each scope can be re-epoched independently at a
//     policy level later; today 'i'/'p' share the object row epoch, 'c' has its own row in
//     tech_card_pattern_viewer_access, 'r' its own in production_run_pack_access and 'f'
//     its own in library_file_public_access).
//     Because 'c', 'r' and 'f' tokens carry DIFFERENT id namespaces, every handler must
//     check the scope it serves — a card token looked up as an object id would resolve to
//     an unrelated object, and a run token looked up as a card id would serve an unrelated
//     card's manifest. The check is an allowlist per endpoint, never a denylist.
//   - id36 / epoch36 are base-36 (lowercase) — compact, so the printed QR stays small.
//   - sig is base64url(HMAC_SHA256(pepper, "v1|"+scope+"|"+id+"|"+epoch)[:16]) — 128 bits.
//
// Expiry deliberately does NOT live in the token: it lives in the row (expires_at), so
// the policy can change without re-printing QR codes. The token is fully deterministic —
// one object always yields one url, which is what lets <object>/<iframe> render without
// cache-busting remounts.
package patterntoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Scope says which surface a token was minted for.
type Scope byte

const (
	// ScopeInternal marks tokens embedded in admin API responses (view_url/download_url).
	ScopeInternal Scope = 'i'
	// ScopePrint marks tokens embedded in printed tech-pack QR codes.
	ScopePrint Scope = 'p'
	// ScopeCard marks card-viewer tokens (/api/pv/{token}): the id is a TECH CARD id at
	// the tech_card_pattern_viewer_access epoch — NOT a pattern_object_access row id.
	// Handlers of the object scopes must refuse it, or a card token would be looked up
	// against pattern_object_access and serve whatever object shares the number.
	ScopeCard Scope = 'c'
	// ScopeRunPack marks run-pack tokens (/api/rp/{token}, migration 0293): the id is a
	// PRODUCTION RUN id at the production_run_pack_access epoch. It is the third id
	// namespace in this format and shares numbers with the other two, so it is the third
	// reason every handler allowlists its own scope: a run token accepted by /api/pv would
	// print the tech card that happens to carry the run's number.
	ScopeRunPack Scope = 'r'
	// ScopeFile marks public library-file links (/api/f/{token}, migration 0317): the id is
	// a LIBRARY FILE id (library_file.id) at the library_file_public_access epoch. It is the
	// FOURTH id namespace in this format, and the one most likely to be handed to an
	// outsider — the whole point of the scope is that a person outside the company holds
	// this string. Sharing numbers with patterns, cards and runs is therefore not a
	// theoretical collision: without the per-endpoint allowlist, a contractor's file link
	// re-labelled 'c' would print a tech card, and a QR from the shop floor re-labelled 'f'
	// would hand out a file. Re-labelling breaks the signature (the scope byte is signed),
	// so the allowlist and the signature close the door together, not separately.
	//
	// The row also holds the level check's other half: the epoch here says "this link is
	// still the current one", NOT "this file is public". Publicity lives in
	// library_file.access_level, and the handler must read it — a file switched back to
	// 'team' keeps its access row, and a token that only matched the epoch would outlive
	// the decision to close the file.
	ScopeFile Scope = 'f'
)

// valid reports whether s is one of the known scopes. Parse refuses everything else, so an
// unknown scope byte never reaches a handler's allowlist. Kept as a switch (not a chain of
// !=) because the list grows: the failure mode of a forgotten scope is silent — Parse would
// reject legitimate tokens of a scope that mints fine.
func (s Scope) valid() bool {
	switch s {
	case ScopeInternal, ScopePrint, ScopeCard, ScopeRunPack, ScopeFile:
		return true
	default:
		return false
	}
}

const sigBytes = 16

// ErrInvalid is returned for any structurally broken or badly signed token. Callers must
// not distinguish parse failures from signature failures externally (uniform 404).
var ErrInvalid = errors.New("invalid pattern token")

// Minter mints and verifies tokens with a server-side pepper.
type Minter struct {
	pepper []byte
}

// NewMinter fails closed on an empty pepper — an empty HMAC key would make every token
// forgeable by anyone who reads this file.
func NewMinter(pepper string) (*Minter, error) {
	if strings.TrimSpace(pepper) == "" {
		return nil, errors.New("pattern token pepper is empty: set PATTERN_TOKEN_PEPPER")
	}
	return &Minter{pepper: []byte(pepper)}, nil
}

func (m *Minter) sig(scope Scope, id int64, epoch int) []byte {
	mac := hmac.New(sha256.New, m.pepper)
	fmt.Fprintf(mac, "v1|%c|%d|%d", scope, id, epoch)
	return mac.Sum(nil)[:sigBytes]
}

// Mint returns the deterministic token for (scope, row id, epoch).
func (m *Minter) Mint(scope Scope, id int64, epoch int) string {
	return fmt.Sprintf("%c%s.%s.%s",
		scope,
		strconv.FormatInt(id, 36),
		strconv.FormatInt(int64(epoch), 36),
		base64.RawURLEncoding.EncodeToString(m.sig(scope, id, epoch)))
}

// Parse verifies the signature and returns the token's claims. The epoch returned is the
// one the token was MINTED for; the caller must still compare it against the row's
// current epoch (revocation) and check the row's expiry/revoked state.
func (m *Minter) Parse(token string) (scope Scope, id int64, epoch int, err error) {
	if len(token) < 2 {
		return 0, 0, 0, ErrInvalid
	}
	scope = Scope(token[0])
	if !scope.valid() {
		return 0, 0, 0, ErrInvalid
	}
	parts := strings.Split(token[1:], ".")
	if len(parts) != 3 {
		return 0, 0, 0, ErrInvalid
	}
	id, err = strconv.ParseInt(parts[0], 36, 64)
	if err != nil || id <= 0 {
		return 0, 0, 0, ErrInvalid
	}
	epoch64, err := strconv.ParseInt(parts[1], 36, 32)
	if err != nil || epoch64 < 0 {
		return 0, 0, 0, ErrInvalid
	}
	epoch = int(epoch64)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(got) != sigBytes {
		return 0, 0, 0, ErrInvalid
	}
	if !hmac.Equal(got, m.sig(scope, id, epoch)) {
		return 0, 0, 0, ErrInvalid
	}
	// CANONICALITY. The signature covers the PARSED values, but parsing is not injective:
	// ParseInt(base36) accepts leading zeros, '+' and upper case, and RawURLEncoding
	// ignores the unused trailing bits of the last character — so one valid token has
	// thousands of equivalent spellings that all verify. That is not a forgery, but any
	// caller keying state on the token STRING (rate limiting, caching, dedupe) would see
	// each spelling as a fresh token. Re-minting and comparing pins the one spelling.
	if token != m.Mint(scope, id, epoch) {
		return 0, 0, 0, ErrInvalid
	}
	return scope, id, epoch, nil
}
