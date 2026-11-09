package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	StyleSizeChartCellSourceManual  = "manual"
	StyleSizeChartCellSourcePattern = "pattern"
)

// Sources of the size chart picture (tech_card_size_chart_picture.source, 0459).
const (
	StyleSizeChartPictureSourceIcon   = "icon"
	StyleSizeChartPictureSourceSketch = "sketch"
	StyleSizeChartPictureSourceUpload = "upload"

	// StyleSizeChartPictureMaxLines caps the free lines of one picture.
	StyleSizeChartPictureMaxLines = 200
	// styleSizeChartIconKeyMaxLen is tech_card_size_chart_picture.icon_key VARCHAR(64).
	styleSizeChartIconKeyMaxLen = 64
	// styleSizeChartLineScale is the stored scale of a line coordinate, DECIMAL(6,5).
	styleSizeChartLineScale = 1e5
)

// StyleSizeChartCell is one measurement value in a style's size chart (R5): the value of a named
// measurement at a given size. Persisted to tech_card_size_measurement, keyed by the style
// (tech_card_id) — the chart is style-owned, shared by every colourway of the style.
type StyleSizeChartCell struct {
	SizeID            int             `db:"size_id"`
	MeasurementNameID int             `db:"measurement_name_id"`
	Value             decimal.Decimal `db:"measurement_value"`
	Source            string          `db:"source"`
}

// StyleSizeChartPoint is the row metadata shared by every size cell of one named measurement.
// SourceFingerprint is server-owned persistence state; Stale is recomputed on every read.
type StyleSizeChartPoint struct {
	MeasurementNameID  int
	PomCode            string
	Position           int
	TolMinusCm         decimal.Decimal
	TolPlusCm          decimal.Decimal
	InShop             bool
	BasisPieceLineKeys []string
	SourceFingerprint  string
	Stale              bool
}

// StyleSizeChartGradeStep is one measurement's grade increment — the per-size-position step the
// expanded chart was authored from. Persisted to tech_card_grade_rule, keyed by the style.
type StyleSizeChartGradeStep struct {
	MeasurementNameID int             `db:"measurement_name_id"`
	Step              decimal.Decimal `db:"step"`
}

// StyleSizeChartLine is one free line drawn on the size chart picture, in coordinates normalised to
// the picture box (0..1). MeasurementNameID 0 = the user has not picked the point yet (stored NULL).
// Lines predrawn by an icon are not stored: they come from the client icon library.
type StyleSizeChartLine struct {
	MeasurementNameID int
	X1, Y1, X2, Y2    float64
	Position          int
}

// StyleSizeChartPicture is the drawing next to the size chart table (0459). Source "" means no picture.
// IconKey is kept only for an icon, MediaID only for a sketch or an upload. MediaURL is read-only:
// the full-size url of MediaID, "" when the image was deleted from the library (media_id SET NULL).
type StyleSizeChartPicture struct {
	Source   string
	IconKey  string
	MediaID  int
	MediaURL string
	Lines    []StyleSizeChartLine
}

// ValidateStyleSizeChartPicture checks the picture rules that need no database and normalises the
// picture in place, so what is stored and what is returned agree: the source and icon key are
// trimmed, the field of the other source is cleared, coordinates are rounded to the stored five
// decimals, and positions become 1..n in the sent order (stable by position). A picture with an
// empty source is "no picture" and may carry nothing else. Whether media_id and the lines'
// measurement names exist is the store's check.
func ValidateStyleSizeChartPicture(p *StyleSizeChartPicture) error {
	if p == nil {
		return nil
	}
	p.Source = strings.TrimSpace(p.Source)
	p.IconKey = strings.TrimSpace(p.IconKey)
	p.MediaURL = ""
	switch p.Source {
	case "":
		if p.IconKey != "" || p.MediaID != 0 || len(p.Lines) > 0 {
			return NewFieldViolation("picture.source", "required", "", "icon, sketch or upload")
		}
		return nil
	case StyleSizeChartPictureSourceIcon:
		p.MediaID = 0
		if p.IconKey == "" {
			return NewFieldViolation("picture.icon_key", "required", "", "an icon picture names its icon")
		}
		if len(p.IconKey) > styleSizeChartIconKeyMaxLen || !iconKeyValid(p.IconKey) {
			return NewFieldViolation("picture.icon_key", "invalid", p.IconKey,
				"up to 64 lowercase letters, digits, hyphens or underscores")
		}
	case StyleSizeChartPictureSourceSketch, StyleSizeChartPictureSourceUpload:
		p.IconKey = ""
		if p.MediaID <= 0 {
			return NewFieldViolation("picture.media_id", "required", "", "a sketch or an upload names its image")
		}
	default:
		return NewFieldViolation("picture.source", "invalid", p.Source, "icon, sketch or upload")
	}
	if len(p.Lines) > StyleSizeChartPictureMaxLines {
		return NewFieldViolation("picture.lines", "too_many", fmt.Sprintf("%d lines", len(p.Lines)),
			fmt.Sprintf("at most %d lines", StyleSizeChartPictureMaxLines))
	}
	for i := range p.Lines {
		l := &p.Lines[i]
		at := fmt.Sprintf("picture.lines[%d]", i)
		if l.MeasurementNameID < 0 {
			return NewFieldViolation(at+".measurement_name_id", "invalid", "", "a measurement name id, or 0 while not picked")
		}
		for _, c := range []struct {
			name string
			v    *float64
		}{{"x1", &l.X1}, {"y1", &l.Y1}, {"x2", &l.X2}, {"y2", &l.Y2}} {
			// NaN fails both comparisons, so it is rejected here too.
			if !(*c.v >= 0 && *c.v <= 1) {
				return NewFieldViolation(at+"."+c.name, "out_of_range", "", "between 0 and 1")
			}
			*c.v = math.Round(*c.v*styleSizeChartLineScale) / styleSizeChartLineScale
		}
	}
	sort.SliceStable(p.Lines, func(i, j int) bool { return p.Lines[i].Position < p.Lines[j].Position })
	for i := range p.Lines {
		p.Lines[i].Position = i + 1
	}
	return nil
}

