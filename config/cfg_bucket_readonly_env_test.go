package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// BUCKET_S3_READONLY_HOSTS is optional: unset leaves the field empty (today's behaviour), set reaches
// the bucket config verbatim (the bucket parses and validates it).
func TestBucketReadOnlyHostsFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Empty(t, cfg.Bucket.ReadOnlyHosts)
	require.False(t, cfg.Bucket.TeamsMode)

	t.Setenv("BUCKET_S3_READONLY_HOSTS", "files.grbpwr.com")
	cfg, err = LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, "files.grbpwr.com", cfg.Bucket.ReadOnlyHosts)
}

// A read-only host that is also our CDN host, or a read-only bucket that is our write bucket, stops
// the boot with the variable and the host named. Unset = no new requirement (covered above).
// loadConfig skips Validate under go test, so the test calls it on the loaded config.
func TestBucketReadOnlyOverlapRefusesBoot(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("PATTERN_TOKEN_PEPPER", "test-pepper")
	t.Setenv("PATTERN_TOKEN_PUBLIC_BASE_URL", "https://backend.example")
	if os.Getenv("MYSQL_DSN") == "" {
		t.Setenv("MYSQL_DSN", "u:p@tcp(h:3306)/grbpwr_beta?parseTime=true")
	}
	load := func() error {
		cfg, err := LoadConfig("")
		require.NoError(t, err)
		return cfg.Validate()
	}
	require.NoError(t, load(), "no read-only variables: validation as before")
	t.Setenv("BUCKET_SUBDOMAIN_ENDPOINT", "files.lekalo.test")
	t.Setenv("BUCKET_S3_ENDPOINT", "fra1.digitaloceanspaces.com")
	t.Setenv("BUCKET_S3_BUCKET_NAME", "lekalo")

	t.Setenv("BUCKET_S3_READONLY_HOSTS", "files.grbpwr.com,files.lekalo.test")
	err := load()
	require.ErrorContains(t, err, "BUCKET_S3_READONLY_HOSTS")
	require.ErrorContains(t, err, "files.lekalo.test")

	t.Setenv("BUCKET_S3_READONLY_HOSTS", "files.grbpwr.com")
	t.Setenv("BUCKET_S3_READONLY_BUCKETS", "fra1.digitaloceanspaces.com/LEKALO")
	err = load()
	require.ErrorContains(t, err, "BUCKET_S3_READONLY_BUCKETS")
	require.ErrorContains(t, err, "fra1.digitaloceanspaces.com/lekalo")

	t.Setenv("BUCKET_S3_READONLY_BUCKETS", "fra1.digitaloceanspaces.com/grbpwr")
	require.NoError(t, load())
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, "fra1.digitaloceanspaces.com/grbpwr", cfg.Bucket.ReadOnlyBuckets)
}
