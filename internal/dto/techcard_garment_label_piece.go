package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Garment label on a fabric piece and prints on a label (0467).
//
// The piece: the label is sewn onto a small spare piece of fabric whose top edge goes into the seam.
// Every piece field has its own presence bit (absent = keep the stored value, carried by line key).
// label_on_piece is also the presence bit of the prints: a client that knows 0467 always sends it,
// and its prints replace the stored ones; a client that does not keeps them.
//
// The fit (label inside the piece, below the seam allowance) and the prints' rectangle (inside the
// label) are judged after the carry, in finalizeGarmentLabelGeometry, on the values the save writes.

const (
	maxLabelPrintsPerEntry = 50
	maxLabelPrintText      = 64
)

var (
	maxPieceMm = decimal.RequireFromString("99999.99") // DECIMAL(7,2)
	maxSeamMm  = decimal.RequireFromString("999.99")   // DECIMAL(5,2)
)

// Label on piece values. "" = not set.
const (
	GarmentLabelOnPieceFourSides = "4_sides"
	GarmentLabelOnPieceTopEdge   = "top_edge"
)

// NormalizeGarmentLabelOnPiece puts label_on_piece in its stored form: 4_sides, top_edge or "".
func NormalizeGarmentLabelOnPiece(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case GarmentLabelOnPieceFourSides, GarmentLabelOnPieceTopEdge:
		return v
	default:
		return ""
	}
}

var garmentLabelPrintMethods = map[string]bool{
	"screen": true, "heat_transfer": true, "woven": true, "embroidery": true, "pad_print": true,
}

// parseGarmentLabelMm reads a millimetre value that is >= 0 with at most 2 decimals and at most max.
// nil or "" = 0.
func parseGarmentLabelMm(field string, raw *pb_decimal.Decimal, max decimal.Decimal) (decimal.Decimal, error) {
	if raw == nil || strings.TrimSpace(raw.Value) == "" {
		return decimal.Zero, nil
	}
	v, err := decimal.NewFromString(strings.TrimSpace(raw.Value))
	if err != nil {
		return decimal.Zero, entity.NewFieldViolation(field, "not_a_number", raw.Value, "enter millimetres")
	}
	if v.IsNegative() {
		return decimal.Zero, entity.NewFieldViolation(field, "must not be negative", v.String(), "0 or more")
	}
	if v.GreaterThan(max) {
		return decimal.Zero, entity.NewFieldViolation(field, "out_of_range", v.String(), "at most "+max.String())
	}
	if v.Exponent() < -2 && !v.Equal(v.Round(2)) {
		return decimal.Zero, entity.NewFieldViolation(field, "too_many_decimal_places", v.String(), "at most 2 decimal places")
	}
	return v.Round(2), nil
}

// parseGarmentLabelPiece reads the piece fields of one label into row, with their presence bits.
func parseGarmentLabelPiece(f string, l *pb_common.TechCardGarmentLabel, row *entity.TechCardGarmentLabel) error {
	row.PieceOmitted = entity.GarmentLabelPieceOmitted{
		BomItem: l.PieceBomItemId == nil, Width: l.PieceWidthMm == nil, Height: l.PieceHeightMm == nil,
		Seam: l.PieceSeamMm == nil, LabelX: l.LabelXMm == nil, LabelY: l.LabelYMm == nil,
		LabelOnPiece: l.LabelOnPiece == nil,
	}
	if l.GetPieceBomItemId() < 0 {
		return entity.NewFieldViolation(f+".piece_bom_item_id", "must not be negative", fmt.Sprint(l.GetPieceBomItemId()), "send 0 for none")
	}
	if id := l.GetPieceBomItemId(); id > 0 {
		row.PieceBomItemId = sql.NullInt32{Int32: id, Valid: true}
	}
	var err error
	for _, c := range []struct {
		name string
		raw  *pb_decimal.Decimal
		max  decimal.Decimal
		dst  *decimal.Decimal
	}{
		{"piece_width_mm", l.PieceWidthMm, maxPieceMm, &row.PieceWidthMm},
		{"piece_height_mm", l.PieceHeightMm, maxPieceMm, &row.PieceHeightMm},
		{"piece_seam_mm", l.PieceSeamMm, maxSeamMm, &row.PieceSeamMm},
		{"label_x_mm", l.LabelXMm, maxPieceMm, &row.LabelXMm},
		{"label_y_mm", l.LabelYMm, maxPieceMm, &row.LabelYMm},
	} {
		if *c.dst, err = parseGarmentLabelMm(f+"."+c.name, c.raw, c.max); err != nil {
			return err
		}
	}
	row.LabelOnPiece = NormalizeGarmentLabelOnPiece(l.GetLabelOnPiece())
	row.PrintsOmitted = l.LabelOnPiece == nil
	row.Prints, err = parseGarmentLabelPrints(f+".prints", l.Prints)
	return err
}

