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

// DerivedLineNorm is what one BOM line gets from its cut strips and measured topstitch lines, in
// metres per garment, before wastage.
type DerivedLineNorm struct {
	BomItemId  int
	BomLineKey string
	// Flat is set when every contributor is size-independent. Topstitch runs are always per-size.
	Flat decimal.NullDecimal
	// BySize has a row only for a size whose length is known on every contributor. A size with
	// no row reads as no norm, which costing and the material plan already report as incomplete.
	BySize []TechCardBomSizeConsumption
	// Complete is false when any size has no known length or the line is not bought in metres.
	Complete bool
}

// CutStripLineNorm is kept as an alias for callers compiled against the original strip-only name.
type CutStripLineNorm = DerivedLineNorm

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

// CutStripLineNorms computes strip-only norms. New card readers use DerivedLineNorms so measured
// topstitch thread is composed onto the same BOM line and manual-restore path.
func CutStripLineNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, sizeIDs []int) []CutStripLineNorm {
	return DerivedLineNorms(bomItems, strips, nil, sizeIDs)
}

// DerivedLineNorms is the compatibility entry point for callers with no loaded material catalog.
// Roll lines can still resolve from the BOM line's fabric-width snapshot.
func DerivedLineNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, topstitch []TechCardTopstitchLine, sizeIDs []int) []DerivedLineNorm {
	return DerivedLineNormsForRecipe(bomItems, strips, topstitch, sizeIDs, nil, nil)
}

// DerivedLineNormsForRecipe composes every derived contribution per BOM line, in BOM order.
// Running-basis strips contribute cut_mm * qty / 1000. Roll-basis strips contribute
// cut_mm * qty / floor(cutting_width_mm / strip_width_mm) / 1000. Topstitch always contributes
// running metres. One unknown contributor, width or conflicting colourway pin makes that size
// unknown; a partial sum is never returned.
func DerivedLineNormsForRecipe(bomItems []TechCardBomItem, strips []TechCardCutStrip,
	topstitch []TechCardTopstitchLine, sizeIDs []int, linked map[int]MaterialWithPrice,
	usages []TechCardColorwayUsage,
) []DerivedLineNorm {
	if len(strips) == 0 && len(topstitch) == 0 {
		return nil
	}
	byLine := make(map[string][]*TechCardCutStrip, len(strips))
	for i := range strips {
		key := CutStripLineKey(strips[i].BomLineKey)
		byLine[key] = append(byLine[key], &strips[i])
	}
	topByLine := make(map[string][]*TechCardTopstitchLine, len(topstitch))
	for i := range topstitch {
		key := CutStripLineKey(topstitch[i].BomLineKey)
		topByLine[key] = append(topByLine[key], &topstitch[i])
	}
	out := make([]DerivedLineNorm, 0, len(byLine)+len(topByLine))
	for i := range bomItems {
		key := CutStripLineKey(bomItems[i].LineKey)
		onLine, topOnLine := byLine[key], topByLine[key]
		if len(onLine) == 0 && len(topOnLine) == 0 {
			continue
		}
		pinned, conflict := derivedLinePinnedArticle(bomItems, i, usages)
		out = append(out, derivedLineNorm(&bomItems[i], onLine, topOnLine, sizeIDs, linked, pinned, conflict))
	}
	return out
}

func derivedLinePinnedArticle(bomItems []TechCardBomItem, bomIndex int, usages []TechCardColorwayUsage) (int64, bool) {
	bom := &bomItems[bomIndex]
	var pinned int64
	for i := range usages {
		u := &usages[i]
		if u.IsPieceMaterialAssignment() || !usageBelongsToBom(u, bomItems, bomIndex) ||
			!u.MaterialId.Valid || u.MaterialId.Int64 <= 0 ||
			(bom.MaterialId.Valid && u.MaterialId.Int64 == bom.MaterialId.Int64) {
			continue
		}
		if pinned == 0 {
			pinned = u.MaterialId.Int64
			continue
		}
		if pinned != u.MaterialId.Int64 {
			return 0, true
		}
	}
	return pinned, false
}

