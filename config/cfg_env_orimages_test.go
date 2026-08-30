package config

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests prove the BINDINGS, not a naming coincidence.
//
// viper.AutomaticEnv is deliberately OFF in this package (see loadConfig), so a variable reaches
// the process only through an explicit viper.BindEnv line. Without that line every field below
// unmarshals as its zero value — and every zero value here is ALSO the correct "leave the default
// alone" state, so a missing binding produces no error, no log line and no visible difference. The
// only way to tell the two apart is to set the variable and insist the value arrives.
//
// The failure this prevents is specific and expensive: OPENROUTER_MODEL_IMAGE set in the DO
// dashboard, quietly ignored, and every generation billed against whatever the constant says.

// TestOpenRouterImagesEnvBindings covers each dedicated variable of the image client.
func TestOpenRouterImagesEnvBindings(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_IMAGES_API_KEY", "image-key")
	t.Setenv("OPENROUTER_IMAGES_BASE_URL", "https://proxy.example/api/v1")
	t.Setenv("OPENROUTER_MODEL_IMAGE", "openai/gpt-image-1-mini")
	t.Setenv("OPENROUTER_IMAGES_TIMEOUT", "240s")
	t.Setenv("OPENROUTER_IMAGES_MAX_RESPONSE_BYTES", "8388608") // 8 MiB

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "image-key", cfg.OpenRouterImages.APIKey,
		"OPENROUTER_IMAGES_API_KEY must reach the config; unbound it is silently empty, which reads as 'feature off'")
	assert.Equal(t, "https://proxy.example/api/v1", cfg.OpenRouterImages.BaseURL,
		"OPENROUTER_IMAGES_BASE_URL must reach the config")
	assert.Equal(t, "openai/gpt-image-1-mini", cfg.OpenRouterImages.Model,
		"OPENROUTER_MODEL_IMAGE must reach the config; unbound, every run silently uses the baked-in slug")
	assert.Equal(t, 240*time.Second, cfg.OpenRouterImages.HTTPTimeout,
		"OPENROUTER_IMAGES_TIMEOUT must reach the config; a timeout shorter than the work bills for pictures nobody receives")
	assert.Equal(t, int64(8<<20), cfg.OpenRouterImages.MaxResponseBytes,
		"OPENROUTER_IMAGES_MAX_RESPONSE_BYTES must reach the config; it is the OOM guard on a 0.5 GiB box")

	// And the values must survive into the client, not just into the struct.
	c := orimages.New(cfg.OpenRouterImages)
	assert.True(t, c.Enabled())
	assert.Equal(t, "openai/gpt-image-1-mini", c.Model())
	assert.Equal(t, "https://proxy.example/api/v1", c.BaseURL())
	assert.Equal(t, int64(8<<20), c.MaxResponseBytes())
}

// TestOpenRouterImagesFallsBackToTheChatAccount is the other half of the two-name binding: it is
// ONE OpenRouter account, so a deployment that already has text AI working must get pictures with
// no new secret at all. If the fallback name were dropped from the BindEnv list, image generation
// would be silently disabled on both beta and prod — an unset key looks exactly like "off".
func TestOpenRouterImagesFallsBackToTheChatAccount(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_API_KEY", "shared-account-key")
	t.Setenv("OPENROUTER_BASE_URL", "https://shared.example/api/v1")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "shared-account-key", cfg.OpenRouterImages.APIKey,
		"with no image-specific key the images client must inherit the chat account's key")
	assert.Equal(t, "https://shared.example/api/v1", cfg.OpenRouterImages.BaseURL,
		"with no image-specific root the images client must inherit the chat root")

	c := orimages.New(cfg.OpenRouterImages)
	assert.True(t, c.Enabled(), "an existing OPENROUTER_API_KEY is enough to turn generation on")
	assert.Equal(t, orimages.DefaultModel, c.Model(),
		"an unset OPENROUTER_MODEL_IMAGE must resolve to the package default (gpt-image-2)")
}

// TestOpenRouterImagesDedicatedKeyWinsOverTheSharedOne pins the ORDER of the two names. viper takes
// the first variable of the list that is set, and the point of the dedicated name is to be able to
// move picture spend onto its own key later; if the order were reversed, setting it would do
// nothing and the spend would stay where it was.
func TestOpenRouterImagesDedicatedKeyWinsOverTheSharedOne(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_API_KEY", "shared-account-key")
	t.Setenv("OPENROUTER_IMAGES_API_KEY", "dedicated-image-key")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "dedicated-image-key", cfg.OpenRouterImages.APIKey,
		"the image-specific key must take precedence over the shared one")
	assert.Equal(t, "shared-account-key", cfg.OpenRouter.APIKey,
		"and the chat client must be unaffected by the image key")
}

// TestOpenRouterImagesUnsetIsAnHonestOff: with nothing set, the client is disabled rather than
// half-configured. That matters upstream — an unset key must present as a locked button, not as a
// run that waits for ever on a provider nobody can call.
func TestOpenRouterImagesUnsetIsAnHonestOff(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_IMAGES_API_KEY", "")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.OpenRouterImages.APIKey)
	assert.False(t, orimages.New(cfg.OpenRouterImages).Enabled(),
		"no key anywhere => the image client is disabled")
}
