package entity

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// УСЛОВИЯ СЪЁМКИ РАСКЛАДКИ (Ф3): the rules a marker was measured under, so a stored раскладка stops
// being an unlabelled picture and becomes a reproducible measurement.
//
// A marker used to carry the width, the кромка, the gap, the edge margin and the efficiency — and
// nothing about WHAT was laid out or UNDER WHICH RULES. A раскладка measured along the seam line and
// one measured along the cut line looked identical in the database while their consumption differed
// by the allowance around the perimeter of every piece. This file holds the rules that make the
// difference decidable: the allowance arithmetic, the norm designation and its tiebreak, and the
// fingerprint of the card's piece set.
//
// EVERY CONDITION IS OPTIONAL, AND ABSENCE MEANS «NOT RECORDED», NEVER ZERO. Ф1 already settled this
// argument on fabric_direction and the answer is the same here: an admin SPA tab survives a deploy,
// a bundle that predates Ф3 sends none of these fields, and refusing such a save would only stop the
// geometry being stored at all. A раскладка with no recorded allowance honestly becomes «СТАРАЯ
// НОРМА» — a DERIVED category (see IsLegacyNorm), which is why no `is_legacy` column exists and no
// migration backfills anything: the unmarked stays old by itself.

// Stable machine-readable reason codes for the Ф3 refusals. A client switches on these.
const (
	// ReasonDoubleSeamAllowance — the раскладка was laid along a contour that ALREADY contains the
	// allowance and an offset was added on top. The allowance would be counted twice.
	ReasonDoubleSeamAllowance = "double_seam_allowance"
)

// MarkerAllowance is what a раскладка states about the allowance between the seam line and the cut
// line on the cloth — the number a readiness gate compares against the required standard.
//
// THREE STATES, NOT TWO, and the middle one is the whole reason this is a struct instead of a
// decimal.NullDecimal:
//
//	Recorded=false             → «старая норма». Nobody wrote down what was laid out. No verdict.
//	Recorded=true,  Confirmed=false → the offset is known, but whether the laid contour ALREADY held
//	                                  an allowance was never measured off the file. The total is a
//	                                  LOWER BOUND, and a gate must say «происхождение не проверено»
//	                                  rather than treat it as the whole truth.
//	Recorded=true,  Confirmed=true  → both halves measured; Mm is the allowance on the cloth.
type MarkerAllowance struct {
	// Mm is contour_allowance_mm + seam_allowance_mm, or just the offset when the contour half was
	// never measured. Meaningless unless Recorded.
	//
	// THE SUFFIX IS LEVIED, not decoration. This field was `Cm` when the columns were centimetres, and
	// the rename to millimetres left the name behind — so every message built from it went on saying
	// «см» next to a millimetre number while the arithmetic stayed right. A wrong unit in a refusal is
	// worse than no refusal: «завышена примерно на 20 см по периметру» sends a cutter looking for a
	// twenty-centimetre error that is two.
	Mm decimal.Decimal
	// Recorded says the раскладка states its offset at all. False = «старая норма».
	Recorded bool
	// Confirmed says the contour half was measured off the file, so Mm is the total and not a floor.
	Confirmed bool
}

// MarkerAllowanceOf assembles the two recorded halves into one answer.
//
// The two are ONE quantity split in two, and keeping them apart is what turns «двойной припуск» from
// a taxonomy question into arithmetic: seam_allowance_mm is what the LAYOUT added by offsetting the
// contour outward, contour_allowance_mm is what the FILE already had inside the contour that was
// laid, and their sum is what actually lies on the cloth.
func MarkerAllowanceOf(seam, contour decimal.NullDecimal) MarkerAllowance {
	if !seam.Valid {
		return MarkerAllowance{}
	}
	if !contour.Valid {
		return MarkerAllowance{Mm: seam.Decimal, Recorded: true}
	}
	return MarkerAllowance{Mm: contour.Decimal.Add(seam.Decimal), Recorded: true, Confirmed: true}
}

