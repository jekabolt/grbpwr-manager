package tiermanagement

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewInviteToken returns a new random opaque token (hex) for a hacker invite.
func NewInviteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the sha256 hex digest of a raw invite token. Only the hash
// is stored; the raw token lives only in the invite URL.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
