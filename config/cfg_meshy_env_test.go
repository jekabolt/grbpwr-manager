package config

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMeshyConfigFromEnv proves the BINDINGS, one variable at a time.
//
// viper.AutomaticEnv is deliberately off in this package (see loadConfig), so a key reaches the
// process ONLY through an explicit viper.BindEnv line. A forgotten line does not fail, does not
// log, and does not look different from a correctly-unset optional override: the value simply stays
// at its default while whoever set it in the DigitalOcean dashboard believes it took effect. The
// only way to tell the two apart is to set every variable to a value nothing else would produce and
// insist it arrives — which is what this test does.
//
// The variables themselves are set in the DO DASHBOARD, never in .do/app.yaml: pushing the spec
// deploys prod and overwrites live SECRET values with the empty ones in the file.
func TestMeshyConfigFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	t.Setenv("MESHY_API_KEY", "msy-test-key")
	t.Setenv("MESHY_BASE_URL", "https://meshy.example.test")
	t.Setenv("MESHY_HTTP_TIMEOUT", "45s")
	t.Setenv("MESHY_POLL_INTERVAL", "7s")
	t.Setenv("MESHY_POLL_TIMEOUT", "20m")
	t.Setenv("MESHY_DOWNLOAD_TIMEOUT", "9m")
	t.Setenv("MESHY_CREDIT_USD", "0.025")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "msy-test-key", cfg.Meshy.APIKey,
		"MESHY_API_KEY must reach the config: it is the whole switch, and unbound it reads as "+
			"'3D is not configured' on a deployment where the key was set")
	assert.Equal(t, "https://meshy.example.test", cfg.Meshy.BaseURL)
	assert.Equal(t, 45*time.Second, cfg.Meshy.HTTPTimeout)
	assert.Equal(t, 7*time.Second, cfg.Meshy.PollInterval)
	assert.Equal(t, 20*time.Minute, cfg.Meshy.PollTimeout)
	assert.Equal(t, 9*time.Minute, cfg.Meshy.DownloadTimeout,
		"the download budget is separate from the poll ceiling on purpose — a fetch cut by the "+
			"wait loses an artifact that is already paid for and whose link dies in three days")
	assert.InDelta(t, 0.025, cfg.Meshy.CreditUSD, 1e-9)

	// The values must survive the constructor, not merely land in the struct: a default applied
	// over a configured value would be the same silent failure one layer down.
	c := meshy.New(cfg.Meshy)
	assert.True(t, c.Enabled(), "a configured key must enable the client")
	assert.Equal(t, 7*time.Second, c.PollInterval())
	assert.Equal(t, 20*time.Minute, c.PollTimeout())
	assert.Equal(t, "0.25", c.CostUSD(10).String(), "the configured credit rate must be the one that prices a run")
}

// TestMeshyUnsetIsAnHonestLock is the other half. With no key the client is disabled — and that
// must stay a CLOSED BUTTON rather than a queued run: a 3D run submitted to a provider that cannot
// be called would sit in 'pending' until a sweeper eventually called it abandoned, with the reason
// nowhere on the screen.
func TestMeshyUnsetIsAnHonestLock(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("MESHY_API_KEY", "") // explicit: an empty variable is the same as an absent one

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.Meshy.APIKey)
	assert.False(t, meshy.New(cfg.Meshy).Enabled(),
		"no key => disabled, so StartRun can refuse a 3D run instead of queueing one nobody can execute")
}
