package bigquery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/api/iterator"
)

// GetFunnelAnalysis runs a full 10-step ecommerce funnel query.
// Counts sessions (user_pseudo_id + ga_session_id) to match GA4's session definition.
// Enforces sequential funnel logic: sessions can only appear at step N if they completed step N-1.
// size_selected uses GREATEST(did_size_selected, did_add_to_cart) so that one-size products
// (where the explicit size_selected event never fires) still count as passing the size step.
// This keeps the funnel monotonically decreasing while not gating add_to_cart on size_selected.
// The first step counts all sessions with any funnel event (not just session_start events),
// ensuring alignment with GA4 Data API session counts.
func (c *Client) GetFunnelAnalysis(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DailyFunnel, error) {

	var result []entity.DailyFunnel
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getFunnelAnalysis(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

// GetFunnelAnalysisStream runs the funnel query and invokes fn for each batch of rows.
// Use for constant-memory sync: delete by date range first, then stream-insert in batches.
// batchSize controls how many rows are passed to fn per call; 0 defaults to 500.
//
// Error Recovery: If fn fails after processing some batches, previously inserted batches
// remain committed. The caller should delete the entire date range before calling this
// function to ensure data consistency (all-or-nothing at the date range level).
//
// Memory Safety: Each batch is allocated fresh to prevent memory leaks from retained
// capacity in the underlying slice array.
func (c *Client) GetFunnelAnalysisStream(
	ctx context.Context,
	startDate, endDate time.Time,
	batchSize int,
	fn func([]entity.DailyFunnel) error,
) error {
	if batchSize <= 0 {
		batchSize = 500
	}
	return c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		return c.getFunnelAnalysisStream(ctx, startDate, endDate, batchSize, fn)
	})
}

func (c *Client) buildFunnelAnalysisSQL(src string, startDate, endDate time.Time) string {
	return fmt.Sprintf(`
		WITH user_events AS (
			SELECT
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				event_name
			FROM %s
			WHERE %s
				AND event_name IN (
					'session_start', 'view_item_list', 'select_item',
					'view_item', 'size_selected', 'add_to_cart',
					'begin_checkout', 'add_shipping_info',
					'add_payment_info', 'purchase'
				)
		),
		session_events AS (
			SELECT
				event_date,
				user_pseudo_id,
				session_id,
				MAX(CASE WHEN event_name = 'session_start'      THEN 1 ELSE 0 END) AS did_session_start,
				MAX(CASE WHEN event_name = 'view_item_list'     THEN 1 ELSE 0 END) AS did_view_item_list,
				MAX(CASE WHEN event_name = 'select_item'        THEN 1 ELSE 0 END) AS did_select_item,
				MAX(CASE WHEN event_name = 'view_item'          THEN 1 ELSE 0 END) AS did_view_item,
				MAX(CASE WHEN event_name = 'size_selected'      THEN 1 ELSE 0 END) AS did_size_selected,
				MAX(CASE WHEN event_name = 'add_to_cart'        THEN 1 ELSE 0 END) AS did_add_to_cart,
				MAX(CASE WHEN event_name = 'begin_checkout'     THEN 1 ELSE 0 END) AS did_begin_checkout,
				MAX(CASE WHEN event_name = 'add_shipping_info'  THEN 1 ELSE 0 END) AS did_add_shipping_info,
				MAX(CASE WHEN event_name = 'add_payment_info'   THEN 1 ELSE 0 END) AS did_add_payment_info,
				MAX(CASE WHEN event_name = 'purchase'           THEN 1 ELSE 0 END) AS did_purchase
			FROM user_events
			WHERE session_id IS NOT NULL
			GROUP BY event_date, user_pseudo_id, session_id
		),
		sequential_funnel AS (
			SELECT
				event_date,
				user_pseudo_id,
				session_id,
				did_view_item_list AS seq_view_item_list,
				CASE WHEN did_view_item_list = 1
					THEN did_select_item ELSE 0 END AS seq_select_item,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1
					THEN did_view_item ELSE 0 END AS seq_view_item,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1
					THEN GREATEST(did_size_selected, did_add_to_cart) ELSE 0 END AS seq_size_selected,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1
					THEN did_add_to_cart ELSE 0 END AS seq_add_to_cart,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1 AND did_add_to_cart = 1
					THEN did_begin_checkout ELSE 0 END AS seq_begin_checkout,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1 AND did_add_to_cart = 1 AND did_begin_checkout = 1
					THEN did_add_shipping_info ELSE 0 END AS seq_add_shipping_info,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1 AND did_add_to_cart = 1 AND did_begin_checkout = 1 AND did_add_shipping_info = 1
					THEN did_add_payment_info ELSE 0 END AS seq_add_payment_info,
				CASE WHEN did_view_item_list = 1 AND did_select_item = 1 AND did_view_item = 1 AND did_add_to_cart = 1 AND did_begin_checkout = 1 AND did_add_shipping_info = 1 AND did_add_payment_info = 1
					THEN did_purchase ELSE 0 END AS seq_purchase
			FROM session_events
		)
		SELECT
			event_date,
			COUNT(*)                             AS session_start_users,
			COUNTIF(seq_view_item_list = 1)      AS view_item_list_users,
			COUNTIF(seq_select_item = 1)         AS select_item_users,
			COUNTIF(seq_view_item = 1)           AS view_item_users,
			COUNTIF(seq_size_selected = 1)       AS size_selected_users,
			COUNTIF(seq_add_to_cart = 1)         AS add_to_cart_users,
			COUNTIF(seq_begin_checkout = 1)      AS begin_checkout_users,
			COUNTIF(seq_add_shipping_info = 1)   AS add_shipping_info_users,
			COUNTIF(seq_add_payment_info = 1)    AS add_payment_info_users,
			COUNTIF(seq_purchase = 1)            AS purchase_users
		FROM sequential_funnel
		GROUP BY event_date
	`, src, c.dateFilterSQL(startDate, endDate))
}

