package config

import (
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
