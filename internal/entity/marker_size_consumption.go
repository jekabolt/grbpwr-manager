package entity

import (
	"github.com/shopspring/decimal"
)

// РАСХОД ПО ПЛОЩАДИ (Ф2.4). Ф2 gave a раскладка a СОСТАВ and, with it, a problem it deliberately
// refused to paper over: used_length / total_units on a mixed состав is the MEAN across sizes — it
// overstates S and understates XL by exactly the spread Ф2 exists to remove — so the server withheld
// the scalar instead of labelling it (MarkerScalarNormRefusal). This file is what LIFTS that
// withholding: every size of the состав gets its OWN number, and the mean stops being the only thing
// on offer.
//
// ТА САМАЯ ФОРМУЛА, из 03-composition.md:
//
//	расход(размер) = площадь его деталей × (длина настила / суммарная площадь состава)
//
// Written out with the blob's own quantities — a_s is the area of ONE garment of size s, q_s is how
// many garments of that size the настил cuts, L is used_length_cm:
//
//	A = Σ_s q_s · a_s          (the area of the WHOLE состав, i.e. of everything the spread lays out)
//	расход(s) = a_s · L / A
//
// СХОДИМОСТЬ ЭТО НЕ ПОЖЕЛАНИЕ, А ТОЖДЕСТВО, and it is the acceptance criterion the plan names:
//
//	Σ_s q_s · расход(s) = (L / A) · Σ_s q_s · a_s = (L / A) · A = L
//
// The distributed lengths add back up to the length that was measured — exactly, in exact
// arithmetic, and independently of how a_s was rounded on the way to storage (the same a_s appears in
// the numerator and inside A). A distribution that does not converge is not a rounding question: it
// means the areas and the состав describe different раскладки, and the tests assert the identity
// rather than a tolerance for that reason.
//
// ЧТО ЗДЕСЬ ПРИНЦИПИАЛЬНО НЕ ЖИВЁТ — размеры, которых нет в составе. The plan continues the same
// formula across the whole размерный ряд using piece areas «известны из файлов БЕЗ маркера», and
// THE SERVER CANNOT DO THAT HALF: it never parses a DXF, and the blob it does read contains pieces
// only for the sizes the состав cuts (the save path refuses a piece pointing at a size the состав
// does not cut — dto.MarkerLayoutFactsFromPb). So the server publishes the two numbers that make the
// continuation possible for whoever CAN parse the files — a_s per size (AreaPerGarmentCm2, on the
// wire beside each состав line) and, through them, the constant L/A — and stops there. Inventing an
// area for a size it has never seen the выкройки of would be exactly the plausible-looking lie this
// whole subsystem is built to refuse.

// MarkerPieceArea is one distinct piece of a layout blob, reduced to what the area distribution
// needs: which size's gradation it belongs to, how many instances ONE GARMENT cuts, and the area of
// one instance. It exists so the arithmetic can live in entity — pure, and reachable by a test with
// no protobuf and no database — while dto keeps the job of reading the blob, exactly as
// MarkerLayoutFacts splits that same seam.
type MarkerPieceArea struct {
	// SizeId is the size whose GRADATION this contour belongs to. 0 = size-agnostic: a block with no
	// size suffix does not grade, so ONE set of it is cut per garment of the WHOLE состав.
	SizeId int
	// Quantity is instances per ONE GARMENT (TechCardMarkerPiece.quantity), >= 1.
	Quantity int
	// AreaCm2 is the area of ONE instance of the contour.
	AreaCm2 decimal.Decimal
}

