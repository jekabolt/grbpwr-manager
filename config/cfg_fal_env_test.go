package config

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFalConfigFromEnv proves the BINDINGS, one variable at a time.
//
// viper.AutomaticEnv is deliberately off in this package, so a key reaches the process ONLY through
// an explicit viper.BindEnv line. A forgotten line does not fail, does not log, and looks exactly
// like a correctly-unset optional override: the value stays at its default while whoever set it in
// the DigitalOcean dashboard believes it took effect. The only way to tell those two apart is to
// set every variable to a value nothing else would produce and insist it arrives.
//
// The variables are set in the DO DASHBOARD, never in .do/app.yaml: pushing the spec deploys prod
// and overwrites live SECRET values with the empty ones in the file.
func TestFalConfigFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	t.Setenv("FAL_KEY", "fal-test-key")
	t.Setenv("FAL_BASE_URL", "https://fal.example.test")
	t.Setenv("FAL_MODEL_3D", "vendor/model/v9/multi-view-to-3d")
	t.Setenv("FAL_HTTP_TIMEOUT", "45s")
	t.Setenv("FAL_POLL_INTERVAL", "7s")
	t.Setenv("FAL_POLL_TIMEOUT", "20m")
	t.Setenv("FAL_DOWNLOAD_TIMEOUT", "9m")
	t.Setenv("FAL_UNIT_USD", "0.75")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "fal-test-key", cfg.Fal.APIKey,
		"FAL_KEY must reach the config: it is the whole switch, and unbound it reads as "+
			"'3D is not configured' on a deployment where the key WAS set")
	assert.Equal(t, "https://fal.example.test", cfg.Fal.BaseURL)
	assert.Equal(t, "vendor/model/v9/multi-view-to-3d", cfg.Fal.Model3D,
		"the slug must be overridable without a deploy: a retired one is a 404 on every press")
	assert.Equal(t, 45*time.Second, cfg.Fal.HTTPTimeout)
	assert.Equal(t, 7*time.Second, cfg.Fal.PollInterval)
	assert.Equal(t, 20*time.Minute, cfg.Fal.PollTimeout)
	assert.Equal(t, 9*time.Minute, cfg.Fal.DownloadTimeout,
		"the download budget is separate from the poll ceiling on purpose — a fetch cut by the "+
			"wait loses an artifact that is already paid for and whose link expires")
	assert.InDelta(t, 0.75, cfg.Fal.UnitUSD, 1e-9)

	// THE VALUES MUST SURVIVE THE CONSTRUCTOR, not merely land in the struct: a default applied
	// over a configured value is the same silent failure one layer down.
	c := fal.New(cfg.Fal)
	assert.True(t, c.Enabled())
	assert.Equal(t, 7*time.Second, c.PollInterval())
	assert.Equal(t, 20*time.Minute, c.PollTimeout())
	assert.Equal(t, "vendor/model/v9/multi-view-to-3d", c.Model())
	assert.Equal(t, "1.5", c.CostUSD(2).String(),
		"the configured unit rate must be the one that prices a build")
}

// TestFalUnsetIsAnHonestLock is the other half. With no key the client is disabled — and that must
// stay a CLOSED BUTTON that NAMES THE VARIABLE, not a queued run: a 3D run submitted to a provider
// nobody can call would sit in `pending` until a sweeper eventually called it abandoned, with the
// reason nowhere on the screen.
func TestFalUnsetIsAnHonestLock(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("FAL_KEY", "") // explicit: an empty variable is the same as an absent one

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.Fal.APIKey)
	assert.False(t, fal.New(cfg.Fal).Enabled())
	assert.Contains(t, fal.ErrNotConfigured.Error(), "FAL_KEY",
		"the refusal a person reads has to name the setting they can act on")
}

// TestTheThreedRouteIsChosenByAWORD_AND_DEFAULTS_TO_FAL.
//
// ⚠ THIS SETTING DECIDES WHO GETS PAID for a turntable, so it is an explicit word rather than an
// inference from which key happens to be present — a rule like «use fal if FAL_KEY is set» would
// move the owner's money between two vendors as a side effect of typing a key into a dashboard.
//
// The default is `fal` because the owner named that provider. An unknown word normalises to the
// default rather than refusing the boot (a typo in a route name must not take the backend down) and
// app.go logs the effective route on every start.
func TestTheThreedRouteIsChosenByAWORD_AND_DEFAULTS_TO_FAL(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderFal, effectiveThreedProvider(cfg.DesignGen),
		"unset must mean the provider the owner asked for by name")

	t.Setenv("DESIGN_THREED_PROVIDER", "meshy")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderMeshy, effectiveThreedProvider(cfg.DesignGen))

	t.Setenv("DESIGN_THREED_PROVIDER", "MESHY")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderMeshy, effectiveThreedProvider(cfg.DesignGen),
		"an operator typing the word in capitals meant the same vendor")

	t.Setenv("DESIGN_THREED_PROVIDER", "notaprovider")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, designgen.ThreedProviderFal, effectiveThreedProvider(cfg.DesignGen),
		"a typo falls back to the default; it must not take the boot down and must not be silent — "+
			"app.go logs the route it wired")
}

// effectiveThreedProvider runs the config through the SAME normalisation app.go runs before it
// picks a route — where the word is lower-cased and an unknown one falls back. Asking the raw
// struct would test viper rather than the behaviour, and would have missed the very defect this
// helper was written after: app.go once compared the RAW value, so `MESHY` wired fal in silence.
func effectiveThreedProvider(c designgen.Config) string {
	designgen.Normalize(&c)
	return c.ThreedProvider
}
