package entity

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	TechCardTopstitchMaxPerCard = 200
	TechCardTopstitchMaxParts   = 16
	TechCardTopstitchMaxRows    = 4
	TechCardThreadPerMmMax      = 50
)

var techCardTopstitchMaxMm = decimal.New(999999999, -2) // DECIMAL(9,2)

// TechCardTopstitchLineSize is one client-measured size of a topstitch run.
type TechCardTopstitchLineSize struct {
	LineId int                 `db:"line_id"`
	SizeId int                 `db:"size_id"`
	LineMm decimal.NullDecimal `db:"line_mm"`
}

// TechCardTopstitchLine is a measured run owned by one operation.
type TechCardTopstitchLine struct {
	Id                int                         `db:"id"`
	OperationId       int                         `db:"operation_id"`
	TechCardId        int                         `db:"tech_card_id"`
	BomItemId         int                         `db:"bom_item_id"`
	BomLineKey        string                      `db:"bom_line_key"`
	LineKey           string                      `db:"line_key"`
	Parts             []TechCardSeamAnchor        `db:"-"`
	AnchoredSize      string                      `db:"anchored_size"`
	Rows              int                         `db:"rows"`
	SourceFingerprint sql.NullString              `db:"source_fingerprint"`
	DisplayOrder      int                         `db:"display_order"`
	Sizes             []TechCardTopstitchLineSize `db:"-"`
	Measured          bool                        `db:"-"`
	Stale             bool                        `db:"-"`
	// RestoreSourceFingerprint says this line came from a snapshot restore. The store must stamp
	// the restored source instead of carrying whatever fingerprint the current row happens to have.
	RestoreSourceFingerprint bool `db:"-" json:"-"`
	// RestoreStale preserves a snapshot's stale verdict with a fingerprint that cannot match.
	RestoreStale bool `db:"-" json:"-"`
}