// MarkerSizeAreasPerGarment computes a_s — the area of ONE garment of each size of the состав, cm² —
// out of the blob's pieces. It is the ONLY place the size-agnostic attribution is decided, so the
// decision is stated here once:
//
// ДЕТАЛИ БЕЗ РАЗМЕРА ПРИНАДЛЕЖАТ КАЖДОМУ ИЗДЕЛИЮ ЦЕЛИКОМ, НЕ ДОЛЕЙ ОТ СОСТАВА. The instance formula
// on TechCardMarkerPiece already says so — a piece with no size is cut `quantity × total_units`
// times, i.e. `quantity` times per garment, the same for every size — and the area attribution must
// agree with the geometry that was actually laid out, not invent a second rule beside it. So a_s
// carries the FULL per-garment area of every size-agnostic piece, identically for S and for XL, and
// what differs between sizes is only the graded part. Attributing those pieces by a size's SHARE of
// the состав (q_s / total_units) would have been the other plausible reading and it is wrong twice
// over: it would make a size's own norm depend on how many of the OTHER sizes happened to be in the
// spread, and on a раскладка where nothing grades it would hand the small sizes less cloth than the
// large ones for pieces that are literally the same contour.
//
// Note that the identity Σ_s q_s · a_s = Σ_pieces area × instances holds under this attribution and
// under no other — the agnostic term telescopes into total_units × (per-garment agnostic area),
// which is precisely its instance count. That is why the convergence criterion is provable rather
// than merely tested.
//
// ok = false means the distribution cannot be based on this blob at all, and the caller must then
// publish NOTHING per size rather than something partial: a denominator missing one size's pieces
// makes every OTHER size's number wrong too, silently and in the expensive direction.
func MarkerSizeAreasPerGarment(composition []MarkerCompositionEntry, pieces []MarkerPieceArea) (map[int]decimal.Decimal, bool) {
	if len(composition) == 0 || len(pieces) == 0 {
		return nil, false
	}
	inComposition := make(map[int]bool, len(composition))
	for _, c := range composition {
		inComposition[c.SizeId] = true
	}
	areas := make(map[int]decimal.Decimal, len(composition))
	for _, c := range composition {
		areas[c.SizeId] = decimal.Zero
	}
	agnostic := decimal.Zero
	for _, p := range pieces {
		if p.Quantity < 1 || p.AreaCm2.IsNegative() {
			// A negative area or a non-positive instance count is a blob no engine of ours writes. It
			// is not refused here — the geometry is still storable and used_length is still a true
			// measurement — but nothing may be DERIVED from it, so the whole distribution is withheld.
			return nil, false
		}
		contribution := p.AreaCm2.Mul(decimal.NewFromInt(int64(p.Quantity)))
		if p.SizeId == 0 {
			agnostic = agnostic.Add(contribution)
			continue
		}
		if !inComposition[p.SizeId] {
			// The save path refuses this payload outright (a piece cut zero times by the instance
			// formula); reaching it here would mean the pieces and the состав describe different
			// раскладки, and a distribution over such a pair is meaningless.
			return nil, false
		}
		areas[p.SizeId] = areas[p.SizeId].Add(contribution)
	}
	for _, c := range composition {
		// Rounded to the stored scale HERE and not at the storage boundary, so what the distribution
		// is checked against is the number that will actually be persisted and read back. Convergence
		// is indifferent to it — the same a_s stands in the numerator and inside the denominator.
		a := areas[c.SizeId].Add(agnostic).Round(MarkerAreaScale)
		if !a.IsPositive() {
			// A size that occupies no area cannot be a basis for its own share of the length, and
			// keeping it at zero would be worse than withholding: the other sizes' numbers would come
			// out of a denominator that silently pretends this size costs nothing. It also matches the
			// column, which CHECKs the area positive precisely so «measured as zero» cannot be stored.
			// Reachable from a parse that produced contours with no extent, and from a состав size
			// whose only pieces round to nothing at the stored scale.
			return nil, false
		}
		areas[c.SizeId] = a
	}
	return areas, true
}

