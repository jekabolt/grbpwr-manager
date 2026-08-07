package entity

import (
	"database/sql"
	"sort"

	"github.com/shopspring/decimal"
)

// ГЕЙТ ГОТОВНОСТИ ПРОГОНА (Ф6): the vocabulary and the rules. The assembly — walking a card,
// calling the material plan, building the wire message — lives in internal/dto/run_readiness.go;
// what lives HERE is everything that is a JUDGEMENT rather than a mapping, so each judgement is
// testable without building a protobuf.
//
// The gate answers a question GetTechCardReadiness cannot: «may THIS run be created». That one is
// ADVISORY, takes a card id, and sees colourways as two aggregates. This one takes the colourways
// and the quantity grid — because the norm and the article are resolved PER COLOURWAY, and the
// coverage across cloths is only countable per size.

// --- ЧЕТЫРЕ СТЕПЕНИ -----------------------------------------------------------------------------

// RunReadinessSeverity is the degree of one check. Mirrors
// admin.ProductionRunReadinessSeverity.
//
// FOUR, NOT A BOOL, AND THE LOAD-BEARING ONE IS UNKNOWN.
//
// UNKNOWN means THE SERVER HAS NO INSTRUMENT TO ANSWER — not «the data is bad», and not «probably
// fine». It NEVER blocks, in any mode, and it is NEVER counted as passed. Both of those are
// enforced structurally below (Blocks, Passed, and the report aggregation) rather than by every
// caller remembering, because the two wrong readings are exactly the two that are easy to fall into:
//
//   - blocking on UNKNOWN punishes an operator for a phase WE have not finished building;
//   - folding UNKNOWN into OK silently skips a check we promised to run, and the promise is what the
//     operator is relying on.
//
// This is the discipline Ф1 established for направление ткани, stated there as «NULL IS NOT A
// FOURTH VALUE» (fabric_direction.go). The same argument applies here for the same reason: a value
// that means «unknown» must not be quietly given the meaning of one of the known values, in either
// direction.
//
// THE BOUNDARY BETWEEN UNKNOWN AND BLOCKER is where the difficulty actually is, so it is stated as a
// test rather than as a feeling: does the missing thing PREVENT US FROM ANSWERING, or IS IT THE
// ANSWER? «The conditions this раскладка was measured under were never recorded» is an ANSWER —
// fitness under the «not worse than» rule cannot be established, so the norm is not fit — hence
// BLOCKER. «There is no size index for this card» is an absent INSTRUMENT — hence UNKNOWN.
type RunReadinessSeverity int

const (
	// RunReadinessUnspecified is the zero value and is never emitted. It exists so a struct literal
	// that forgets the severity produces a value that fails the vocabulary test, rather than
	// defaulting into OK and silently declaring a check passed.
	RunReadinessUnspecified RunReadinessSeverity = iota
	RunReadinessOK
	RunReadinessUnknown
	RunReadinessWarning
	RunReadinessBlocker
)

// String is for logs and test failures; the wire carries the enum, never this.
func (s RunReadinessSeverity) String() string {
	switch s {
	case RunReadinessOK:
		return "ok"
	case RunReadinessUnknown:
		return "unknown"
	case RunReadinessWarning:
		return "warning"
	case RunReadinessBlocker:
		return "blocker"
	}
	return "unspecified"
}

// --- СЛОВАРЬ КЛЮЧЕЙ -----------------------------------------------------------------------------