// MarkerAllowanceRefusal returns the refusal for a DOUBLE ALLOWANCE, or nil when the pair is honest.
//
// Non-nil exactly when the file was measured to already carry an allowance (contour > 0) AND the
// layout offset the contour outward on top of it (seam > 0). Then the allowance is counted twice and
// the spread length is overstated by contour+seam around the perimeter of every piece.
//
// EVERY OTHER COMBINATION IS ACCEPTED, and each for its own reason:
//
//	contour ABSENT, seam > 0     → HONEST IGNORANCE. There was nothing in the file to measure against
//	                               (one closed contour in the block, or the second crosses the first —
//	                               how a non-grading layer behaves on a non-base size). Absence of
//	                               evidence is not evidence of a violation; the row is stored with a
//	                               NULL contour and the allowance reads as unconfirmed.
//	contour > 0,  seam 0/absent  → the CUT line was laid and no offset was added. Effective = contour.
//	contour 0,    seam > 0       → the SEAM line was laid and offset outward. The normal case, and the
//	                               default: the contour-layer ranking already puts the grading (seam)
//	                               layer first, so a double allowance is only reachable by an operator
//	                               overriding the layer by hand.
//
// The same invariant also stands as chk_tcm_no_double_allowance in the schema — but a CHECK gives
// error 3819 with no address, so the server has to refuse EARLIER and in words. The CHECK is a net,
// not a message; one reaching the API layer is a server bug and correctly surfaces as Internal.
func MarkerAllowanceRefusal(seam, contour decimal.NullDecimal, contourLayer string) *ValidationError {
	if !seam.Valid || !contour.Valid ||
		!seam.Decimal.IsPositive() || !contour.Decimal.IsPositive() {
		return nil
	}
	layer := strings.TrimSpace(contourLayer)
	if layer == "" {
		layer = "выбранный контурный слой"
	} else {
		layer = fmt.Sprintf("слой %q", layer)
	}
	total := contour.Decimal.Add(seam.Decimal)
	return NewFieldViolation("seam_allowance_mm", ReasonDoubleSeamAllowance, seam.Decimal.String(),
		fmt.Sprintf("%s — это линия КРОЯ: замерено, что она лежит на %s мм снаружи линии шва. "+
			"Раскладка добавила ещё %s мм офсета, и припуск посчитан дважды — длина настила завышена "+
			"примерно на %s мм по периметру каждой детали. Либо поставьте припуск 0, либо выберите "+
			"контурный слой с линией шва.",
			layer, contour.Decimal.String(), seam.Decimal.String(), total.String()))
}

// Plausibility band for the REQUIRED seam allowance — the workshop default and a card's override,
// in MILLIMETRES. Repeated verbatim by chk_workshop_settings_seam_allowance and
// chk_tc_required_seam_allowance (0277, rewritten in 0290); the numbers must move together, and the
// client's MAX_SEAM_ALLOWANCE_MM is the third copy of the same ceiling.
//
// THE FLOOR IS ZERO AND THAT IS THE POINT. Unlike the cutting table length, whose validator rejects
// 0 because a 0 cm table is nonsense, a 0 mm required allowance is a REAL workshop setting: «our
// выкройки carry the cut line, no offset is needed». «Not configured» is expressed by NULL, and
// exactly this difference is why 0272 chose a typed column per setting over a shared key/value
// table — one CHECK across all keys cannot hold two opposite floors.
//
// The ceiling is a unit check, and since 0290 it catches the OPPOSITE mistake to the one it used to.
// The widest real allowance is a 40-50 mm hem, so past 100 mm it is a stray zero; and a value BELOW
// 1 is centimetres typed into a millimetre field, which the validator now names explicitly because
// «0.5» is otherwise a perfectly plausible-looking number that means five millimetres to the person
// typing it and half a millimetre to everything downstream.
const (
	MinSeamAllowanceMm = 0
	MaxSeamAllowanceMm = 100
	// Below this a SET value is almost certainly centimetres. Zero is exempt — it is a real setting.
	SuspiciouslySmallSeamAllowanceMm = 1
	// MarkerAllowanceMaxFrac is the fraction the millimetre columns actually store: DECIMAL(6,1).
	// It is ONE, not two — the columns held centimetres with hundredths before 0290, and the code
	// that rounded to two decimals kept doing so afterwards, which let 7.55 mm pass validation and
	// be silently rewritten to 7.6 by MySQL. Tenths of a millimetre is the same physical precision
	// hundredths of a centimetre were; the digit that vanished never carried anything.
	MarkerAllowanceMaxFrac = 1
)