// PieceLineKeys is the sorted distinct piece set covered by the source fingerprint.
func (l TechCardTopstitchLine) PieceLineKeys() []string {
	seen := make(map[string]bool, len(l.Parts))
	out := make([]string, 0, len(l.Parts))
	for _, part := range l.Parts {
		key := CutStripLineKey(part.PieceLineKey)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateThreadPerMm validates a present BOM factor against DECIMAL(6,3) and the product bound.
func ValidateThreadPerMm(field string, v decimal.NullDecimal) error {
	if !v.Valid {
		return nil
	}
	if !v.Decimal.IsPositive() || v.Decimal.GreaterThan(decimal.NewFromInt(TechCardThreadPerMmMax)) {
		return NewFieldViolation(field, "out_of_range", v.Decimal.String(), "above 0 and up to 50")
	}
	if v.Decimal.Exponent() < -3 {
		return NewFieldViolation(field, "scale", v.Decimal.String(), "at most 3 decimal places")
	}
	return nil
}

// ValidateTechCardTopstitchLines validates and normalises all operation-owned lines of a card.
// It deliberately does not require an A2 operation: no reliable server-side A2 predicate exists.
func ValidateTechCardTopstitchLines(tc *TechCardInsert) error {
	if tc == nil {
		return nil
	}
	for i := range tc.BomItems {
		if err := ValidateThreadPerMm(fmt.Sprintf("bom_items[%d].thread_per_mm", i), tc.BomItems[i].ThreadPerMm); err != nil {
			return err
		}
	}
	pieceKeys := make(map[string]bool, len(tc.Pieces))
	taken := make(map[string]string, len(tc.Pieces)+len(tc.CutStrips)+len(tc.GarmentLabels))
	for i := range tc.Pieces {
		key := CutStripLineKey(tc.Pieces[i].LineKey)
		if key != "" {
			pieceKeys[key] = true
			taken[key] = fmt.Sprintf("pieces[%d].line_key", i)
		}
	}
	for i := range tc.CutStrips {
		if key := CutStripLineKey(tc.CutStrips[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("cut_strips[%d].line_key", i)
		}
	}
	for i := range tc.GarmentLabels {
		if key := CutStripLineKey(tc.GarmentLabels[i].LineKey); key != "" {
			taken[key] = fmt.Sprintf("garment_labels[%d].line_key", i)
		}
	}
	bomByKey := make(map[string]*TechCardBomItem, len(tc.BomItems))
	for i := range tc.BomItems {
		bomByKey[CutStripLineKey(tc.BomItems[i].LineKey)] = &tc.BomItems[i]
	}
	inRange := make(map[int]bool, len(tc.SizeIds))
	for _, id := range tc.SizeIds {
		inRange[id] = true
	}
	total := 0
	for i := range tc.Operations {
		op := &tc.Operations[i]
		opBom := make(map[string]bool, len(op.BomLineKeys))
		for _, key := range op.BomLineKeys {
			opBom[CutStripLineKey(key)] = true
		}
		for j := range op.TopstitchLines {
			total++
			path := fmt.Sprintf("operations[%d].topstitch_lines[%d]", i, j)
			line := &op.TopstitchLines[j]
			line.LineKey = NormalizeTechCardSeamKey(line.LineKey)
			if !ValidTechCardSeamKey(line.LineKey) {
				return NewFieldViolation(path+".line_key", "invalid", "", "a 26-char ULID")
			}
			if owner, exists := taken[line.LineKey]; exists {
				return NewFieldViolation(path+".line_key", "duplicate", owner, "each card line has its own key")
			}
			taken[line.LineKey] = path + ".line_key"
			line.BomLineKey = strings.TrimSpace(line.BomLineKey)
			bomKey := CutStripLineKey(line.BomLineKey)
			if bomKey == "" {
				return NewFieldViolation(path+".bom_line_key", "required", "", "a thread BOM line")
			}
			if !opBom[bomKey] {
				return NewFieldViolation(path+".bom_line_key", "not_on_operation", line.BomLineKey,
					"the operation must consume this BOM line")
			}
			bom := bomByKey[bomKey]
			if bom == nil {
				return NewFieldViolation(path+".bom_line_key", "missing", line.BomLineKey, "a BOM line of this card")
			}
			line.BomLineKey = bom.LineKey
			if bom.Section != BomSectionThread {
				return NewFieldViolation(path+".bom_line_key", "not_thread", line.BomLineKey, "a THREAD line")
			}
			if !IsCutStripUnit(bom.Unit.String) {
				return NewFieldViolation(path+".bom_line_key", "not_metres", bom.Unit.String, "a thread line bought in metres")
			}
			line.AnchoredSize = strings.TrimSpace(line.AnchoredSize)
			if utf8.RuneCountInString(line.AnchoredSize) > TechCardSeamAnchoredSizeMaxRunes {
				return NewFieldViolation(path+".anchored_size", "too_long", "", "at most 16 characters")
			}
			if line.Rows < 1 || line.Rows > TechCardTopstitchMaxRows {
				return NewFieldViolation(path+".rows", "out_of_range", fmt.Sprint(line.Rows), "1 to 4")
			}
			if err := ValidateTechCardSeamSide(path, line.Parts, TechCardTopstitchMaxParts); err != nil {
				return err
			}
			for k, part := range line.Parts {
				if !pieceKeys[CutStripLineKey(part.PieceLineKey)] {
					return NewFieldViolation(fmt.Sprintf("%s.parts[%d].piece_line_key", path, k),
						"piece_not_on_card", part.PieceLineKey, "a pattern piece of this card")
				}
			}
			if err := normalizeTopstitchSizes(line, path, inRange); err != nil {
				return err
			}
		}
	}
	if total > TechCardTopstitchMaxPerCard {
		return NewFieldViolation("operations", "too_many_topstitch_lines", fmt.Sprint(total), "at most 200 per card")
	}
	return nil
}

func normalizeTopstitchSizes(line *TechCardTopstitchLine, path string, inRange map[int]bool) error {
	kept := make([]TechCardTopstitchLineSize, 0, len(line.Sizes))
	seen := make(map[int]bool, len(line.Sizes))
	for i := range line.Sizes {
		row := line.Sizes[i]
		field := fmt.Sprintf("%s.sizes[%d]", path, i)
		if row.SizeId <= 0 || !inRange[row.SizeId] {
			return NewFieldViolation(field+".size_id", "not_in_range", fmt.Sprint(row.SizeId), "a size of the card range")
		}
		if seen[row.SizeId] {
			return NewFieldViolation(field+".size_id", "duplicate", fmt.Sprint(row.SizeId), "one row per size")
		}
		seen[row.SizeId] = true
		if !row.LineMm.Valid {
			continue
		}
		if row.LineMm.Decimal.Exponent() < -2 || !row.LineMm.Decimal.IsPositive() ||
			row.LineMm.Decimal.GreaterThan(techCardTopstitchMaxMm) {
			return NewFieldViolation(field+".line_mm", "out_of_range", row.LineMm.Decimal.String(),
				"a positive length with at most 2 decimals")
		}
		kept = append(kept, row)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].SizeId < kept[j].SizeId })
	if len(kept) == 0 {
		kept = nil
	}
	line.Sizes = kept
	return nil
}

// KeepTopstitchSizes drops measurements outside a narrowed card range.
func KeepTopstitchSizes(line *TechCardTopstitchLine, inRange map[int]bool) {
	if len(inRange) == 0 {
		line.Sizes = nil
		return
	}
	line.Sizes = append([]TechCardTopstitchLineSize(nil), line.Sizes...)
	line.Sizes = slicesDeleteTopstitch(line.Sizes, func(row TechCardTopstitchLineSize) bool { return !inRange[row.SizeId] })
}

func slicesDeleteTopstitch(in []TechCardTopstitchLineSize, drop func(TechCardTopstitchLineSize) bool) []TechCardTopstitchLineSize {
	out := in[:0]
	for _, row := range in {
		if !drop(row) {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TopstitchLines flattens operation-owned lines without changing their order.
func TopstitchLines(ops []TechCardOperation) []TechCardTopstitchLine {
	var out []TechCardTopstitchLine
	for i := range ops {
		out = append(out, ops[i].TopstitchLines...)
	}
	return out
}

// CarryStoredTopstitchLines preserves operation child rows for an unaware internal writer. Public
// full-replace writes are additionally protected by the awareness gate.
func CarryStoredTopstitchLines(in, stored *TechCardInsert) {
	if in == nil || stored == nil || in.TopstitchLinesAware {
		return
	}
	inRange := make(map[int]bool, len(in.SizeIds))
	for _, id := range in.SizeIds {
		inRange[id] = true
	}
	livePieces := make(map[string]bool, len(in.Pieces))
	for i := range in.Pieces {
		livePieces[CutStripLineKey(in.Pieces[i].LineKey)] = true
	}
	liveBom := make(map[string]bool, len(in.BomItems))
	for i := range in.BomItems {
		liveBom[CutStripLineKey(in.BomItems[i].LineKey)] = true
	}
	for i := range in.Operations {
		if i >= len(stored.Operations) {
			break
		}
		in.Operations[i].TopstitchLines = nil
		opBom := make(map[string]bool, len(in.Operations[i].BomLineKeys))
		for _, key := range in.Operations[i].BomLineKeys {
			opBom[CutStripLineKey(key)] = true
		}
		for _, old := range stored.Operations[i].TopstitchLines {
			bomKey := CutStripLineKey(old.BomLineKey)
			if !liveBom[bomKey] || !opBom[bomKey] {
				continue
			}
			kept := true
			for _, part := range old.Parts {
				if !livePieces[CutStripLineKey(part.PieceLineKey)] {
					kept = false
					break
				}
			}
			if !kept {
				continue
			}
			line := old
			line.Parts = append([]TechCardSeamAnchor(nil), old.Parts...)
			KeepTopstitchSizes(&line, inRange)
			in.Operations[i].TopstitchLines = append(in.Operations[i].TopstitchLines, line)
		}
	}
}

// CarryStoredThreadPerMm makes an omitted factor visible to validation and digesting exactly as the
// guarded BOM UPDATE will preserve it.
func CarryStoredThreadPerMm(in, stored *TechCardInsert) {
	if in == nil || stored == nil {
		return
	}
	byKey := make(map[string]TechCardBomItem, len(stored.BomItems))
	for _, item := range stored.BomItems {
		byKey[CutStripLineKey(item.LineKey)] = item
	}
	for i := range in.BomItems {
		if !in.BomItems[i].ThreadPerMmOmitted {
			continue
		}
		if old, ok := byKey[CutStripLineKey(in.BomItems[i].LineKey)]; ok {
			in.BomItems[i].ThreadPerMm = old.ThreadPerMm
		}
	}
}