// The gate's keys are PERMANENT WIRE VOCABULARY. The rules, restated here because they are contract
// and not style:
//
//   - a key names the FACT that is missing — never the screen that fixes it, and never the sentence
//     we happen to describe it with. `norm_conditions_recorded` is a fact; «open the patterns tab»
//     is not;
//   - rewording a label or a detail is NEVER a contract change;
//   - two facts a client would route to two different places are ALWAYS two keys, even when the
//     sentence is the same. This is how colorway_linked and lab_dip already live: one screen, two
//     keys, and the aux filter drops them separately;
//   - a key NEVER carries identifiers. Those live in the target;
//   - a key is never re-used for a different fact. A retired key stops being emitted; a new fact
//     gets a new key. A client that does not know a key must still render the row and route it to
//     the header tab.
//
// There are no prefixes: a key is globally unique so a client can branch with ONE map.
const (
	// --- карточка ---
	// RunReadinessKeyCardAuxiliary — the card produces a material, not a garment. The gate short
	// circuits on it (see AuxiliaryReport) and emits nothing else at all.
	RunReadinessKeyCardAuxiliary = "card_auxiliary"
	// RunReadinessKeyReleaseFrozen — is the run planned against a frozen release or against the live
	// card. WARNING, never BLOCKER, and that is a decision with an argument: a release is created as a
	// SIDE EFFECT of moving a card to RELEASED, and it is created BEST-EFFORT — a failure logs
	// «RELEASE SNAPSHOT LOST» and lets the release happen anyway. Making a release mandatory for a run
	// would make production depend on a snapshot we have already agreed we may lose, i.e. would
	// declare somebody else's accepted shortfall to be our requirement.
	RunReadinessKeyReleaseFrozen = "release_frozen"
	RunReadinessKeyCardSizeRange = "card_size_range"
	RunReadinessKeyCardPieces    = "card_pieces"
	// RunReadinessKeyCardPiecesDxfMatched — every cut piece has at least one DXF block alias (0262).
	RunReadinessKeyCardPiecesDxfMatched = "card_pieces_dxf_matched"
	// RunReadinessKeyPatternBindingResolved — every pattern sheet and every alias still resolves to a
	// live roll-goods line. WARNING because fabric_scope.go already declares a dangling binding to be
	// a UI STATE («слот удалён») rather than an error, and the store refuses it only at the moment of
	// the first write. Promoting it to a blocker here would overrule a decision the neighbouring
	// layer has already taken.
	RunReadinessKeyPatternBindingResolved = "pattern_binding_resolved"

	// --- колорвей (гасит галочку) ---
	RunReadinessKeyColorwayLive = "colorway_live"
	// RunReadinessKeySlotArticle / RunReadinessKeySlotNorm are NOT recomputed here: they are the two
	// blockers ComputeProductionRunMaterialPlan already produces, read back through
	// MaterialPlanBlocker.key. One fact, one implementation.
	RunReadinessKeySlotArticle = "slot_article"
	RunReadinessKeySlotNorm    = "slot_norm"
	// RunReadinessKeyNormProvenance — WHERE this cloth's norm came from: a нормировочная раскладка,
	// or an operator's hand. Manual is accepted (WARNING): without that escape hatch the first
	// strange DXF stops production.
	RunReadinessKeyNormProvenance = "norm_provenance"
	// RunReadinessKeyNormConditionsRecorded — «старая норма»: the раскладка predates Ф3 and states
	// nothing about the rules it was measured under.
	RunReadinessKeyNormConditionsRecorded = "norm_conditions_recorded"
	RunReadinessKeyNormSeamAllowance      = "norm_seam_allowance"
	RunReadinessKeyNormFlipPolicy         = "norm_flip_policy"
	RunReadinessKeyNormPieceSet           = "norm_piece_set"
	RunReadinessKeyNormWidthVsArticle     = "norm_width_vs_article"
	// RunReadinessKeyNormMultiple — more than one раскладка carries the norm flag for one cloth.
	// WARNING, not BLOCKER: the tiebreak is deterministic, so the run is physically possible and the
	// number is not random — but it may not be the number a human meant, and a human has to look.
	// Reporting it is a direct condition of the Ф3 decision to hold exclusivity in a transaction
	// instead of a UNIQUE index: silently taking the first is forbidden.
	RunReadinessKeyNormMultiple = "norm_multiple"

	// --- прогон ---
	RunReadinessKeySizesInRange      = "sizes_in_range"
	RunReadinessKeySizesInDxf        = "sizes_in_dxf"
	RunReadinessKeyQuantitiesPresent = "quantities_present"
	RunReadinessKeyStockShortage     = "stock_shortage"
)