// parseGarmentLabelPrints validates the shape of the prints of one label. The rectangle against the
// label's size is checked after the carry (validateGarmentLabelPrints).
func parseGarmentLabelPrints(field string, pbs []*pb_common.TechCardGarmentLabelPrint) ([]entity.TechCardGarmentLabelPrint, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	if len(pbs) > maxLabelPrintsPerEntry {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(pbs)), fmt.Sprintf("at most %d prints", maxLabelPrintsPerEntry))
	}
	out := make([]entity.TechCardGarmentLabelPrint, 0, len(pbs))
	for i, p := range pbs {
		if p == nil {
			continue
		}
		ff := fmt.Sprintf("%s[%d]", field, i)
		name, colour := strings.TrimSpace(p.Name), strings.TrimSpace(p.Colour)
		if err := checkLabelText(ff+".name", name, maxLabelPrintText); err != nil {
			return nil, err
		}
		if err := checkLabelText(ff+".colour", colour, maxLabelPrintText); err != nil {
			return nil, err
		}
		method := strings.ToLower(strings.TrimSpace(p.Method))
		if !garmentLabelPrintMethods[method] {
			return nil, entity.NewFieldViolation(ff+".method", "invalid", p.Method,
				"use screen, heat_transfer, woven, embroidery or pad_print")
		}
		if p.LibraryFileId < 0 {
			return nil, entity.NewFieldViolation(ff+".library_file_id", "must not be negative", fmt.Sprint(p.LibraryFileId), "send 0 for none")
		}
		row := entity.TechCardGarmentLabelPrint{Name: name, Colour: colour, Method: method}
		if p.LibraryFileId > 0 {
			row.LibraryFileId = sql.NullInt32{Int32: p.LibraryFileId, Valid: true}
		}
		var err error
		for _, c := range []struct {
			name string
			raw  *pb_decimal.Decimal
			dst  *decimal.Decimal
		}{
			{"width_mm", p.WidthMm, &row.WidthMm}, {"height_mm", p.HeightMm, &row.HeightMm},
			{"x_mm", p.XMm, &row.XMm}, {"y_mm", p.YMm, &row.YMm},
		} {
			if *c.dst, err = parseGarmentLabelMm(ff+"."+c.name, c.raw, maxPieceMm); err != nil {
				return nil, err
			}
		}
		if !row.WidthMm.IsPositive() {
			return nil, entity.NewFieldViolation(ff+".width_mm", "must be positive", row.WidthMm.String(), "more than 0")
		}
		if !row.HeightMm.IsPositive() {
			return nil, entity.NewFieldViolation(ff+".height_mm", "must be positive", row.HeightMm.String(), "more than 0")
		}
		out = append(out, row)
	}
	return out, nil
}

// labelSize is the label's size for the fit checks; unset = 0.
func labelSize(v decimal.NullDecimal) decimal.Decimal {
	if !v.Valid {
		return decimal.Zero
	}
	return v.Decimal
}

// ValidateGarmentLabelPiece checks the label against its piece: with a piece (width and height > 0)
// the label lies inside it and starts at or below the seam allowance. No piece = nothing to check.
func ValidateGarmentLabelPiece(f string, l *entity.TechCardGarmentLabel) error {
	if !l.PieceWidthMm.IsPositive() || !l.PieceHeightMm.IsPositive() {
		return nil
	}
	w, h := labelSize(l.WidthMm), labelSize(l.HeightMm)
	if l.PieceSeamMm.GreaterThanOrEqual(l.PieceHeightMm) {
		return entity.NewFieldViolation(f+".piece_seam_mm", "seam_past_piece", l.PieceSeamMm.String(),
			"less than the piece height "+l.PieceHeightMm.String())
	}
	if l.LabelYMm.LessThan(l.PieceSeamMm) {
		return entity.NewFieldViolation(f+".label_y_mm", "label_in_seam", l.LabelYMm.String(),
			"at least the seam allowance "+l.PieceSeamMm.String())
	}
	if l.LabelXMm.Add(w).GreaterThan(l.PieceWidthMm) {
		return entity.NewFieldViolation(f+".label_x_mm", "label_outside_piece", l.LabelXMm.Add(w).String(),
			"label must fit the piece width "+l.PieceWidthMm.String())
	}
	if l.LabelYMm.Add(h).GreaterThan(l.PieceHeightMm) {
		return entity.NewFieldViolation(f+".label_y_mm", "label_outside_piece", l.LabelYMm.Add(h).String(),
			"label must fit the piece height "+l.PieceHeightMm.String())
	}
	return nil
}

