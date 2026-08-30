package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDesignGenerationEnvBindings proves the six bindings of the generation worker, one at a time.
//
// viper.AutomaticEnv is off in this package, so a key reaches the process ONLY through an explicit
// viper.BindEnv line. A forgotten line does not fail, does not log, and is indistinguishable from a
// correctly-unset optional override — the value stays at its default while whoever set it in the
// DigitalOcean dashboard believes it took effect.
//
// For five of these six that would be a wrong timeout. For DESIGN_GENERATION_ENABLED it is worse:
// the flag decides whether the worker is constructed at all, and the handler refuses to open a run
// while it reads false. An unbound flag therefore means the owner switches generation on, sees
// nothing change, and has no error anywhere to explain it.
func TestDesignGenerationEnvBindings(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	t.Setenv("DESIGN_GENERATION_ENABLED", "true")
	t.Setenv("DESIGN_WORKER_INTERVAL", "11s")
	t.Setenv("DESIGN_WORKER_BATCH_SIZE", "7")
	t.Setenv("DESIGN_WORKER_CLAIM_LEASE", "13m")
	t.Setenv("DESIGN_WORKER_RUN_TIMEOUT", "6m")
	t.Setenv("DESIGN_IMAGE_QUALITY", "high")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.True(t, cfg.DesignGen.Enabled,
		"DESIGN_GENERATION_ENABLED must reach the config: unbound, the worker is never built and "+
			"every GENERATE is refused, with nothing anywhere saying why")
	assert.Equal(t, 11*time.Second, cfg.DesignGen.WorkerInterval)
	assert.Equal(t, 7, cfg.DesignGen.BatchSize)
	assert.Equal(t, 13*time.Minute, cfg.DesignGen.ClaimLease)
	assert.Equal(t, 6*time.Minute, cfg.DesignGen.RunTimeout)
	assert.Equal(t, "high", cfg.DesignGen.ImageQuality,
		"DESIGN_IMAGE_QUALITY is bound because it is a MONEY knob: it must move together with the "+
			"handler's price estimate, and a value that silently fails to arrive breaks that pairing")
}

// TestDesignGenerationUnsetIsAnHonestOff pins the other half. The default has to be OFF, because
// prod stands at migration 0339 and has no DESIGN band at all — a binary that started generating
// there would be spending money against tables that do not exist.
func TestDesignGenerationUnsetIsAnHonestOff(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	cfg, err := LoadConfig("")
	require.NoError(t, err)

	assert.False(t, cfg.DesignGen.Enabled,
		"unset must read as off — the feature ships inert and is switched on deliberately")
}
