package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// StyleSizeChartToPb projects a style's size chart to the wire message (R5). Admin-facing: it carries
// the internal size_id/measurement_name_id (the storefront gets resolved codes via PublicStyleSizeChart).
func StyleSizeChartToPb(c entity.StyleSizeChart) *pb_common.StyleSizeChart {
	cells := make([]*pb_common.StyleSizeChartCell, 0, len(c.Cells))
	for _, cell := range c.Cells {
		cells = append(cells, &pb_common.StyleSizeChartCell{
			SizeId:            int32(cell.SizeID),
			MeasurementNameId: int32(cell.MeasurementNameID),
			Value:             &pb_decimal.Decimal{Value: cell.Value.String()},
			Source:            styleSizeChartCellSourceToPb(cell.Source),
		})
	}
	steps := make([]*pb_common.StyleSizeChartGradeStep, 0, len(c.GradeSteps))
	for _, s := range c.GradeSteps {
		steps = append(steps, &pb_common.StyleSizeChartGradeStep{
			MeasurementNameId: int32(s.MeasurementNameID),
			Step:              &pb_decimal.Decimal{Value: s.Step.String()},
		})
	}
	points := make([]*pb_common.StyleSizeChartPoint, 0, len(c.Points))
	for _, p := range c.Points {
		points = append(points, &pb_common.StyleSizeChartPoint{
			MeasurementNameId: int32(p.MeasurementNameID), PomCode: p.PomCode,
			Position: int32(p.Position), TolMinusCm: pbDecimalFromDecimal(p.TolMinusCm),
			TolPlusCm: pbDecimalFromDecimal(p.TolPlusCm), InShop: p.InShop,
			BasisPieceLineKeys: append([]string(nil), p.BasisPieceLineKeys...), Stale: p.Stale,
		})
	}
	return &pb_common.StyleSizeChart{
		StyleId:         int32(c.StyleID),
		LockVersion:     int32(c.LockVersion),
		Cells:           cells,
		GradeBaseSizeId: int32(c.GradeBaseSizeID),
		GradeSteps:      steps,
		Points:          points,
		ShrinkagePct:    pbDecimalFromNull(c.ShrinkagePct),
		Picture:         StyleSizeChartPictureToPb(c.Picture),
	}
}

// StyleSizeChartPictureToPb projects the size chart picture; nil (no picture) stays absent.
func StyleSizeChartPictureToPb(p *entity.StyleSizeChartPicture) *pb_common.StyleSizeChartPicture {
	if p == nil {
		return nil
	}
	lines := make([]*pb_common.StyleSizeChartLine, 0, len(p.Lines))
	for _, l := range p.Lines {
		lines = append(lines, &pb_common.StyleSizeChartLine{
			MeasurementNameId: int32(l.MeasurementNameID),
			X1:                l.X1, Y1: l.Y1, X2: l.X2, Y2: l.Y2,
			Position: int32(l.Position),
		})
	}
	return &pb_common.StyleSizeChartPicture{
		Source: p.Source, IconKey: p.IconKey, MediaId: int32(p.MediaID),
		Lines: lines, MediaUrl: p.MediaURL,
	}
}

// StyleSizeChartPictureFromPb reads the picture of a request that set picture_set. An absent picture
// is the empty one, which removes the stored picture and its lines. media_url is output-only and
// ignored; the store validates and normalises the rest.
func StyleSizeChartPictureFromPb(in *pb_common.StyleSizeChartPicture) *entity.StyleSizeChartPicture {
	out := &entity.StyleSizeChartPicture{}
	if in == nil {
		return out
	}
	out.Source, out.IconKey, out.MediaID = in.GetSource(), in.GetIconKey(), int(in.GetMediaId())
	for _, l := range in.GetLines() {
		if l == nil {
			continue
		}
		out.Lines = append(out.Lines, entity.StyleSizeChartLine{
			MeasurementNameID: int(l.GetMeasurementNameId()),
			X1:                l.GetX1(), Y1: l.GetY1(), X2: l.GetX2(), Y2: l.GetY2(),
			Position: int(l.GetPosition()),
		})
	}
	return out
}

