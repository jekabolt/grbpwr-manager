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

// GetTimeOnPage returns time-on-page metrics per page per day
func (c *Client) GetTimeOnPage(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.TimeOnPageRow, error) {
	var result []entity.TimeOnPageRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getTimeOnPage(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getTimeOnPage(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.TimeOnPageRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetTimeOnPage: %w", err)
	}

	// The frontend fires time_on_page as periodic heartbeats (~40 s interval).
	// Each heartbeat carries cumulative visible/total time for the current page visit.
	// We keep only the LAST heartbeat per (user, session, page, day) — the final
	// measurement — and cap values at 1800 s (30 min) to filter bots/background tabs.
	sql := fmt.Sprintf(`
		WITH ranked AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path') AS page_path,
				SAFE_CAST((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'visible_time_seconds') AS FLOAT64) AS visible_time,
				SAFE_CAST((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'total_time_seconds') AS FLOAT64) AS total_time,
				SAFE_CAST((SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'engagement_score') AS FLOAT64) AS engagement_score,
				ROW_NUMBER() OVER (
					PARTITION BY
						DATE(TIMESTAMP_MICROS(event_timestamp)),
						user_pseudo_id,
						(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id'),
						(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path')
					ORDER BY event_timestamp DESC
				) AS rn
			FROM %s
			WHERE %s
				AND event_name = 'time_on_page'
				AND (SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_path') IS NOT NULL
		)
		SELECT
			event_date,
			page_path,
			COALESCE(AVG(LEAST(COALESCE(visible_time, 0), 1800)), 0.0) AS avg_visible_time_seconds,
			COALESCE(AVG(LEAST(COALESCE(total_time, 0), 1800)), 0.0) AS avg_total_time_seconds,
			COALESCE(AVG(COALESCE(engagement_score, 0)), 0.0) AS avg_engagement_score,
			COUNT(*) AS page_views
		FROM ranked
		WHERE rn = 1
		GROUP BY event_date, page_path
		ORDER BY event_date, page_views DESC
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
		return nil, fmt.Errorf("GetTimeOnPage: %w", err)
	}

	var rows []entity.TimeOnPageRow
	for {
		var r struct {
			EventDate              civil.Date `bigquery:"event_date"`
			PagePath               string     `bigquery:"page_path"`
			AvgVisibleTimeSeconds  float64    `bigquery:"avg_visible_time_seconds"`
			AvgTotalTimeSeconds    float64    `bigquery:"avg_total_time_seconds"`
			AvgEngagementScore     float64    `bigquery:"avg_engagement_score"`
			PageViews              int64      `bigquery:"page_views"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetTimeOnPage iterate: %w", err)
		}
		rows = append(rows, entity.TimeOnPageRow{
			Date:                  civilDateToTime(r.EventDate),
			PagePath:              r.PagePath,
			AvgVisibleTimeSeconds: r.AvgVisibleTimeSeconds,
			AvgTotalTimeSeconds:   r.AvgTotalTimeSeconds,
			AvgEngagementScore:    r.AvgEngagementScore,
			PageViews:             ClampInt64(r.PageViews),
		})
	}
	return rows, nil
}

// GetProductZoom returns product zoom interactions per day
func (c *Client) GetProductZoom(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ProductZoomRow, error) {
	var result []entity.ProductZoomRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getProductZoom(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getProductZoom(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ProductZoomRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetProductZoom: %w", err)
	}

	sql := fmt.Sprintf(`
		WITH zoom_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'zoom_method'), 'unknown') AS zoom_method
			FROM %s
			WHERE %s
				AND event_name = 'product_zoom'
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
			z.event_date,
			z.product_id,
			COALESCE(pn.product_name, z.product_id) AS product_name,
			z.zoom_method,
			COUNT(*) AS zoom_count
		FROM zoom_events z
		LEFT JOIN product_names pn ON pn.product_id = z.product_id
		GROUP BY z.event_date, z.product_id, product_name, z.zoom_method
		ORDER BY z.event_date, zoom_count DESC
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
		return nil, fmt.Errorf("GetProductZoom: %w", err)
	}

	var rows []entity.ProductZoomRow
	for {
		var r struct {
			EventDate   civil.Date `bigquery:"event_date"`
			ProductID   string     `bigquery:"product_id"`
			ProductName string     `bigquery:"product_name"`
			ZoomMethod  string     `bigquery:"zoom_method"`
			ZoomCount   int64      `bigquery:"zoom_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetProductZoom iterate: %w", err)
		}
		rows = append(rows, entity.ProductZoomRow{
			Date:        civilDateToTime(r.EventDate),
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			ZoomMethod:  r.ZoomMethod,
			ZoomCount:   ClampInt64(r.ZoomCount),
		})
	}
	return rows, nil
}