func (c *Client) getFunnelAnalysis(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DailyFunnel, error) {
	slog.Default().InfoContext(ctx, "bq funnel query start",
		slog.String("query", "getFunnelAnalysis"),
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")),
		slog.String("project_id", c.projectID),
		slog.String("dataset_id", c.datasetID))

	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetFunnelAnalysis: %w", err)
	}
	sql := c.buildFunnelAnalysisSQL(src, startDate, endDate)

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
		slog.Default().InfoContext(ctx, "bq query params",
			slog.String("start_date_rfc3339", startDate.Format(time.RFC3339)),
			slog.String("end_date_rfc3339", endDate.Format(time.RFC3339)))
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetFunnelAnalysis: %w", err)
	}

	var rows []entity.DailyFunnel
	for {
		var r struct {
			EventDate            civil.Date `bigquery:"event_date"`
			SessionStartUsers    int64      `bigquery:"session_start_users"`
			ViewItemListUsers    int64      `bigquery:"view_item_list_users"`
			SelectItemUsers      int64      `bigquery:"select_item_users"`
			ViewItemUsers        int64      `bigquery:"view_item_users"`
			SizeSelectedUsers    int64      `bigquery:"size_selected_users"`
			AddToCartUsers       int64      `bigquery:"add_to_cart_users"`
			BeginCheckoutUsers   int64      `bigquery:"begin_checkout_users"`
			AddShippingInfoUsers int64      `bigquery:"add_shipping_info_users"`
			AddPaymentInfoUsers  int64      `bigquery:"add_payment_info_users"`
			PurchaseUsers        int64      `bigquery:"purchase_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetFunnelAnalysis iterate: %w", err)
		}
		rows = append(rows, entity.DailyFunnel{
			Date: civilDateToTime(r.EventDate),
			FunnelSteps: entity.FunnelSteps{
				SessionStartUsers:    ClampInt64(r.SessionStartUsers),
				ViewItemListUsers:    ClampInt64(r.ViewItemListUsers),
				SelectItemUsers:      ClampInt64(r.SelectItemUsers),
				ViewItemUsers:        ClampInt64(r.ViewItemUsers),
				SizeSelectedUsers:    ClampInt64(r.SizeSelectedUsers),
				AddToCartUsers:       ClampInt64(r.AddToCartUsers),
				BeginCheckoutUsers:   ClampInt64(r.BeginCheckoutUsers),
				AddShippingInfoUsers: ClampInt64(r.AddShippingInfoUsers),
				AddPaymentInfoUsers:  ClampInt64(r.AddPaymentInfoUsers),
				PurchaseUsers:        ClampInt64(r.PurchaseUsers),
			},
		})
	}
	slog.Default().InfoContext(ctx, "bq funnel query done",
		slog.String("query", "getFunnelAnalysis"),
		slog.Int("rows_returned", len(rows)))
	return rows, nil
}

func (c *Client) getFunnelAnalysisStream(
	ctx context.Context,
	startDate, endDate time.Time,
	batchSize int,
	fn func([]entity.DailyFunnel) error,
) error {
	slog.Default().InfoContext(ctx, "bq funnel stream query start",
		slog.String("query", "getFunnelAnalysisStream"),
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")),
		slog.String("project_id", c.projectID),
		slog.String("dataset_id", c.datasetID))

	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "event_params", "event_name")
	if err != nil {
		return fmt.Errorf("GetFunnelAnalysisStream: %w", err)
	}
	sql := c.buildFunnelAnalysisSQL(src, startDate, endDate)

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "start_date", Value: startDate},
			{Name: "end_date", Value: endDate},
		}
		slog.Default().InfoContext(ctx, "bq query params",
			slog.String("start_date_rfc3339", startDate.Format(time.RFC3339)),
			slog.String("end_date_rfc3339", endDate.Format(time.RFC3339)))
	}

	it, err := query.Read(ctx)
	if err != nil {
		return fmt.Errorf("GetFunnelAnalysisStream: %w", err)
	}

	batch := make([]entity.DailyFunnel, 0, batchSize)
	var totalRows int
	for {
		var r struct {
			EventDate            civil.Date `bigquery:"event_date"`
			SessionStartUsers    int64      `bigquery:"session_start_users"`
			ViewItemListUsers    int64      `bigquery:"view_item_list_users"`
			SelectItemUsers      int64      `bigquery:"select_item_users"`
			ViewItemUsers        int64      `bigquery:"view_item_users"`
			SizeSelectedUsers    int64      `bigquery:"size_selected_users"`
			AddToCartUsers       int64      `bigquery:"add_to_cart_users"`
			BeginCheckoutUsers   int64      `bigquery:"begin_checkout_users"`
			AddShippingInfoUsers int64      `bigquery:"add_shipping_info_users"`
			AddPaymentInfoUsers  int64      `bigquery:"add_payment_info_users"`
			PurchaseUsers        int64      `bigquery:"purchase_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return fmt.Errorf("GetFunnelAnalysisStream iterate: %w", err)
		}
		batch = append(batch, entity.DailyFunnel{
			Date: civilDateToTime(r.EventDate),
			FunnelSteps: entity.FunnelSteps{
				SessionStartUsers:    ClampInt64(r.SessionStartUsers),
				ViewItemListUsers:    ClampInt64(r.ViewItemListUsers),
				SelectItemUsers:      ClampInt64(r.SelectItemUsers),
				ViewItemUsers:        ClampInt64(r.ViewItemUsers),
				SizeSelectedUsers:    ClampInt64(r.SizeSelectedUsers),
				AddToCartUsers:       ClampInt64(r.AddToCartUsers),
				BeginCheckoutUsers:   ClampInt64(r.BeginCheckoutUsers),
				AddShippingInfoUsers: ClampInt64(r.AddShippingInfoUsers),
				AddPaymentInfoUsers:  ClampInt64(r.AddPaymentInfoUsers),
				PurchaseUsers:        ClampInt64(r.PurchaseUsers),
			},
		})
		totalRows++
		if len(batch) >= batchSize {
			if err := fn(batch); err != nil {
				return err
			}
			batch = make([]entity.DailyFunnel, 0, batchSize)
		}
	}
	if len(batch) > 0 {
		if err := fn(batch); err != nil {
			return err
		}
	}
	slog.Default().InfoContext(ctx, "bq funnel stream query done",
		slog.String("query", "getFunnelAnalysisStream"),
		slog.Int("rows_returned", totalRows))
	return nil
}

