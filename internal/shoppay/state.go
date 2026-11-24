package shoppay

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// The Connect OAuth state: who started the connect, for which team, until when, and where the owner
// lands afterwards — signed (HMAC under SHOP_STRIPE_MASTER), short-lived and single use on this
// instance. The callback runs on the API host without the admin's session, so the state is the only
// proof that the team's owner asked for this account.

const (
	stateTTL         = 15 * time.Minute
	maxReturnPath    = 200
	defaultReturn    = "/shop"
	maxStateTokenLen = 1024
)

var errBadState = errors.New("shoppay: bad connect state")

type connectState struct {
	Team     int64  `json:"t"`
	User     int64  `json:"u"`
	Operator bool   `json:"o,omitempty"`
	Exp      int64  `json:"e"`
	Nonce    string `json:"n"`
	Return   string `json:"r"`
}

func (k *keys) signState(s connectState) string {
	b, _ := json.Marshal(s)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(k.mac(b))
}

// verifyState checks the signature and the expiry (the nonce is the caller's: nonces.use).
func (k *keys) verifyState(tok string, now time.Time) (connectState, error) {
	if tok == "" || len(tok) > maxStateTokenLen {
		return connectState{}, errBadState
	}
	body, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return connectState{}, errBadState
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return connectState{}, errBadState
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, k.mac(b)) {
		return connectState{}, errBadState
	}
	var s connectState
	if err := json.Unmarshal(b, &s); err != nil || s.Team <= 0 || s.User <= 0 || s.Nonce == "" {
		return connectState{}, errBadState
	}
	if now.Unix() > s.Exp {
		return connectState{}, errBadState
	}
	if _, ok := cleanReturn(s.Return); !ok {
		return connectState{}, errBadState
	}
	return s, nil
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// cleanReturn validates the admin path the owner lands on after Stripe: a path on the team's own
// admin host, never another host. "" = the shop screen.
func cleanReturn(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return defaultReturn, true
	}
	if len(p) > maxReturnPath || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") ||
		strings.ContainsAny(p, "\\\r\n\t ") || strings.Contains(p, "://") {
		return "", false
	}
	return p, true
}

// nonces remembers used state nonces until their state expires: a state opens one callback.
type nonces struct {
	mu   sync.Mutex
	used map[string]time.Time
}

// use records n (valid until exp) and reports whether it was fresh.
func (n *nonces) use(nonce string, exp, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.used == nil {
		n.used = make(map[string]time.Time)
	}
	for k, e := range n.used {
		if now.After(e) {
			delete(n.used, k)
		}
	}
	if _, seen := n.used[nonce]; seen {
		return false
	}
	n.used[nonce] = exp
	return true
}
