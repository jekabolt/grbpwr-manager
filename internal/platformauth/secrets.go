// Package platformauth is the identity logic of lekalo.studio (02-DESIGN §3–§5, §8): sign-up with a
// letter that carries a six-digit code AND a link, sign-in, hand-off between hosts, password reset,
// invite acceptance and operator sync. The gRPC handlers in internal/apisrv/auth are thin wrappers
// around Service; the rows live in internal/platformstore.
//
// Secrets never leave this package in clear except inside the outbox row of one letter (blanked by
// the mail worker's sweep once expired) and in the answer to the caller who earned them. Links and
// codes are stored hashed: a link token as sha256, a code as HMAC-SHA256 under PLATFORM_TOKEN_PEPPER
// bound to its purpose and user, so a stolen table yields nothing to replay.
package platformauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
)

// Purposes of an email_token row.
const (
	PurposeVerify  = "verify"
	PurposeReset   = "reset"
	PurposeHandoff = "handoff"
	PurposeSignIn  = "signin"
)

// tokenBytes is the entropy of a link token and of a hand-off code (256 bits).
const tokenBytes = 32

// minPepperLen: the pepper is an HMAC key; shorter than this it is a password, not a key.
const minPepperLen = 32

var errPepper = errors.New("platformauth: PLATFORM_TOKEN_PEPPER must be at least 32 characters")

// NewToken returns a fresh link token (base64url, no padding) and its stored hash.
func NewToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("platformauth: random token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the stored form of a link token or a hand-off code: sha256, hex.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewCode returns a six-digit code, uniform over 000000–999999 (rejection sampling, no modulo bias).
func NewCode() (string, error) {
	const space = 1_000_000
	// The largest multiple of space that fits in a uint32: values at or above it are redrawn.
	limit := uint32((1 << 32) / space * space)
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("platformauth: random code: %w", err)
		}
		v := binary.BigEndian.Uint32(b[:])
		if v < limit {
			return fmt.Sprintf("%06d", v%space), nil
		}
	}
}

// CodeHash is the stored form of a code: HMAC-SHA256(pepper, purpose|user_id|code), hex. Binding the
// purpose and the user means a code of one letter can never match another letter's row.
func CodeHash(pepper, purpose string, userID int64, code string) string {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(purpose))
	m.Write([]byte{'|'})
	m.Write([]byte(strconv.FormatInt(userID, 10)))
	m.Write([]byte{'|'})
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// CodeMatches compares a typed code with a stored hash in constant time.
func CodeMatches(pepper, purpose string, userID int64, code, stored string) bool {
	want, err := hex.DecodeString(stored)
	if err != nil || len(want) != sha256.Size {
		// Still spend the HMAC so a malformed row is not faster than a wrong code.
		_ = CodeHash(pepper, purpose, userID, code)
		return false
	}
	got, _ := hex.DecodeString(CodeHash(pepper, purpose, userID, code))
	return hmac.Equal(got, want)
}

// NormalizeCode strips the spaces and dashes a person may type ("482 913") and reports whether what
// is left is six digits.
func NormalizeCode(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '-' || r == ' ':
			continue
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	out := b.String()
	return out, len(out) == 6
}

// SpacedCode renders a code as the letter shows it: "482 913".
func SpacedCode(code string) string {
	if len(code) != 6 {
		return code
	}
	return code[:3] + " " + code[3:]
}

// NewSessionID returns a user_session sid: 16 random bytes, 32 hex characters.
func NewSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("platformauth: random sid: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// UnusableHash returns a maker of password hashes nobody can match: a real PBKDF2 hash (the same
// work as any account's) of 32 random bytes that are thrown away. A seeded operator sets a password
// through reset.
func UnusableHash(h *pwhash.PasswordHasher) func() (string, error) {
	return func() (string, error) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("platformauth: random hash: %w", err)
		}
		return h.HashPassword(hex.EncodeToString(b))
	}
}

// wellFormedHash reports whether stored has the shape pwhash writes: PBKDF2$sha512$<n>$<salt>$<key>.
func wellFormedHash(stored string) bool {
	return strings.HasPrefix(stored, "PBKDF2$") && strings.Count(stored, "$") == 4
}

// CheckPepper refuses an empty or short pepper.
func CheckPepper(p string) error {
	if len(strings.TrimSpace(p)) < minPepperLen {
		return errPepper
	}
	return nil
}
