package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// TestPlatformSignupAndTermsEnv — PLATFORM_SIGNUP_APPROVAL and PLATFORM_TERMS_VERSION come through
// the config (not os.Getenv at call time): both optional, approval on and terms 2026-11 by default,
// an unreadable approval keeps it on and never fails the load. Mode OFF (PLATFORM_MYSQL_DSN empty)
// loads with or without them.
func TestPlatformSignupAndTermsEnv(t *testing.T) {
	cases := []struct {
		name, dsn, approval, terms string
		wantApproval               bool
		wantTerms                  string
	}{
		{name: "mode off, unset", wantApproval: true, wantTerms: platformstore.DefaultTermsVersion},
		{name: "mode off, set (ignored by the off binary)", approval: "false", terms: "2027-01", wantApproval: false, wantTerms: "2027-01"},
		{name: "teams, unset", dsn: "u:p@tcp(h:3306)/lekalo_platform?tls=custom", wantApproval: true, wantTerms: "2026-11"},
		{name: "teams, approval off", dsn: "u:p@tcp(h:3306)/lekalo_platform", approval: "off", wantApproval: false, wantTerms: "2026-11"},
		{name: "teams, approval 0", dsn: "u:p@tcp(h:3306)/lekalo_platform", approval: "0", wantApproval: false, wantTerms: "2026-11"},
		{name: "teams, unreadable approval", dsn: "u:p@tcp(h:3306)/lekalo_platform", approval: "junk", wantApproval: true, wantTerms: "2026-11"},
		{name: "teams, terms set", dsn: "u:p@tcp(h:3306)/lekalo_platform", approval: "true", terms: " 2027-02 ", wantApproval: true, wantTerms: "2027-02"},
		{name: "teams, terms too wide", dsn: "u:p@tcp(h:3306)/lekalo_platform", terms: "0123456789012345678901234567890123", wantApproval: true, wantTerms: "2026-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearDBParts(t)
			t.Setenv("PLATFORM_MYSQL_DSN", tc.dsn)
			t.Setenv("PLATFORM_SIGNUP_APPROVAL", tc.approval)
			t.Setenv("PLATFORM_TERMS_VERSION", tc.terms)
			cfg, err := LoadConfig("")
			require.NoError(t, err)
			assert.Equal(t, tc.dsn != "", cfg.Platform.Enabled())
			assert.Equal(t, tc.wantApproval, cfg.Platform.SignupApprovalOn())
			assert.Equal(t, tc.wantTerms, cfg.Platform.Terms())
		})
	}
}

// An explicitly empty PLATFORM_SIGNUP_APPROVAL / PLATFORM_TERMS_VERSION means the built-in default
// (approval on, terms 2026-11), not the config file's value; unset leaves the file's value.
func TestPlatformSignupEmptyEnvMeansDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(file, []byte("[platform]\nsignup_approval = \"false\"\nterms_version = \"2027-05\"\n"), 0o600))
	t.Cleanup(viper.Reset) // the file stays in the global viper otherwise

	load := func(t *testing.T) *Config {
		t.Helper()
		viper.Reset()
		cfg, err := LoadConfig(file)
		require.NoError(t, err)
		return cfg
	}
	t.Run("unset: the file's values", func(t *testing.T) {
		clearDBParts(t)
		unsetEnv(t, "PLATFORM_SIGNUP_APPROVAL")
		unsetEnv(t, "PLATFORM_TERMS_VERSION")
		cfg := load(t)
		assert.False(t, cfg.Platform.SignupApprovalOn())
		assert.Equal(t, "2027-05", cfg.Platform.Terms())
	})
	t.Run("explicitly empty: the defaults", func(t *testing.T) {
		clearDBParts(t)
		t.Setenv("PLATFORM_SIGNUP_APPROVAL", "")
		t.Setenv("PLATFORM_TERMS_VERSION", "")
		cfg := load(t)
		assert.True(t, cfg.Platform.SignupApprovalOn())
		assert.Equal(t, platformstore.DefaultTermsVersion, cfg.Platform.Terms())
	})
	t.Run("set: the variable wins", func(t *testing.T) {
		clearDBParts(t)
		t.Setenv("PLATFORM_SIGNUP_APPROVAL", "true")
		t.Setenv("PLATFORM_TERMS_VERSION", "2027-09")
		cfg := load(t)
		assert.True(t, cfg.Platform.SignupApprovalOn())
		assert.Equal(t, "2027-09", cfg.Platform.Terms())
	})
}

// unsetEnv removes a variable for one test and puts it back after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	require.NoError(t, os.Unsetenv(key))
}
