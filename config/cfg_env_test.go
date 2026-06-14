package config

import (
	"testing"

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
