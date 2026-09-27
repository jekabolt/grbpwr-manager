package keyring

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// masterOf returns a base64 master key whose 32 bytes start at `from` — two different `from`s give
// two different keys, and nothing here depends on randomness.
func masterOf(from byte) string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = from + byte(i)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func mustRing(t *testing.T, master string) *Ring {
	t.Helper()
	r, err := New(master)
	require.NoError(t, err)
	require.True(t, r.Enabled())
	return r
}

const secret = "sk-test-0123456789abcdef-1a2b"

// TestKeyringRoundTrip — what Seal writes, Open reads back, bound to the row's AAD; the layout is
// nonce(12) ‖ ciphertext ‖ tag(16), and two seals of one key differ (a fresh nonce each time).
//
// MUTATION: Open reads the nonce from blob[1:13] instead of blob[:12] → red.
// MUTATION: Seal reuses a zero nonce (drop rand.Read) → the "two seals differ" assertion goes red.
func TestKeyringRoundTrip(t *testing.T) {
	r := mustRing(t, masterOf(0))
	aad := AAD("openai", "api")
	require.Equal(t, "openai:api", aad)

	blob, err := r.Seal(secret, aad)
	require.NoError(t, err)
	require.Len(t, blob, 12+len(secret)+16)
	require.False(t, bytes.Contains(blob, []byte(secret)), "the plaintext is not in the blob")

	got, err := r.Open(blob, aad)
	require.NoError(t, err)
	require.Equal(t, secret, got)

	again, err := r.Seal(secret, aad)
	require.NoError(t, err)
	require.NotEqual(t, blob, again, "a fresh random nonce per seal")

	// A ring rebuilt from the same master (the next boot) opens what this one sealed.
	got, err = mustRing(t, masterOf(0)).Open(blob, aad)
	require.NoError(t, err)
	require.Equal(t, secret, got)
}

// TestKeyringWrongAADFails — a blob moved to another provider's row, or from the api column to the
// admin one, does not open.
//
// MUTATION: Seal and Open pass nil instead of []byte(aad) → red.
func TestKeyringWrongAADFails(t *testing.T) {
	r := mustRing(t, masterOf(0))
	blob, err := r.Seal(secret, AAD("openai", "api"))
	require.NoError(t, err)

	for _, aad := range []string{AAD("anthropic", "api"), AAD("openai", "admin"), "", "openai:api "} {
		got, err := r.Open(blob, aad)
		require.Error(t, err, "aad %q", aad)
		require.Empty(t, got)
	}
}

// TestKeyringWrongKeyFails — another environment's master key (beta vs prod) cannot read the blob.
//
// MUTATION: New ignores its argument and derives the key from a constant → red.
func TestKeyringWrongKeyFails(t *testing.T) {
	blob, err := mustRing(t, masterOf(0)).Seal(secret, AAD("fal", "api"))
	require.NoError(t, err)

	got, err := mustRing(t, masterOf(100)).Open(blob, AAD("fal", "api"))
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), secret)
}

// TestKeyringTamperedByteFails — flipping ANY one byte (nonce, ciphertext or tag) or cutting the
// blob fails authentication; Open never returns a partial plaintext.
//
// MUTATION: Open swallows the GCM authentication error (returns the decrypt result regardless) →
// red: every flipped byte would "open".
func TestKeyringTamperedByteFails(t *testing.T) {
	r := mustRing(t, masterOf(0))
	aad := AAD("meshy", "api")
	blob, err := r.Seal(secret, aad)
	require.NoError(t, err)

	for i := range blob {
		bad := append([]byte(nil), blob...)
		bad[i] ^= 0x01
		got, err := r.Open(bad, aad)
		require.Error(t, err, "byte %d flipped", i)
		require.Empty(t, got, "byte %d flipped", i)
	}
	for _, n := range []int{0, 1, 11, 12, 27, len(blob) - 1} {
		got, err := r.Open(blob[:n], aad)
		require.Error(t, err, "cut to %d", n)
		require.Empty(t, got)
	}
	got, err := r.Open(append(append([]byte(nil), blob...), 0), aad)
	require.Error(t, err, "a byte appended")
	require.Empty(t, got)
}

