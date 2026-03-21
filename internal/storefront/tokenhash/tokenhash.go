// Package tokenhash provides constant-time hashing for opaque storefront tokens (login + refresh).
package tokenhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// Hash returns a hex-encoded HMAC-SHA256 of value using pepper as the key.
func Hash(pepper, value string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// Equal compares two hex hashes in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
