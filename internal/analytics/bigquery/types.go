package bigquery

import "time"

// SizeIntentRow represents size selection intent from the size_selected custom event.
type SizeIntentRow struct {
	Date       time.Time
	ProductID  string
	SizeID     int
	SizeName   string
	SizeClicks int64
}