// validateGarmentLabelPrints checks each print lies inside the label rectangle, which needs the
// label's size.
func validateGarmentLabelPrints(f string, l *entity.TechCardGarmentLabel) error {
	if len(l.Prints) == 0 {
		return nil
	}
	w, h := labelSize(l.WidthMm), labelSize(l.HeightMm)
	if !w.IsPositive() || !h.IsPositive() {
		return entity.NewFieldViolation(f+".prints", "label_size_unset", "", "set the label width and height first")
	}
	for j, p := range l.Prints {
		pf := fmt.Sprintf("%s.prints[%d]", f, j)
		if p.XMm.Add(p.WidthMm).GreaterThan(w) {
			return entity.NewFieldViolation(pf+".x_mm", "print_outside_label", p.XMm.Add(p.WidthMm).String(),
				"print must fit the label width "+w.String())
		}
		if p.YMm.Add(p.HeightMm).GreaterThan(h) {
			return entity.NewFieldViolation(pf+".y_mm", "print_outside_label", p.YMm.Add(p.HeightMm).String(),
				"print must fit the label height "+h.String())
		}
	}
	return nil
}

// validateGarmentLabelPieceAndPrints runs both checks on the values the save writes. A row whose
// client knew nothing of 0467 (every piece field and the prints carried) is not judged: its stored
// values were valid when written, and refusing an older tab's unrelated edit would be worse.
func validateGarmentLabelPieceAndPrints(in *entity.TechCardInsert) error {
	for i := range in.GarmentLabels {
		l := &in.GarmentLabels[i]
		f := fmt.Sprintf("garment_labels[%d]", i)
		if !l.PieceOmitted.All() {
			if err := ValidateGarmentLabelPiece(f, l); err != nil {
				return err
			}
		}
		if !l.PrintsOmitted {
			if err := validateGarmentLabelPrints(f, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// carryOmittedGarmentLabelPiece keeps the stored piece fields a client did not send, and the stored
// prints of a row whose client did not send label_on_piece. The row is found by its line key.
func carryOmittedGarmentLabelPiece(stored, in *entity.TechCardInsert) {
	byLineKey := make(map[string]*entity.TechCardGarmentLabel, len(stored.GarmentLabels))
	for i := range stored.GarmentLabels {
		if key := normalizeGarmentLabelLineKey(stored.GarmentLabels[i].LineKey); key != "" {
			byLineKey[key] = &stored.GarmentLabels[i]
		}
	}
	for i := range in.GarmentLabels {
		row := &in.GarmentLabels[i]
		if !row.PieceOmitted.Any() && !row.PrintsOmitted {
			continue
		}
		prev := byLineKey[normalizeGarmentLabelLineKey(row.LineKey)]
		if prev == nil {
			continue
		}
		o := row.PieceOmitted
		if o.BomItem {
			row.PieceBomItemId, row.PieceBomLineKey = prev.PieceBomItemId, prev.PieceBomLineKey
		}
		if o.Width {
			row.PieceWidthMm = prev.PieceWidthMm
		}
		if o.Height {
			row.PieceHeightMm = prev.PieceHeightMm
		}
		if o.Seam {
			row.PieceSeamMm = prev.PieceSeamMm
		}
		if o.LabelX {
			row.LabelXMm = prev.LabelXMm
		}
		if o.LabelY {
			row.LabelYMm = prev.LabelYMm
		}
		if o.LabelOnPiece {
			row.LabelOnPiece = prev.LabelOnPiece
		}
		if row.PrintsOmitted {
			row.Prints = append([]entity.TechCardGarmentLabelPrint(nil), prev.Prints...)
		}
	}
}

// garmentLabelPrintsToPb converts the prints for the wire, in display order.
func garmentLabelPrintsToPb(prints []entity.TechCardGarmentLabelPrint) []*pb_common.TechCardGarmentLabelPrint {
	if len(prints) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardGarmentLabelPrint, 0, len(prints))
	for _, p := range prints {
		out = append(out, &pb_common.TechCardGarmentLabelPrint{
			Id:            int32(p.Id),
			Name:          p.Name,
			LibraryFileId: p.LibraryFileId.Int32,
			Method:        p.Method,
			Colour:        p.Colour,
			WidthMm:       pbDecimalFromDecimal(p.WidthMm),
			HeightMm:      pbDecimalFromDecimal(p.HeightMm),
			XMm:           pbDecimalFromDecimal(p.XMm),
			YMm:           pbDecimalFromDecimal(p.YMm),
		})
	}
	return out
}
