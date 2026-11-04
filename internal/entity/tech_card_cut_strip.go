package entity

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// CUT-TO-LENGTH STRIPS (tech_card_cut_strip, 0449).
//
// A BOM line bought by the metre and cut to length (elastic, cord, tape, binding, ribbon) can carry a
// strip: a record of how long the piece cut for one garment is. A strip is never a row of
// tech_card_piece, so fabric consumption, marker and cut sheet do not see it.
//
// THE NORM IS RESOLVED ON READ, HERE, AND NEVER STAMPED. Every reader of a norm (colourway cost, style
// estimate, material plan, run readiness, cost basis report, advisories) walks the recipe rows of the
// enriched card, so ApplyCutStripNorms materialises the number on those rows in memory and no reader
// changes its formula. Stamping would need every write path (card save, recipe full-replace, colourway
// create, palette apply, restore, import, relink), would go stale on a size range change, and a new
// colourway would have no number until someone saved. Nothing derived reaches the database, and that
// is why ConsumptionSourceCutLength needs no value in chk_tccu_consumption_source.

const (
	// CutStripModeFromSeam: cut = round(seam_mm * fit_pct / 100 + overlap_mm), half-up, whole mm.
	CutStripModeFromSeam = "from_seam"
	// CutStripModePerSize: every size is typed by hand.
	CutStripModePerSize = "per_size"
	// CutStripModeOneLength: one number for all sizes, one norm.
	CutStripModeOneLength = "one_length"
)

// ValidCutStripModes is the set of accepted length modes.
var ValidCutStripModes = map[string]bool{
	CutStripModeFromSeam:  true,
	CutStripModePerSize:   true,
	CutStripModeOneLength: true,
}

// Limits of the write path. The upper bounds follow the column types of 0449. The fit range is wider
// than the 50..100 the panel offers: the mockup's range is a default for people, not a rule of physics.
const (
	CutStripMaxPerCard = 200
	CutStripMaxFitPct  = 200
	CutStripMaxMm      = 99999
	CutStripMaxQty     = 1000
)

// TechCardCutStripSize is one size of a strip.
type TechCardCutStripSize struct {
	StripId int `db:"strip_id"`
	SizeId  int `db:"size_id"`
	// SeamMm is the seam length at this size. The client parses geometry and sends the number.
	SeamMm decimal.NullDecimal `db:"seam_mm"`
	// ManualCutMm is the typed cut length. It wins over the formula and survives every other change.
	ManualCutMm sql.NullInt32 `db:"manual_cut_mm"`
}

// TechCardCutStrip is one strip of a BOM line.
type TechCardCutStrip struct {
	Id         int `db:"id"`
	TechCardId int `db:"tech_card_id"`
	BomItemId  int `db:"bom_item_id"`
	// BomLineKey is the wire reference of the line. The read joins it from tech_card_bom_item.
	BomLineKey   string                 `db:"bom_line_key"`
	LineKey      string                 `db:"line_key"`
	LengthMode   string                 `db:"length_mode"`
	SeamKey      sql.NullString         `db:"seam_key"`
	FitPct       decimal.Decimal        `db:"fit_pct"`
	OverlapMm    decimal.Decimal        `db:"overlap_mm"`
	WidthMm      decimal.Decimal        `db:"width_mm"`
	OneLengthMm  sql.NullInt32          `db:"one_length_mm"`
	Qty          int                    `db:"qty"`
	DisplayOrder int                    `db:"display_order"`
	Sizes        []TechCardCutStripSize `db:"-"`
}