// GetImageSwipes returns image swipe interactions per day
func (c *Client) GetImageSwipes(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ImageSwipeRow, error) {
	var result []entity.ImageSwipeRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getImageSwipes(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getImageSwipes(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.ImageSwipeRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetImageSwipes: %w", err)
	}

	sql := fmt.Sprintf(`
		WITH swipe_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'swipe_direction'), 'unknown') AS swipe_direction
			FROM %s
			WHERE %s
				AND event_name = 'product_image_swipe'
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
			s.event_date,
			s.product_id,
			COALESCE(pn.product_name, s.product_id) AS product_name,
			s.swipe_direction,
			COUNT(*) AS swipe_count
		FROM swipe_events s
		LEFT JOIN product_names pn ON pn.product_id = s.product_id
		GROUP BY s.event_date, s.product_id, product_name, s.swipe_direction
		ORDER BY s.event_date, swipe_count DESC
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
		return nil, fmt.Errorf("GetImageSwipes: %w", err)
	}

	var rows []entity.ImageSwipeRow
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			ProductID      string     `bigquery:"product_id"`
			ProductName    string     `bigquery:"product_name"`
			SwipeDirection string     `bigquery:"swipe_direction"`
			SwipeCount     int64      `bigquery:"swipe_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetImageSwipes iterate: %w", err)
		}
		rows = append(rows, entity.ImageSwipeRow{
			Date:           civilDateToTime(r.EventDate),
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			SwipeDirection: r.SwipeDirection,
			SwipeCount:     ClampInt64(r.SwipeCount),
		})
	}
	return rows, nil
}

// GetSizeGuideClicks returns size guide click events per day
func (c *Client) GetSizeGuideClicks(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SizeGuideClickRow, error) {
	var result []entity.SizeGuideClickRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getSizeGuideClicks(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getSizeGuideClicks(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.SizeGuideClickRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetSizeGuideClicks: %w", err)
	}

	sql := fmt.Sprintf(`
		WITH size_guide_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_location'), 'unknown') AS page_location
			FROM %s
			WHERE %s
				AND event_name = 'size_guide_click'
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
			sg.event_date,
			sg.product_id,
			COALESCE(pn.product_name, sg.product_id) AS product_name,
			sg.page_location,
			COUNT(*) AS click_count
		FROM size_guide_events sg
		LEFT JOIN product_names pn ON pn.product_id = sg.product_id
		GROUP BY sg.event_date, sg.product_id, product_name, sg.page_location
		ORDER BY sg.event_date, click_count DESC
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
		return nil, fmt.Errorf("GetSizeGuideClicks: %w", err)
	}

	var rows []entity.SizeGuideClickRow
	for {
		var r struct {
			EventDate    civil.Date `bigquery:"event_date"`
			ProductID    string     `bigquery:"product_id"`
			ProductName  string     `bigquery:"product_name"`
			PageLocation string     `bigquery:"page_location"`
			ClickCount   int64      `bigquery:"click_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetSizeGuideClicks iterate: %w", err)
		}
		rows = append(rows, entity.SizeGuideClickRow{
			Date:         civilDateToTime(r.EventDate),
			ProductID:    r.ProductID,
			ProductName:  r.ProductName,
			PageLocation: r.PageLocation,
			ClickCount:   ClampInt64(r.ClickCount),
		})
	}
	return rows, nil
}