// RunReadinessGroup is the list a row lands in. It is part of the contract only in so far as a
// client reads three lists; the grouping itself is fixed per key and is asserted by a test, so a key
// cannot quietly change lists between releases and reappear somewhere a client is not looking.
type RunReadinessGroup string

const (
	RunReadinessGroupCard     RunReadinessGroup = "card"
	RunReadinessGroupColorway RunReadinessGroup = "colorway"
	RunReadinessGroupRun      RunReadinessGroup = "run"
)

// RunReadinessKeyGroups is THE registry of the vocabulary. It exists for two jobs that both matter:
// it is what the completeness test enumerates (no key may be emitted that is not here, and none of
// these may silently stop being emitted), and it is what documents the group of each key in one
// place rather than in the branch that happens to build it.
var RunReadinessKeyGroups = map[string]RunReadinessGroup{
	RunReadinessKeyCardAuxiliary:          RunReadinessGroupCard,
	RunReadinessKeyReleaseFrozen:          RunReadinessGroupCard,
	RunReadinessKeyCardSizeRange:          RunReadinessGroupCard,
	RunReadinessKeyCardPieces:             RunReadinessGroupCard,
	RunReadinessKeyCardPiecesDxfMatched:   RunReadinessGroupCard,
	RunReadinessKeyPatternBindingResolved: RunReadinessGroupCard,

	RunReadinessKeyColorwayLive:           RunReadinessGroupColorway,
	RunReadinessKeySlotArticle:            RunReadinessGroupColorway,
	RunReadinessKeySlotNorm:               RunReadinessGroupColorway,
	RunReadinessKeyNormProvenance:         RunReadinessGroupColorway,
	RunReadinessKeyNormConditionsRecorded: RunReadinessGroupColorway,
	RunReadinessKeyNormSeamAllowance:      RunReadinessGroupColorway,
	RunReadinessKeyNormFlipPolicy:         RunReadinessGroupColorway,
	RunReadinessKeyNormPieceSet:           RunReadinessGroupColorway,
	RunReadinessKeyNormWidthVsArticle:     RunReadinessGroupColorway,
	RunReadinessKeyNormMultiple:           RunReadinessGroupColorway,

	RunReadinessKeySizesInRange:      RunReadinessGroupRun,
	RunReadinessKeySizesInDxf:        RunReadinessGroupRun,
	RunReadinessKeyQuantitiesPresent: RunReadinessGroupRun,
	RunReadinessKeyStockShortage:     RunReadinessGroupRun,
}

// Machine keys of the two blockers ComputeProductionRunMaterialPlan produces. They live beside the
// human `reason` strings the plan has always emitted, so nobody is tempted to branch on prose.
const (
	MaterialPlanBlockerNoArticle = "no_article"
	MaterialPlanBlockerNoNorm    = "no_norm"
)

// The human reasons those two keys accompany. Declared as constants so the key↔reason pairing is
// written once and the plan cannot emit a key under the other one's sentence.
const (
	MaterialPlanReasonNoArticle = "no article (no pin, no slot default)"
	MaterialPlanReasonNoNorm    = "no consumption norm"
)

// --- СТРОКИ И ОТЧЁТ -----------------------------------------------------------------------------

// RunReadinessCell is one cell of the planned grid — the same key production_run_line has, because
// that is the run about to be written.
type RunReadinessCell struct {
	ColorwayId int
	SizeId     int
	PlannedQty int
}

// RunReadinessTarget carries IDENTIFIERS ONLY. The client picks the tab from the key; the server
// hands it what it needs to build a URL. A zero field means «this check is not about that kind of
// object», never «id 0».
type RunReadinessTarget struct {
	TechCardId int
	ColorwayId int
	BomLineKey string
	BomItemId  int64
	MarkerId   int
	MaterialId int64
	SizeId     int
}