// CutStripLengthMm is the one function that computes a strip's length. Norm, cost and print read it,
// directly or through CutStripLineNorms. The second result is false when the length is unknown, and
// unknown is never 0: a from_seam strip with no seam and no manual value has no length yet.
func CutStripLengthMm(strip *TechCardCutStrip, sizeID int) (int, bool) {
	if strip == nil {
		return 0, false
	}
	if strip.LengthMode == CutStripModeOneLength {
		if strip.OneLengthMm.Valid && strip.OneLengthMm.Int32 > 0 {
			return int(strip.OneLengthMm.Int32), true
		}
		return 0, false
	}
	size := strip.sizeRow(sizeID)
	if size == nil {
		return 0, false
	}
	if size.ManualCutMm.Valid && size.ManualCutMm.Int32 > 0 {
		return int(size.ManualCutMm.Int32), true
	}
	if strip.LengthMode != CutStripModeFromSeam || !size.SeamMm.Valid || !size.SeamMm.Decimal.IsPositive() {
		return 0, false
	}
	// Decimal, not float: 412.5 * 100 / 100 must round to 413 on every machine. Round is half away
	// from zero, which on a positive length is the half-up the spec asks for.
	cut := size.SeamMm.Decimal.Mul(strip.FitPct).Div(decimal.NewFromInt(100)).Add(strip.OverlapMm).Round(0)
	if !cut.IsPositive() {
		return 0, false
	}
	return int(cut.IntPart()), true
}

// CutStripSewnMm is the sewn length at a known cut: cut minus overlap.
func CutStripSewnMm(strip *TechCardCutStrip, cutMm int) decimal.Decimal {
	return decimal.NewFromInt(int64(cutMm)).Sub(strip.OverlapMm)
}

func (s *TechCardCutStrip) sizeRow(sizeID int) *TechCardCutStripSize {
	for i := range s.Sizes {
		if s.Sizes[i].SizeId == sizeID {
			return &s.Sizes[i]
		}
	}
	return nil
}

// CutStripLineNorm is what one BOM line gets from its strips, in metres per garment, before wastage.
type CutStripLineNorm struct {
	BomItemId  int
	BomLineKey string
	// Flat is set when every strip of the line is one_length, the case with one norm for all sizes
	// and the only one that needs no size range.
	Flat decimal.NullDecimal
	// BySize has a row only for a size whose length is known on every strip of the line. A size with
	// no row reads as no norm, which costing and the material plan already report as incomplete.
	BySize []TechCardBomSizeConsumption
	// Complete is false when any size has no known length or the line is not bought in metres.
	Complete bool
}

// IsCutStripUnit reports whether a BOM line's unit is one a strip's norm can be expressed in. The norm
// is metres, and it is never reinterpreted for any other unit.
func IsCutStripUnit(unit string) bool {
	u, ok := NormalizeMaterialUnit(unit)
	return ok && u == MaterialUnitM
}

// CutStripLineProblem holds the two rules of the mark and returns the code of the broken one, or "". A
// strip's norm is metres, so the line is bought by the metre, and the main fabric is cut as pieces.
func CutStripLineProblem(unit string, section TechCardBomSection, purpose string) string {
	if !IsCutStripUnit(unit) {
		return "cut_strip_not_metres"
	}
	if role, _ := DerivePieceLayerRole(section, purpose); role == BomPurposeMain {
		return "cut_strip_main_fabric"
	}
	return ""
}

// CutStripLineKey is the comparison form of a BOM line or strip key. The columns collate case
// insensitively and legitimate mints are upper case.
func CutStripLineKey(key string) string {
	return strings.ToUpper(strings.TrimSpace(key))
}

// CutStripLineNorms computes the norm of every BOM line that has strips, in BOM order.
func CutStripLineNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, sizeIDs []int) []CutStripLineNorm {
	if len(strips) == 0 {
		return nil
	}
	byLine := make(map[string][]*TechCardCutStrip, len(strips))
	for i := range strips {
		key := CutStripLineKey(strips[i].BomLineKey)
		byLine[key] = append(byLine[key], &strips[i])
	}
	out := make([]CutStripLineNorm, 0, len(byLine))
	for i := range bomItems {
		onLine := byLine[CutStripLineKey(bomItems[i].LineKey)]
		if len(onLine) == 0 {
			continue
		}
		out = append(out, cutStripLineNorm(&bomItems[i], onLine, sizeIDs))
	}
	return out
}