// ValidateSeamAllowanceStandardMm checks a REQUIRED allowance (the workshop default or a card's
// override) against the band above. An INVALID value is accepted — clearing the standard is legal;
// only a value being SET has to be plausible. `field` names the caller's field so the refusal points
// at the control the operator touched.
func ValidateSeamAllowanceStandardMm(field string, v decimal.NullDecimal) error {
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -1 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 1 decimal place — the column stores tenths of a millimetre and no more, so the extra digits would be lost silently")
	}
	if v.Decimal.IsNegative() {
		return NewFieldViolation(field, "must_not_be_negative", v.Decimal.String(),
			"a required allowance is 0 or more; to record that no standard is set, clear the field instead of entering a negative number")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxSeamAllowanceMm)) {
		return NewFieldViolation(field, "implausibly_wide", v.Decimal.String(),
			fmt.Sprintf("the value is in MILLIMETRES and the widest real allowance is a 40-50 mm hem — %d is the ceiling; check for a stray zero", MaxSeamAllowanceMm))
	}
	// Not folded into the check above: this one is the CENTIMETRE mistake, and it needs its own words.
	// «1» typed by someone thinking in centimetres is a tenth of what they meant, and it is silently
	// plausible — the gate would then pass every marker in the shop.
	if v.Decimal.IsPositive() && v.Decimal.LessThan(decimal.NewFromInt(SuspiciouslySmallSeamAllowanceMm)) {
		return NewFieldViolation(field, "implausibly_narrow", v.Decimal.String(),
			"the value is in MILLIMETRES — under 1 mm looks like centimetres typed into a millimetre field (10 mm, not 1); enter 0 if the выкройки genuinely carry the cut line")
	}
	return nil
}

// ValidateMarkerAllowanceMm checks an allowance RECORDED ON A РАСКЛАДКА — either half: the offset the
// layout added by inflating the contour, or the allowance measured inside the contour that was laid.
//
// THIS IS WHERE THE CEILING LIVES NOW, and the move is the point. It used to be a CHECK constraint
// (0290, first edition), but MySQL validates ADD CONSTRAINT against every existing row, so a ceiling
// introduced today would retroactively condemn раскладки recorded when no ceiling existed — and take
// the whole deploy down with them (3819 on migration, and with MYSQL_AUTOMIGRATE that is a halted
// start). A unit check belongs on the WRITE, where it can name the field and say what is wrong; the
// schema keeps only what is true of every row that has ever existed (non-negative). See 0291.
//
// NO «implausibly narrow» RULE HERE, unlike the standard. One of the two halves is MEASURED off the
// drawing, and the measurement's own floor is half a millimetre — refusing a sub-millimetre value
// would reject an honest reading of a file where the two lines nearly coincide. The centimetre
// mistake is caught where it actually does damage: on the STANDARD, which defines the gate.
func ValidateMarkerAllowanceMm(field string, v decimal.NullDecimal) error {
	if !v.Valid {
		return nil
	}
	if v.Decimal.IsNegative() {
		return NewFieldViolation(field, "must_not_be_negative", v.Decimal.String(),
			"an allowance is 0 or more; to record that it was never measured, omit the field instead of sending a negative number")
	}
	if v.Decimal.Exponent() < -MarkerAllowanceMaxFrac {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 1 decimal place — the column stores tenths of a millimetre, and the extra digits would be dropped by the database without saying so")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxSeamAllowanceMm)) {
		return NewFieldViolation(field, "implausibly_wide", v.Decimal.String(),
			fmt.Sprintf("the value is in MILLIMETRES and %d mm is the ceiling — past that this is not a seam allowance but the wrong pair of contours; pick a different contour layer, or check for a stray zero", MaxSeamAllowanceMm))
	}
	return nil
}

