package bigquery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}

func TestNewClient_NilConfig(t *testing.T) {
	ctx := context.Background()
	client, err := NewClient(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, client)
}

func TestNewClient_NotConfigured_MissingProjectID(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		ProjectID: "",
		DatasetID: "ds",
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	assert.Nil(t, client)
}

func TestNewClient_NotConfigured_MissingDatasetID(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		ProjectID: "proj",
		DatasetID: "",
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	assert.Nil(t, client)
}

func TestNewClient_CredentialsFileNotFound(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{
		ProjectID:       "proj",
		DatasetID:       "ds",
		CredentialsJSON: "/nonexistent/path/to/creds.json",
	}
	client, err := NewClient(ctx, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "credentials file not found")
}

func TestClient_TableRef(t *testing.T) {
	c := &Client{
		projectID: "my-proj",
		datasetID: "analytics_123",
	}

	assert.Equal(t, "`my-proj.analytics_123.events_*`", c.tableRef())
}

func TestNeedsIntraday(t *testing.T) {
	today := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC).Truncate(24 * time.Hour)
	c := &Client{
		now: func() time.Time { return today },
	}

	assert.True(t, c.needsIntraday(today), "endDate == today should include intraday")
	assert.True(t, c.needsIntraday(today.Add(24*time.Hour)), "endDate in the future should include intraday")
	assert.False(t, c.needsIntraday(today.Add(-24*time.Hour)), "endDate yesterday should skip intraday")
}

func TestClient_DateFilterSQL(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 7, 0, 0, 0, 0, time.UTC)
	c := &Client{projectID: "p", datasetID: "d"}
	assert.Contains(t, c.dateFilterSQL(start, end), "TIMESTAMP_MICROS")
	assert.NotContains(t, c.dateFilterSQL(start, end), "_TABLE_SUFFIX")
}

func TestClient_EventsSourceColumns(t *testing.T) {
	today := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
	c := &Client{
		projectID: "my-proj",
		datasetID: "analytics_123",
		now:       func() time.Time { return today },
	}
	yesterday := today.Add(-24 * time.Hour)
	lastWeek := today.Add(-7 * 24 * time.Hour)

	t.Run("historical only — no intraday, no UNION ALL", func(t *testing.T) {
		src, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name", "event_timestamp")
		require.NoError(t, err)
		assert.Contains(t, src, "SELECT event_name, event_timestamp FROM")
		assert.NotContains(t, src, "SELECT * FROM")
		assert.Contains(t, src, "_TABLE_SUFFIX BETWEEN")
		assert.NotContains(t, src, "UNION ALL")
		assert.NotContains(t, src, "intraday_")
		assert.Equal(t, 1, countOccurrences(src, "_TABLE_SUFFIX BETWEEN"))
	})

	t.Run("includes today — adds intraday suffix filter via OR", func(t *testing.T) {
		src, err := c.eventsSourceColumns(lastWeek, today, "event_name", "event_timestamp")
		require.NoError(t, err)
		assert.Contains(t, src, "SELECT event_name, event_timestamp FROM")
		assert.NotContains(t, src, "UNION ALL", "single wildcard, no UNION ALL")
		assert.Contains(t, src, "intraday_")
		assert.Equal(t, 2, countOccurrences(src, "_TABLE_SUFFIX BETWEEN"),
			"daily + intraday suffix filters")
		assert.Contains(t, src, "OR")
	})

	t.Run("no columns falls back to SELECT *", func(t *testing.T) {
		src, err := c.eventsSourceColumns(lastWeek, yesterday)
		require.NoError(t, err)
		assert.Contains(t, src, "SELECT * FROM")
	})

	t.Run("nested columns preserved", func(t *testing.T) {
		src, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name", "device.category", "geo.country")
		require.NoError(t, err)
		assert.Contains(t, src, "SELECT event_name, device.category, geo.country FROM")
	})

	t.Run("AS clause columns work", func(t *testing.T) {
		src, err := c.eventsSourceColumns(lastWeek, yesterday, "device.category AS device_category")
		require.NoError(t, err)
		assert.Contains(t, src, "SELECT device.category AS device_category FROM")
	})

	t.Run("SQL injection attempts blocked - semicolon", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name; DROP TABLE events")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - comment", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name--")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - union", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name UNION SELECT")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - select keyword", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name FROM select")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - parentheses", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name()")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - cast", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "CAST(event_name AS STRING)")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - multi-line comment", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name /* comment */")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - alias with dot", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "device.category AS device.cat")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - starting with number", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "1event_name")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - special chars", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event@name")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("SQL injection attempts blocked - double dash comment", func(t *testing.T) {
		_, err := c.eventsSourceColumns(lastWeek, yesterday, "event_name -- comment")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})
}

