package ga4

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
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

func TestAvgSessionDurationForStorage(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 851.0/39.0, avgSessionDurationForStorage(39, 2213, 851, true), 0.001)
	assert.InDelta(t, 25.3, avgSessionDurationForStorage(10, 25.3, 0, false), 0.001)
	assert.InDelta(t, 0.0, avgSessionDurationForStorage(10, 25.3, 0, true), 0.001)
	assert.InDelta(t, 100.0, avgSessionDurationForStorage(0, 100, 50, false), 0.001)
}

func TestMetricValueToSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		value      string
		headerType string
		want       float64
	}{
		{"milliseconds to seconds", "13635", "TYPE_MILLISECONDS", 13.635},
		{"seconds as-is", "25.3", "TYPE_SECONDS", 25.3},
		{"minutes to seconds", "2.5", "TYPE_MINUTES", 150.0},
		{"hours to seconds", "1.5", "TYPE_HOURS", 5400.0},
		{"no header - assume seconds", "100", "", 100.0},
		{"nil header - assume seconds", "42.5", "", 42.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *analyticsdata.MetricHeader
			if tt.headerType != "" {
				h = &analyticsdata.MetricHeader{Type: tt.headerType}
			}
			got := metricValueToSeconds(tt.value, h)
			assert.InDelta(t, tt.want, got, 0.0001)
		})
	}
}

func TestResolveGA4DailyIndices(t *testing.T) {
	t.Parallel()
	t.Run("with userEngagementDuration", func(t *testing.T) {
		headers := []*analyticsdata.MetricHeader{
			{Name: "sessions"},
			{Name: "totalUsers"},
			{Name: "newUsers"},
			{Name: "screenPageViews"},
			{Name: "bounceRate"},
			{Name: "averageSessionDuration"},
			{Name: "screenPageViewsPerSession"},
			{Name: "userEngagementDuration"},
		}
		idx, hasEngagement, ok := resolveGA4DailyIndices(headers)
		assert.True(t, ok)
		assert.True(t, hasEngagement)
		assert.Equal(t, 0, idx.sessions)
		assert.Equal(t, 7, idx.userEngagementDuration)
	})

	t.Run("without userEngagementDuration", func(t *testing.T) {
		headers := []*analyticsdata.MetricHeader{
			{Name: "sessions"},
			{Name: "totalUsers"},
			{Name: "newUsers"},
			{Name: "screenPageViews"},
			{Name: "bounceRate"},
			{Name: "averageSessionDuration"},
			{Name: "screenPageViewsPerSession"},
		}
		idx, hasEngagement, ok := resolveGA4DailyIndices(headers)
		assert.True(t, ok)
		assert.False(t, hasEngagement)
		assert.Equal(t, 0, idx.sessions)
	})

	t.Run("missing required metric", func(t *testing.T) {
		headers := []*analyticsdata.MetricHeader{
			{Name: "sessions"},
			{Name: "totalUsers"},
		}
		_, _, ok := resolveGA4DailyIndices(headers)
		assert.False(t, ok)
	})

	t.Run("empty headers - use positional fallback", func(t *testing.T) {
		idx, hasEngagement, ok := resolveGA4DailyIndices(nil)
		assert.True(t, ok)
		assert.True(t, hasEngagement)
		assert.Equal(t, 0, idx.sessions)
		assert.Equal(t, 7, idx.userEngagementDuration)
	})
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