// TestKeyringEmptyMasterKey — no master key is a state, not a crash: the ring is built, reports
// disabled, and BOTH directions refuse with ErrNoMasterKey (the registry falls back to env; the key
// write RPC names the variable). A nil ring behaves the same.
//
// MUTATION: New("") returns an error → red. MUTATION: drop the !Enabled guard in Open → red (nil
// aead dereference) — the Open half is what the brief names separately.
func TestKeyringEmptyMasterKey(t *testing.T) {
	for _, master := range []string{"", "   ", "\n"} {
		r, err := New(master)
		require.NoError(t, err, "master %q", master)
		require.False(t, r.Enabled())

		_, err = r.Seal(secret, AAD("openai", "api"))
		require.ErrorIs(t, err, ErrNoMasterKey)

		sealed, err := mustRing(t, masterOf(0)).Seal(secret, AAD("openai", "api"))
		require.NoError(t, err)
		got, err := r.Open(sealed, AAD("openai", "api"))
		require.ErrorIs(t, err, ErrNoMasterKey)
		require.Empty(t, got)
	}

	var nilRing *Ring
	require.False(t, nilRing.Enabled())
	_, err := nilRing.Seal(secret, "x:api")
	require.ErrorIs(t, err, ErrNoMasterKey)
	_, err = nilRing.Open([]byte("whatever-long-enough-to-pass-the-length-check"), "x:api")
	require.ErrorIs(t, err, ErrNoMasterKey)
	require.Contains(t, ErrNoMasterKey.Error(), "AI_KEYS_MASTER_KEY", "the refusal names the variable")
}

// TestKeyringBadMasterKey — a master that is not base64 or not 32 bytes is a boot error naming the
// variable and never echoing the value; a pasted trailing newline or missing padding is accepted.
//
// MUTATION: drop the length check (AES accepts 16/24 bytes) → the 16- and 24-byte cases go red.
func TestKeyringBadMasterKey(t *testing.T) {
	short16 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	short24 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 24))
	long33 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 33))
	for _, master := range []string{"not base64 at all!", short16, short24, long33, "%%%%"} {
		r, err := New(master)
		require.Error(t, err, "master %q", master)
		require.Nil(t, r)
		require.Contains(t, err.Error(), "AI_KEYS_MASTER_KEY")
		require.NotContains(t, err.Error(), master, "the error never echoes the value")
	}

	padded := masterOf(0)
	unpadded := strings.TrimRight(padded, "=")
	require.NotEqual(t, padded, unpadded, "precondition: 32 bytes of base64 carry padding")
	blob, err := mustRing(t, padded+"\n").Seal(secret, "a:api")
	require.NoError(t, err)
	got, err := mustRing(t, "  "+unpadded).Open(blob, "a:api")
	require.NoError(t, err)
	require.Equal(t, secret, got, "whitespace and padding do not change the key")
}

// TestKeyringLast4 — the four characters the panel shows ("set ···1a2b"); nothing for shorter keys.
//
// MUTATION: `len(r) < 4` → `len(r) < 3` → red (the "abc" case slices out of range).
func TestKeyringLast4(t *testing.T) {
	require.Equal(t, "", Last4(""))
	require.Equal(t, "", Last4("abc"))
	require.Equal(t, "abcd", Last4("abcd"))
	require.Equal(t, "1a2b", Last4(secret))
	require.Equal(t, "ключ", Last4("мой-ключ"), "runes, not bytes")
}

// TestKeyringNeverPrintsState — %v / %+v / %#v of a ring say only whether a master is set.
//
// MUTATION: delete String/GoString → %+v prints the aead struct → red.
func TestKeyringNeverPrintsState(t *testing.T) {
	r := mustRing(t, masterOf(0))
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		require.Equal(t, "keyring.Ring{master:set}", fmt.Sprintf(verb, r), verb)
	}
	off, err := New("")
	require.NoError(t, err)
	require.Equal(t, "keyring.Ring{master:unset}", fmt.Sprintf("%v", off))
}