func usageBelongsToBom(u *TechCardColorwayUsage, bomItems []TechCardBomItem, bomIndex int) bool {
	if u.BomItemId.Valid {
		return int(u.BomItemId.Int64) == bomItems[bomIndex].Id
	}
	return u.BomItemIndex.Valid && int(u.BomItemIndex.Int32) == bomIndex
}

func derivedLineNorm(bom *TechCardBomItem, strips []*TechCardCutStrip, topstitch []*TechCardTopstitchLine,
	sizeIDs []int, linked map[int]MaterialWithPrice, pinned int64, pinConflict bool,
) DerivedLineNorm {
	norm := DerivedLineNorm{BomItemId: bom.Id, BomLineKey: bom.LineKey}
	if CutStripLineProblem(bom.Unit.String, bom.Section, bom.Purpose.String) != "" || pinConflict {
		return norm
	}
	rollBasis := len(strips) > 0 && IsRollGoodsSection(bom.Section)
	cuttingWidth := decimal.NullDecimal{}
	if rollBasis {
		cuttingWidth = CuttingWidthCm(bom, pinned, linked)
	}
	allOne := len(topstitch) == 0
	for _, s := range strips {
		allOne = allOne && s.LengthMode == CutStripModeOneLength
	}
	if allOne {
		if sum, ok := derivedSumMetres(bom, strips, nil, 0, rollBasis, cuttingWidth); ok {
			norm.Flat = decimal.NullDecimal{Decimal: sum, Valid: true}
			norm.Complete = true
		}
		return norm
	}
	sizes := sizeIDs
	if len(sizes) == 0 {
		sizes = derivedRowSizes(strips, topstitch)
	}
	norm.Complete = len(sizes) > 0
	for _, sizeID := range sizes {
		sum, ok := derivedSumMetres(bom, strips, topstitch, sizeID, rollBasis, cuttingWidth)
		if !ok {
			norm.Complete = false
			continue
		}
		norm.BySize = append(norm.BySize, TechCardBomSizeConsumption{SizeId: sizeID, Consumption: sum})
	}
	return norm
}

// derivedSumMetres is the complete sum for one line and size. The final line norm is rounded to the
// same three decimals the strip contract exposes.
func derivedSumMetres(bom *TechCardBomItem, strips []*TechCardCutStrip, topstitch []*TechCardTopstitchLine,
	sizeID int, rollBasis bool, cuttingWidth decimal.NullDecimal,
) (decimal.Decimal, bool) {
	strip, ok := cutStripSumMetres(strips, sizeID, rollBasis, cuttingWidth)
	if !ok {
		return decimal.Zero, false
	}
	sum := strip
	if len(topstitch) > 0 && !bom.ThreadPerMm.Valid {
		return decimal.Zero, false
	}
	for _, line := range topstitch {
		if line.Stale {
			return decimal.Zero, false
		}
		var mm decimal.Decimal
		found := false
		for _, row := range line.Sizes {
			if row.SizeId == sizeID && row.LineMm.Valid {
				mm, found = row.LineMm.Decimal, true
				break
			}
		}
		if !found {
			return decimal.Zero, false
		}
		sum = sum.Add(mm.Mul(decimal.NewFromInt(int64(line.Rows))).Mul(bom.ThreadPerMm.Decimal).Div(decimal.NewFromInt(1000)))
	}
	return sum.Round(3), true
}

// cutStripSumMetres is cut * qty / 1000 over the strips of one line. One unknown strip makes the sum
// unknown: a partial sum would price a garment missing one of its strips.
func cutStripSumMetres(strips []*TechCardCutStrip, sizeID int, rollBasis bool,
	cuttingWidth decimal.NullDecimal,
) (decimal.Decimal, bool) {
	if rollBasis && (!cuttingWidth.Valid || !cuttingWidth.Decimal.IsPositive()) {
		return decimal.Zero, false
	}
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
		contribution := decimal.NewFromInt(int64(mm) * int64(qty))
		if rollBasis {
			if !s.WidthMm.IsPositive() {
				return decimal.Zero, false
			}
			lanes := cuttingWidth.Decimal.Mul(decimal.NewFromInt(10)).Div(s.WidthMm).Floor().IntPart()
			if lanes < 1 {
				return decimal.Zero, false
			}
			contribution = contribution.Div(decimal.NewFromInt(lanes))
		}
		sum = sum.Add(contribution)
	}
	return sum.Div(decimal.NewFromInt(1000)), true
}