func styleSizeChartCellSourceToPb(source string) pb_common.StyleSizeChartCellSource {
	if source == entity.StyleSizeChartCellSourcePattern {
		return pb_common.StyleSizeChartCellSource_STYLE_SIZE_CHART_CELL_SOURCE_PATTERN
	}
	return pb_common.StyleSizeChartCellSource_STYLE_SIZE_CHART_CELL_SOURCE_MANUAL
}

const (
	// tech_card_size_measurement.measurement_value and tech_card_grade_rule.step are both
	// DECIMAL(10,2) (0141, 0210): at most 2 fraction digits and 8 integer digits.
	chartDecimalMaxFrac = 2
	chartDecimalLimit   = 100_000_000
)

// StyleSizeChartGradeStepsFromPb parses the grade rule of a full-replace size-chart request. A step is
// dropped when its measurement is not named — a rule that grades nothing is the same as no rule.
func StyleSizeChartGradeStepsFromPb(steps []*pb_common.StyleSizeChartGradeStep) ([]entity.StyleSizeChartGradeStep, error) {
	out := make([]entity.StyleSizeChartGradeStep, 0, len(steps))
	seen := make(map[int]bool, len(steps))
	for i, s := range steps {
		if s == nil {
			continue
		}
		nameID := int(s.GetMeasurementNameId())
		if nameID == 0 {
			return nil, fmt.Errorf("grade step: measurement_name_id is required")
		}
		if seen[nameID] {
			return nil, fmt.Errorf("duplicate grade step for measurement %d", nameID)
		}
		seen[nameID] = true
		v := decimal.Zero
		if raw := s.GetStep().GetValue(); raw != "" {
			parsed, err := decimal.NewFromString(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid grade step %q: %w", raw, err)
			}
			v = parsed
		}
		// A step is signed (a measurement may shrink across the run), so only scale and magnitude
		// are checked — 0.125 must not be silently stored as 0.13 and then graded from.
		if err := validateDecimalFits(fmt.Sprintf("grade_steps[%d].step", i), v,
			chartDecimalMaxFrac, chartDecimalLimit, true); err != nil {
			return nil, err
		}
		out = append(out, entity.StyleSizeChartGradeStep{MeasurementNameID: nameID, Step: v})
	}
	return out, nil
}

// StyleSizeChartCellsFromPb parses the cells of a full-replace size-chart request into entity cells (R5).
//
// A cell is identified by (size_id, measurement_name_id) — that pair is uniq_tech_card_size_measurement
// in the schema. Repeating it in one payload is a client bug with no sane resolution (which of the two
// values is the measurement?), so it is rejected here: letting it through hit the unique index and came
// back as an opaque Internal, which told the author nothing about which cell to fix.
func StyleSizeChartCellsFromPb(cells []*pb_common.StyleSizeChartCell) ([]entity.StyleSizeChartCell, error) {
	out := make([]entity.StyleSizeChartCell, 0, len(cells))
	seen := make(map[[2]int]int, len(cells))
	for i, c := range cells {
		if c == nil {
			continue
		}
		sizeID, nameID := int(c.GetSizeId()), int(c.GetMeasurementNameId())
		if sizeID <= 0 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("cells[%d].size_id", i), "required", "", "every cell names the size it measures")
		}
		if nameID <= 0 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("cells[%d].measurement_name_id", i), "required", "", "every cell names the point of measure it records")
		}
		key := [2]int{sizeID, nameID}
		if prev, dup := seen[key]; dup {
			return nil, entity.NewFieldViolation(fmt.Sprintf("cells[%d]", i), "duplicate_cell",
				fmt.Sprintf("size %d / measurement %d (already set by cells[%d])", sizeID, nameID, prev),
				"keep one value per size and point of measure")
		}
		seen[key] = i
		v := decimal.Zero
		if raw := c.GetValue().GetValue(); raw != "" {
			parsed, err := decimal.NewFromString(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid measurement value %q: %w", raw, err)
			}
			v = parsed
		}
		// A point of measure is a length: never negative, and never finer than the column stores.
		if err := validateDecimalFits(fmt.Sprintf("cells[%d].value", i), v,
			chartDecimalMaxFrac, chartDecimalLimit, false); err != nil {
			return nil, err
		}
		out = append(out, entity.StyleSizeChartCell{
			SizeID:            sizeID,
			MeasurementNameID: nameID,
			Value:             v,
			Source:            styleSizeChartCellSourceFromPb(c.GetSource()),
		})
	}
	return out, nil
}