// GetDetailsExpansion returns details expansion events per day
func (c *Client) GetDetailsExpansion(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DetailsExpansionRow, error) {
	var result []entity.DetailsExpansionRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getDetailsExpansion(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getDetailsExpansion(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DetailsExpansionRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetDetailsExpansion: %w", err)
	}

	sql := fmt.Sprintf(`
		WITH details_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'section_name'), 'unknown') AS section_name
			FROM %s
			WHERE %s
				AND event_name = 'details_expand'
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
			de.event_date,
			de.product_id,
			COALESCE(pn.product_name, de.product_id) AS product_name,
			de.section_name,
			COUNT(*) AS expand_count
		FROM details_events de
		LEFT JOIN product_names pn ON pn.product_id = de.product_id
		GROUP BY de.event_date, de.product_id, product_name, de.section_name
		ORDER BY de.event_date, expand_count DESC
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
		return nil, fmt.Errorf("GetDetailsExpansion: %w", err)
	}

	var rows []entity.DetailsExpansionRow
	for {
		var r struct {
			EventDate   civil.Date `bigquery:"event_date"`
			ProductID   string     `bigquery:"product_id"`
			ProductName string     `bigquery:"product_name"`
			SectionName string     `bigquery:"section_name"`
			ExpandCount int64      `bigquery:"expand_count"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetDetailsExpansion iterate: %w", err)
		}
		rows = append(rows, entity.DetailsExpansionRow{
			Date:        civilDateToTime(r.EventDate),
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			SectionName: r.SectionName,
			ExpandCount: ClampInt64(r.ExpandCount),
		})
	}
	return rows, nil
}

// GetNotifyMeIntent returns notify-me intent events per day with conversion rate
func (c *Client) GetNotifyMeIntent(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NotifyMeIntentRow, error) {
	var result []entity.NotifyMeIntentRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getNotifyMeIntent(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getNotifyMeIntent(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.NotifyMeIntentRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "event_params", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetNotifyMeIntent: %w", err)
	}

	sql := fmt.Sprintf(`
		WITH notify_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				(SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'product_id') AS product_id,
				COALESCE((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'action'), 'unknown') AS action
			FROM %s
			WHERE %s
				AND event_name = 'notify_me_action'
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
		),
		action_counts AS (
			SELECT
				ne.event_date,
				ne.product_id,
				COALESCE(pn.product_name, ne.product_id) AS product_name,
				ne.action,
				COUNT(*) AS count
			FROM notify_events ne
			LEFT JOIN product_names pn ON pn.product_id = ne.product_id
			GROUP BY ne.event_date, ne.product_id, product_name, ne.action
		),
		conversion_rates AS (
			SELECT
				event_date,
				product_id,
				product_name,
				MAX(CASE WHEN action = 'opened' THEN count ELSE 0 END) AS opened_count,
				MAX(CASE WHEN action = 'submitted' THEN count ELSE 0 END) AS submitted_count
			FROM action_counts
			GROUP BY event_date, product_id, product_name
		)
		SELECT
			ac.event_date,
			ac.product_id,
			ac.product_name,
			ac.action,
			ac.count,
			CASE 
				WHEN cr.opened_count > 0 AND ac.action = 'submitted'
				THEN SAFE_DIVIDE(ac.count, cr.opened_count) * 100.0
				ELSE 0.0
			END AS conversion_rate
		FROM action_counts ac
		LEFT JOIN conversion_rates cr 
			ON ac.event_date = cr.event_date 
			AND ac.product_id = cr.product_id
		ORDER BY ac.event_date, ac.count DESC
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
		return nil, fmt.Errorf("GetNotifyMeIntent: %w", err)
	}

	var rows []entity.NotifyMeIntentRow
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			ProductID      string     `bigquery:"product_id"`
			ProductName    string     `bigquery:"product_name"`
			Action         string     `bigquery:"action"`
			Count          int64      `bigquery:"count"`
			ConversionRate float64    `bigquery:"conversion_rate"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetNotifyMeIntent iterate: %w", err)
		}
		rows = append(rows, entity.NotifyMeIntentRow{
			Date:           civilDateToTime(r.EventDate),
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			Action:         r.Action,
			Count:          ClampInt64(r.Count),
			ConversionRate: r.ConversionRate,
		})
	}
	return rows, nil
}
