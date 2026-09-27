// Package keyring seals provider API keys for storage in the database (ai_provider.api_key_enc /
// admin_key_enc) and opens them again, with AES-256-GCM from the standard library and ONE master
// key that never touches the database: AI_KEYS_MASTER_KEY, 32 random bytes in standard base64
// (`openssl rand -base64 32`), set as a SECRET in the DigitalOcean console per environment.
//
// Why a master key at all: a database dump — a backup, a support export, a read replica with wider
// access — must not be a list of working provider keys. With the master key only in the process
// environment, the dump holds ciphertext.
//
// Why the AAD: every blob is bound to its row and column (AAD = provider_key + ":" + kind), so a
// ciphertext copied from the openai row into the anthropic row, or from the api column into the
// admin one, does not open — the swap is an error, never a key silently used for the wrong account.
//
// NOTHING IN THIS PACKAGE LOGS, and no error it returns carries a key, a plaintext or the master.
package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrNoMasterKey — the ring was built from an empty AI_KEYS_MASTER_KEY: nothing can be sealed, and a
// stored blob cannot be opened. The registry reads it as "keys come from env only" and the key
// write RPC refuses with FailedPrecondition naming the variable.
var ErrNoMasterKey = errors.New("AI_KEYS_MASTER_KEY is not set")

// masterKeyLen — AES-256.
const masterKeyLen = 32

// nonceLen — the standard GCM nonce. A fresh random one per Seal; with 96 random bits the collision
// bound is far beyond the handful of key writes this table will ever see.
const nonceLen = 12

// Ring seals and opens with one master key. A Ring built from an empty master is valid and
// disabled; so is a nil *Ring.
type Ring struct {
	aead cipher.AEAD // nil = no master key
}

// New builds a ring from the base64 master key. Surrounding whitespace is ignored (a value pasted
// into a console often carries a newline); padding is optional. "" → a disabled ring, no error.
// A value that is not base64 or does not decode to exactly 32 bytes → an error that names the
// variable and never the value.
func New(masterBase64 string) (*Ring, error) {
	s := strings.TrimSpace(masterBase64)
	if s == "" {
		return &Ring{}, nil
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, errors.New("AI_KEYS_MASTER_KEY is not valid standard base64 (generate one with `openssl rand -base64 32`)")
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("AI_KEYS_MASTER_KEY must decode to %d bytes, got %d (generate one with `openssl rand -base64 32`)", masterKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AI_KEYS_MASTER_KEY: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AI_KEYS_MASTER_KEY: %w", err)
	}
	clear(key)
	return &Ring{aead: aead}, nil
}

// Enabled reports whether the ring holds a master key.
func (r *Ring) Enabled() bool { return r != nil && r.aead != nil }

// String and GoString keep %v / %+v / %#v of a Ring from ever reaching into the cipher state.
func (r *Ring) String() string {
	if r.Enabled() {
		return "keyring.Ring{master:set}"
	}
	return "keyring.Ring{master:unset}"
}

// GoString — see String.
func (r *Ring) GoString() string { return r.String() }

// AAD is the additional authenticated data that binds a blob to its row and column:
// providerKey + ":" + kind (kind = entity.AIKeyAPI | entity.AIKeyAdmin).
func AAD(providerKey string, kind string) string {
	return providerKey + ":" + kind
}

// Seal returns nonce(12) ‖ AES-256-GCM(plain) authenticated with aad.
func (r *Ring) Seal(plain string, aad string) ([]byte, error) {
	if !r.Enabled() {
		return nil, ErrNoMasterKey
	}
	nonce := make([]byte, nonceLen, nonceLen+len(plain)+r.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyring: nonce: %w", err)
	}
	return r.aead.Seal(nonce, nonce, []byte(plain), []byte(aad)), nil
}

// Open reverses Seal. A wrong master key, a wrong aad, a truncated or tampered blob all fail GCM
// authentication and return ("", error) — never a partial plaintext.
func (r *Ring) Open(blob []byte, aad string) (string, error) {
	if !r.Enabled() {
		return "", ErrNoMasterKey
	}
	if len(blob) < nonceLen+r.aead.Overhead() {
		return "", errors.New("keyring: sealed key is too short")
	}
	plain, err := r.aead.Open(nil, blob[:nonceLen], blob[nonceLen:], []byte(aad))
	if err != nil {
		// The GCM error is "message authentication failed": it carries no byte of the input.
		return "", fmt.Errorf("keyring: cannot open sealed key for %q: %w", aad, err)
	}
	return string(plain), nil
}

// Last4 returns the last four characters of s for display ("set ···1a2b"), "" when s is shorter
// than four. Counted in runes, so a multi-byte tail is never cut in half.
func Last4(s string) string {
	r := []rune(s)
	if len(r) < 4 {
		return ""
	}
	return string(r[len(r)-4:])
}
