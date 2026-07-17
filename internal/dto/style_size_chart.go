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
	return &pb_common.StyleSizeChart{
		StyleId:     int32(c.StyleID),
		LockVersion: int32(c.LockVersion),
		Cells:       cells,
	}
}

// StyleSizeChartCellsFromPb parses the cells of a full-replace size-chart request into entity cells (R5).
func StyleSizeChartCellsFromPb(cells []*pb_common.StyleSizeChartCell) ([]entity.StyleSizeChartCell, error) {
	out := make([]entity.StyleSizeChartCell, 0, len(cells))
	for _, c := range cells {
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
		out = append(out, entity.StyleSizeChartCell{
			SizeID:            int(c.GetSizeId()),
			MeasurementNameID: int(c.GetMeasurementNameId()),
			Value:             v,
		})
	}
	return out, nil
}
