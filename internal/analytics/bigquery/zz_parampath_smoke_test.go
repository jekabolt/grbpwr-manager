package bigquery

import (
	"context"
	"os"
	"testing"
	"time"
)

// Param-path coverage guard: exercises a broad spread of Get* methods on the
// PARAMETERIZED path (UseLiteralDates:false) against live BigQuery, confirming every
// query binds its @start_date/@end_date (and other) params. This is the only test that
// covers all queries on the parameterized path; it caught a missing start_date/end_date
// binding in the hero-funnel queries. Skips without BigQuery creds (normal CI/dev).
func TestClient_ParamPathSmoke_Integration(t *testing.T) {
	credsPath := findBQCreds(t)
	if credsPath == "" {
		t.Skip("no creds")
	}
	projectID := os.Getenv("BIGQUERY_PROJECT_ID")
	datasetID := os.Getenv("BIGQUERY_DATASET_ID")
	if projectID == "" || datasetID == "" {
		t.Skip("need project/dataset")
	}
	ctx := context.Background()
	c, err := NewClient(ctx, &Config{ProjectID: projectID, DatasetID: datasetID, CredentialsJSON: credsPath /* UseLiteralDates defaults false → parameterized */})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)

	run := func(name string, fn func() error) {
		if err := fn(); err != nil {
			t.Errorf("FAIL %s: %v", name, err)
		} else {
			t.Logf("ok   %s", name)
		}
	}

	run("GetFunnelAnalysis", func() error { _, e := c.GetFunnelAnalysis(ctx, start, end); return e })
	run("GetOOSImpact", func() error { _, e := c.GetOOSImpact(ctx, start, end); return e })
	run("GetDeviceFunnel", func() error { _, e := c.GetDeviceFunnel(ctx, start, end); return e })
	run("GetProductEngagement", func() error { _, e := c.GetProductEngagement(ctx, start, end); return e })
	run("GetFormErrors", func() error { _, e := c.GetFormErrors(ctx, start, end); return e })
	run("GetExceptions", func() error { _, e := c.GetExceptions(ctx, start, end); return e })
	run("Get404Pages", func() error { _, e := c.Get404Pages(ctx, start, end); return e })
	run("GetHeroFunnel", func() error { _, e := c.GetHeroFunnel(ctx, start, end); return e })
	run("GetSizeConfidence", func() error { _, e := c.GetSizeConfidence(ctx, start, end); return e })
	run("GetPaymentRecovery", func() error { _, e := c.GetPaymentRecovery(ctx, start, end); return e })
	run("GetCheckoutTimings", func() error { _, e := c.GetCheckoutTimings(ctx, start, end); return e })
	run("GetAddToCartRate", func() error { _, e := c.GetAddToCartRate(ctx, start, end); return e })
	run("GetBrowserBreakdown", func() error { _, e := c.GetBrowserBreakdown(ctx, start, end); return e })
	run("GetNewsletterSignups", func() error { _, e := c.GetNewsletterSignups(ctx, start, end); return e })
	run("GetAbandonedCart", func() error { _, e := c.GetAbandonedCart(ctx, start, end); return e })
	run("GetTimeOnPage", func() error { _, e := c.GetTimeOnPage(ctx, start, end); return e })
	run("GetProductZoom", func() error { _, e := c.GetProductZoom(ctx, start, end); return e })
	run("GetImageSwipes", func() error { _, e := c.GetImageSwipes(ctx, start, end); return e })
	run("GetSizeGuideClicks", func() error { _, e := c.GetSizeGuideClicks(ctx, start, end); return e })
	run("GetDetailsExpansion", func() error { _, e := c.GetDetailsExpansion(ctx, start, end); return e })
	run("GetNotifyMeIntent", func() error { _, e := c.GetNotifyMeIntent(ctx, start, end); return e })
	run("GetHeroFunnelAggregate", func() error { _, e := c.GetHeroFunnelAggregate(ctx, start, end); return e })
	run("GetUserJourneys", func() error { _, e := c.GetUserJourneys(ctx, start, end, 100); return e })
}
