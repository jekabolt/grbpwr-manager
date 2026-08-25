package config

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMasterPasswordFromEnv reproduces the DO runtime (no config file, env only)
// and verifies AUTH_MASTER_PASSWORD reaches cfg.Auth.MasterPassword intact —
// i.e. the viper BindEnv->Unmarshal path actually delivers the value.
func TestMasterPasswordFromEnv(t *testing.T) {
	t.Setenv("AUTH_MASTER_PASSWORD", "string")
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("AUTH_PASSWORD_HASHER_SALT_SIZE", "16")
	t.Setenv("AUTH_PASSWORD_HASHER_ITERATIONS", "100000")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "string", cfg.Auth.MasterPassword, "master password must come through from env")
	assert.Equal(t, "test-secret", cfg.Auth.JWTSecret)
	assert.Equal(t, 16, cfg.Auth.PasswordHasherSaltSize)
	assert.Equal(t, 100000, cfg.Auth.PasswordHasherIterations)
}

// TestOpenRouterAnalysisModelFromEnv proves the BINDING, not a naming coincidence.
//
// viper.AutomaticEnv is deliberately off in this package (see loadConfig), so a key reaches the
// process only through an explicit viper.BindEnv line. Without that line OPENROUTER_MODEL_ANALYSIS
// would unmarshal as empty — and empty is ALSO the correct value for "no override configured", so
// the missing binding produces no error, no log line and no visible difference. The only way to
// tell the two apart is to set the variable and insist the value arrives.
func TestOpenRouterAnalysisModelFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_MODEL", "shared/slug")
	t.Setenv("OPENROUTER_MODEL_ANALYSIS", "x/y")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "x/y", cfg.OpenRouter.ModelAnalysis,
		"OPENROUTER_MODEL_ANALYSIS must reach the config: with no explicit viper.BindEnv it is "+
			"silently empty, which looks exactly like a correct unset override")
	assert.Equal(t, "shared/slug", cfg.OpenRouter.Model, "the shared slug must be unaffected")
	assert.Equal(t, "x/y", openrouter.New(cfg.OpenRouter).AnalysisModel(),
		"the override must be the slug the analysis pass actually sends")
}

// TestOpenRouterAnalysisModelUnsetFallsBackToSharedSlug is the other half: with the variable unset
// the analysis pass must run on the shared slug, not on a second baked-in default of its own.
func TestOpenRouterAnalysisModelUnsetFallsBackToSharedSlug(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("OPENROUTER_MODEL", "shared/slug")
	t.Setenv("OPENROUTER_MODEL_ANALYSIS", "") // explicit: an empty variable is the same as an absent one

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.Empty(t, cfg.OpenRouter.ModelAnalysis, "an unset override must stay empty in the config")
	assert.Equal(t, "shared/slug", openrouter.New(cfg.OpenRouter).AnalysisModel(),
		"empty override => the shared slug")
}