// RequiredSeamAllowanceMm is the STANDARD a readiness gate compares a раскладка's recorded allowance
// against. The order of the sources is not to be rearranged: the card's own requirement wins, and
// the workshop default is the fallback.
//
// AN INVALID RESULT MEANS «THERE IS NO STANDARD», and a consumer is obliged to return «no verdict»
// rather than substitute zero. Zero is a legal setting here («our выкройки carry the cut line»), and
// confusing it with «not configured» would declare every раскладка with a 1 cm allowance in breach
// of a standard nobody ever set. Same discipline as the cutting table length (0272).
//
// It takes the two values rather than the two structs on purpose: this is a rule about two numbers,
// and a rule that named TechCard and WorkshopSettings would be untestable without building both.
func RequiredSeamAllowanceMm(cardOverride, workshopDefault decimal.NullDecimal) decimal.NullDecimal {
	if cardOverride.Valid {
		return cardOverride
	}
	return workshopDefault
}

// --- НАБОР ДЕТАЛЕЙ: отпечаток -----------------------------------------------------------------

// PieceSetEntry is one cut-piece as the fingerprint sees it. Exactly TWO fields, and both have to be
// here: line_key answers WHICH piece (a stable ULID — 0168 minted it precisely so a rename does not
// move it), pieces_per_garment answers HOW MANY of it are cut. Nothing else enters the fingerprint.
// The db tags are here for the same reason MarkerCompositionEntry carries them: the store reads
// straight into this shape, and the two fields the fingerprint hashes are exactly the two columns it
// selects.
type PieceSetEntry struct {
	LineKey          string `db:"line_key"`
	PiecesPerGarment int    `db:"pieces_per_garment"`
}

// pieceSetDigestEntry is the hashed projection of one piece. The field names are one letter because
// they are hashed and never read; what matters is that the ENCODING IS PINNED BY TYPE. A []any of
// mixed values would re-encode differently the day somebody hands it an int64 or a float, and every
// stored fingerprint would silently become «changed».
type pieceSetDigestEntry struct {
	K string `json:"k"`
	N int    `json:"n"`
}

