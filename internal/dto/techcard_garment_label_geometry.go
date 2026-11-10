package dto

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// CarryStoredGarmentLabelGeometry protects geometry and operation links from an older whole-card
// client. Label key is a human-facing kind/name, so it is the only address that client can echo;
// matching is allowed only when it identifies exactly one stored row.
func CarryStoredGarmentLabelGeometry(stored, in *entity.TechCardInsert) {
	if stored == nil || in == nil {
		return
	}
	allLabelsOmittedKeys := true
	for i := range in.GarmentLabels {
		if strings.TrimSpace(in.GarmentLabels[i].LineKey) != "" {
			allLabelsOmittedKeys = false
			break
		}
	}
	allOperationsOmittedKeys := true
	for i := range in.Operations {
		if len(in.Operations[i].LabelKeys) != 0 {
			allOperationsOmittedKeys = false
			break
		}
	}

	if !in.LabelsAware {
		in.GarmentLabels = cloneGarmentLabels(stored.GarmentLabels)
	} else {
		byKey := make(map[string][]*entity.TechCardGarmentLabel, len(stored.GarmentLabels))
		for i := range stored.GarmentLabels {
			row := &stored.GarmentLabels[i]
			key := strings.ToLower(strings.TrimSpace(row.Key))
			byKey[key] = append(byKey[key], row)
		}
		for i := range in.GarmentLabels {
			row := &in.GarmentLabels[i]
			if strings.TrimSpace(row.LineKey) != "" {
				continue
			}
			matches := byKey[strings.ToLower(strings.TrimSpace(row.Key))]
			if len(matches) != 1 {
				continue
			}
			row.LineKey = matches[0].LineKey
			row.WidthMm = matches[0].WidthMm
			row.HeightMm = matches[0].HeightMm
			row.Fold = matches[0].Fold
		}
	}

	if in.LabelsAware {
		carryOmittedGarmentLabelFoldLines(stored, in)
		carryOmittedGarmentLabelPiece(stored, in)
	}

	if !allLabelsOmittedKeys || !allOperationsOmittedKeys {
		return
	}
	liveLabels := make(map[string]bool, len(in.GarmentLabels))
	for i := range in.GarmentLabels {
		if key := normalizeGarmentLabelLineKey(in.GarmentLabels[i].LineKey); key != "" {
			liveLabels[key] = true
		}
	}
	storedOperations := make(map[int32]*entity.TechCardOperation, len(stored.Operations))
	for i := range stored.Operations {
		if n := stored.Operations[i].OperationNumber; n.Valid {
			storedOperations[n.Int32] = &stored.Operations[i]
		}
	}
	for i := range in.Operations {
		op := &in.Operations[i]
		if !op.OperationNumber.Valid {
			continue
		}
		prev := storedOperations[op.OperationNumber.Int32]
		if prev == nil {
			continue
		}
		seen := make(map[string]bool, len(prev.LabelKeys))
		for _, raw := range prev.LabelKeys {
			key := normalizeGarmentLabelLineKey(raw)
			if key == "" || !liveLabels[key] || seen[key] {
				continue
			}
			seen[key] = true
			op.LabelKeys = append(op.LabelKeys, key)
		}
	}
}

func cloneGarmentLabels(in []entity.TechCardGarmentLabel) []entity.TechCardGarmentLabel {
	if len(in) == 0 {
		return nil
	}
	out := make([]entity.TechCardGarmentLabel, len(in))
	copy(out, in)
	for i := range out {
		out[i].MediaIds = append([]int(nil), in[i].MediaIds...)
		out[i].Files = append([]entity.TechCardGarmentLabelFile(nil), in[i].Files...)
		out[i].Prints = append([]entity.TechCardGarmentLabelPrint(nil), in[i].Prints...)
	}
	return out
}

func normalizeGarmentLabelLineKey(key string) string {
	return strings.ToUpper(strings.TrimSpace(key))
}

// finalizeGarmentLabelGeometry runs after the private compatibility carry and before any operation
// is written or a sign-off is stamped. It mints missing keys, enforces the shared key space, and
// checks every operation link against the effective labels of this payload.
func finalizeGarmentLabelGeometry(in *entity.TechCardInsert) error {
	taken := make(map[string]string, len(in.Pieces)+len(in.CutStrips)+len(in.GarmentLabels))
	for i := range in.Pieces {
		if key := normalizeGarmentLabelLineKey(in.Pieces[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("pieces[%d].line_key", i)
		}
	}
	for i := range in.CutStrips {
		if key := normalizeGarmentLabelLineKey(in.CutStrips[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("cut_strips[%d].line_key", i)
		}
	}
	live := make(map[string]bool, len(in.GarmentLabels))
	for i := range in.GarmentLabels {
		path := fmt.Sprintf("garment_labels[%d].line_key", i)
		key := normalizeGarmentLabelLineKey(in.GarmentLabels[i].LineKey)
		if key == "" {
			minted, err := mintTechCardLineKey()
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			key = minted
		}
		if !entity.IsValidProductionRunLineKey(key) {
			return entity.NewFieldViolation(path, "invalid", in.GarmentLabels[i].LineKey, "a 26-character ULID")
		}
		if owner, exists := taken[key]; exists {
			return entity.NewFieldViolation(path, "duplicate", owner, "each piece, cut strip and label has its own key")
		}
		taken[key] = path
		live[key] = true
		in.GarmentLabels[i].LineKey = key
	}
	if err := validateGarmentLabelPieceAndPrints(in); err != nil {
		return err
	}
	for i := range in.Operations {
		for j := range in.Operations[i].LabelKeys {
			key := normalizeGarmentLabelLineKey(in.Operations[i].LabelKeys[j])
			in.Operations[i].LabelKeys[j] = key
			if !live[key] {
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].label_keys[%d]", i, j),
					"label_missing", key, "a garment label of this card")
			}
		}
	}
	return nil
}

