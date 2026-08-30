package config

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecraftEnvBindings proves the BINDINGS, not a naming coincidence.
//
// viper.AutomaticEnv is deliberately off in this package (see loadConfig), so a key reaches the
// process ONLY through an explicit viper.BindEnv line. Without that line each of these unmarshals
// as its zero value — and a zero value is ALSO exactly what a correctly-unset optional override
// looks like. There is no error, no log line and no visible difference; the only way to tell a
// missing binding from a working one is to set the variable and insist the value arrives.
//
// The failure this guards against is quiet and expensive: a key typed into the DigitalOcean
// dashboard that the process never reads, i.e. a paid feature that stays disabled while every
// setting looks right.
func TestRecraftEnvBindings(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("RECRAFT_ROUTE", "direct")
	t.Setenv("RECRAFT_MODEL_VECTOR", "recraftv4_vector_next")
	t.Setenv("RECRAFT_MODEL_VECTOR_PRO", "recraftv4_pro_vector_next")
	t.Setenv("RECRAFT_API_KEY", "recraft-key")
	t.Setenv("RECRAFT_BASE_URL", "https://stub.invalid/v1")
	t.Setenv("RECRAFT_HTTP_TIMEOUT", "90s")
	t.Setenv("RECRAFT_CREDIT_USD", "0.002")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "direct", cfg.Recraft.Route, "RECRAFT_ROUTE must reach the config")
	assert.Equal(t, "recraftv4_vector_next", cfg.Recraft.ModelVector,
		"RECRAFT_MODEL_VECTOR must reach the config: a rotted provider slug has to be fixable "+
			"with a dashboard variable rather than a deploy")
	assert.Equal(t, "recraftv4_pro_vector_next", cfg.Recraft.ModelVectorPro)
	assert.Equal(t, "recraft-key", cfg.Recraft.Direct.APIKey,
		"RECRAFT_API_KEY is the whole switch of the fallback route")
	assert.Equal(t, "https://stub.invalid/v1", cfg.Recraft.Direct.BaseURL)
	assert.Equal(t, 90*time.Second, cfg.Recraft.Direct.HTTPTimeout)
	assert.InDelta(t, 0.002, cfg.Recraft.Direct.CreditUSD, 1e-9,
		"RECRAFT_CREDIT_USD is the only bridge from provider credits to money in the ledger")

	// The values must not merely land in the struct — they must be what the client actually uses.
	c := recraft.New(cfg.Recraft, nil)
	assert.Equal(t, recraft.RouteDirect, c.Route())
	assert.True(t, c.Enabled(), "a configured direct route must read as enabled")
	assert.Equal(t, "recraftv4_vector_next", c.Model(recraft.TierVector))
	assert.Equal(t, "recraftv4_pro_vector_next", c.Model(recraft.TierProVector))
}

// TestRecraftUnsetFallsBackToTheOpenRouterRoute is the other half, and it is the state EVERY
// deployment is in until somebody types something: no Recraft variables at all.
//
// It must produce the owner's P-5 default — the vector models reached through the shared OpenRouter
// image client — with the two verified slugs, and it must NOT quietly enable the direct route.
func TestRecraftUnsetFallsBackToTheOpenRouterRoute(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.Recraft.Route, "an unset route must stay empty in the config")
	assert.Empty(t, cfg.Recraft.Direct.APIKey)

	c := recraft.New(cfg.Recraft, nil)
	assert.Equal(t, recraft.RouteOpenRouter, c.Route(), "unset => through OpenRouter (P-5)")
	assert.Equal(t, "recraft/recraft-v4-vector", c.Model(recraft.TierVector))
	assert.Equal(t, "recraft/recraft-v4-pro-vector", c.Model(recraft.TierProVector))
	assert.False(t, c.Enabled(), "with no transport wired the button must refuse, not queue a run")
}