func cutStripLineNorm(bom *TechCardBomItem, strips []*TechCardCutStrip, sizeIDs []int) CutStripLineNorm {
	norm := CutStripLineNorm{BomItemId: bom.Id, BomLineKey: bom.LineKey}
	if !IsCutStripUnit(bom.Unit.String) {
		return norm
	}
	allOne := true
	for _, s := range strips {
		allOne = allOne && s.LengthMode == CutStripModeOneLength
	}
	if allOne {
		if sum, ok := cutStripSumMetres(strips, 0); ok {
			norm.Flat = decimal.NullDecimal{Decimal: sum, Valid: true}
			norm.Complete = true
		}
		return norm
	}
	sizes := sizeIDs
	if len(sizes) == 0 {
		sizes = cutStripRowSizes(strips)
	}
	norm.Complete = len(sizes) > 0
	for _, sizeID := range sizes {
		sum, ok := cutStripSumMetres(strips, sizeID)
		if !ok {
			norm.Complete = false
			continue
		}
		norm.BySize = append(norm.BySize, TechCardBomSizeConsumption{SizeId: sizeID, Consumption: sum})
	}
	return norm
}

// cutStripSumMetres is cut * qty / 1000 over the strips of one line. One unknown strip makes the sum
// unknown: a partial sum would price a garment missing one of its strips.
func cutStripSumMetres(strips []*TechCardCutStrip, sizeID int) (decimal.Decimal, bool) {
	sum := decimal.Zero
	for _, s := range strips {
		mm, ok := CutStripLengthMm(s, sizeID)
		if !ok {
			return decimal.Zero, false
		}
		qty := s.Qty
		if qty < 1 {
			qty = 1
		}
		sum = sum.Add(decimal.NewFromInt(int64(mm) * int64(qty)))
	}
	return sum.Div(decimal.NewFromInt(1000)), true
}

func cutStripRowSizes(strips []*TechCardCutStrip) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range strips {
		for i := range s.Sizes {
			if id := s.Sizes[i].SizeId; !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Ints(out)
	return out
}

// ApplyCutStripNorms is the read-time resolver: it marks the strip lines and puts each line's norm on
// the recipe rows of every colourway. Run once on a freshly read card, before anyone reads a norm.
func ApplyCutStripNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, sizeIDs []int, colorways []TechCardColorway) {
	norms := CutStripLineNorms(bomItems, strips, sizeIDs)
	if len(norms) == 0 {
		return
	}
	MarkCutStripLines(bomItems, norms)
	for i := range colorways {
		colorways[i].Usages = ResolveCutStripUsages(colorways[i].Usages, bomItems, norms)
	}
}

// MarkCutStripLines sets the in-memory mark on the strip lines. The mark is what keeps the countable
// path of a hardware line quiet (SlotCarriesCountableNorm, CountablePairQty). The slot's own
// qty_per_garment and spare_qty stay untouched, because the MATERIALS digest hashes them.
func MarkCutStripLines(bomItems []TechCardBomItem, norms []CutStripLineNorm) {
	for i := range norms {
		for j := range bomItems {
			if bomItems[j].Id == norms[i].BomItemId {
				bomItems[j].CutToLength = true
			}
		}
	}
}

