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

// GetProductEngagement returns engagement metrics per product per day.
// Uses view_item (ecommerce items[]) for product identification,
// scroll_depth events on product pages for engagement depth, and
// product_zoom custom events (event_params.product_id) for zoom counts.
//
// Both sides use product_id for the JOIN: product_views from item.item_id (must be
// product ID string per frontend convention), product_scrolls from the last path
// segment of /product/... URLs (dto.GetProductSlug format), zoom from the same
// string as GetProductZoom. If item_id uses SKU instead of product ID, scroll and
// zoom joins will not match — frontend must send consistent product IDs.
func (c *Client) GetProductEngagement(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ProductEngagementMetric, error) {

	var result []entity.ProductEngagementMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getProductEngagement(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getProductEngagement(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ProductEngagementMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetProductEngagement: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH product_views AS (
			SELECT
				DATE(TIMESTAMP_MICROS(e.event_timestamp)) AS event_date,
				item.item_id AS product_id,
				ANY_VALUE(item.item_name) AS product_name,
				COUNT(*) AS view_count
			FROM %[1]s AS e, UNNEST(e.items) AS item
			WHERE %[2]s
				AND e.event_name = 'view_item'
				AND item.item_id IS NOT NULL
			GROUP BY event_date, product_id
		),
		product_scrolls AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				REGEXP_EXTRACT(
					(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path'),
					r'/([0-9]+)/?$'
				) AS product_id,
				COUNTIF((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'percent_scrolled') >= 75
					AND (SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'percent_scrolled') < 100
				) AS scroll_75,
				COUNTIF((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'percent_scrolled') = 100
				) AS scroll_100
			FROM %[1]s
			WHERE %[2]s
				AND event_name = 'scroll_depth'
				AND REGEXP_CONTAINS(
					IFNULL((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path'), ''),
					r'/product[s]?/'
				)
				AND REGEXP_EXTRACT(
					(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path'),
					r'/([0-9]+)/?$'
				) IS NOT NULL
			GROUP BY event_date, product_id
		),
		product_zoom_counts AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COUNT(*) AS zoom_count
			FROM %[1]s
			WHERE %[2]s
				AND event_name = 'product_zoom'
				AND (SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') IS NOT NULL
			GROUP BY event_date, product_id
		)
		SELECT
			pv.event_date,
			pv.product_id,
			pv.product_name,
			pv.view_count AS image_views,
			COALESCE(pz.zoom_count, 0) AS zoom_events,
			COALESCE(ps.scroll_75, 0) AS scroll_75,
			COALESCE(ps.scroll_100, 0) AS scroll_100
		FROM product_views pv
		LEFT JOIN product_scrolls ps
			ON pv.event_date = ps.event_date
			AND pv.product_id = ps.product_id
		LEFT JOIN product_zoom_counts pz
			ON pv.event_date = pz.event_date
			AND pv.product_id = pz.product_id
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
		return nil, fmt.Errorf("GetProductEngagement: %w", err)
	}

	var rows []entity.ProductEngagementMetric
	for {
		var r struct {
			EventDate   civil.Date `bigquery:"event_date"`
			ProductID   string    `bigquery:"product_id"`
			ProductName string    `bigquery:"product_name"`
			ImageViews  int64     `bigquery:"image_views"`
			ZoomEvents  int64     `bigquery:"zoom_events"`
			Scroll75    int64     `bigquery:"scroll_75"`
			Scroll100   int64     `bigquery:"scroll_100"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetProductEngagement iterate: %w", err)
		}
		rows = append(rows, entity.ProductEngagementMetric{
			Date:        civilDateToTime(r.EventDate),
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			ImageViews:  ClampInt64(r.ImageViews),
			ZoomEvents:  ClampInt64(r.ZoomEvents),
			Scroll75:    ClampInt64(r.Scroll75),
			Scroll100:   ClampInt64(r.Scroll100),
		})
	}
	return rows, nil
}

// GetSizeConfidence returns size_guide_view vs size_selected counts per product per day.
func (c *Client) GetSizeConfidence(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SizeConfidenceMetric, error) {

	var result []entity.SizeConfidenceMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getSizeConfidence(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getSizeConfidence(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SizeConfidenceMetric, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetSizeConfidence: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH size_confidence_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				event_name
			FROM %s
			WHERE %s
				AND event_name IN ('size_guide_view', 'size_selected')
				AND (SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') IS NOT NULL
		),
		product_names AS (
			SELECT DISTINCT
				item.item_id AS product_id,
				ANY_VALUE(item.item_name) AS product_name
			FROM %s AS e, UNNEST(e.items) AS item
			WHERE %s
				AND item.item_id IS NOT NULL
			GROUP BY item.item_id
		)
		SELECT
			sc.event_date,
			sc.product_id,
			COALESCE(pn.product_name, sc.product_id) AS product_name,
			COUNTIF(sc.event_name = 'size_guide_view') AS size_guide_views,
			COUNTIF(sc.event_name = 'size_selected') AS size_selections
		FROM size_confidence_events sc
		LEFT JOIN product_names pn ON pn.product_id = sc.product_id
		GROUP BY sc.event_date, sc.product_id, product_name
		ORDER BY sc.event_date, size_guide_views DESC
	`, src, c.dateFilterSQL(startDate, endDate), src, c.dateFilterSQL(startDate, endDate))

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetSizeConfidence: %w", err)
	}

	var rows []entity.SizeConfidenceMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			ProductID      string    `bigquery:"product_id"`
			ProductName    string    `bigquery:"product_name"`
			SizeGuideViews int64     `bigquery:"size_guide_views"`
			SizeSelections int64     `bigquery:"size_selections"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetSizeConfidence iterate: %w", err)
		}
		rows = append(rows, entity.SizeConfidenceMetric{
			Date:           civilDateToTime(r.EventDate),
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			SizeGuideViews: ClampInt64(r.SizeGuideViews),
			SizeSelections: ClampInt64(r.SizeSelections),
		})
	}
	return rows, nil
}

// GetSizeIntent queries size_selected events, grouping by size_id (not size_name)
// to avoid merging sizes that share a display name across different size charts.
func (c *Client) GetSizeIntent(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]SizeIntentRow, error) {

	var result []SizeIntentRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getSizeIntent(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getSizeIntent(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]SizeIntentRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetSizeIntent: %w", err)
	}
	sql := fmt.Sprintf(`
		SELECT
			DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
			(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
			SAFE_CAST((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'size_id') AS INT64) AS size_id,
			(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'size_name') AS size_name,
			COUNT(*) AS size_clicks
		FROM %s
		WHERE %s
			AND event_name = 'size_selected'
			AND (SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'size_id') IS NOT NULL
		GROUP BY event_date, product_id, size_id, size_name
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
		return nil, fmt.Errorf("GetSizeIntent: %w", err)
	}

	var rows []SizeIntentRow
	for {
		var r struct {
			EventDate  civil.Date `bigquery:"event_date"`
			ProductID  string    `bigquery:"product_id"`
			SizeID     int64     `bigquery:"size_id"`
			SizeName   string    `bigquery:"size_name"`
			SizeClicks int64     `bigquery:"size_clicks"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetSizeIntent iterate: %w", err)
		}
		rows = append(rows, SizeIntentRow{
			Date:       civilDateToTime(r.EventDate),
			ProductID:  r.ProductID,
			SizeID:     int(r.SizeID),
			SizeName:   r.SizeName,
			SizeClicks: ClampInt64(r.SizeClicks),
		})
	}
	return rows, nil
}
