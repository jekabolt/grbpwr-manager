package config

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
)

// TestAIKeysMasterKeyFromEnv proves the BINDING of AI_KEYS_MASTER_KEY.
//
// viper.AutomaticEnv is deliberately off in this package, so the variable reaches the process ONLY
// through its explicit viper.BindEnv line. Without it the master key set in the DigitalOcean console
// reads as empty — which is also what a correctly-unset key looks like: the panel would refuse to
// store keys "because AI_KEYS_MASTER_KEY is not set" on a deployment where it WAS set, and nothing
// would fail, log or differ visibly. The only way to tell those apart is to set it and insist it
// arrives.
//
// The value is set in the DO CONSOLE, never pushed from .do/app.yaml: applying the spec overwrites
// the live SECRET with the empty one in the file, and every stored key becomes unreadable at once.
//
// MUTATION: delete `viper.BindEnv("ai.keys_master_key", "AI_KEYS_MASTER_KEY")` → red.
func TestAIKeysMasterKeyFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	master := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	t.Setenv("AI_KEYS_MASTER_KEY", master)

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, master, cfg.AI.KeysMasterKey,
		"AI_KEYS_MASTER_KEY must reach the config: unbound, the panel says 'not set' on a deployment where it was")

	// THE VALUE MUST SURVIVE THE CONSTRUCTOR, not merely land in the struct.
	ring, err := keyring.New(cfg.AI.KeysMasterKey)
	require.NoError(t, err)
	assert.True(t, ring.Enabled())
}

// TestAIKeysMasterKeyUnsetIsAnHonestOff — no variable is a disabled ring, not a boot failure: every
// provider keeps running on its own env key exactly as before this section existed.
//
// MUTATION: bind the key to a default ("x") via viper.SetDefault → red (unset no longer reads empty).
func TestAIKeysMasterKeyUnsetIsAnHonestOff(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("AI_KEYS_MASTER_KEY", "") // explicit: an empty variable is the same as an absent one

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Empty(t, cfg.AI.KeysMasterKey)

	ring, err := keyring.New(cfg.AI.KeysMasterKey)
	require.NoError(t, err, "an unset master key must not stop the process")
	assert.False(t, ring.Enabled())
	_, err = ring.Seal("k", keyring.AAD("openai", "api"))
	assert.ErrorIs(t, err, keyring.ErrNoMasterKey)
}

// TestAIConfigStringRedactsTheMasterKey — %v / %+v / %s of the section, or of the whole Config
// through its field, never print the key; an unset key stays visibly unset.
//
// MUTATION: String prints c.KeysMasterKey instead of the redaction mark → red.
func TestAIConfigStringRedactsTheMasterKey(t *testing.T) {
	const master = "c2VjcmV0LW1hc3Rlci1rZXktdGhhdC1tdXN0LW5vdC1sZWFr"
	c := AIConfig{KeysMasterKey: master}
	for _, verb := range []string{"%v", "%+v", "%s"} {
		out := fmt.Sprintf(verb, c)
		assert.NotContains(t, out, master, verb)
		assert.Contains(t, out, "***REDACTED***", verb)
	}
	assert.NotContains(t, fmt.Sprintf("%+v", Config{AI: c}), master, "nested in Config, too")
	assert.Equal(t, "config.AIConfig{KeysMasterKey:}", AIConfig{}.String())
}
