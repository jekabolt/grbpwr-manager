package storeutil

import (
	"fmt"
	"strings"
	"time"
)

// DefaultBQPageLimit is the default limit when 0 is passed to paginated BQ reads.
const DefaultBQPageLimit = 500

// BQPageParams holds limit/offset for paginated BQ cache reads.
type BQPageParams struct {
	Limit  int // 0 = DefaultBQPageLimit
	Offset int // must be >= 0
}

// EffectiveLimit returns the limit to use, defaulting to DefaultBQPageLimit.
func (p BQPageParams) EffectiveLimit() int {
	if p.Limit <= 0 {
		return DefaultBQPageLimit
	}
	return p.Limit
}

// EffectiveOffset returns the offset to use, defaulting to 0.
func (p BQPageParams) EffectiveOffset() int {
	if p.Offset < 0 {
		return 0
	}
	return p.Offset
}

// PagePathMaxLen matches varchar(512) in bq_exceptions and bq_not_found_pages.
const PagePathMaxLen = 512

// TruncatePagePath ensures page_path fits varchar(512). GA4 page_location can be long.
func TruncatePagePath(s string) string {
	if len(s) <= PagePathMaxLen {
		return s
	}
	return s[:PagePathMaxLen]
}

// JoinInts returns a comma-separated string of ints for SQL IN clause.
func JoinInts(ids []int) string {
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

// ParseDateStr parses a date string from MySQL (DATE or DATETIME column).
// Handles both "2006-01-02" and "2006-01-02 15:00:00" formats to avoid silent
// parse failures when MySQL returns datetime for a date column.
func ParseDateStr(s string) (time.Time, error) {
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
