package patterntoken

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// TeamOne is the team every v1 token belongs to: v1 links were minted only by team #1 (grbpwr) and
// carry no team, so they resolve there — forever, including QR codes already printed on paper.
const TeamOne int64 = 1

// v2 tokens (teams wave, lane X1) carry the team that minted them:
//
//	{scope}{team36}.{id36}.{epoch36}.{sig}   sig = base64url(HMAC_SHA256(pepper, "v2|"+scope+"|"+team+"|"+id+"|"+epoch)[:16])
//
// Four dot parts against v1's three, so the two never parse as each other; the "v2|" prefix
// separates the signed strings, so no v1 signature verifies as a v2 one. The team is SIGNED: a link
// re-labelled to another team breaks its signature. Team #1 keeps minting v1 (the exact url it
// always had), and a v2 token naming team #1 is refused — one object, one spelling.

func (m *Minter) sigV2(scope Scope, team, id int64, epoch int) []byte {
	mac := hmac.New(sha256.New, m.pepper)
	fmt.Fprintf(mac, "v2|%c|%d|%d|%d", scope, team, id, epoch)
	return mac.Sum(nil)[:sigBytes]
}

// MintFor returns the token of (team, scope, id, epoch). Team #1 — and team <= 0, a caller with no
// team (teams mode off) — gets the v1 token, byte for byte what Mint returns.
func (m *Minter) MintFor(team int64, scope Scope, id int64, epoch int) string {
	if team <= TeamOne {
		return m.Mint(scope, id, epoch)
	}
	return fmt.Sprintf("%c%s.%s.%s.%s",
		scope,
		strconv.FormatInt(team, 36),
		strconv.FormatInt(id, 36),
		strconv.FormatInt(int64(epoch), 36),
		base64.RawURLEncoding.EncodeToString(m.sigV2(scope, team, id, epoch)))
}

// ParseTeam verifies a v1 or v2 token and returns its claims with the team: TeamOne for v1. As with
// Parse, the caller still checks the epoch and the row, and — new here — that the team is the one
// the request runs as.
func (m *Minter) ParseTeam(token string) (team int64, scope Scope, id int64, epoch int, err error) {
	if strings.Count(token, ".") != 3 {
		scope, id, epoch, err = m.Parse(token)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		return TeamOne, scope, id, epoch, nil
	}
	if len(token) < 2 {
		return 0, 0, 0, 0, ErrInvalid
	}
	scope = Scope(token[0])
	if !scope.valid() {
		return 0, 0, 0, 0, ErrInvalid
	}
	parts := strings.Split(token[1:], ".")
	if len(parts) != 4 {
		return 0, 0, 0, 0, ErrInvalid
	}
	team, err = strconv.ParseInt(parts[0], 36, 64)
	if err != nil || team <= TeamOne {
		return 0, 0, 0, 0, ErrInvalid
	}
	id, err = strconv.ParseInt(parts[1], 36, 64)
	if err != nil || id <= 0 {
		return 0, 0, 0, 0, ErrInvalid
	}
	epoch64, err := strconv.ParseInt(parts[2], 36, 32)
	if err != nil || epoch64 < 0 {
		return 0, 0, 0, 0, ErrInvalid
	}
	epoch = int(epoch64)
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(got) != sigBytes || !hmac.Equal(got, m.sigV2(scope, team, id, epoch)) {
		return 0, 0, 0, 0, ErrInvalid
	}
	// Canonical spelling only (see Parse): state keyed on the token string must see one token.
	if token != m.MintFor(team, scope, id, epoch) {
		return 0, 0, 0, 0, ErrInvalid
	}
	return team, scope, id, epoch, nil
}

// TokenTeam is the verified team of a token of any scope, for the edge that binds the request to
// it before any handler runs (ok=false: not a valid token — the handler answers its own 404).
func (m *Minter) TokenTeam(token string) (int64, bool) {
	team, _, _, _, err := m.ParseTeam(token)
	return team, err == nil
}

// LinkTeam is the team a link minted or served in ctx belongs to: the team in ctx, or team #1 when
// ctx carries none (teams mode off, a legacy caller).
func LinkTeam(ctx context.Context) int64 {
	if t, ok := tenant.From(ctx); ok {
		return t.ID
	}
	return TeamOne
}