// WithMarkerSizeAreas returns the состав with AreaPerGarmentCm2 filled in from the blob's pieces —
// the shape the store persists, one number per состав line, alongside the quantity it is already
// writing in the same transaction.
//
// WHY THE AREA IS STORED AND THE CONSUMPTION IS NOT. The consumption is a DERIVED figure over
// used_length, and a stored derived figure drifts from its inputs the first time one of them is
// corrected — the reason ConsumptionPerUnitCm has never been a column. The area is not derived from
// anything: it is a MEASUREMENT of the geometry, in the same class as used_length_cm, placed_count
// and efficiency_pct, all of which this row already snapshots off the very same blob. It has to be a
// column because tech_card_marker_size exists for exactly this — the card's marker list travels
// WITHOUT the blob (60-100 KB per marker), and a per-size norm that appeared only on
// GetTechCardMarker would be a field whose meaning depends on which RPC you asked.
//
// ALL LINES OR NONE. If the blob cannot yield a positive area for every size of the состав the whole
// slice comes back untouched — INVALID areas throughout, no per-size norm downstream. A partial
// answer would be the expensive kind of wrong: a denominator missing one size's pieces inflates every
// OTHER size's норма, and nothing about the result would look unusual.
func WithMarkerSizeAreas(composition []MarkerCompositionEntry, pieces []MarkerPieceArea) []MarkerCompositionEntry {
	areas, ok := MarkerSizeAreasPerGarment(composition, pieces)
	if !ok {
		return composition
	}
	out := make([]MarkerCompositionEntry, len(composition))
	copy(out, composition)
	for i := range out {
		a, seen := areas[out[i].SizeId]
		if !seen {
			continue
		}
		out[i].AreaPerGarmentCm2 = decimal.NullDecimal{Decimal: a, Valid: true}
	}
	return out
}

// MarkerAreaScale is the stored scale of area_per_garment_cm2 (DECIMAL(14,2), migration 0278). The
// areas are rounded to it at DERIVATION time, not at the storage boundary, so the number the
// distribution is validated against is the number that will be read back. Rounding cannot disturb the
// convergence identity: the SAME rounded a_s appears in the numerator of every size and inside the
// shared denominator, so the distributed lengths still add back to used_length exactly.
const MarkerAreaScale = 2

// MarkerSizeConsumption is one line of the per-size norm: the состав line it belongs to, and the
// length of cloth ONE garment of that size takes off this раскладка.
//
// It carries SizeId and Quantity as well as the number, and that is not convenience. The состав, the
// per-size norms, total_units and the scalar refusal all have to be derived from ONE slice, or the
// wire could describe three different раскладки in three adjacent fields; making this the slice that
// carries all of it is how that stops being a convention someone has to remember.
type MarkerSizeConsumption struct {
	SizeId   int
	Quantity int
	// ConsumptionCm is cm of cloth per ONE garment of this size. INVALID = this раскладка cannot say:
	// either it lost its состав, or it is mixed and its per-size areas were never recorded (a marker
	// saved before Ф2.4). INVALID is never to be replaced downstream by the mean — that substitution
	// is the whole defect Ф2.4 exists to remove.
	ConsumptionCm decimal.NullDecimal
	// AreaPerGarmentCm2 is a_s, carried through so the wire can publish the BASIS of the number and
	// not only the number. A reader that can compute areas for a size this раскладка does NOT cut
	// (i.e. one that parses the выкройки) continues the same formula with it — see the file header.
	AreaPerGarmentCm2 decimal.NullDecimal
}