// ResolveCutStripUsages returns one colourway's recipe with the strip lines' numbers on its rows.
//
//   - The first garment-level row of a line carries the norm. A further row of the same line, a second
//     placement, gets a valid zero so that it cannot multiply the norm.
//   - A stored number on such a row is replaced in memory: the norm of a strip line is not typed.
//   - A colourway with no row for the line gets one with Id 0. It is never written.
//   - A piece-bound row states which fabric a piece is cut from and carries no norm, so it stays.
//   - A legacy positional row, resolved by bom_item_index, is part of the slot and is resolved too.
func ResolveCutStripUsages(usages []TechCardColorwayUsage, bomItems []TechCardBomItem, norms []CutStripLineNorm) []TechCardColorwayUsage {
	if len(norms) == 0 {
		return usages
	}
	byID := make(map[int]*CutStripLineNorm, len(norms))
	for i := range norms {
		byID[norms[i].BomItemId] = &norms[i]
	}
	normOf := func(u *TechCardColorwayUsage) *CutStripLineNorm {
		if u.BomItemId.Valid {
			return byID[int(u.BomItemId.Int64)]
		}
		if u.BomItemIndex.Valid {
			if idx := int(u.BomItemIndex.Int32); idx >= 0 && idx < len(bomItems) {
				return byID[bomItems[idx].Id]
			}
		}
		return nil
	}
	carried := make(map[int]bool, len(norms))
	for i := range usages {
		u := &usages[i]
		if u.IsPieceMaterialAssignment() {
			continue
		}
		norm := normOf(u)
		if norm == nil {
			continue
		}
		u.Consumption, u.Quantity, u.SizeConsumptions = decimal.NullDecimal{}, decimal.NullDecimal{}, nil
		u.WasteSelvedgePct, u.WasteCutPct = decimal.NullDecimal{}, decimal.NullDecimal{}
		u.NormMarkerId, u.NormAppliedAt = sql.NullInt64{}, sql.NullTime{}
		u.ConsumptionSource = sql.NullString{String: ConsumptionSourceCutLength, Valid: true}
		if carried[norm.BomItemId] {
			u.Consumption = decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}
			continue
		}
		carried[norm.BomItemId] = true
		norm.putOn(u)
	}
	for i := range norms {
		if carried[norms[i].BomItemId] {
			continue
		}
		u := TechCardColorwayUsage{
			BomItemId:         sql.NullInt64{Int64: int64(norms[i].BomItemId), Valid: true},
			BomLineKey:        norms[i].BomLineKey,
			ConsumptionSource: sql.NullString{String: ConsumptionSourceCutLength, Valid: true},
		}
		norms[i].putOn(&u)
		usages = append(usages, u)
	}
	return usages
}

func (n *CutStripLineNorm) putOn(u *TechCardColorwayUsage) {
	u.Consumption = n.Flat
	if len(n.BySize) > 0 {
		u.SizeConsumptions = append([]TechCardBomSizeConsumption(nil), n.BySize...)
	}
}

// ClearCutStripRecipeRow strips a recipe row of a strip line down to what really is the colourway's, and
// reports whether anything is left to store. The source goes to manual explicitly, so a stored
// provenance is not carried across this row: the store's carry applies only to a presence-less source.
func ClearCutStripRecipeRow(u *TechCardColorwayUsage) bool {
	claimed := u.IsCutLengthNorm()
	u.Consumption, u.Quantity, u.SizeConsumptions = decimal.NullDecimal{}, decimal.NullDecimal{}, nil
	u.WasteSelvedgePct, u.WasteCutPct = decimal.NullDecimal{}, decimal.NullDecimal{}
	u.NormMarkerId, u.NormMarkerIdSet = sql.NullInt64{}, true
	u.NormAppliedAt = sql.NullTime{}
	// A row of a stale client names no material and no source: its material_id is absent, which means
	// «keep the stored pin», so it is not bare. A row claiming cut_length is an echo of what the read
	// gave, and that echo states the pin explicitly when there is one.
	presenceLess := !claimed && !u.MaterialIdSet
	u.ConsumptionSource = sql.NullString{String: ConsumptionSourceManual, Valid: true}
	return presenceLess || u.MaterialIdSet && u.MaterialId.Valid && u.MaterialId.Int64 > 0 ||
		u.Color.Valid && u.Color.String != "" || u.Pantone.Valid && u.Pantone.String != "" ||
		u.Placement.Valid && u.Placement.String != ""
}

// IsCutLengthNorm reports that this recipe row's numbers came from the line's strips. Such a row is not
// a colourway's own recipe: a colourway that inherits the style's figure must keep inheriting it.
func (u *TechCardColorwayUsage) IsCutLengthNorm() bool {
	return u.ConsumptionSource.Valid && u.ConsumptionSource.String == ConsumptionSourceCutLength
}