// GetDeviceFunnel returns funnel metrics segmented by device category per day.
// Enforces sequential funnel logic (session → add_to_cart → checkout → purchase)
// so totals align with GetFunnelAnalysis.
func (c *Client) GetDeviceFunnel(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DeviceFunnelMetric, error) {

	var result []entity.DeviceFunnelMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getDeviceFunnel(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getDeviceFunnel(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.DeviceFunnelMetric, error) {
	slog.Default().InfoContext(ctx, "bq device funnel query start",
		slog.String("query", "getDeviceFunnel"),
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")),
		slog.String("project_id", c.projectID),
		slog.String("dataset_id", c.datasetID))

	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "user_pseudo_id", "event_timestamp", "device.category AS device_category", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetDeviceFunnel: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH device_events AS (
			SELECT
				user_pseudo_id,
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				device_category,
				event_name
			FROM %s
			WHERE %s
				AND event_name IN (
					'session_start', 'add_to_cart',
					'begin_checkout', 'purchase'
				)
		),
		daily_user_events AS (
			SELECT
				event_date,
				IFNULL(device_category, 'unknown') AS device_category,
				user_pseudo_id,
				MAX(CASE WHEN event_name = 'session_start'  THEN 1 ELSE 0 END) AS did_session,
				MAX(CASE WHEN event_name = 'add_to_cart'    THEN 1 ELSE 0 END) AS did_atc,
				MAX(CASE WHEN event_name = 'begin_checkout' THEN 1 ELSE 0 END) AS did_checkout,
				MAX(CASE WHEN event_name = 'purchase'       THEN 1 ELSE 0 END) AS did_purchase
			FROM device_events
			GROUP BY event_date, device_category, user_pseudo_id
		),
		sequential_funnel AS (
			SELECT
				event_date,
				device_category,
				user_pseudo_id,
				did_session,
				CASE WHEN did_session = 1 THEN did_atc ELSE 0 END AS seq_atc,
				CASE WHEN did_session = 1 AND did_atc = 1 THEN did_checkout ELSE 0 END AS seq_checkout,
				CASE WHEN did_session = 1 AND did_atc = 1 AND did_checkout = 1 THEN did_purchase ELSE 0 END AS seq_purchase
			FROM daily_user_events
		)
		SELECT
			event_date,
			device_category,
			COUNTIF(did_session = 1)    AS sessions,
			COUNTIF(seq_atc = 1)        AS add_to_cart_users,
			COUNTIF(seq_checkout = 1)   AS checkout_users,
			COUNTIF(seq_purchase = 1)   AS purchase_users
		FROM sequential_funnel
		GROUP BY event_date, device_category
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
		return nil, fmt.Errorf("GetDeviceFunnel: %w", err)
	}

	var rows []entity.DeviceFunnelMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			DeviceCategory string     `bigquery:"device_category"`
			Sessions       int64      `bigquery:"sessions"`
			AddToCartUsers int64      `bigquery:"add_to_cart_users"`
			CheckoutUsers  int64      `bigquery:"checkout_users"`
			PurchaseUsers  int64      `bigquery:"purchase_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetDeviceFunnel iterate: %w", err)
		}
		rows = append(rows, entity.DeviceFunnelMetric{
			Date:           civilDateToTime(r.EventDate),
			DeviceCategory: r.DeviceCategory,
			Sessions:       ClampInt64(r.Sessions),
			AddToCartUsers: ClampInt64(r.AddToCartUsers),
			CheckoutUsers:  ClampInt64(r.CheckoutUsers),
			PurchaseUsers:  ClampInt64(r.PurchaseUsers),
		})
	}
	slog.Default().InfoContext(ctx, "bq device funnel query done",
		slog.String("query", "getDeviceFunnel"),
		slog.Int("rows_returned", len(rows)))
	return rows, nil
}

// heroFunnelPurchaseLookaheadDays is how long after the first hero_click (same user, UTC day)
// we still count a client-side purchase toward that hero day.
const heroFunnelPurchaseLookaheadDays = 7

// GetHeroFunnel returns hero_click → view_item → purchase mini-funnel per calendar day (UTC).
//
// Semantics (fixed from same-day bag-of-events):
//   - hero_click_users: distinct users with hero_click on that date.
//   - view_item_users: among those, users with at least one view_item on the same UTC date
//     at or after their first hero_click timestamp that day.
//   - purchase_users: among those with view_item as above, users with at least one
//     client-side purchase (excludes Measurement Protocol purchases tagged server_side)
//     at or after first hero_click and within heroFunnelPurchaseLookaheadDays.
//
// The events scan extends endDate by heroFunnelPurchaseLookaheadDays so delayed purchases
// are visible; output rows are still limited to [startDate, endDate].
func (c *Client) GetHeroFunnel(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.HeroFunnelMetric, error) {

	var result []entity.HeroFunnelMetric
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getHeroFunnel(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getHeroFunnel(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.HeroFunnelMetric, error) {
	slog.Default().InfoContext(ctx, "bq hero funnel query start",
		slog.String("query", "getHeroFunnel"),
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")),
		slog.String("project_id", c.projectID),
		slog.String("dataset_id", c.datasetID))

	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	scanEnd := endDate.AddDate(0, 0, heroFunnelPurchaseLookaheadDays)
	src, err := c.eventsSourceColumns(startDate, scanEnd, "user_pseudo_id", "event_timestamp", "event_name", "event_params")
	if err != nil {
		return nil, fmt.Errorf("GetHeroFunnel: %w", err)
	}

	var scanWhere, outWhere string
	if c.useLiteralDates {
		scanWhere = fmt.Sprintf(
			`DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE('%s') AND DATE('%s')`,
			startDate.UTC().Format("2006-01-02"),
			scanEnd.UTC().Format("2006-01-02"),
		)
		outWhere = fmt.Sprintf(
			`event_date BETWEEN DATE('%s') AND DATE('%s')`,
			startDate.UTC().Format("2006-01-02"),
			endDate.UTC().Format("2006-01-02"),
		)
	} else {
		scanWhere = `DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE(@scan_start) AND DATE(@scan_end)`
		outWhere = `event_date BETWEEN DATE(@out_start) AND DATE(@out_end)`
	}

	sql := fmt.Sprintf(`
		WITH raw AS (
			SELECT
				user_pseudo_id,
				event_timestamp,
				event_name
			FROM %s
			WHERE %s
				AND event_name IN ('hero_click', 'view_item', 'purchase')
				AND (
					event_name != 'purchase'
					OR NOT (
						COALESCE(
							(SELECT ep.value.int_value FROM UNNEST(event_params) ep WHERE ep.key = 'server_side' LIMIT 1),
							0
						) = 1
						OR LOWER(COALESCE(
							(SELECT ep.value.string_value FROM UNNEST(event_params) ep WHERE ep.key = 'server_side' LIMIT 1),
							''
						)) IN ('true', '1')
					)
				)
		),
		hero AS (
			SELECT
				user_pseudo_id,
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS hero_date,
				MIN(event_timestamp) AS first_hero_ts
			FROM raw
			WHERE event_name = 'hero_click'
			GROUP BY user_pseudo_id, hero_date
		),
		user_funnel AS (
			SELECT
				h.hero_date AS event_date,
				h.user_pseudo_id,
				EXISTS (
					SELECT 1 FROM raw r
					WHERE r.user_pseudo_id = h.user_pseudo_id
						AND r.event_name = 'view_item'
						AND r.event_timestamp >= h.first_hero_ts
						AND DATE(TIMESTAMP_MICROS(r.event_timestamp)) = h.hero_date
				) AS after_view_item,
				EXISTS (
					SELECT 1 FROM raw r
					WHERE r.user_pseudo_id = h.user_pseudo_id
						AND r.event_name = 'purchase'
						AND r.event_timestamp >= h.first_hero_ts
						AND TIMESTAMP_MICROS(r.event_timestamp) <= TIMESTAMP_ADD(TIMESTAMP_MICROS(h.first_hero_ts), INTERVAL %d DAY)
				) AS after_purchase
			FROM hero h
		)
		SELECT
			event_date,
			COUNT(*) AS hero_click_users,
			COUNTIF(after_view_item) AS view_item_users,
			COUNTIF(after_view_item AND after_purchase) AS purchase_users
		FROM user_funnel
		WHERE %s
		GROUP BY event_date
	`, src, scanWhere, heroFunnelPurchaseLookaheadDays, outWhere)

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "scan_start", Value: startDate},
			{Name: "scan_end", Value: scanEnd},
			{Name: "out_start", Value: startDate},
			{Name: "out_end", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetHeroFunnel: %w", err)
	}

	var rows []entity.HeroFunnelMetric
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			HeroClickUsers int64      `bigquery:"hero_click_users"`
			ViewItemUsers  int64      `bigquery:"view_item_users"`
			PurchaseUsers  int64      `bigquery:"purchase_users"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetHeroFunnel iterate: %w", err)
		}
		rows = append(rows, entity.HeroFunnelMetric{
			Date:           civilDateToTime(r.EventDate),
			HeroClickUsers: ClampInt64(r.HeroClickUsers),
			ViewItemUsers:  ClampInt64(r.ViewItemUsers),
			PurchaseUsers:  ClampInt64(r.PurchaseUsers),
		})
	}
	slog.Default().InfoContext(ctx, "bq hero funnel query done",
		slog.String("query", "getHeroFunnel"),
		slog.Int("rows_returned", len(rows)))
	return rows, nil
}

