package store

import (
	"fmt"
	"strings"
	"time"
)

// pagePathMaxLen matches varchar(512) in bq_exceptions and bq_not_found_pages.
// Exceptions and 404s use page_location from GA4 (full URL); long URLs may exceed this.
const pagePathMaxLen = 512

// truncatePagePath ensures page_path fits varchar(512). GA4 page_location can be long.
func truncatePagePath(s string) string {
	if len(s) <= pagePathMaxLen {
		return s
	}
	return s[:pagePathMaxLen]
}

// joinInts returns a comma-separated string of ints for SQL IN clause.
func joinInts(ids []int) string {
	if len(ids) == 0 {
		return "0"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(s, ",")
}

// MySQL date formats: DATE returns "2006-01-02", DATETIME returns "2006-01-02 15:04:05" or with fractional seconds.
// With parseTime=true, the driver converts DATE to time.Time then to string in RFC3339 format ("2006-01-02T15:04:05Z").
var dateFormats = []string{
	"2006-01-02",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999",
	time.RFC3339,
	time.RFC3339Nano,
}

// parseDateStr parses a date string from MySQL (DATE or DATETIME column).
// Handles both "2006-01-02" and "2006-01-02 15:00:00" formats to avoid silent
// parse failures when MySQL returns datetime for a date column.
func parseDateStr(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range dateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse date %q: no matching format", s)
}
