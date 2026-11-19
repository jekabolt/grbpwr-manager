package provision

import (
	"crypto/rand"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/store"
)

const nameAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewDBName returns prefix + 8 random [a-z0-9], already checked against the allowlist.
func NewDBName(prefix, team1DB string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("provision: random name: %w", err)
	}
	suffix := make([]byte, len(b))
	for i, v := range b {
		// 256 % 36 = 4: a negligible bias, and the name is a label, not a secret.
		suffix[i] = nameAlphabet[int(v)%len(nameAlphabet)]
	}
	name := prefix + string(suffix)
	if err := store.NewTeamDBName(prefix, team1DB, name); err != nil {
		return "", err
	}
	return name, nil
}
