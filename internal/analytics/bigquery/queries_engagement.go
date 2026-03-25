package bigquery

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"google.golang.org/api/iterator"
)

// GetWebVitals queries LCP/INP/CLS/FCP/TTFB events with session conversion correlation.
func (c *Client) GetWebVitals(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.WebVitalMetric, error) {

	var result []entity.WebVitalMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getWebVitals(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getWebVitals(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.WebVitalMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetWebVitals: %w", err)
	}
	df := c.dateFilterSQL(startDate, endDate)

	sql := fmt.Sprintf(`
		WITH base_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				event_name,
				COALESCE(
					(SELECT value.double_value FROM UNNEST(event_params) WHERE key = 'value'),
					(SELECT CAST(value.int_value AS FLOAT64) FROM UNNEST(event_params) WHERE key = 'value')
				) AS metric_value,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'metric_rating') AS metric_rating
			FROM %s
			WHERE %s
				AND (event_name IN ('LCP','INP','CLS','FCP','TTFB') OR event_name = 'purchase')
		),
		vitals AS (
			SELECT
				event_date,
				user_pseudo_id,
				session_id,
				event_name AS metric_name,
				metric_value,
				metric_rating
			FROM base_events
			WHERE event_name IN ('LCP','INP','CLS','FCP','TTFB')
		),
		session_conversions AS (
			SELECT
				event_date,
				user_pseudo_id,
				session_id,
				MAX(CASE WHEN event_name = 'purchase' THEN 1 ELSE 0 END) AS converted
			FROM base_events
			GROUP BY event_date, user_pseudo_id, session_id
		)
		SELECT
			v.event_date,
			v.metric_name,
			v.metric_rating,
			COUNT(DISTINCT CONCAT(v.user_pseudo_id, '-', COALESCE(CAST(v.session_id AS STRING), ''))) AS session_count,
			COUNT(DISTINCT CASE WHEN sc.converted = 1
				THEN CONCAT(v.user_pseudo_id, '-', COALESCE(CAST(v.session_id AS STRING), ''))
			END) AS conversions,
			COALESCE(AVG(v.metric_value), 0.0) AS avg_metric_value
		FROM vitals v
		LEFT JOIN session_conversions sc
			ON v.event_date = sc.event_date
		   AND v.user_pseudo_id = sc.user_pseudo_id
		   AND (v.session_id = sc.session_id OR (v.session_id IS NULL AND sc.session_id IS NULL))
		GROUP BY v.event_date, v.metric_name, v.metric_rating
	`, src, df)

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetWebVitals: %w", err)
	}

	var rows []entity.WebVitalMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			MetricName     string     `bigquery:"metric_name"`
			MetricRating   string     `bigquery:"metric_rating"`
			SessionCount   int64      `bigquery:"session_count"`
			Conversions    int64      `bigquery:"conversions"`
			AvgMetricValue float64    `bigquery:"avg_metric_value"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetWebVitals iterate: %w", err)
		}
		rows = append(rows, entity.WebVitalMetric{
			Date:           civilDateToTime(r.EventDate),
			MetricName:     r.MetricName,
			MetricRating:   r.MetricRating,
			SessionCount:   ClampInt64(r.SessionCount),
			Conversions:    ClampInt64(r.Conversions),
			AvgMetricValue: SanitizeFloat64(r.AvgMetricValue),
		})
	}
	return rows, nil
}

// GetUserJourneys queries ecommerce event sequences per session, returning
// top journey paths by frequency. The limit is applied per day (top N per event_date).
func (c *Client) GetUserJourneys(
	ctx context.Context,
	startDate, endDate time.Time,
	limit int,
) ([]entity.UserJourneyMetric, error) {
	if limit <= 0 {
		limit = 500
	}

	var result []entity.UserJourneyMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getUserJourneys(ctx, startDate, endDate, limit)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getUserJourneys(
	ctx context.Context,
	startDate, endDate time.Time,
	limit int,
) ([]entity.UserJourneyMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetUserJourneys: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH user_sessions AS (
			SELECT
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				event_name,
				event_timestamp,
				MIN(DATE(TIMESTAMP_MICROS(event_timestamp)))
					OVER (PARTITION BY
						user_pseudo_id,
						(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id')
					) AS session_start_date
			FROM %s
			WHERE %s
				AND event_name IN (
					'view_item', 'add_to_cart', 'begin_checkout',
					'add_shipping_info', 'add_payment_info', 'purchase'
				)
		),
		session_paths AS (
			SELECT
				session_start_date AS event_date,
				user_pseudo_id,
				session_id,
				STRING_AGG(event_name, ' -> ' ORDER BY event_timestamp) AS journey_path,
				MAX(CASE WHEN event_name = 'purchase' THEN 1 ELSE 0 END) AS converted
			FROM user_sessions
			GROUP BY session_start_date, user_pseudo_id, session_id
		),
		aggregated AS (
			SELECT
				event_date,
				journey_path,
				COUNT(*) AS session_count,
				SUM(converted) AS conversions
			FROM session_paths
			GROUP BY event_date, journey_path
		),
		ranked AS (
			SELECT
				event_date,
				journey_path,
				session_count,
				conversions,
				ROW_NUMBER() OVER (PARTITION BY event_date ORDER BY session_count DESC) AS rn
			FROM aggregated
		)
		SELECT event_date, journey_path, session_count, conversions
		FROM ranked
		WHERE rn <= @limit
		ORDER BY event_date DESC, session_count DESC
	`, src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{{Name: "limit", Value: int64(limit)}}
	} else {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
			{Name: "limit", Value: int64(limit)},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetUserJourneys: %w", err)
	}

	var rows []entity.UserJourneyMetric
	for {
		var r struct {
			EventDate    civil.Date `bigquery:"event_date"`
			JourneyPath  string     `bigquery:"journey_path"`
			SessionCount int64      `bigquery:"session_count"`
			Conversions  int64      `bigquery:"conversions"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetUserJourneys iterate: %w", err)
		}
		rows = append(rows, entity.UserJourneyMetric{
			Date:         civilDateToTime(r.EventDate),
			JourneyPath:  r.JourneyPath,
			SessionCount: ClampInt64(r.SessionCount),
			Conversions:  ClampInt64(r.Conversions),
		})
	}
	return rows, nil
}

// GetSessionDuration computes average and approximate median inter-event
// time per day as a proxy for session engagement duration.
// Filters to meaningful interaction events (session_start, page_view, user_engagement)
// to avoid scanning millions of low-value events (e.g. first_visit, scroll_depth).
func (c *Client) GetSessionDuration(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SessionDurationMetric, error) {

	var result []entity.SessionDurationMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getSessionDuration(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getSessionDuration(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SessionDurationMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetSessionDuration: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH event_gaps AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				event_timestamp,
				LAG(event_timestamp) OVER (
					PARTITION BY user_pseudo_id,
						(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id')
					ORDER BY event_timestamp
				) AS prev_ts
			FROM %s
			WHERE %s
				AND event_name IN ('session_start', 'page_view', 'user_engagement')
		),
		gaps AS (
			SELECT
				event_date,
				(event_timestamp - prev_ts) / 1000000.0 AS gap_seconds
			FROM event_gaps
			WHERE prev_ts IS NOT NULL
				AND (event_timestamp - prev_ts) / 1000000.0 BETWEEN 0.1 AND 1800
		)
		SELECT
			event_date,
			AVG(gap_seconds) AS avg_time_between_events_seconds,
			APPROX_QUANTILES(gap_seconds, 100)[OFFSET(50)] AS median_time_between_events
		FROM gaps
		GROUP BY event_date
	`, src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetSessionDuration: %w", err)
	}

	var rows []entity.SessionDurationMetric
	for {
		var r struct {
			EventDate                   civil.Date `bigquery:"event_date"`
			AvgTimeBetweenEventsSeconds float64    `bigquery:"avg_time_between_events_seconds"`
			MedianTimeBetweenEvents     float64    `bigquery:"median_time_between_events"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetSessionDuration iterate: %w", err)
		}
		rows = append(rows, entity.SessionDurationMetric{
			Date:                        civilDateToTime(r.EventDate),
			AvgTimeBetweenEventsSeconds: SanitizeFloat64(r.AvgTimeBetweenEventsSeconds),
			MedianTimeBetweenEvents:     SanitizeFloat64(r.MedianTimeBetweenEvents),
		})
	}
	return rows, nil
}

// GetBrowserBreakdown returns sessions/conversions by browser per day.
func (c *Client) GetBrowserBreakdown(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.BrowserBreakdownRow, error) {

	var result []entity.BrowserBreakdownRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getBrowserBreakdown(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getBrowserBreakdown(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.BrowserBreakdownRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "device.web_info.browser AS browser", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetBrowserBreakdown: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH browser_sessions AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				IFNULL(browser, 'unknown') AS browser,
				MAX(CASE WHEN event_name = 'purchase' THEN 1 ELSE 0 END) AS converted
			FROM %s
			WHERE %s
			GROUP BY event_date, user_pseudo_id, session_id, browser
		)
		SELECT
			event_date,
			browser,
			COUNT(*) AS sessions,
			COUNT(DISTINCT user_pseudo_id) AS users,
			SUM(converted) AS conversions,
			SAFE_DIVIDE(SUM(converted), COUNT(*)) AS conversion_rate
		FROM browser_sessions
		GROUP BY event_date, browser
	`, src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetBrowserBreakdown: %w", err)
	}

	var rows []entity.BrowserBreakdownRow
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			Browser        string     `bigquery:"browser"`
			Sessions       int64      `bigquery:"sessions"`
			Users          int64      `bigquery:"users"`
			Conversions    int64      `bigquery:"conversions"`
			ConversionRate float64    `bigquery:"conversion_rate"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetBrowserBreakdown iterate: %w", err)
		}
		rows = append(rows, entity.BrowserBreakdownRow{
			Date:           civilDateToTime(r.EventDate),
			Browser:        r.Browser,
			Sessions:       ClampInt64(r.Sessions),
			Users:          ClampInt64(r.Users),
			Conversions:    ClampInt64(r.Conversions),
			ConversionRate: SanitizeRate(r.ConversionRate),
		})
	}
	return rows, nil
}

// GetNewsletterSignups returns newsletter_signup events aggregated per day.
// signup_count is raw events; unique_users is COUNT(DISTINCT user_pseudo_id) for that day (not email dedupe; same person can generate multiple events).
func (c *Client) GetNewsletterSignups(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NewsletterMetricRow, error) {

	var result []entity.NewsletterMetricRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getNewsletterSignups(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getNewsletterSignups(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NewsletterMetricRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetNewsletterSignups: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			COUNT(*) AS signup_count,
			COUNT(DISTINCT user_pseudo_id) AS unique_users
		FROM %s
		WHERE %s
			AND event_name = 'newsletter_signup'
		GROUP BY event_date
	`, src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetNewsletterSignups: %w", err)
	}

	var rows []entity.NewsletterMetricRow
	for {
		var r struct {
			EventDate   civil.Date `bigquery:"event_date"`
			SignupCount int64      `bigquery:"signup_count"`
			UniqueUsers int64      `bigquery:"unique_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetNewsletterSignups iterate: %w", err)
		}
		rows = append(rows, entity.NewsletterMetricRow{
			Date:        civilDateToTime(r.EventDate),
			SignupCount: ClampInt64(r.SignupCount),
			UniqueUsers: ClampInt64(r.UniqueUsers),
		})
	}
	return rows, nil
}

// GetCampaignAttribution returns per-campaign sessions, conversions, and revenue per day.
// Relies on utm_source/utm_medium/utm_campaign enrichment pushed on all events.
func (c *Client) GetCampaignAttribution(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.CampaignAttributionRow, error) {

	var result []entity.CampaignAttributionRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getCampaignAttribution(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getCampaignAttribution(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.CampaignAttributionRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "traffic_source", "event_name", "ecommerce")
	if err != nil {
		return nil, fmt.Errorf("GetCampaignAttribution: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH purchase_dedup AS (
			SELECT
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				ecommerce.transaction_id,
				MAX(ecommerce.purchase_revenue) AS purchase_revenue
			FROM %[1]s
			WHERE %[2]s
				AND event_name = 'purchase'
				AND ecommerce.transaction_id IS NOT NULL
				AND ecommerce.transaction_id != 'false'
			GROUP BY user_pseudo_id, session_id, ecommerce.transaction_id
		),
		session_purchases AS (
			SELECT
				user_pseudo_id,
				session_id,
				COUNT(DISTINCT transaction_id) AS conversions,
				SUM(COALESCE(purchase_revenue, 0)) AS total_revenue
			FROM purchase_dedup
			GROUP BY user_pseudo_id, session_id
		),
		session_campaigns AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				COALESCE(
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'utm_source') END),
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'source') END),
					MAX(IFNULL(traffic_source.source, '(direct)'))
				) AS utm_source,
				COALESCE(
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'utm_medium') END),
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'medium') END),
					MAX(IFNULL(traffic_source.medium, '(none)'))
				) AS utm_medium,
				COALESCE(
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'utm_campaign') END),
					MAX(CASE WHEN event_name = 'session_start' THEN
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'campaign') END),
					MAX(IFNULL(traffic_source.name, '(not set)'))
				) AS utm_campaign
			FROM %[1]s
			WHERE %[2]s
			GROUP BY event_date, user_pseudo_id, session_id
		)
		SELECT
			sc.event_date,
			sc.utm_source,
			sc.utm_medium,
			sc.utm_campaign,
			COUNT(*) AS sessions,
			COUNT(DISTINCT sc.user_pseudo_id) AS users,
			COALESCE(SUM(sp.conversions), 0) AS conversions,
			COALESCE(SUM(sp.total_revenue), 0) AS revenue,
			SAFE_DIVIDE(SUM(CASE WHEN sp.conversions > 0 THEN 1 ELSE 0 END), COUNT(*)) AS conversion_rate
		FROM session_campaigns sc
		LEFT JOIN session_purchases sp
			ON sc.user_pseudo_id = sp.user_pseudo_id AND sc.session_id = sp.session_id
		GROUP BY sc.event_date, sc.utm_source, sc.utm_medium, sc.utm_campaign
		HAVING COUNT(*) > 0
		ORDER BY sc.event_date DESC, COUNT(*) DESC
	`, src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetCampaignAttribution: %w", err)
	}

	var rows []entity.CampaignAttributionRow
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			UTMSource      string     `bigquery:"utm_source"`
			UTMMedium      string     `bigquery:"utm_medium"`
			UTMCampaign    string     `bigquery:"utm_campaign"`
			Sessions       int64      `bigquery:"sessions"`
			Users          int64      `bigquery:"users"`
			Conversions    int64      `bigquery:"conversions"`
			Revenue        float64    `bigquery:"revenue"`
			ConversionRate float64    `bigquery:"conversion_rate"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetCampaignAttribution iterate: %w", err)
		}
		rows = append(rows, entity.CampaignAttributionRow{
			Date:           civilDateToTime(r.EventDate),
			UTMSource:      r.UTMSource,
			UTMMedium:      r.UTMMedium,
			UTMCampaign:    r.UTMCampaign,
			Sessions:       ClampInt64(r.Sessions),
			Users:          ClampInt64(r.Users),
			Conversions:    ClampInt64(r.Conversions),
			Revenue:        decimal.NewFromFloat(SanitizeFloat64ForDecimal(r.Revenue)),
			ConversionRate: SanitizeRate(r.ConversionRate),
		})
	}
	return rows, nil
}
