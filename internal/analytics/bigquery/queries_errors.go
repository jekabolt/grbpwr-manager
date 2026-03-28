package bigquery

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/api/iterator"
)

// GetFormErrors returns form_error events aggregated by error fields per day.
// Actual payload: form_id, form_name, error_fields (string), error_count (int), page_path.
func (c *Client) GetFormErrors(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.FormErrorMetric, error) {

	var result []entity.FormErrorMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getFormErrors(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getFormErrors(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.FormErrorMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetFormErrors: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			IFNULL((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'error_fields'), 'unknown') AS field_name,
			COALESCE(SUM((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'error_count')), COUNT(*)) AS error_count
		FROM %s
		WHERE %s
			AND event_name = 'form_error'
		GROUP BY event_date, field_name
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
		return nil, fmt.Errorf("GetFormErrors: %w", err)
	}

	var rows []entity.FormErrorMetric
	for {
		var r struct {
			EventDate  civil.Date `bigquery:"event_date"`
			FieldName  string    `bigquery:"field_name"`
			ErrorCount int64     `bigquery:"error_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetFormErrors iterate: %w", err)
		}
		rows = append(rows, entity.FormErrorMetric{
			Date:       civilDateToTime(r.EventDate),
			FieldName:  r.FieldName,
			ErrorCount: ClampInt64(r.ErrorCount),
		})
	}
	return rows, nil
}

// GetExceptions returns JS exception events aggregated by page path per day.
// Note: page_location from GA4 contains full URLs (e.g., https://example.com/checkout?step=2),
// not just paths. Frontend filtering by checkout path substring should work but may need
// normalization if URLs vary (query params, domains). For more robust filtering, consider
// extracting path-only via REGEXP_EXTRACT in the SQL below.
func (c *Client) GetExceptions(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ExceptionMetric, error) {

	var result []entity.ExceptionMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getExceptions(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getExceptions(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ExceptionMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetExceptions: %w", err)
	}
	// page_location is typically a full URL (https://domain.com/path?query).
	// For checkout filtering, storing the full URL allows substring matching on "/checkout".
	// If more precise path extraction is needed, uncomment the REGEXP_EXTRACT line below.
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			IFNULL((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_location'), '/') AS page_path,
			-- For path-only extraction: REGEXP_EXTRACT(page_location, r'https?://[^/]+(/[^?]*)') AS page_path,
			COUNT(*) AS exception_count,
			IFNULL(
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'description'),
				''
			) AS description
		FROM %s
		WHERE %s
			AND event_name = 'exception'
		GROUP BY event_date, page_path, description
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
		return nil, fmt.Errorf("GetExceptions: %w", err)
	}

	var rows []entity.ExceptionMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			PagePath       string    `bigquery:"page_path"`
			ExceptionCount int64     `bigquery:"exception_count"`
			Description    string    `bigquery:"description"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetExceptions iterate: %w", err)
		}
		rows = append(rows, entity.ExceptionMetric{
			Date:           civilDateToTime(r.EventDate),
			PagePath:       r.PagePath,
			ExceptionCount: ClampInt64(r.ExceptionCount),
			Description:    r.Description,
		})
	}
	return rows, nil
}

// Get404Pages returns page_not_found events aggregated by URL per day.
func (c *Client) Get404Pages(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NotFoundMetric, error) {

	var result []entity.NotFoundMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.get404Pages(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) get404Pages(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NotFoundMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("Get404Pages: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			IFNULL((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_location'), '/') AS page_path,
			COUNT(*) AS hit_count
		FROM %s
		WHERE %s
			AND event_name = 'page_not_found'
		GROUP BY event_date, page_path
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
		return nil, fmt.Errorf("Get404Pages: %w", err)
	}

	var rows []entity.NotFoundMetric
	for {
		var r struct {
			EventDate civil.Date `bigquery:"event_date"`
			PagePath  string    `bigquery:"page_path"`
			HitCount  int64     `bigquery:"hit_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("Get404Pages iterate: %w", err)
		}
		rows = append(rows, entity.NotFoundMetric{
			Date:     civilDateToTime(r.EventDate),
			PagePath: r.PagePath,
			HitCount: ClampInt64(r.HitCount),
		})
	}
	return rows, nil
}
