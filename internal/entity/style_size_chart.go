package entity

import "github.com/shopspring/decimal"

// StyleSizeChartCell is one measurement value in a style's size chart (R5): the value of a named
// measurement at a given size. Persisted to tech_card_size_measurement, keyed by the style
// (tech_card_id) — the chart is style-owned, shared by every colourway of the style.
type StyleSizeChartCell struct {
	SizeID            int             `db:"size_id"`
	MeasurementNameID int             `db:"measurement_name_id"`
	Value             decimal.Decimal `db:"measurement_value"`
}

// StyleSizeChart is a style's full size chart plus the shared optimistic-lock token (R5). It is written
// full-replace under tech_card.lock_version; there is no separate chart version.
type StyleSizeChart struct {
	StyleID     int
	LockVersion int
	Cells       []StyleSizeChartCell
}
