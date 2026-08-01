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
		})
	}
	steps := make([]*pb_common.StyleSizeChartGradeStep, 0, len(c.GradeSteps))
	for _, s := range c.GradeSteps {
		steps = append(steps, &pb_common.StyleSizeChartGradeStep{
			MeasurementNameId: int32(s.MeasurementNameID),
			Step:              &pb_decimal.Decimal{Value: s.Step.String()},
		})
	}
	return &pb_common.StyleSizeChart{
		StyleId:          int32(c.StyleID),
		LockVersion:      int32(c.LockVersion),
		Cells:            cells,
		GradeBaseSizeId:  int32(c.GradeBaseSizeID),
		GradeSteps:       steps,
	}
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
func StyleSizeChartCellsFromPb(cells []*pb_common.StyleSizeChartCell) ([]entity.StyleSizeChartCell, error) {
	out := make([]entity.StyleSizeChartCell, 0, len(cells))
	for i, c := range cells {
		if c == nil {
			continue
		}
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
			SizeID:            int(c.GetSizeId()),
			MeasurementNameID: int(c.GetMeasurementNameId()),
			Value:             v,
		})
	}
	return out, nil
}
