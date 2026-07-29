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

// StyleSizeChartGradeStep is one measurement's grade increment — the per-size-position step the
// expanded chart was authored from. Persisted to tech_card_grade_rule, keyed by the style.
type StyleSizeChartGradeStep struct {
	MeasurementNameID int             `db:"measurement_name_id"`
	Step              decimal.Decimal `db:"step"`
}

// StyleSizeChart is a style's full size chart plus the shared optimistic-lock token (R5). It is written
// full-replace under tech_card.lock_version; there is no separate chart version.
//
// GradeBaseSizeID + GradeSteps are the authoring rule the expanded Cells grid came from
// (value = base + step × position delta). Cells stays the source of truth: a cell the grader
// overtyped keeps its typed value, and whether a cell still follows the rule is derived by
// recomputing it, never stored. Zero base + empty steps = no rule (a hand-typed chart).
type StyleSizeChart struct {
	StyleID         int
	LockVersion     int
	Cells           []StyleSizeChartCell
	GradeBaseSizeID int
	GradeSteps      []StyleSizeChartGradeStep
}