func iconKeyValid(key string) bool {
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
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
	Points          []StyleSizeChartPoint
	ShrinkagePct    decimal.NullDecimal
	// Picture is the drawing next to the table with its free lines; nil = no picture chosen.
	Picture *StyleSizeChartPicture
}

// ValidateStyleSizeChartPoints checks the point-list rules that need no database. It normalises
// pom_code and basis piece keys in place so the stored fingerprint and the returned payload agree.
func ValidateStyleSizeChartPoints(points []StyleSizeChartPoint) error {
	seenName := make(map[int]int, len(points))
	seenPosition := make(map[int]int, len(points))
	seenCode := make(map[string]int, len(points))
	for i := range points {
		p := &points[i]
		at := fmt.Sprintf("points[%d]", i)
		if p.MeasurementNameID <= 0 {
			return NewFieldViolation(at+".measurement_name_id", "required", "", "")
		}
		if j, ok := seenName[p.MeasurementNameID]; ok {
			return NewFieldViolation(at+".measurement_name_id", "duplicate", fmt.Sprintf("points[%d]", j), "one point per measurement")
		}
		seenName[p.MeasurementNameID] = i
		if p.Position < 1 || p.Position > 32767 {
			return NewFieldViolation(at+".position", "out_of_range", "", "between 1 and 32767")
		}
		if j, ok := seenPosition[p.Position]; ok {
			return NewFieldViolation(at+".position", "duplicate", fmt.Sprintf("points[%d]", j), "one point per position")
		}
		seenPosition[p.Position] = i
		p.PomCode = strings.TrimSpace(p.PomCode)
		if len(p.PomCode) > 32 || !pomCodeValid(p.PomCode) {
			return NewFieldViolation(at+".pom_code", "invalid", p.PomCode, "zero to 32 lowercase letters or hyphens")
		}
		if p.PomCode != "" {
			if j, ok := seenCode[p.PomCode]; ok {
				return NewFieldViolation(at+".pom_code", "duplicate", fmt.Sprintf("points[%d]", j), "one row per non-empty pom_code")
			}
			seenCode[p.PomCode] = i
		}
		if p.TolMinusCm.IsNegative() || p.TolMinusCm.GreaterThan(decimal.RequireFromString("999.99")) || p.TolMinusCm.Exponent() < -2 {
			return NewFieldViolation(at+".tol_minus_cm", "out_of_range", "", "zero to 999.99 with at most two decimals")
		}
		if p.TolPlusCm.IsNegative() || p.TolPlusCm.GreaterThan(decimal.RequireFromString("999.99")) || p.TolPlusCm.Exponent() < -2 {
			return NewFieldViolation(at+".tol_plus_cm", "out_of_range", "", "zero to 999.99 with at most two decimals")
		}
		p.BasisPieceLineKeys = NormalizedPieceLineKeys(p.BasisPieceLineKeys)
	}
	return nil
}

func pomCodeValid(code string) bool {
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

// NormalizedPieceLineKeys returns a deterministic upper-case, sorted set.
func NormalizedPieceLineKeys(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// StyleSizeChartPointFingerprint is the single fingerprint projection used on chart writes and
// reads. Empty basis means manual/no provenance and deliberately returns an empty fingerprint.
func StyleSizeChartPointFingerprint(basis []string, pieceSourceFingerprint string, shrinkage decimal.NullDecimal) string {
	basis = NormalizedPieceLineKeys(basis)
	if len(basis) == 0 {
		return ""
	}
	var b strings.Builder
	for _, key := range basis {
		b.WriteString("piece\x00")
		b.WriteString(key)
		b.WriteByte('\n')
	}
	b.WriteString("source\x00")
	b.WriteString(pieceSourceFingerprint)
	b.WriteByte('\n')
	b.WriteString("shrinkage\x00")
	if shrinkage.Valid {
		b.WriteString(shrinkage.Decimal.String())
	}
	b.WriteByte('\n')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