// MarkerPerSizeConsumption distributes a раскладка's measured length across the sizes of its состав
// by piece area, one entry per состав line, in the order given. Derived at READ time and never
// stored, so it cannot drift from used_length or from the состав.
//
// THE HOMOGENEOUS CASE NEEDS NO AREAS AT ALL, and is answered first: one size means the whole spread
// belongs to that size and the norm is L / q — the identical number the scalar path has always
// emitted. That is what keeps every раскладка taken before Ф2.4, and every legacy (size_id, sets)
// row, answering the per-size question too: the areas are the price of MIXING sizes, not the price of
// having a norm.
//
// A MIXED состав WITHOUT AREAS ANSWERS «НЕ ЗНАЮ», loudly and per size, rather than falling back to
// the mean. Such rows exist — a mixed marker saved between Ф2.1 and Ф2.4 has a состав and no areas —
// and the fallback would reintroduce, silently, precisely the number the refusal beside it exists to
// keep out of a recipe.
//
// A состав where NOTHING grades (no piece carries a size) legitimately yields the SAME number for
// every size, because every garment then really does cut the same contours; the arithmetic arrives
// there by itself, and the scalar refusal still stands — «apply one number to all sizes» and «write
// one number into the recipe as THE consumption» are different acts.
func MarkerPerSizeConsumption(composition []MarkerCompositionEntry, usedLengthCm decimal.Decimal) []MarkerSizeConsumption {
	if len(composition) == 0 {
		return nil
	}
	out := make([]MarkerSizeConsumption, 0, len(composition))
	for _, c := range composition {
		out = append(out, MarkerSizeConsumption{
			SizeId: c.SizeId, Quantity: c.Quantity, AreaPerGarmentCm2: c.AreaPerGarmentCm2,
		})
	}
	if !usedLengthCm.IsPositive() {
		return out
	}
	for _, c := range composition {
		if c.Quantity < 1 {
			// ValidateMarkerComposition forbids it and the column CHECKs it; if it is here anyway the
			// denominator is untrustworthy for every size, not just this one.
			return out
		}
	}
	if len(composition) == 1 {
		out[0].ConsumptionCm = decimal.NullDecimal{
			Decimal: usedLengthCm.Div(decimal.NewFromInt(int64(composition[0].Quantity))),
			Valid:   true,
		}
		return out
	}
	total := decimal.Zero
	for _, c := range composition {
		if !c.AreaPerGarmentCm2.Valid || !c.AreaPerGarmentCm2.Decimal.IsPositive() {
			return out
		}
		total = total.Add(c.AreaPerGarmentCm2.Decimal.Mul(decimal.NewFromInt(int64(c.Quantity))))
	}
	if !total.IsPositive() {
		return out
	}
	for i, c := range composition {
		out[i].ConsumptionCm = decimal.NullDecimal{
			Decimal: c.AreaPerGarmentCm2.Decimal.Mul(usedLengthCm).Div(total),
			Valid:   true,
		}
	}
	return out
}

// MarkerPerSizeConsumptionComplete reports that EVERY line of the состав carries a per-size norm —
// the state in which «применить по размерам» is a real offer. Partial is treated as none on purpose:
// the sizes are applied together into one recipe, and a coverage gap in the middle of that would put
// a measured norm on three sizes and nothing on the fourth.
func MarkerPerSizeConsumptionComplete(rows []MarkerSizeConsumption) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if !r.ConsumptionCm.Valid {
			return false
		}
	}
	return true
}

// MarkerCompositionAreaCm2 is A — the area of everything the spread lays out, Σ q_s · a_s. The
// denominator of the distribution, exposed for tests and for readers that want to state the basis;
// INVALID when any line is missing its area, because a partial sum is a smaller denominator and a
// smaller denominator inflates every norm derived from it.
func MarkerCompositionAreaCm2(composition []MarkerCompositionEntry) decimal.NullDecimal {
	if len(composition) == 0 {
		return decimal.NullDecimal{}
	}
	total := decimal.Zero
	for _, c := range composition {
		if !c.AreaPerGarmentCm2.Valid || c.Quantity < 1 {
			return decimal.NullDecimal{}
		}
		total = total.Add(c.AreaPerGarmentCm2.Decimal.Mul(decimal.NewFromInt(int64(c.Quantity))))
	}
	return decimal.NullDecimal{Decimal: total, Valid: true}
}