// PieceSetFingerprint hashes the card's CUT-PIECE SET as of one moment, so a раскладка taken then
// can be compared against the card as it stands now. ok=false means the set cannot be fingerprinted
// and the column must go NULL — which readers turn into UNKNOWN, never into «changed».
//
// THE ONE INVARIANT THAT MAKES THIS WORK, stated exactly as TechCardSectionDigests states it: THE
// SAME FUNCTION RUNS ON THE WRITE SIDE AND ON THE READ SIDE. The store computes it inside the
// SERIALIZABLE save transaction and stores it; the card read computes it again off the pieces it has
// already loaded and compares. Two implementations would drift, and the day they drifted every
// marker would read as changed.
//
// WHAT IS DELIBERATELY NOT IN IT — this list IS the stability against irrelevant edits:
//
//	name, note          — renaming a piece does not change what is cut; 0168 introduced line_key for
//	                      exactly this.
//	display_order       — reordering the list is not a change of set. Hence the sort by line_key and
//	                      NOT the card read's ORDER BY display_order, id.
//	id                  — stable since 0168, but line_key is the identity the wire sees.
//	callout_number,
//	detached            — these bind a piece to a SKETCH CALLOUT, not to the cut.
//	grainline           — the раскладка reads the долевая out of the DXF, not out of this column;
//	                      including it would declare an authority the engine never consults.
//	fused               — fusing is a second piece of a duplicating material, not a change to this
//	                      piece's geometry.
//	mirrored            — RETIRED by 0266 and read by nothing. A dead column in a fingerprint is a
//	                      fingerprint that can twitch from historical noise.
//	piece materials     — that is a COLOURWAY binding, not the set.
//
// NORMALISATION. The key is trimmed and upper-cased: Crockford ULIDs are upper-case already and the
// legacy backfill is `LEGACY` + digits, so the fold costs nothing and makes the fingerprint immune to
// a collation change — this project has been bitten by utf8mb3/utf8mb4 diverging between prod and a
// container before. The order is by key, byte-ascending.
//
// AN EMPTY SET IS A LEGAL STATE, not a NULL: it hashes stably, and adding the FIRST piece changes it.
// A card with no pieces has to stay distinguishable from a marker whose fingerprint was never
// recorded.
//
// AN EMPTY line_key MAKES THE SET UNFINGERPRINTABLE (ok=false) rather than hashing as "". Hashing ""
// would collapse two keyless pieces into one and then report «unchanged» for a set one of them was
// deleted from. In practice the branch is unreachable — the store mints a key when one is missing and
// 0168 backfilled the old rows — which is precisely why it must be fail-safe rather than panic.
func PieceSetFingerprint(entries []PieceSetEntry) (string, bool) {
	projection := make([]pieceSetDigestEntry, 0, len(entries))
	for _, e := range entries {
		key := strings.ToUpper(strings.TrimSpace(e.LineKey))
		if key == "" {
			return "", false
		}
		projection = append(projection, pieceSetDigestEntry{K: key, N: e.PiecesPerGarment})
	}
	sort.Slice(projection, func(i, j int) bool { return projection[i].K < projection[j].K })
	b, err := json.Marshal(projection)
	if err != nil {
		// Plain data; a marshal failure is a programming error, not a data one. Returning a fake
		// digest would read as a real one, so report «cannot fingerprint» instead.
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// PieceSetFingerprintNull is THE entry point both the write side and the read side call, so the
// «one function, both sides» invariant is enforceable by a test rather than by discipline. An
// unfingerprintable set becomes SQL NULL — which is «unknown», never «changed».
func PieceSetFingerprintNull(entries []PieceSetEntry) sql.NullString {
	fp, ok := PieceSetFingerprint(entries)
	if !ok {
		return sql.NullString{}
	}
	return sql.NullString{String: fp, Valid: true}
}

// PieceSetEntriesOf projects a card's loaded cut-pieces onto the fingerprint's input, so the read
// path never has to restate which two fields matter.
func PieceSetEntriesOf(pieces []TechCardPiece) []PieceSetEntry {
	out := make([]PieceSetEntry, 0, len(pieces))
	for _, p := range pieces {
		out = append(out, PieceSetEntry{LineKey: p.LineKey, PiecesPerGarment: p.PiecesPerGarment})
	}
	return out
}

// MarkerPieceSetStatus is the three-valued comparison of a раскладка's stored fingerprint against
// the card's set today. Mirrors common.TechCardMarkerPieceSetStatus.
type MarkerPieceSetStatus int

const (
	// MarkerPieceSetUnknown — no fingerprint recorded (a раскладка taken before Ф3), or none can be
	// computed today. NEVER to be rendered as «changed»: collapsing the two would put the badge on
	// every stored marker at once, which is noise where a signal is needed.
	MarkerPieceSetUnknown MarkerPieceSetStatus = iota
	MarkerPieceSetMatches
	MarkerPieceSetChanged
)

// --- НОРМА: выбор и конфликт ------------------------------------------------------------------

// NormScope is the cloth a norm is exclusive within: one norm per (card, BOM line).
//
// NOT one per CARD: a garment is not cut until ALL of its cloths are, and sufficiency is counted per
// (cloth × width) — one norm per card would let a LINING norm displace the MAIN fabric's and the
// gate would swallow it.
//
// NOT per COLOURWAY: two colourways of the same cutting width are cut identically; the colourway
// link exists to attribute WIDTH and consumption, not fitness. Scoping by colourway would demand a
// norm per colour and make the shared marker (colorway_id = 0) eligible to be nobody's norm.
//
// NOT per SIZE: since Ф2 a marker has a СОСТАВ rather than a size, and sufficiency is not per-size.
//
// An UNLINKED marker (bom_item_id IS NULL) falls into its own «no cloth» scope, one norm per card.
// The gate will not accept it — it asks about a cloth — but making it inexpressible would break the
// transitional state where a раскладка has not been attributed yet.
type NormScope struct {
	BomItemId int64
	// Bound is false for the «no cloth» scope. It exists because 0 is not a usable sentinel for a
	// column whose NULL and whose values are both meaningful, and because MySQL's UNIQUE treats
	// repeated NULLs as distinct — a trap 0264 documented and Ф2 rediscovered.
	Bound bool
}

// NormPeer is one раскладка carrying the norm flag, reduced to what picking a winner needs.
type NormPeer struct {
	Id        int
	Name      string
	Scope     NormScope
	UpdatedAt time.Time
}

// MarkerNormScope is the scope a раскладка's norm would be exclusive within.
func MarkerNormScope(m TechCardMarkerSummary) NormScope {
	return NormScope{BomItemId: m.BomItemId.Int64, Bound: m.BomItemId.Valid}
}

// NormPeersOf collects the markers that carry the flag, so a caller holding a card's summaries never
// has to ask the database a second time.
func NormPeersOf(markers []TechCardMarkerSummary) []NormPeer {
	var out []NormPeer
	for _, m := range markers {
		if !m.IsNorm {
			continue
		}
		out = append(out, NormPeer{
			Id: m.Id, Name: m.Name, Scope: MarkerNormScope(m), UpdatedAt: m.UpdatedAt,
		})
	}
	return out
}

// SelectNorm picks THE norm of one scope and reports every contender for it.
//
// THE TIEBREAK IS THE PRICE OF NOT HAVING A UNIQUE INDEX, and it is mandatory in every reader.
// Exclusivity is held by the SetTechCardMarkerNorm transaction rather than by a UNIQUE index because
// a unique index whose key involves bom_item_id turns DELETING A BOM LINE into ERROR 1761 — the FK
// is ON DELETE SET NULL (0257), so the deletion moves that marker's norm into the «no cloth» scope,
// and if one already lives there the delete fails. BOM rows are diffed INSIDE UpdateTechCard, so the
// whole card save would fail, with an error naming neither a norm nor a раскладка. That is exactly
// the trade 0257 refused when it chose SET NULL.
//
// The residual is named rather than hidden: without the index a bug CAN leave two norms in one scope.
// Three things contain it — one writer, in a SERIALIZABLE transaction; this deterministic tiebreak,
// so a corrupted state still yields ONE answer to every reader instead of a different one per screen;
// and the conflict being REPORTED (NormConflictReport) rather than silently resolved.
//
// Newest first by updated_at, then by id — the same order the marker list query uses, and id breaks
// the ties a second-resolution TIMESTAMP will produce.
func SelectNorm(peers []NormPeer, scope NormScope) (winner NormPeer, contenders []NormPeer, ok bool) {
	for _, p := range peers {
		if p.Scope == scope {
			contenders = append(contenders, p)
		}
	}
	if len(contenders) == 0 {
		return NormPeer{}, nil, false
	}
	sort.SliceStable(contenders, func(i, j int) bool {
		if !contenders[i].UpdatedAt.Equal(contenders[j].UpdatedAt) {
			return contenders[i].UpdatedAt.After(contenders[j].UpdatedAt)
		}
		return contenders[i].Id > contenders[j].Id
	})
	return contenders[0], contenders, true
}

// NormConflictReport is "" when a scope holds at most one norm, and the prose to show otherwise.
//
// IT REPORTS RATHER THAN RESOLVES SILENTLY. Readers already resolve it — SelectNorm gives them all
// one answer — but a state the schema cannot prevent must not also be invisible, or the only thing
// the missing index would have bought is that nobody ever finds out.
func NormConflictReport(peers []NormPeer, scope NormScope) string {
	winner, contenders, ok := SelectNorm(peers, scope)
	if !ok || len(contenders) < 2 {
		return ""
	}
	return fmt.Sprintf("на этой ткани отмечено %d норм сразу — так быть не должно. Действует "+
		"раскладка %q (последняя изменённая); остальные помечены нормой, но не учитываются. "+
		"Переназначьте норму, чтобы состояние снова читалось однозначно.",
		len(contenders), winner.Name)
}
