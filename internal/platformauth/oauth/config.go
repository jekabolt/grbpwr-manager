package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
)

// Config contains the provider credentials and the public API base used for
// OAuth callbacks. Environment binding belongs to the caller.
type Config struct {
	GoogleClientID     string
	GoogleClientSecret string

	AppleEnabled    bool
	AppleServicesID string
	AppleTeamID     string
	AppleKeyID      string
	ApplePrivateKey string

	CallbackBaseURL string
}

// Validate checks credentials that would make an enabled provider unusable.
// Apple credentials are deliberately ignored while Apple is disabled.
func (c Config) Validate() error {
	googleID := c.GoogleClientID != ""
	googleSecret := c.GoogleClientSecret != ""
	if googleID != googleSecret {
		return errors.New("oauth: google configuration incomplete")
	}
	if !c.AppleEnabled {
		return nil
	}
	if c.AppleServicesID == "" || c.AppleTeamID == "" || c.AppleKeyID == "" || c.ApplePrivateKey == "" {
		return errors.New("oauth: apple configuration incomplete")
	}
	if _, err := parseApplePrivateKey(c.ApplePrivateKey); err != nil {
		return errors.New("oauth: apple private key invalid")
	}
	return nil
}

func parseApplePrivateKey(src string) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(src))
	if block == nil || block.Type != "PRIVATE KEY" || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("invalid PEM")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid PKCS8 key")
	}
	key, ok := raw.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("invalid Apple key type")
	}
	return key, nil
}