// RunReadinessFinding is one check.
type RunReadinessFinding struct {
	Key      string
	Severity RunReadinessSeverity
	Label    string
	// Detail is the actual state. EMPTY ONLY ON AN OK ROW — the same rule readinessReq already
	// enforces for the advisory checklist, and for the same reason: a detail is the explanation of a
	// failure, so a passing row carrying one reads as a failure that was let through.
	Detail string
	Target RunReadinessTarget
}

// Blocks is the ONLY thing that may make a run unready. Nothing else — not WARNING, and above all
// not UNKNOWN — is allowed to reach that decision, which is why every readiness question in this
// file routes through this one predicate instead of comparing severities inline.
func (f RunReadinessFinding) Blocks() bool { return f.Severity == RunReadinessBlocker }

// Passed reports a check that actually ran and was satisfied. UNKNOWN is deliberately NOT passed:
// the count of passing checks is what a screen shows as «10 of 12 verified», and an UNKNOWN folded
// in there would advertise verification that never happened.
func (f RunReadinessFinding) Passed() bool { return f.Severity == RunReadinessOK }

// RunReadinessColorway is one colourway's verdict — `Ready` is what greys the modal's checkbox.
type RunReadinessColorway struct {
	ColorwayId int
	Name       string
	Findings   []RunReadinessFinding
}

// Ready reports no BLOCKER on this colourway. UNKNOWN and WARNING rows leave it ready by
// construction: see RunReadinessFinding.Blocks.
func (c RunReadinessColorway) Ready() bool {
	for _, f := range c.Findings {
		if f.Blocks() {
			return false
		}
	}
	return true
}

// RunReadinessReport is the whole verdict, before it is turned into a wire message.
type RunReadinessReport struct {
	Card      []RunReadinessFinding
	Colorways []RunReadinessColorway
	Run       []RunReadinessFinding
	// BlockingEnabled is the server's mode right now (workshop_settings.run_readiness_blocking).
	BlockingEnabled bool
	// Caveats are the material plan's own irreducible sentences, passed through verbatim. They are
	// already written for a human, and restating them as keys would give one fact a second
	// definition — and the second definition would be the one that drifts.
	Caveats []string
}

// Ready reports that not one BLOCKER was raised anywhere.
func (r RunReadinessReport) Ready() bool { return len(r.Blockers()) == 0 }

// WouldBlock is what CreateProductionRun WILL DO with this request. Computed here rather than left
// to the caller's «!ready && blocking» because that sentence is what the modal prints, it is the one
// the client must not get wrong, and there must be exactly one place it can be wrong in.
func (r RunReadinessReport) WouldBlock() bool { return r.BlockingEnabled && !r.Ready() }

// UnknownCount is how many checks could not answer. It is reported next to the verdict on purpose:
// zero is NOT «everything was verified», it is «no check had to admit it lacked an instrument», and
// a non-zero count beside ready=true is the honest state «nothing is known to be broken, and these
// things were not checked at all».
func (r RunReadinessReport) UnknownCount() int {
	n := 0
	r.each(func(f RunReadinessFinding) {
		if f.Severity == RunReadinessUnknown {
			n++
		}
	})
	return n
}

// Blockers returns every BLOCKER row, in card → colourway → run order — the order a refusal lists
// them in, so the operator reads them in the order they would fix them.
func (r RunReadinessReport) Blockers() []RunReadinessFinding {
	var out []RunReadinessFinding
	r.each(func(f RunReadinessFinding) {
		if f.Blocks() {
			out = append(out, f)
		}
	})
	return out
}

// UnreadyColorways lists the colourways carrying at least one BLOCKER, in request order. This is
// what a create-time refusal names: «a ready and an unready colourway in one request ⇒ the error
// names the unready one».
func (r RunReadinessReport) UnreadyColorways() []RunReadinessColorway {
	var out []RunReadinessColorway
	for _, c := range r.Colorways {
		if !c.Ready() {
			out = append(out, c)
		}
	}
	return out
}

