package config

import (
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// clearDBParts empties every variable the DSN can be assembled from, so a test sees only its own.
func clearDBParts(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MYSQL_DSN", "TENANT_TEAM1_DB",
		"MYSQL_HOST", "MYSQL_PORT", "MYSQL_USER", "MYSQL_PASSWORD", "MYSQL_DATABASE",
		"db.HOSTNAME", "db.PORT", "db.USERNAME", "db.PASSWORD", "db.DATABASE",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
}

// TestTenantFromTLSCustomDSN — beta and prod run on a DSN with tls=custom, and that name is
// registered only inside store.New, after the config is loaded. Loading must not depend on it.
//
// MUTATION: resolveTenant back on mysql.ParseDSN → red ("unknown config name: custom").
func TestTenantFromTLSCustomDSN(t *testing.T) {
	clearDBParts(t)
	t.Setenv("MYSQL_DSN", "u:p@tcp(h:3306)/grbpwr_beta?parseTime=true&tls=custom")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, "grbpwr_beta", cfg.Platform.Team1DB)
	assert.Equal(t, tenant.TeamOne("grbpwr_beta"), cfg.DB.Tenant.Team1)
	assert.False(t, cfg.DB.Tenant.Strict, "PLATFORM_MYSQL_DSN unset: teams mode off")
}

// TestTenantFromDSNParts — the DSN assembled from MYSQL_* carries tls=custom too; a password with
// '/' and '@' does not move the database name.
func TestTenantFromDSNParts(t *testing.T) {
	clearDBParts(t)
	t.Setenv("MYSQL_HOST", "db.example")
	t.Setenv("MYSQL_USER", "doadmin")
	t.Setenv("MYSQL_PASSWORD", "pa/ss@wo:rd")
	t.Setenv("MYSQL_DATABASE", "grbpwr_beta")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Contains(t, cfg.DB.DSN, "tls=custom")
	assert.Equal(t, "grbpwr_beta", cfg.Platform.Team1DB)
}

// TestTenantTeam1DBMustMatchDSN — TENANT_TEAM1_DB equal to the DSN database is accepted, another
// name is refused at load.
func TestTenantTeam1DBMustMatchDSN(t *testing.T) {
	clearDBParts(t)
	t.Setenv("MYSQL_DSN", "u:p@tcp(h:3306)/grbpwr_beta?tls=custom")
	t.Setenv("TENANT_TEAM1_DB", "grbpwr_beta")
	_, err := LoadConfig("")
	require.NoError(t, err)

	t.Setenv("TENANT_TEAM1_DB", "grbpwr")
	_, err = LoadConfig("")
	require.ErrorContains(t, err, "TENANT_TEAM1_DB")
}

// TestDSNDatabaseMatchesDriver — store.DSNDatabase names the same database as the driver for every
// DSN the driver parses without a TLS registry lookup, and accepts the tls=custom forms.
func TestDSNDatabaseMatchesDriver(t *testing.T) {
	for _, dsn := range []string{
		"u:p@tcp(h:3306)/grbpwr_beta?parseTime=true",
		"u:pa/ss@word@tcp(h:3306)/grbpwr?charset=utf8&parseTime=true",
		"u:p@tcp(h:3306)/",
		"u:p@tcp(h:3306)/db",
		"/db?x=1",
		"u@unix(/tmp/mysql.sock)/db",
	} {
		mc, err := mysql.ParseDSN(dsn)
		require.NoError(t, err, dsn)
		got, err := store.DSNDatabase(dsn)
		require.NoError(t, err, dsn)
		assert.Equal(t, mc.DBName, got, dsn)
	}
	for dsn, want := range map[string]string{
		"u:p@tcp(h:3306)/grbpwr_beta?tls=custom":          "grbpwr_beta",
		"u:p/a@ss@tcp(h:3306)/lekalo_beta?tls=custom&x=1": "lekalo_beta",
		"": "",
	} {
		got, err := store.DSNDatabase(dsn)
		require.NoError(t, err, dsn)
		assert.Equal(t, want, got, dsn)
	}
	_, err := store.DSNDatabase("no-slash-at-all")
	require.Error(t, err)
}