// findBQCreds searches for GCP credentials for BigQuery.
// Checks config/creds for .json files (same as GA4) or BIGQUERY_CREDENTIALS_JSON env.
func findBQCreds(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("BIGQUERY_CREDENTIALS_JSON"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		// Inline JSON — pass through; NewClient uses WithCredentialsJSON when it starts with {
		if len(p) > 0 && p[0] == '{' {
			return p
		}
	}
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

func TestNewClient_WithCreds_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BIGQUERY_CREDENTIALS_JSON or config/creds/*.json not found - skipping BigQuery integration test")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       os.Getenv("BIGQUERY_PROJECT_ID"),
		DatasetID:       os.Getenv("BIGQUERY_DATASET_ID"),
		CredentialsJSON: credsPath,
	}
	if cfg.ProjectID == "" || cfg.DatasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required for integration test")
	}

	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NotNil(t, client.client)
	defer client.Close()
}

func TestClient_GetFunnelAnalysis_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -2)

	rows, err := client.GetFunnelAnalysis(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetFunnelAnalysis returned %d rows", len(rows))
}

func TestClient_GetOOSImpact_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetOOSImpact(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetOOSImpact returned %d rows", len(rows))
}

func TestClient_GetPaymentFailures_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetPaymentFailures(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetPaymentFailures returned %d rows", len(rows))
}

func TestClient_GetWebVitals_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
		UseLiteralDates: true,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetWebVitals(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetWebVitals returned %d rows", len(rows))

	nonZeroCount := 0
	for _, r := range rows {
		if r.AvgMetricValue > 0 {
			nonZeroCount++
			t.Logf("Sample: date=%s metric=%s rating=%s sessions=%d avg=%.2f",
				r.Date.Format("2006-01-02"), r.MetricName, r.MetricRating, r.SessionCount, r.AvgMetricValue)
		}
	}
	if len(rows) > 0 {
		t.Logf("Found %d/%d rows with non-zero avg_metric_value", nonZeroCount, len(rows))
		assert.Greater(t, nonZeroCount, 0, "Expected at least some rows with non-zero avg_metric_value (LCP/FCP/TTFB should always be >0)")
	}
}

func TestClient_GetUserJourneys_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetUserJourneys(ctx, start, end, 20)
	require.NoError(t, err)
	t.Logf("GetUserJourneys returned %d rows", len(rows))
}

func TestClient_GetSessionDuration_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetSessionDuration(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetSessionDuration returned %d rows", len(rows))
}

func TestClient_GetSizeIntent_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("BigQuery credentials not found - skipping integration test")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("BIGQUERY_PROJECT_ID and BIGQUERY_DATASET_ID required")
	}

	ctx := context.Background()
	cfg := &Config{
		ProjectID:       projectID,
		DatasetID:       datasetID,
		CredentialsJSON: credsPath,
	}
	client, err := NewClient(ctx, cfg)
	require.NoError(t, err)
	defer client.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	rows, err := client.GetSizeIntent(ctx, start, end)
	require.NoError(t, err)
	t.Logf("GetSizeIntent returned %d rows", len(rows))
}