func (r RunReadinessReport) each(fn func(RunReadinessFinding)) {
	for _, f := range r.Card {
		fn(f)
	}
	for _, c := range r.Colorways {
		for _, f := range c.Findings {
			fn(f)
		}
	}
	for _, f := range r.Run {
		fn(f)
	}
}

// --- РЕЖИМ --------------------------------------------------------------------------------------

// RunReadinessBlocking reports whether the gate is in blocking mode.
//
// NOT CONFIGURED MEANS REPORT-ONLY, and this is the ONE setting in «дом настроек цеха» whose «unset»
// has a defined behaviour instead of «no verdict». The reason is not symmetry, it is arithmetic: on
// the day Ф6 ships, not one card carries a norm with recorded measurement conditions — Ф3 declared
// every pre-existing раскладка «старая норма» — so a rule of «no verdict ⇒ refuse» would have
// refused every run in the shop. A setting whose default stops the factory does not get created.
//
// A stored FALSE and an unset column therefore behave identically, and that is deliberate too: there
// is no third thing a create command can do, so there is nothing for a third state to mean.
func RunReadinessBlocking(ws *WorkshopSettings) bool {
	return ws != nil && ws.RunReadinessBlocking.Valid && ws.RunReadinessBlocking.Bool
}

// --- ПРАВИЛА ------------------------------------------------------------------------------------

// NormWidthBasis says WHICH of the two right-hand sides the width comparison used. The detail text
// is obliged to say so: «the marker was taken at 150 cm and the narrowest available roll measures
// 148» and «…and the article's nominal width less the selvedge is 146» are two different
// conversations with an operator, and only one of them is about a purchase.
type NormWidthBasis int

const (
	// NormWidthBasisNone — neither a measured lot nor a nominal width is known. No verdict.
	NormWidthBasisNone NormWidthBasis = iota
	// NormWidthBasisLot — the article has lots with a measured width and stock remaining. The
	// NARROWEST of them wins: that is the width the shop actually has to lay on, and it is the same
	// rule by which the shop makes the marker in the first place.
	NormWidthBasisLot
	// NormWidthBasisNominal — no measured lot; the article's catalogue width less its selvedge.
	NormWidthBasisNominal
)

// NormWidthVerdict is the answer of the width comparison, with the arithmetic exposed so the caller
// can write a sentence containing the actual numbers rather than a verdict on its own.
type NormWidthVerdict struct {
	Severity RunReadinessSeverity
	Basis    NormWidthBasis
	// TodayCuttingCm is the CUTTING width available today — the number the marker's own width is
	// compared against. Invalid exactly when Basis is None.
	TodayCuttingCm decimal.NullDecimal
	// MeasuredRollCm is the raw narrowest measured roll width, before the selvedge was taken off.
	// Valid only on the lot branch, and only so the detail can show the subtraction.
	MeasuredRollCm decimal.NullDecimal
}

