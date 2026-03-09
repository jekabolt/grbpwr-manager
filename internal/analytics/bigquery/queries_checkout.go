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

// GetPaymentRecovery tracks users who had payment_failed then subsequently purchased within 24h.
func (c *Client) GetPaymentRecovery(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.PaymentRecoveryMetric, error) {

	var result []entity.PaymentRecoveryMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getPaymentRecovery(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getPaymentRecovery(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.PaymentRecoveryMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetPaymentRecovery: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH failed AS (
			SELECT DISTINCT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				event_timestamp AS fail_ts
			FROM %[1]s
			WHERE %[2]s
				AND event_name = 'payment_failed'
		),
		purchases AS (
			SELECT user_pseudo_id, event_timestamp
			FROM %[1]s
			WHERE %[2]s AND event_name = 'purchase'
		),
		recovered AS (
			SELECT DISTINCT f.event_date, f.user_pseudo_id
			FROM failed f
			JOIN purchases p ON f.user_pseudo_id = p.user_pseudo_id
				AND p.event_timestamp > f.fail_ts
				AND TIMESTAMP_MICROS(p.event_timestamp) <= TIMESTAMP_ADD(TIMESTAMP_MICROS(f.fail_ts), INTERVAL 1 DAY)
		)
		SELECT
			f.event_date,
			COUNT(DISTINCT f.user_pseudo_id) AS failed_users,
			COUNT(DISTINCT r.user_pseudo_id) AS recovered_users
		FROM failed f
		LEFT JOIN recovered r
			ON f.event_date = r.event_date AND f.user_pseudo_id = r.user_pseudo_id
		GROUP BY f.event_date
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
		return nil, fmt.Errorf("GetPaymentRecovery: %w", err)
	}

	var rows []entity.PaymentRecoveryMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			FailedUsers    int64     `bigquery:"failed_users"`
			RecoveredUsers int64     `bigquery:"recovered_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetPaymentRecovery iterate: %w", err)
		}
		rows = append(rows, entity.PaymentRecoveryMetric{
			Date:           civilDateToTime(r.EventDate),
			FailedUsers:    ClampInt64(r.FailedUsers),
			RecoveredUsers: ClampInt64(r.RecoveredUsers),
		})
	}
	return rows, nil
}

// GetCheckoutTimings measures the time between form_start and purchase events per day.
func (c *Client) GetCheckoutTimings(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.CheckoutTimingMetric, error) {

	var result []entity.CheckoutTimingMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getCheckoutTimings(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getCheckoutTimings(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.CheckoutTimingMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetCheckoutTimings: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH form_starts AS (
			SELECT
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				MIN(event_timestamp) AS start_ts
			FROM %[1]s
			WHERE %[2]s AND event_name = 'form_start'
			GROUP BY user_pseudo_id, session_id, event_date
		),
		purchases AS (
			SELECT
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				MIN(event_timestamp) AS purchase_ts
			FROM %[1]s
			WHERE %[2]s AND event_name = 'purchase'
			GROUP BY user_pseudo_id, session_id
		),
		timings AS (
			SELECT
				f.event_date,
				(p.purchase_ts - f.start_ts) / 1000000.0 AS checkout_seconds
			FROM form_starts f
			JOIN purchases p
				ON f.user_pseudo_id = p.user_pseudo_id
				AND f.session_id = p.session_id
			WHERE p.purchase_ts > f.start_ts
				AND (p.purchase_ts - f.start_ts) / 1000000.0 BETWEEN 1 AND 3600
		)
		SELECT
			event_date,
			AVG(checkout_seconds) AS avg_checkout_seconds,
			APPROX_QUANTILES(checkout_seconds, 100)[OFFSET(50)] AS median_checkout_seconds,
			COUNT(*) AS session_count
		FROM timings
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
		return nil, fmt.Errorf("GetCheckoutTimings: %w", err)
	}

	var rows []entity.CheckoutTimingMetric
	for {
		var r struct {
			EventDate             civil.Date `bigquery:"event_date"`
			AvgCheckoutSeconds    float64   `bigquery:"avg_checkout_seconds"`
			MedianCheckoutSeconds float64   `bigquery:"median_checkout_seconds"`
			SessionCount          int64     `bigquery:"session_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetCheckoutTimings iterate: %w", err)
		}
		rows = append(rows, entity.CheckoutTimingMetric{
			Date:                  civilDateToTime(r.EventDate),
			AvgCheckoutSeconds:    SanitizeFloat64(r.AvgCheckoutSeconds),
			MedianCheckoutSeconds: SanitizeFloat64(r.MedianCheckoutSeconds),
			SessionCount:          ClampInt64(r.SessionCount),
		})
	}
	return rows, nil
}