// KeepCutStripSizes drops the size rows outside the range. The store prunes the same rows on a save that
// narrows the range, so the digest of the write equals the digest of the next read. An empty range
// filters nothing: a card with no declared range keeps what it has.
func KeepCutStripSizes(s *TechCardCutStrip, inRange map[int]bool) {
	if len(inRange) == 0 || len(s.Sizes) == 0 {
		return
	}
	kept := make([]TechCardCutStripSize, 0, len(s.Sizes))
	for i := range s.Sizes {
		if inRange[s.Sizes[i].SizeId] {
			kept = append(kept, s.Sizes[i])
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	s.Sizes = kept
}

// CarryStoredCutStrips puts the stored strips on the payload of a client that does not know about them.
// The store writes nothing for such a payload. The carry is there so that the assembly classifier and
// the digests see the card as it will read next: an echoed strip input stays a strip input. A strip
// whose BOM line this save removes goes with the line, as the foreign key will do it.
func CarryStoredCutStrips(in, stored *TechCardInsert) {
	if in == nil || stored == nil || in.CutStripsAware || len(stored.CutStrips) == 0 {
		return
	}
	lines := make(map[string]bool, len(in.BomItems))
	for i := range in.BomItems {
		lines[CutStripLineKey(in.BomItems[i].LineKey)] = true
	}
	inRange := make(map[int]bool, len(in.SizeIds))
	for _, id := range in.SizeIds {
		inRange[id] = true
	}
	in.CutStrips = nil
	for i := range stored.CutStrips {
		if !lines[CutStripLineKey(stored.CutStrips[i].BomLineKey)] {
			continue
		}
		strip := stored.CutStrips[i]
		strip.Sizes = append([]TechCardCutStripSize(nil), strip.Sizes...)
		KeepCutStripSizes(&strip, inRange)
		in.CutStrips = append(in.CutStrips, strip)
	}
}

// CutStripKeySet is the classifier's set of strip keys.
func CutStripKeySet(strips []TechCardCutStrip) map[string]bool {
	if len(strips) == 0 {
		return nil
	}
	out := make(map[string]bool, len(strips))
	for i := range strips {
		if strips[i].LineKey != "" {
			out[strips[i].LineKey] = true
		}
	}
	return out
}

// CutStripInputOf finds an operation of the payload that still takes the strip as an input.
func CutStripInputOf(ops []TechCardOperation, stripKey string) (int, bool) {
	for i := range ops {
		for _, in := range ops[i].AssemblyInputs {
			if in.Kind == AssemblyInputStrip && CutStripLineKey(in.Key) == CutStripLineKey(stripKey) {
				return i, true
			}
		}
	}
	return 0, false
}

// NormalizeCutStrip validates one strip of a write and brings it to the form the read returns, so that
// the digest of the write equals the digest of the read. path is the wire address of the strip.
func NormalizeCutStrip(s *TechCardCutStrip, path string) error {
	field := func(name string) string { return path + "." + name }
	s.BomLineKey = strings.TrimSpace(s.BomLineKey)
	if s.BomLineKey == "" {
		return NewFieldViolation(field("bom_line_key"), "required", "", "a strip is cut from a BOM line")
	}
	if !ValidCutStripModes[s.LengthMode] {
		return NewFieldViolation(field("length_mode"), "invalid", s.LengthMode, "from_seam, per_size or one_length")
	}
	if s.FitPct.IsZero() {
		s.FitPct = decimal.NewFromInt(100)
	}
	s.FitPct = s.FitPct.Round(2)
	if !s.FitPct.IsPositive() || s.FitPct.GreaterThan(decimal.NewFromInt(CutStripMaxFitPct)) {
		return NewFieldViolation(field("fit_pct"), "out_of_range", s.FitPct.String(),
			fmt.Sprintf("a percentage of the seam above 0 and up to %d", CutStripMaxFitPct))
	}
	s.OverlapMm = s.OverlapMm.Round(2)
	if s.OverlapMm.IsNegative() || s.OverlapMm.GreaterThan(decimal.NewFromInt(CutStripMaxMm)) {
		return NewFieldViolation(field("overlap_mm"), "out_of_range", s.OverlapMm.String(),
			fmt.Sprintf("0 to %d mm", CutStripMaxMm))
	}
	s.WidthMm = s.WidthMm.Round(2)
	if !s.WidthMm.IsPositive() || s.WidthMm.GreaterThan(decimal.NewFromInt(CutStripMaxMm)) {
		return NewFieldViolation(field("width_mm"), "out_of_range", s.WidthMm.String(),
			fmt.Sprintf("above 0 and up to %d mm", CutStripMaxMm))
	}
	if s.OneLengthMm.Valid && (s.OneLengthMm.Int32 <= 0 || s.OneLengthMm.Int32 > CutStripMaxMm) {
		return NewFieldViolation(field("one_length_mm"), "out_of_range", fmt.Sprint(s.OneLengthMm.Int32),
			fmt.Sprintf("above 0 and up to %d mm", CutStripMaxMm))
	}
	if s.LengthMode == CutStripModeOneLength && !s.OneLengthMm.Valid {
		return NewFieldViolation(field("one_length_mm"), "required", "", "one length needs a length")
	}
	if s.Qty == 0 {
		s.Qty = 1
	}
	if s.Qty < 1 || s.Qty > CutStripMaxQty {
		return NewFieldViolation(field("qty"), "out_of_range", fmt.Sprint(s.Qty), fmt.Sprintf("1 to %d", CutStripMaxQty))
	}
	if s.SeamKey.Valid {
		s.SeamKey.String = NormalizeTechCardSeamKey(s.SeamKey.String)
		s.SeamKey.Valid = s.SeamKey.String != ""
		if len(s.SeamKey.String) > 26 {
			return NewFieldViolation(field("seam_key"), "invalid", s.SeamKey.String, "a seam key is up to 26 characters")
		}
	}
	return s.normalizeSizes(path)
}

func (s *TechCardCutStrip) normalizeSizes(path string) error {
	kept := make([]TechCardCutStripSize, 0, len(s.Sizes))
	seen := make(map[int]bool, len(s.Sizes))
	for i := range s.Sizes {
		row := s.Sizes[i]
		rowPath := fmt.Sprintf("%s.sizes[%d]", path, i)
		if row.SizeId <= 0 {
			return NewFieldViolation(rowPath+".size_id", "required", "", "a size of the card range")
		}
		if seen[row.SizeId] {
			return NewFieldViolation(rowPath+".size_id", "duplicate", fmt.Sprint(row.SizeId), "one row per size")
		}
		seen[row.SizeId] = true
		if row.SeamMm.Valid {
			row.SeamMm.Decimal = row.SeamMm.Decimal.Round(2)
			if !row.SeamMm.Decimal.IsPositive() || row.SeamMm.Decimal.GreaterThan(decimal.NewFromInt(9999999)) {
				return NewFieldViolation(rowPath+".seam_mm", "out_of_range", row.SeamMm.Decimal.String(), "a seam length above 0")
			}
		}
		if row.ManualCutMm.Valid && (row.ManualCutMm.Int32 <= 0 || row.ManualCutMm.Int32 > CutStripMaxMm) {
			return NewFieldViolation(rowPath+".manual_cut_mm", "out_of_range", fmt.Sprint(row.ManualCutMm.Int32),
				fmt.Sprintf("above 0 and up to %d mm", CutStripMaxMm))
		}
		if !row.SeamMm.Valid && !row.ManualCutMm.Valid {
			continue // an empty row is no row, or the read and the write would hash differently
		}
		kept = append(kept, row)
	}
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].SizeId < kept[b].SizeId })
	if len(kept) == 0 {
		kept = nil
	}
	s.Sizes = kept
	return nil
}
