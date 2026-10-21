package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The automatic environment mapper is intentionally disabled, so the retention knob reaches the
// worker only if bindEnvVars names it explicitly.
func TestTaskAutoArchiveRetentionFromEnv(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("TASK_AUTO_ARCHIVE_RETENTION_PERIOD", "504h")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.Equal(t, 21*24*time.Hour, cfg.TaskAutoArchive.RetentionPeriod)
}