// GetOOSImpact queries out_of_stock_click events to estimate lost revenue.
func (c *Client) GetOOSImpact(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.OOSImpactMetric, error) {

	var result []entity.OOSImpactMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getOOSImpact(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getOOSImpact(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.OOSImpactMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetOOSImpact: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
			ANY_VALUE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_name')) AS product_name,
			SAFE_CAST((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'size_id') AS INT64) AS size_id,
			ANY_VALUE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'size_name')) AS size_name,
			COALESCE(MAX((SELECT value.double_value FROM UNNEST(event_params) WHERE key = 'product_price')), 0) AS product_price,
			ANY_VALUE(COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'currency'), 'USD')) AS currency,
			COUNT(*) AS click_count
		FROM %s
		WHERE %s
			AND event_name = 'out_of_stock_click'
			AND (SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'size_id') IS NOT NULL
		GROUP BY event_date, product_id, size_id
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
		return nil, fmt.Errorf("GetOOSImpact: %w", err)
	}

	var rows []entity.OOSImpactMetric
	for {
		var r struct {
			EventDate    civil.Date `bigquery:"event_date"`
			ProductID    string     `bigquery:"product_id"`
			ProductName  string     `bigquery:"product_name"`
			SizeID       int64      `bigquery:"size_id"`
			SizeName     string     `bigquery:"size_name"`
			ProductPrice float64    `bigquery:"product_price"`
			Currency     string     `bigquery:"currency"`
			ClickCount   int64      `bigquery:"click_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetOOSImpact iterate: %w", err)
		}
		row := entity.OOSImpactMetric{
			Date:         civilDateToTime(r.EventDate),
			ProductID:    r.ProductID,
			ProductName:  r.ProductName,
			SizeID:       int(r.SizeID),
			SizeName:     r.SizeName,
			ProductPrice: decimal.NewFromFloat(SanitizeFloat64ForDecimal(r.ProductPrice)),
			Currency:     r.Currency,
			ClickCount:   ClampInt64(r.ClickCount),
		}
		row.EstimatedLostSales = decimal.NewFromInt(row.ClickCount).Mul(decimal.NewFromFloat(0.02))
		row.EstimatedLostRevenue = row.EstimatedLostSales.Mul(row.ProductPrice)
		rows = append(rows, row)
	}
	return rows, nil
}

// GetPaymentFailures queries payment_failed events aggregated by error code and type.
func (c *Client) GetPaymentFailures(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.PaymentFailureMetric, error) {

	var result []entity.PaymentFailureMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getPaymentFailures(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getPaymentFailures(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.PaymentFailureMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetPaymentFailures: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'error_code') AS error_code,
			(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'payment_type') AS payment_type,
			COUNT(*) AS failure_count,
			COALESCE(SUM((SELECT value.double_value FROM UNNEST(event_params) WHERE key = 'order_value')), 0) AS total_failed_value,
			COALESCE(AVG((SELECT value.double_value FROM UNNEST(event_params) WHERE key = 'order_value')), 0) AS avg_failed_order_value
		FROM %s
		WHERE %s
			AND event_name = 'payment_failed'
		GROUP BY event_date, error_code, payment_type
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
		return nil, fmt.Errorf("GetPaymentFailures: %w", err)
	}

	var rows []entity.PaymentFailureMetric
	for {
		var r struct {
			EventDate           civil.Date `bigquery:"event_date"`
			ErrorCode           string    `bigquery:"error_code"`
			PaymentType         string    `bigquery:"payment_type"`
			FailureCount        int64     `bigquery:"failure_count"`
			TotalFailedValue    float64   `bigquery:"total_failed_value"`
			AvgFailedOrderValue float64   `bigquery:"avg_failed_order_value"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetPaymentFailures iterate: %w", err)
		}
		rows = append(rows, entity.PaymentFailureMetric{
			Date:                civilDateToTime(r.EventDate),
			ErrorCode:           r.ErrorCode,
			PaymentType:         r.PaymentType,
			FailureCount:        ClampInt64(r.FailureCount),
			TotalFailedValue:    decimal.NewFromFloat(SanitizeFloat64ForDecimal(r.TotalFailedValue)),
			AvgFailedOrderValue: decimal.NewFromFloat(SanitizeFloat64ForDecimal(r.AvgFailedOrderValue)),
		})
	}
	return rows, nil
}