// GetHeroFunnelAggregate returns period-level unique users for the hero funnel.
// Unlike GetHeroFunnel which returns daily unique users, this counts each user_pseudo_id
// only once across the entire period, providing accurate period-level metrics.
func (c *Client) GetHeroFunnelAggregate(
	ctx context.Context,
	startDate, endDate time.Time,
) (*entity.HeroFunnelAggregate, error) {
	var result *entity.HeroFunnelAggregate
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		agg, err := c.getHeroFunnelAggregate(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = agg
		return nil
	})
	return result, err
}

func (c *Client) getHeroFunnelAggregate(
	ctx context.Context,
	startDate, endDate time.Time,
) (*entity.HeroFunnelAggregate, error) {
	slog.Default().InfoContext(ctx, "bq hero funnel aggregate query start",
		slog.String("query", "getHeroFunnelAggregate"),
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")),
		slog.String("project_id", c.projectID),
		slog.String("dataset_id", c.datasetID))

	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	scanEnd := endDate.AddDate(0, 0, heroFunnelPurchaseLookaheadDays)
	src, err := c.eventsSourceColumns(startDate, scanEnd, "user_pseudo_id", "event_timestamp", "event_name", "event_params")
	if err != nil {
		return nil, fmt.Errorf("getHeroFunnelAggregate: %w", err)
	}

	var scanWhere, heroWhere, viewWhere string
	if c.useLiteralDates {
		scanWhere = fmt.Sprintf(
			`DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE('%s') AND DATE('%s')`,
			startDate.UTC().Format("2006-01-02"),
			scanEnd.UTC().Format("2006-01-02"),
		)
		heroWhere = fmt.Sprintf(
			`DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE('%s') AND DATE('%s')`,
			startDate.UTC().Format("2006-01-02"),
			endDate.UTC().Format("2006-01-02"),
		)
		viewWhere = fmt.Sprintf(
			`DATE(TIMESTAMP_MICROS(r.event_timestamp)) BETWEEN DATE('%s') AND DATE('%s')`,
			startDate.UTC().Format("2006-01-02"),
			endDate.UTC().Format("2006-01-02"),
		)
	} else {
		scanWhere = `DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE(@scan_start) AND DATE(@scan_end)`
		heroWhere = `DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE(@out_start) AND DATE(@out_end)`
		viewWhere = `DATE(TIMESTAMP_MICROS(r.event_timestamp)) BETWEEN DATE(@out_start) AND DATE(@out_end)`
	}

	// Groups by user_pseudo_id across the full period (not per date) so each user
	// is counted at most once, giving true period-level unique user counts.
	sql := fmt.Sprintf(`
		WITH raw AS (
			SELECT
				user_pseudo_id,
				event_timestamp,
				event_name
			FROM %s
			WHERE %s
				AND event_name IN ('hero_click', 'view_item', 'purchase')
				AND (
					event_name != 'purchase'
					OR NOT (
						COALESCE(
							(SELECT ep.value.int_value FROM UNNEST(event_params) ep WHERE ep.key = 'server_side' LIMIT 1),
							0
						) = 1
						OR LOWER(COALESCE(
							(SELECT ep.value.string_value FROM UNNEST(event_params) ep WHERE ep.key = 'server_side' LIMIT 1),
							''
						)) IN ('true', '1')
					)
				)
		),
		hero AS (
			SELECT
				user_pseudo_id,
				MIN(event_timestamp) AS first_hero_ts
			FROM raw
			WHERE event_name = 'hero_click'
				AND %s
			GROUP BY user_pseudo_id
		),
		user_funnel AS (
			SELECT
				h.user_pseudo_id,
				EXISTS (
					SELECT 1 FROM raw r
					WHERE r.user_pseudo_id = h.user_pseudo_id
						AND r.event_name = 'view_item'
						AND r.event_timestamp >= h.first_hero_ts
						AND %s
				) AS after_view_item,
				EXISTS (
					SELECT 1 FROM raw r
					WHERE r.user_pseudo_id = h.user_pseudo_id
						AND r.event_name = 'purchase'
						AND r.event_timestamp >= h.first_hero_ts
						AND TIMESTAMP_MICROS(r.event_timestamp) <= TIMESTAMP_ADD(TIMESTAMP_MICROS(h.first_hero_ts), INTERVAL %d DAY)
				) AS after_purchase
			FROM hero h
		)
		SELECT
			COUNT(*) AS hero_click_users,
			COUNTIF(after_view_item) AS view_item_users,
			COUNTIF(after_view_item AND after_purchase) AS purchase_users
		FROM user_funnel
	`, src, scanWhere, heroWhere, viewWhere, heroFunnelPurchaseLookaheadDays)

	query := c.client.Query(sql)
	if !c.useLiteralDates {
		query.Parameters = []bigquery.QueryParameter{
			{Name: "scan_start", Value: startDate},
			{Name: "scan_end", Value: scanEnd},
			{Name: "out_start", Value: startDate},
			{Name: "out_end", Value: endDate},
		}
	}

	it, err := query.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("getHeroFunnelAggregate: %w", err)
	}

	var r struct {
		HeroClickUsers int64 `bigquery:"hero_click_users"`
		ViewItemUsers  int64 `bigquery:"view_item_users"`
		PurchaseUsers  int64 `bigquery:"purchase_users"`
	}
	if err := it.Next(&r); err == iterator.Done {
		return &entity.HeroFunnelAggregate{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("getHeroFunnelAggregate iterate: %w", err)
	}

	slog.Default().InfoContext(ctx, "bq hero funnel aggregate query done",
		slog.String("query", "getHeroFunnelAggregate"),
		slog.Int64("hero_click_users", r.HeroClickUsers))

	return &entity.HeroFunnelAggregate{
		HeroClickUsers: ClampInt64(r.HeroClickUsers),
		ViewItemUsers:  ClampInt64(r.ViewItemUsers),
		PurchaseUsers:  ClampInt64(r.PurchaseUsers),
	}, nil
}

