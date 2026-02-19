package ga4

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPropertyID = "299206603"

// findCredsPath searches for GA4 credentials in config/creds.
// Returns the path to the first .json file found, or empty string if none.
// Uses path relative to repo root (internal/analytics/ga4 -> ../../../config/creds).
func findCredsPath(t *testing.T) string {
	t.Helper()
	credsDir := filepath.Join("..", "..", "..", "config", "creds")
	if _, err := os.Stat(credsDir); os.IsNotExist(err) {
		return ""
	}
	entries, err := os.ReadDir(credsDir)
	if err != nil {
		t.Logf("cannot read config/creds: %v", err)
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			return filepath.Join(credsDir, e.Name())
		}
	}
	return ""
}

func TestNewClient_WithConfigCreds(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.True(t, client.enabled)
	assert.Equal(t, testPropertyID, client.propertyID)
}

func TestClient_GetDailyMetrics_Integration(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	// Request a short date range (e.g. last 3 days)
	endDate := time.Now().AddDate(0, 0, -1) // yesterday
	startDate := endDate.AddDate(0, 0, -2)  // 3 days total

	metrics, err := client.GetDailyMetrics(ctx, startDate, endDate)
	require.NoError(t, err)
	t.Logf("GetDailyMetrics returned %+v rows", metrics)
	// May be empty if no data; we mainly verify the API call succeeds
	t.Logf("GetDailyMetrics returned %d rows for %s-%s", len(metrics),
		startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
}

func TestClient_GetProductPageMetrics_Integration(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	endDate := time.Now().AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -6)

	metrics, err := client.GetProductPageMetrics(ctx, startDate, endDate)
	require.NoError(t, err)
	t.Logf("GetProductPageMetrics returned %d rows", len(metrics))
}

func TestClient_GetTrafficSourceMetrics_Integration(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	endDate := time.Now().AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -6)

	metrics, err := client.GetTrafficSourceMetrics(ctx, startDate, endDate)
	require.NoError(t, err)
	t.Logf("GetTrafficSourceMetrics returned %d rows", len(metrics))
}

func TestClient_GetDeviceMetrics_Integration(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	endDate := time.Now().AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -6)

	metrics, err := client.GetDeviceMetrics(ctx, startDate, endDate)
	require.NoError(t, err)
	t.Logf("GetDeviceMetrics returned %d rows", len(metrics))
}

func TestClient_GetCountryMetrics_Integration(t *testing.T) {
	credsPath := findCredsPath(t)
	if credsPath == "" {
		t.Skip("config/creds/*.json not found - skipping GA4 integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		PropertyID:      testPropertyID,
		CredentialsJSON: credsPath,
		Enabled:         true,
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	endDate := time.Now().AddDate(0, 0, -1)
	startDate := endDate.AddDate(0, 0, -6)

	metrics, err := client.GetCountryMetrics(ctx, startDate, endDate)
	require.NoError(t, err)
	t.Logf("GetCountryMetrics returned %d rows", len(metrics))
}