func styleSizeChartCellSourceFromPb(source pb_common.StyleSizeChartCellSource) string {
	switch source {
	case pb_common.StyleSizeChartCellSource_STYLE_SIZE_CHART_CELL_SOURCE_PATTERN:
		return entity.StyleSizeChartCellSourcePattern
	case pb_common.StyleSizeChartCellSource_STYLE_SIZE_CHART_CELL_SOURCE_UNKNOWN,
		pb_common.StyleSizeChartCellSource_STYLE_SIZE_CHART_CELL_SOURCE_MANUAL:
		return entity.StyleSizeChartCellSourceManual
	default:
		return source.String()
	}
}

// StyleSizeChartPointsFromPb parses point metadata. The entity validator owns cross-row uniqueness
// and pom-code rules; this layer owns decimal presence, scale and transport ids.
func StyleSizeChartPointsFromPb(points []*pb_common.StyleSizeChartPoint) ([]entity.StyleSizeChartPoint, error) {
	out := make([]entity.StyleSizeChartPoint, 0, len(points))
	for i, p := range points {
		if p == nil {
			continue
		}
		minus, err := chartToleranceFromPb(p.GetTolMinusCm(), fmt.Sprintf("points[%d].tol_minus_cm", i))
		if err != nil {
			return nil, err
		}
		plus, err := chartToleranceFromPb(p.GetTolPlusCm(), fmt.Sprintf("points[%d].tol_plus_cm", i))
		if err != nil {
			return nil, err
		}
		out = append(out, entity.StyleSizeChartPoint{
			MeasurementNameID: int(p.GetMeasurementNameId()), PomCode: p.GetPomCode(),
			Position: int(p.GetPosition()), TolMinusCm: minus, TolPlusCm: plus,
			InShop: p.GetInShop(), BasisPieceLineKeys: append([]string(nil), p.GetBasisPieceLineKeys()...),
		})
	}
	if err := entity.ValidateStyleSizeChartPoints(out); err != nil {
		return nil, err
	}
	return out, nil
}

func chartToleranceFromPb(in *pb_decimal.Decimal, field string) (decimal.Decimal, error) {
	if in == nil || in.GetValue() == "" {
		return decimal.NewFromInt(1), nil
	}
	value, err := decimal.NewFromString(in.GetValue())
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%s: %w", field, err)
	}
	if err := validateDecimalFits(field, value, 2, 1000, false); err != nil {
		return decimal.Decimal{}, err
	}
	return value, nil
}

func StyleSizeChartShrinkageFromPb(in *pb_decimal.Decimal) (*decimal.Decimal, error) {
	if in == nil || in.GetValue() == "" {
		return nil, nil
	}
	value, err := decimal.NewFromString(in.GetValue())
	if err != nil {
		return nil, fmt.Errorf("invalid shrinkage_pct %q: %w", in.GetValue(), err)
	}
	if value.IsNegative() || value.GreaterThan(decimal.NewFromInt(100)) {
		return nil, entity.NewFieldViolation("shrinkage_pct", "out_of_range", value.String(), "between 0 and 100")
	}
	if value.Exponent() < -2 {
		return nil, entity.NewFieldViolation("shrinkage_pct", "too_many_decimal_places", value.String(), "round to at most 2 decimal places")
	}
	return &value, nil
}