// GetAbandonedCart measures cart abandonment: add_to_cart users who never start checkout.
func (c *Client) GetAbandonedCart(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.AbandonedCartRow, error) {

	var result []entity.AbandonedCartRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getAbandonedCart(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getAbandonedCart(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.AbandonedCartRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_params", "event_name")
	if err != nil {
		return nil, fmt.Errorf("GetAbandonedCart: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH user_sessions AS (
			SELECT
				DATE(TIMESTAMP_MICROS(event_timestamp)) AS event_date,
				user_pseudo_id,
				(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'ga_session_id') AS session_id,
				event_name,
				event_timestamp
			FROM %s
			WHERE %s
				AND event_name IN ('add_to_cart', 'begin_checkout', 'purchase')
		),
		session_summary AS (
			SELECT
				event_date,
				user_pseudo_id,
				session_id,
				MAX(CASE WHEN event_name = 'add_to_cart'    THEN 1 ELSE 0 END) AS did_atc,
				MAX(CASE WHEN event_name = 'begin_checkout' THEN 1 ELSE 0 END) AS did_checkout,
				MAX(CASE WHEN event_name = 'purchase'       THEN 1 ELSE 0 END) AS did_purchase,
				MIN(CASE WHEN event_name = 'add_to_cart'    THEN event_timestamp END) AS atc_ts,
				MIN(CASE WHEN event_name = 'begin_checkout' THEN event_timestamp END) AS checkout_ts,
				MAX(event_timestamp) AS last_event_ts
			FROM user_sessions
			GROUP BY event_date, user_pseudo_id, session_id
		)
		SELECT
			event_date,
			COUNTIF(did_atc = 1) AS carts_started,
			COUNTIF(did_atc = 1 AND did_checkout = 1) AS checkouts_started,
			COALESCE(SAFE_DIVIDE(
				COUNTIF(did_atc = 1 AND did_checkout = 0),
				COUNTIF(did_atc = 1)
			), 0) AS abandonment_rate,
			COALESCE(AVG(CASE WHEN did_checkout = 1 AND checkout_ts > atc_ts
				THEN (checkout_ts - atc_ts) / 60000000.0
			END), 0) AS avg_minutes_to_checkout,
			COALESCE(AVG(CASE WHEN did_atc = 1 AND did_checkout = 0
				THEN (last_event_ts - atc_ts) / 60000000.0
			END), 0) AS avg_minutes_to_abandon
		FROM session_summary
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
		return nil, fmt.Errorf("GetAbandonedCart: %w", err)
	}

	var rows []entity.AbandonedCartRow
	for {
		var r struct {
			EventDate            civil.Date `bigquery:"event_date"`
			CartsStarted         int64      `bigquery:"carts_started"`
			CheckoutsStarted     int64      `bigquery:"checkouts_started"`
			AbandonmentRate      float64   `bigquery:"abandonment_rate"`
			AvgMinutesToCheckout float64   `bigquery:"avg_minutes_to_checkout"`
			AvgMinutesToAbandon  float64   `bigquery:"avg_minutes_to_abandon"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetAbandonedCart iterate: %w", err)
		}
		rows = append(rows, entity.AbandonedCartRow{
			Date:                 civilDateToTime(r.EventDate),
			CartsStarted:         ClampInt64(r.CartsStarted),
			CheckoutsStarted:     ClampInt64(r.CheckoutsStarted),
			AbandonmentRate:      SanitizeRate(r.AbandonmentRate),
			AvgMinutesToCheckout: SanitizeFloat64(r.AvgMinutesToCheckout),
			AvgMinutesToAbandon:  SanitizeFloat64(r.AvgMinutesToAbandon),
		})
	}
	return rows, nil
}

// GetAddToCartRate returns per-product view→add_to_cart conversion per day.
// Uses GA4 ecommerce items[] array (item_id, item_name) not event_params.
func (c *Client) GetAddToCartRate(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.AddToCartRateRow, error) {

	var result []entity.AddToCartRateRow
	err := c.withCircuitBreaker(ctx, func(ctx context.Context) error {
		rows, err := c.getAddToCartRate(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = rows
		return nil
	})
	return result, err
}

func (c *Client) getAddToCartRate(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]entity.AddToCartRateRow, error) {
	ctx, cancel := c.queryContext(ctx)
	defer cancel()
	src, err := c.eventsSourceColumns(startDate, endDate, "event_timestamp", "user_pseudo_id", "event_name", "items")
	if err != nil {
		return nil, fmt.Errorf("GetAddToCartRate: %w", err)
	}
	sql := fmt.Sprintf(`
		WITH product_events AS (
			SELECT
				DATE(TIMESTAMP_MICROS(e.event_timestamp)) AS event_date,
				item.item_id AS product_id,
				item.item_name AS product_name,
				e.user_pseudo_id,
				e.event_name
			FROM %s AS e, UNNEST(e.items) AS item
			WHERE %s
				AND e.event_name IN ('view_item', 'add_to_cart')
				AND item.item_id IS NOT NULL
		),
		daily AS (
			SELECT
				event_date,
				product_id,
				ANY_VALUE(product_name) AS product_name,
				user_pseudo_id,
				MAX(CASE WHEN event_name = 'view_item'   THEN 1 ELSE 0 END) AS did_view,
				MAX(CASE WHEN event_name = 'add_to_cart' THEN 1 ELSE 0 END) AS did_atc
			FROM product_events
			GROUP BY event_date, product_id, user_pseudo_id
		)
		SELECT
			event_date,
			product_id,
			ANY_VALUE(product_name) AS product_name,
			COUNTIF(did_view = 1) AS view_count,
			COUNTIF(did_atc = 1) AS add_to_cart_count,
			SAFE_DIVIDE(COUNTIF(did_atc = 1), COUNTIF(did_view = 1)) AS cart_rate
		FROM daily
		GROUP BY event_date, product_id
		HAVING view_count > 0
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
		return nil, fmt.Errorf("GetAddToCartRate: %w", err)
	}

	var rows []entity.AddToCartRateRow
	for {
		var r struct {
			EventDate      civil.Date `bigquery:"event_date"`
			ProductID      string     `bigquery:"product_id"`
			ProductName    string     `bigquery:"product_name"`
			ViewCount      int64      `bigquery:"view_count"`
			AddToCartCount int64      `bigquery:"add_to_cart_count"`
			CartRate       float64    `bigquery:"cart_rate"`
		}
		if err := it.Next(&r); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GetAddToCartRate iterate: %w", err)
		}
		rows = append(rows, entity.AddToCartRateRow{
			Date:           civilDateToTime(r.EventDate),
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			ViewCount:      ClampInt64(r.ViewCount),
			AddToCartCount: ClampInt64(r.AddToCartCount),
			CartRate:       SanitizeRate(r.CartRate),
		})
	}
	return rows, nil
}