func derivedRowSizes(strips []*TechCardCutStrip, topstitch []*TechCardTopstitchLine) []int {
	seen := map[int]bool{}
	out := cutStripRowSizes(strips)
	for _, id := range out {
		seen[id] = true
	}
	for _, line := range topstitch {
		for _, row := range line.Sizes {
			if !seen[row.SizeId] {
				seen[row.SizeId] = true
				out = append(out, row.SizeId)
			}
		}
	}
	sort.Ints(out)
	return out
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

// ApplyCutStripNorms is the strip-only read-time compatibility wrapper. It marks lines and puts norms on
// the recipe rows of every colourway. Run once on a freshly read card, before anyone reads a norm.
func ApplyCutStripNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, sizeIDs []int, colorways []TechCardColorway) {
	ApplyDerivedLineNorms(bomItems, strips, nil, sizeIDs, colorways)
}

// ApplyDerivedLineNorms is the read-time resolver for both strip and topstitch-derived lines.
func ApplyDerivedLineNorms(bomItems []TechCardBomItem, strips []TechCardCutStrip, topstitch []TechCardTopstitchLine, sizeIDs []int, colorways []TechCardColorway) {
	ApplyDerivedLineNormsWithMaterials(bomItems, strips, topstitch, sizeIDs, colorways, nil)
}

// ApplyDerivedLineNormsWithMaterials resolves the card-level default basis and then each colourway's
// own article pin basis. The card-level projection never inherits a colourway pin.
func ApplyDerivedLineNormsWithMaterials(bomItems []TechCardBomItem, strips []TechCardCutStrip,
	topstitch []TechCardTopstitchLine, sizeIDs []int, colorways []TechCardColorway,
	linked map[int]MaterialWithPrice,
) {
	norms := DerivedLineNormsForRecipe(bomItems, strips, topstitch, sizeIDs, linked, nil)
	if len(norms) == 0 {
		return
	}
	MarkDerivedLines(bomItems, norms)
	for i := range colorways {
		colourNorms := DerivedLineNormsForRecipe(bomItems, strips, topstitch, sizeIDs, linked, colorways[i].Usages)
		colorways[i].Usages = ResolveDerivedLineUsages(colorways[i].Usages, bomItems, colourNorms)
	}
}

// MarkCutStripLines is the strip-only compatibility wrapper for the in-memory derived-line mark.
// The mark is what keeps the countable
// path of a hardware line quiet (SlotCarriesCountableNorm, CountablePairQty). The slot's own
// qty_per_garment and spare_qty stay untouched, because the MATERIALS digest hashes them.
func MarkCutStripLines(bomItems []TechCardBomItem, norms []CutStripLineNorm) {
	MarkDerivedLines(bomItems, norms)
}

// MarkDerivedLines marks BOM rows whose norm is supplied by derived line geometry.
func MarkDerivedLines(bomItems []TechCardBomItem, norms []DerivedLineNorm) {
	for i := range norms {
		for j := range bomItems {
			if bomItems[j].Id == norms[i].BomItemId {
				bomItems[j].CutToLength = true
			}
		}
	}
}