// NormWidthVsArticle judges whether the раскладка's measured width is still available today.
//
// BOTH SIDES ARE CUTTING WIDTHS, and getting that wrong is the whole difficulty of this check.
// tech_card_marker.fabric_width_cm is the CUTTING width — the nesting modal states it in as many
// words («the CUTTING width the layout runs on — roll width minus the кромка on both edges») and
// stores the selvedge it subtracted alongside. material_lot.measured_width_cm is the opposite: it is
// the width that ARRIVED, i.e. the FULL roll as measured. Comparing the marker's cutting width
// against a raw measured roll width would inflate the right-hand side by 2×selvedge — a few
// centimetres, always in the PERMISSIVE direction, i.e. it would pass markers that do not fit. So
// the selvedge comes off the measured lot too. (The plan says the same thing in one word: «по САМОЙ
// УЗКОЙ ПОЛЕЗНОЙ ширине доступных лотов» — полезной, the usable one.)
//
// THE COMPARISON IS SCALAR, NOT GEOMETRIC, and that is a deliberate under-approximation. The honest
// geometric rule — a marker fits a width W if the maximum Y of its placements is ≤ W — needs the
// layout blob, and the blob is 60-100 KB and travels only on GetTechCardMarker; reading one per slot
// per colourway inside a modal is not available. The scalar test is CONSERVATIVE: it rejects some
// markers that would in fact fit (one taken at 150 whose pieces only reach 140 will not pass on
// 145). That is the right side of the error, and a later geometric refinement can only WIDEN
// acceptance, so it lands without a contract change.
func NormWidthVsArticle(markerCuttingCm decimal.Decimal, narrowestMeasuredRollCm decimal.NullDecimal,
	articleSelvedgeCm decimal.Decimal, articleNominalUsableCm decimal.NullDecimal) NormWidthVerdict {
	today := decimal.NullDecimal{}
	basis := NormWidthBasisNone
	measured := decimal.NullDecimal{}
	switch {
	case narrowestMeasuredRollCm.Valid:
		basis = NormWidthBasisLot
		measured = narrowestMeasuredRollCm
		cut := narrowestMeasuredRollCm.Decimal.Sub(articleSelvedgeCm.Mul(decimal.NewFromInt(2)))
		if cut.IsNegative() {
			// A selvedge wider than the roll is operator error; clamping mirrors
			// Material.UsableFabricWidthCm rather than inventing a negative width.
			cut = decimal.Zero
		}
		today = decimal.NullDecimal{Decimal: cut, Valid: true}
	case articleNominalUsableCm.Valid:
		basis = NormWidthBasisNominal
		today = articleNominalUsableCm
	default:
		// No measured lot and no catalogue width: there is nothing to compare against. An absent
		// width is not a narrow one.
		return NormWidthVerdict{Severity: RunReadinessUnknown, Basis: NormWidthBasisNone}
	}
	sev := RunReadinessOK
	if markerCuttingCm.GreaterThan(today.Decimal) {
		sev = RunReadinessBlocker
	}
	return NormWidthVerdict{Severity: sev, Basis: basis, TodayCuttingCm: today, MeasuredRollCm: measured}
}

// NormFlipPolicy judges the раскладка's flip policy against the direction the cloth demands TODAY.
//
// Fitness is «the policy the marker was taken under is NOT LOOSER than what today's cloth requires».
// The direction itself comes from Ф1's own resolver (ScopeFabricDirection over MarkerFabricScope) —
// this function takes its ANSWER rather than re-deriving it, because a second walk over the BOM
// lines would be a second definition of «which cloth is this marker on», and fabric_scope.go says in
// its header why there is exactly one.
//
//	direction unknown            → UNKNOWN. Ф1 refuses to read a NULL direction as a value, and so
//	                               does this: guessing «flip allowed» makes the check decorative on
//	                               exactly the cards that need it.
//	direction is not one_way     → OK. two_way and any both permit turning a piece over.
//	one_way, flip was forbidden  → OK. The marker was searched under the stricter policy already.
//	one_way, flip was allowed    → BLOCKER. The layout may contain pieces lying upside down on cloth
//	                               that cannot take them, and the length it measured is therefore not
//	                               reproducible on this cloth.
//	one_way, policy not recorded → UNKNOWN. A раскладка from before Ф3 states nothing about the
//	                               search policy; usually this slot is already covered by
//	                               norm_conditions_recorded, and this row says the same absence
//	                               without pretending to a verdict.
func NormFlipPolicy(dir TechCardFabricDirection, directionKnown bool, allowFlip sql.NullBool) RunReadinessSeverity {
	if !directionKnown {
		return RunReadinessUnknown
	}
	if dir != FabricDirectionOneWay {
		return RunReadinessOK
	}
	if !allowFlip.Valid {
		return RunReadinessUnknown
	}
	if allowFlip.Bool {
		return RunReadinessBlocker
	}
	return RunReadinessOK
}