// carryOmittedGarmentLabelFoldLines keeps the stored fold lines of a row whose client sent no
// fold_lines_mm. The row is found by its line key; a pre-0454 row got that key from the unique
// label-key match above. The carried lines are cut to the row's height again, since that client can
// still change the height.
func carryOmittedGarmentLabelFoldLines(stored, in *entity.TechCardInsert) {
	byLineKey := make(map[string]*entity.TechCardGarmentLabel, len(stored.GarmentLabels))
	for i := range stored.GarmentLabels {
		if key := normalizeGarmentLabelLineKey(stored.GarmentLabels[i].LineKey); key != "" {
			byLineKey[key] = &stored.GarmentLabels[i]
		}
	}
	for i := range in.GarmentLabels {
		row := &in.GarmentLabels[i]
		if !row.FoldLinesOmitted && !row.StitchLinesOmitted && !row.FoldAxisOmitted && !row.PagesOmitted {
			continue
		}
		prev := byLineKey[normalizeGarmentLabelLineKey(row.LineKey)]
		if prev != nil {
			if row.FoldAxisOmitted {
				row.FoldAxis = prev.FoldAxis
			}
			if row.PagesOmitted {
				row.Pages = prev.Pages
			}
			if row.FoldLinesOmitted {
				row.FoldLinesMm = prev.FoldLinesMm
			}
			if row.StitchLinesOmitted {
				row.StitchLinesMm = prev.StitchLinesMm
			}
		}
		row.Pages = NormalizeGarmentLabelPages(row.Pages)
		row.FoldAxis = NormalizeGarmentLabelFoldAxis(row.FoldAxis)
		// The lines are cut to the length of the effective axis again: this client may have changed the
		// size, and a line it sent was cut at parse without the stored axis.
		limit := GarmentLabelLineLimit(row.FoldAxis, row.WidthMm, row.HeightMm)
		row.FoldLinesMm = NormalizeGarmentLabelFoldLines(row.FoldLinesMm, limit)
		row.StitchLinesMm = NormalizeGarmentLabelStitchLines(row.StitchLinesMm, limit)
	}
}

// NormalizeGarmentLabelFoldAxis puts fold_axis in its stored form: "width" or "" (anything else).
func NormalizeGarmentLabelFoldAxis(raw string) string {
	if strings.ToLower(strings.TrimSpace(raw)) == "width" {
		return "width"
	}
	return ""
}

const maxGarmentLabelPages = 12

// NormalizeGarmentLabelPages clamps the pages of a booklet label to 1..12.
func NormalizeGarmentLabelPages(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxGarmentLabelPages {
		return maxGarmentLabelPages
	}
	return n
}

// GarmentLabelLineLimit is the length the fold and stitch lines are measured against: the width when
// the (already normalised) axis is "width", else the height. Unset = unknown (no upper bound).
func GarmentLabelLineLimit(axis string, width, height decimal.NullDecimal) decimal.NullDecimal {
	if axis == "width" {
		return width
	}
	return height
}

// maxGarmentLabelFoldLines caps the fold lines of one label. Eight lines of "99999.9" with commas
// fit the VARCHAR(255) column many times over.
const maxGarmentLabelFoldLines = 8

var garmentLabelFoldLineMax = decimal.RequireFromString("99999.9")

// NormalizeGarmentLabelFoldLines puts a fold_lines_mm CSV in its stored form: every item that is a
// number, rounded to 0.1 mm, inside 0 < mm < height (mm > 0 when the height is unknown), deduped,
// ascending, at most maxGarmentLabelFoldLines, joined by "," with no spaces. A bad item is dropped,
// never an error: the lines are a drawing aid and must not block a save.
func NormalizeGarmentLabelFoldLines(raw string, heightMm decimal.NullDecimal) string {
	return normalizeGarmentLabelLines(raw, heightMm, false)
}

// NormalizeGarmentLabelStitchLines is NormalizeGarmentLabelFoldLines for stitch_lines_mm, but both
// edges are allowed: 0 <= mm <= height (mm >= 0 when the height is unknown), since a stitch on the
// top or bottom edge sews that edge.
func NormalizeGarmentLabelStitchLines(raw string, heightMm decimal.NullDecimal) string {
	return normalizeGarmentLabelLines(raw, heightMm, true)
}

func normalizeGarmentLabelLines(raw string, heightMm decimal.NullDecimal, edgesAllowed bool) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	seen := make(map[string]bool)
	var lines []decimal.Decimal
	for _, item := range strings.Split(raw, ",") {
		v, err := decimal.NewFromString(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		v = v.Round(1)
		if v.IsNegative() || v.GreaterThan(garmentLabelFoldLineMax) || (!edgesAllowed && v.IsZero()) {
			continue
		}
		if heightMm.Valid && (v.GreaterThan(heightMm.Decimal) || (!edgesAllowed && v.Equal(heightMm.Decimal))) {
			continue
		}
		key := v.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		lines = append(lines, v)
	}
	slices.SortFunc(lines, func(a, b decimal.Decimal) int { return a.Cmp(b) })
	if len(lines) > maxGarmentLabelFoldLines {
		lines = lines[:maxGarmentLabelFoldLines]
	}
	parts := make([]string, len(lines))
	for i, v := range lines {
		parts[i] = v.String()
	}
	return strings.Join(parts, ",")
}
