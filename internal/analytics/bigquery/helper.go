package bigquery

import (
	"time"

	"cloud.google.com/go/civil"
)

// civilDateToTime converts a BigQuery DATE (civil.Date) to time.Time at midnight UTC.
func civilDateToTime(d civil.Date) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// dateFilterSQL returns a WHERE-compatible date range clause.
// When useLiteralDates is true, embeds literal dates to avoid BigQuery parameter serialization issues.
// Otherwise uses @start_date and @end_date parameters.
func (c *Client) dateFilterSQL(startDate, endDate time.Time) string {
	if c.useLiteralDates {
		startStr := startDate.UTC().Format("2006-01-02")
		endStr := endDate.UTC().Format("2006-01-02")
		return "DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE('" + startStr + "') AND DATE('" + endStr + "')"
	}
	return `DATE(TIMESTAMP_MICROS(event_timestamp)) BETWEEN DATE(@start_date) AND DATE(@end_date)`
}