// ResolveCutStripUsages is the strip-only compatibility wrapper for recipe norm resolution.
//
//   - The first garment-level row of a line carries the norm. A further row of the same line, a second
//     placement, gets a valid zero so that it cannot multiply the norm.
//   - A stored number on such a row is replaced in memory: the norm of a strip line is not typed. The
//     table keeps it, and StoredNorm holds it for the one reader that moves a row as stored.
//   - A colourway with no row for the line gets one with Id 0. It is never written.
//   - A piece-bound row states which fabric a piece is cut from and carries no norm, so it stays.
//   - A legacy positional row, resolved by bom_item_index, is part of the slot and is resolved too.
func ResolveCutStripUsages(usages []TechCardColorwayUsage, bomItems []TechCardBomItem, norms []CutStripLineNorm) []TechCardColorwayUsage {
	return ResolveDerivedLineUsages(usages, bomItems, norms)
}

// ResolveDerivedLineUsages applies composed line norms to recipe rows using cut_length provenance.
func ResolveDerivedLineUsages(usages []TechCardColorwayUsage, bomItems []TechCardBomItem, norms []DerivedLineNorm) []TechCardColorwayUsage {
	if len(norms) == 0 {
		return usages
	}
	byID := make(map[int]*DerivedLineNorm, len(norms))
	for i := range norms {
		byID[norms[i].BomItemId] = &norms[i]
	}
	normOf := func(u *TechCardColorwayUsage) *DerivedLineNorm {
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
		if u.StoredNorm == nil {
			u.StoredNorm = &CutStripStoredNorm{
				Consumption: u.Consumption, Quantity: u.Quantity, SizeConsumptions: u.SizeConsumptions,
				ConsumptionSource: u.ConsumptionSource,
				WasteSelvedgePct:  u.WasteSelvedgePct, WasteCutPct: u.WasteCutPct,
				NormMarkerId: u.NormMarkerId, NormAppliedAt: u.NormAppliedAt,
			}
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

func (n *DerivedLineNorm) putOn(u *TechCardColorwayUsage) {
	u.Consumption = n.Flat
	if len(n.BySize) > 0 {
		u.SizeConsumptions = append([]TechCardBomSizeConsumption(nil), n.BySize...)
	}
}

// CutStripStoredNorm is what a recipe row of a strip line holds in the table. While a strip exists no
// reader gets these numbers, and they wait there: removing the strip brings them back.
type CutStripStoredNorm struct {
	Consumption       decimal.NullDecimal
	Quantity          decimal.NullDecimal
	SizeConsumptions  []TechCardBomSizeConsumption
	ConsumptionSource sql.NullString
	WasteSelvedgePct  decimal.NullDecimal
	WasteCutPct       decimal.NullDecimal
	NormMarkerId      sql.NullInt64
	NormAppliedAt     sql.NullTime
}

// StoredRow returns the row with the numbers the table holds: a row the resolver did not touch as it is,
// a resolved one with what it replaced, and a made-up one with no numbers.
func (u *TechCardColorwayUsage) StoredRow() TechCardColorwayUsage {
	out := *u
	if !u.IsCutLengthNorm() {
		return out
	}
	stored := CutStripStoredNorm{}
	if u.StoredNorm != nil {
		stored = *u.StoredNorm
	}
	out.Consumption, out.Quantity, out.SizeConsumptions = stored.Consumption, stored.Quantity, stored.SizeConsumptions
	out.ConsumptionSource = stored.ConsumptionSource
	out.WasteSelvedgePct, out.WasteCutPct = stored.WasteSelvedgePct, stored.WasteCutPct
	out.NormMarkerId, out.NormAppliedAt = stored.NormMarkerId, stored.NormAppliedAt
	return out
}

// ClearCutStripRecipeRow strips a recipe row of a strip line with no stored row behind it down to what
// really is the colourway's, and reports whether anything is left to store. The source goes to manual
// explicitly, so a stored provenance is not carried across this row: the store's carry applies only to a
// presence-less source.
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
	// Zero is "not typed yet": ticking the mark makes the strip before anyone knows its width.
	if s.WidthMm.IsNegative() || s.WidthMm.GreaterThan(decimal.NewFromInt(CutStripMaxMm)) {
		return NewFieldViolation(field("width_mm"), "out_of_range", s.WidthMm.String(),
			fmt.Sprintf("0 to %d mm", CutStripMaxMm))
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