// NormSeamAllowance compares the allowance the раскладка actually laid against the standard this
// card is held to (RequiredSeamAllowanceCm: the card's override, else the workshop default).
//
// THE UNCONFIRMED CASE IS THE INTERESTING ONE, and it is where the UNKNOWN/BLOCKER boundary earns
// its keep. MarkerAllowance is three-valued: an allowance whose CONTOUR half was never measured off
// the file gives a Cm that is a LOWER BOUND, not the total. So:
//
//	not recorded at all      → UNKNOWN. Nothing to compare; norm_conditions_recorded already carries
//	                          this fact as the BLOCKER it is, and reding it twice would read as two
//	                          separate problems.
//	no standard configured   → UNKNOWN. Zero is a LEGAL standard here («our выкройки carry the cut
//	                          line»), so an unset standard must not collapse into 0 — that would put
//	                          every 1 cm allowance in breach of a rule nobody ever set.
//	lower bound ≥ standard   → OK, whether or not it is confirmed: the true allowance is at least the
//	                          bound, so it clears the standard either way.
//	confirmed, below         → BLOCKER. The total is known and it is short.
//	unconfirmed, below       → UNKNOWN. The true total is ≥ Cm and could be anything above it;
//	                          refusing on it would be a verdict the evidence does not support.
func NormSeamAllowance(a MarkerAllowance, standardCm decimal.NullDecimal) RunReadinessSeverity {
	if !a.Recorded {
		return RunReadinessUnknown
	}
	if !standardCm.Valid {
		return RunReadinessUnknown
	}
	if a.Cm.GreaterThanOrEqual(standardCm.Decimal) {
		return RunReadinessOK
	}
	if a.Confirmed {
		return RunReadinessBlocker
	}
	return RunReadinessUnknown
}

// NormPieceSet maps Ф3's three-valued piece-set comparison onto a gate degree. UNKNOWN stays
// UNKNOWN: a раскладка with no recorded fingerprint has not been shown to be stale, and rendering
// «unknown» as «changed» would put the badge on every stored marker at once, which is noise exactly
// where a signal is needed.
func NormPieceSet(status MarkerPieceSetStatus) RunReadinessSeverity {
	switch status {
	case MarkerPieceSetMatches:
		return RunReadinessOK
	case MarkerPieceSetChanged:
		return RunReadinessBlocker
	}
	return RunReadinessUnknown
}

// SizesInDxf judges one size against a scope's stored size index.
//
// Only ONE of the four index states can produce a verdict, and the other three are UNKNOWN for three
// different reasons the caller must state separately in the detail — «nobody ran the check», «the
// files changed since» and «the files carry no size coding» send an operator to three different
// actions. An empty token set in particular is a LEGAL result, not an empty coverage: a card with one
// flat sheet per size yields no tokens at all, and reading that as «no size is present» would be a
// false blocker on every size at once.
func SizesInDxf(state PatternSizeIndexState, tokens map[string]bool, sizeDictionaryName string) RunReadinessSeverity {
	if state != PatternSizeIndexUsable {
		return RunReadinessUnknown
	}
	if SizeCoveredByTokens(sizeDictionaryName, tokens) {
		return RunReadinessOK
	}
	return RunReadinessBlocker
}

// --- ВСПОМОГАТЕЛЬНОЕ ----------------------------------------------------------------------------

// SortedCells returns the grid in a stable order (colourway, then size), so two identical requests
// produce byte-identical answers and a client can cache on the request hash. Map iteration anywhere
// in the assembly would make that false without anything failing.
func SortedCells(cells []RunReadinessCell) []RunReadinessCell {
	out := make([]RunReadinessCell, len(cells))
	copy(out, cells)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ColorwayId != out[j].ColorwayId {
			return out[i].ColorwayId < out[j].ColorwayId
		}
		return out[i].SizeId < out[j].SizeId
	})
	return out
}
