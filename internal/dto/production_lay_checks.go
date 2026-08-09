package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ГОДНОСТЬ МАРКЕР↔НАСТИЛ (Ф4.4) И ВЫСОТА СТОПКИ (Ф4.8) — §8 и §11 спеки, точные предикаты.
//
// Ф5б.1 добавила сюда одиннадцатую — `lay_lot_width` (Р8): ширину маркера против измеренной ширины
// ТОГО РУЛОНА, с которого стелют. Она живёт здесь, а не в новом файле, потому что это проверка
// годности настила той же формы, что и остальные десять, и её вердикт складывается тем же
// WorstLayCheckStatus; отдельный файл дал бы вторую лестницу статусов.
//
// Everything here is a PURE FUNCTION of (настил, маркеры, детали, BOM, настройки цеха, артикул), the
// same shape as ComputeProductionRunMaterialPlan: no store, no wire, no clock. Each predicate answers
// ONE named fact about a настил and returns it as a LayCheck with a stable key.
//
// ТРИ ОТВЕТА, НЕ ДВА. Every check carries UNKNOWN next to OK and BLOCKER, and UNKNOWN never collapses
// into either. «Проверили — всё хорошо» and «проверить было нечем» are different sentences about a
// run somebody is about to cut, and the whole cost of confusing them lands in the cutting room: an
// UNKNOWN read as OK signs off a настил that cannot be spread, an UNKNOWN read as BLOCKER stops one
// that is fine. The stack-height check (§11) is where this is most concrete — an article whose
// thickness nobody measured yields UNKNOWN and NO NUMBER AT ALL, never «0 см, влезает».
//
// ЧТО ЗДЕСЬ НЕ ПЕРЕИЗОБРЕТАЕТСЯ, И ПОЧЕМУ ЭТО ГЛАВНОЕ СВОЙСТВО ФАЙЛА:
//
//	чётность слоёв под режим     → LayerCutInstances / ErrLayModeParity (production_lay_yield.go)
//	деталь → изделия, перекрой   → PieceYieldGarments                   (production_lay_yield.go)
//	лестница OK < UNKNOWN < BLOCKER → WorstCoverageStatus               (production_lay_yield.go)
//	направление ткани по скоупу  → entity.MarkerFabricScope + entity.ScopeFabricDirection (Ф1)
//	ширина маркера против ткани  → entity.NormWidthVsArticle            (Ф6/Ф5а.1)
//
// Ни одного второго экземпляра ни одного из этих правил. A duplicated rule diverges, and the day it
// diverges nothing fails — the report simply stops agreeing with the refusal, which is a defect this
// project has already paid for twice.
//
// НА ЗАПИСИ И НА ЧТЕНИИ (§8.3). SaveLay calls ValidateLayForSave, which runs the subset that must
// REFUSE (lay_marker_scope, lay_mode_parity). Everything else — and lay_marker_scope AGAIN — is
// computed on READ, because a настил can go bad without anybody touching it: `fk_tcm_bom` is SET NULL
// (0257:63), so an edit to the card's BOM silently detaches a marker from its slot, and a настил that
// was fit yesterday must not keep LOOKING fit today.

// LayCheckStatus is the verdict of one проверка. It mirrors the common.ProductionLayCheckStatus enum
// on the wire, but NOT its numbering, and the divergence is deliberate in exactly one way:
//
// THE ZERO VALUE IS UNKNOWN. The proto's zero is UNSPECIFIED, which is the right thing on a wire (a
// field nobody set is distinguishable from a field somebody set to a value) and the wrong thing in
// Go, where a struct literal that omits Status must not come out as either «unspecified» — a fourth
// state every consumer has to remember — or, far worse, OK. A forgotten status costs a grey badge.
//
// The same choice CoverageStatus made two files over, for the same reason and in the same direction.
type LayCheckStatus int

const (
	// LayCheckStatusUnknown — проверить было нечем. Neither an approval nor a fault.
	LayCheckStatusUnknown LayCheckStatus = iota
	// LayCheckStatusOK — проверка прошла. Its Detail is empty, and only its Detail may be.
	LayCheckStatusOK
	// LayCheckStatusWarning — факт установлен, и он законен. Перекрой (§8.2) and a настил longer than
	// the table (§8) are both WARNINGs: they are things a человек must read, not things a server may
	// refuse. See LayCheckKeyOvercut for the physics.
	LayCheckStatusWarning
	// LayCheckStatusBlocker — доказанная негодность. This настил cannot be spread as described.
	LayCheckStatusBlocker
)

func (s LayCheckStatus) String() string {
	switch s {
	case LayCheckStatusUnknown:
		return "UNKNOWN"
	case LayCheckStatusOK:
		return "OK"
	case LayCheckStatusWarning:
		return "WARNING"
	case LayCheckStatusBlocker:
		return "BLOCKER"
	}
	return fmt.Sprintf("LayCheckStatus(%d)", int(s))
}

// Blocks is the ONLY predicate allowed to make a настил unfit. Nothing else — not WARNING, and above
// all not UNKNOWN — may reach that decision. Same discipline as RunReadinessFinding.Blocks.
func (s LayCheckStatus) Blocks() bool { return s == LayCheckStatusBlocker }

// Pb projects the status onto the wire enum. IT EXISTS SO NOBODY CASTS.
//
// The numbering deliberately differs (see the type's doc), and a direct
// `pb_common.ProductionLayCheckStatus(s)` — the obvious thing to write in a handler — maps OK,
// WARNING and BLOCKER correctly and silently turns UNKNOWN (0 here) into UNSPECIFIED (0 there). The
// client would then receive its own zero value for the one status the whole file exists to make
// visible. One mapping, here, pinned by TestLayCheckStatusPbIsNotACast.
//
// A status outside the four maps to UNSPECIFIED rather than to OK: an unreadable answer must not
// arrive as an approval.
func (s LayCheckStatus) Pb() pb_common.ProductionLayCheckStatus {
	switch s {
	case LayCheckStatusOK:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK
	case LayCheckStatusWarning:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_WARNING
	case LayCheckStatusBlocker:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER
	case LayCheckStatusUnknown:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNKNOWN
	}
	return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNSPECIFIED
}

// coverageOf projects a check status onto the three-valued coverage ladder so the fold below can be
// the ONE ladder that already exists rather than a second one written here.
//
// WARNING maps to OK, and that is the whole content of the projection: a warning is a KNOWN, LEGAL
// fact, so it must not out-rank an UNKNOWN («не смогли проверить») and must not read as a fault. It
// is re-applied after the fold, so it survives when nothing worse was found.
func (s LayCheckStatus) coverageOf() CoverageStatus {
	switch s {
	case LayCheckStatusOK, LayCheckStatusWarning:
		return CoverageStatusOK
	case LayCheckStatusBlocker:
		return CoverageStatusBlocker
	}
	return CoverageStatusUnknown
}

// WorstLayCheckStatus folds several verdicts into one by the ladder OK < WARNING < UNKNOWN < BLOCKER.
//
// IT DELEGATES THE ORDER TO WorstCoverageStatus AND DOES NOT RESTATE IT. The ladder is one line of
// arithmetic that is wrong in a way nothing catches if it is written twice, and `max` over the raw
// constants is wrong here in the most expensive direction available: LayCheckStatusUnknown is 0 and
// LayCheckStatusOK is 1, so max(OK, UNKNOWN) answers OK — a silent approval of a check that never
// ran. TestLayCheckLadderIsNotNumericOrder pins that the two orders disagree.
//
// Empty input is UNKNOWN, not OK: nothing was folded, so nothing was proved.
func WorstLayCheckStatus(ss ...LayCheckStatus) LayCheckStatus {
	if len(ss) == 0 {
		return LayCheckStatusUnknown
	}
	core := make([]CoverageStatus, 0, len(ss))
	warned := false
	for _, s := range ss {
		if s == LayCheckStatusWarning {
			warned = true
		}
		core = append(core, s.coverageOf())
	}
	switch WorstCoverageStatus(core...) {
	case CoverageStatusBlocker:
		return LayCheckStatusBlocker
	case CoverageStatusUnknown:
		return LayCheckStatusUnknown
	}
	if warned {
		return LayCheckStatusWarning
	}
	return LayCheckStatusOK
}

// layCheckStatusFromSeverity maps a Ф6 severity onto this file's status, for the checks that reuse a
// readiness predicate wholesale (see LayMarkerWidthCheck).
//
// The default is UNKNOWN and not OK: a severity this switch has never heard of is a statement nobody
// here can read, and reading it as an approval is the one answer that cannot be walked back.
func layCheckStatusFromSeverity(s entity.RunReadinessSeverity) LayCheckStatus {
	switch s {
	case entity.RunReadinessOK:
		return LayCheckStatusOK
	case entity.RunReadinessWarning:
		return LayCheckStatusWarning
	case entity.RunReadinessBlocker:
		return LayCheckStatusBlocker
	}
	return LayCheckStatusUnknown
}

// Ключи проверок. СТАБИЛЬНЫ, ГЛОБАЛЬНО УНИКАЛЬНЫ, НАЗЫВАЮТ ФАКТ, НЕ ЭКРАН, и не переиспользуются
// (правила ключей — spec-f6-readiness.md:410–421). A key is what a client keys its copy, its
// deep-link and its «I know about this one» state on; renaming one silently retargets all three.
const (
	// LayCheckKeyModeParity — face_to_face с нечётными слоями. НА ЗАПИСИ ТОЖЕ (§8.3).
	LayCheckKeyModeParity = "lay_mode_parity"
	// LayCheckKeyMirrorExpansion — развёртка зеркальных деталей маркера против режима настилания.
	LayCheckKeyMirrorExpansion = "lay_mirror_expansion"
	// LayCheckKeyDirectionMode — направленная ткань против режима настилания (§8.1).
	LayCheckKeyDirectionMode = "lay_direction_mode"
	// LayCheckKeyMarkerScope — маркер секции принадлежит именно этому настилу. НА ЗАПИСИ ТОЖЕ (§8.3).
	LayCheckKeyMarkerScope = "lay_marker_scope"
	// LayCheckKeyMarkerWidth — ширина маркера против полезной ширины артикула СЕГОДНЯ.
	LayCheckKeyMarkerWidth = "lay_marker_width"
	// LayCheckKeyLotWidth — ширина маркера против ИЗМЕРЕННОЙ ширины ТОГО РУЛОНА, с которого стелют
	// (Ф5б.1, Р8). Не дубликат lay_marker_width: см. LayLotFacts.
	LayCheckKeyLotWidth = "lay_lot_width"
	// LayCheckKeyStackHeight — высота стопки против предела цеха (Ф4.8, §11).
	LayCheckKeyStackHeight = "lay_stack_height"
	// LayCheckKeyTableLength — длина маркера против длины раскройного стола. ПРЕДУПРЕЖДЕНИЕ.
	LayCheckKeyTableLength = "lay_table_length"
	// LayCheckKeySlotDetached — слот BOM настила удалён (fk_prlay_bom SET NULL).
	LayCheckKeySlotDetached = "lay_slot_detached"
	// LayCheckKeyQuantitiesStale — снимок количеств разошёлся со строками прогона. ПРЕДУПРЕЖДЕНИЕ.
	LayCheckKeyQuantitiesStale = "lay_quantities_stale"
	// LayCheckKeyOvercut — выкроено больше, чем нужно на покрытые изделия. ПРЕДУПРЕЖДЕНИЕ, НЕ ЗАПРЕТ:
	// перекрой законен и его величина — свойство состава деталей изделия, а не режима (§8.2).
	LayCheckKeyOvercut = "lay_overcut"
)

// LayCheck is one finding. Mirrors common.ProductionLayCheck field for field.
type LayCheck struct {
	Key    string
	Status LayCheckStatus
	Label  string
	// Detail is the ACTUAL STATE. EMPTY ONLY ON AN OK ROW — the rule RunReadinessFinding.Detail
	// already states and the proto repeats («ПУСТО только у OK»): a detail is the explanation of a
	// finding, so a passing row carrying one reads as a failure that was let through, and a failing
	// row without one is a red badge nobody can act on. TestEveryCheckDetailsUnlessOK pins both
	// halves over every predicate in this file.
	Detail string
	// MarkerId — 0 = проверка про настил целиком.
	MarkerId int
	// PieceLineKey — "" = проверка не про конкретную деталь. Set only when EXACTLY ONE piece produced
	// the verdict; with two or more the прose names them all and this stays empty, because a client
	// that highlights one of three faulty pieces is worse than one that highlights none.
	PieceLineKey string
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// Входы. Каждый — ЗНАЧЕНИЯ, не строки базы: this file must stay compilable and testable while the
// schema (0281–0283) and the store are still being written next door, and a pure function that
// reaches for a column is not pure.
// ─────────────────────────────────────────────────────────────────────────────────────────────────

// LayIdentity is what the настил IS: whose run, whose card, which pair (колорвей, слот BOM).
type LayIdentity struct {
	RunId      int
	TechCardId int
	// ColorwayId is production_run_lay.colorway_id — NOT NULL in 0281, so a non-positive value here
	// is an unfilled input and never «the same colourway as a marker that has none».
	ColorwayId int
	// BomItemId INVALID = слот удалён из BOM (fk_prlay_bom SET NULL). The настил is BROKEN; see
	// LaySlotDetachedCheck. NOT the same as «matches a marker that also lost its slot»: two absences
	// are not an equality, and LayMarkerScopeCheck refuses to read them as one.
	BomItemId sql.NullInt64
	// BomLineKey is the NOT-NULL snapshot of the slot's line_key taken when the настил was built. It
	// exists to NAME a slot that disappeared, and it is also what the direction scope is resolved
	// against (§8.1) — the настил asks about the cloth it was built for.
	BomLineKey string
	Name       string
}

// LayMarkerFacts is everything a check reads off ONE tech_card_marker row plus its parsed blob.
type LayMarkerFacts struct {
	Id         int
	Name       string
	TechCardId int
	// RunId is tech_card_marker.run_id (0282). 0 = КАРТОЧНЫЙ маркер (норма или свободная раскладка),
	// which can never be a section of a настил — Р2: a настил references only its own copy, so that
	// re-shooting the card's norm cannot retroactively change what a finished run claims it cut.
	RunId int
	// BomItemId INVALID = маркер потерял слот (fk_tcm_bom SET NULL, 0257:63). §14 п.6: this happens
	// from an edit to somebody else's BOM, with no write to the настил at all, which is precisely why
	// lay_marker_scope has to run on READ and not only on save.
	BomItemId sql.NullInt64
	// ColorwayId INVALID (or 0) = ОБЩАЯ раскладка (fk_tcm_colorway SET NULL, 0264:45). It is NOT
	// «this colourway». Read it through ColorwayBindingOf, never by comparing ints — §14 п.7.
	ColorwayId sql.NullInt64
	// FabricWidthCm is the CUTTING width the раскладка runs on (roll width minus кромка on both
	// edges) — the left-hand side entity.NormWidthVsArticle expects.
	FabricWidthCm decimal.Decimal
	// UsedLengthCm is the marker's length along the cloth, the number the table length is judged
	// against (one spread pass is one marker long, however many plies go under it).
	UsedLengthCm decimal.Decimal
	// Yield is the parsed layout blob. NIL = блоб не прочитан (not parsed, unparsable, or a caller
	// that did not wire the distiller) ⇒ UNKNOWN, never «no mirrored placements».
	Yield *MarkerYield
}

// LayCheckSection is one секция настила: a marker laid a number of plies deep.
//
// ПАРА К dto.LaySection (production_lay_coverage.go), НЕ ЕЁ ДУБЛИКАТ. That one carries what COVERAGE
// reads off a section — the distilled blob and the parse's refusal — and knows nothing about the
// row's identity. This one carries what FITNESS reads — whose run, whose slot, whose colourway, how
// wide and how long — and does not need the yield at all except for the mirror expansion. Merging
// them would make every caller of either supply both halves; the store builds one row and projects it
// twice, which is one place, not two answers.
type LayCheckSection struct {
	SectionKey string
	Plies      int
	Marker     LayMarkerFacts
}

func (s LayCheckSection) describe() string {
	name := strings.TrimSpace(s.Marker.Name)
	if name == "" {
		name = fmt.Sprintf("marker #%d", s.Marker.Id)
	} else {
		name = fmt.Sprintf("marker %q", name)
	}
	if key := strings.TrimSpace(s.SectionKey); key != "" {
		return fmt.Sprintf("section %s (%s, plies: %d)", key, name, s.Plies)
	}
	return fmt.Sprintf("%s (plies: %d)", name, s.Plies)
}

// LayArticleFacts is the АРТИКУЛ the колорвей pins TODAY (not a snapshot — §11: cloth whose article
// was swapped after the настил was built is re-judged on the next read).
type LayArticleFacts struct {
	MaterialId int
	Name       string
	// NominalUsableWidthCm is Material.UsableFabricWidthCm — catalogue width minus кромка.
	NominalUsableWidthCm decimal.NullDecimal
	// NarrowestMeasuredLotCm is the narrowest MEASURED ROLL among lots that still have stock (Ф5а.1),
	// RAW — кромка not yet subtracted, because entity.NormWidthVsArticle subtracts it and doing it
	// twice would be invisible and always permissive.
	NarrowestMeasuredLotCm decimal.NullDecimal
	// SelvedgeCm is the article's кромка per edge.
	SelvedgeCm decimal.Decimal
	// FabricThicknessMm is material.fabric_thickness_mm (0283) — толщина полотна в ОДИН слой.
	// INVALID = НЕ ЗАМЕРЕНО, and that is the whole of Ф4.8's discipline: «нет толщины — нет проверки,
	// не догадка». No default, per class or otherwise, may ever be substituted here.
	FabricThicknessMm decimal.NullDecimal
}

// LayLotFacts is the РУЛОН this настил is actually spread from: production_run_lay.lot_id /
// lot_code (0285) joined to material_lot.measured_width_cm (0269).
//
// ЭТО НЕ LayArticleFacts.NarrowestMeasuredLotCm, И РАЗНИЦА — ВЕСЬ СМЫСЛ ОТДЕЛЬНОЙ ПРОВЕРКИ. That one
// is the NARROWEST measured lot the article still has stock of — a question about the CATALOGUE,
// asked so a norm is not approved against cloth nobody has. This one is the roll under the knife.
// The two answer differently in both directions: an article whose narrowest lot is 146 can be cut
// today off a 152 roll (lay_marker_width refuses, lay_lot_width passes), and a marker fit for the
// catalogue's narrowest can still be wider than the roll somebody just put on the table if that lot
// was measured after the marker was taken. Folding them into one check would answer one question
// with the other's evidence.
type LayLotFacts struct {
	// LotId is production_run_lay.lot_id. INVALID = либо лот не выбран вовсе, либо он УДАЛЁН
	// (fk_prlay_lot ON DELETE SET NULL, 0285). Обе — «сверять не с чем», обе UNKNOWN, и обе НАЗЫВАЮТ
	// свою причину: для человека это разные новости, даже когда статус один.
	LotId sql.NullInt64
	// LotCode is the NOT NULL snapshot taken when the lot was bound (Р6). It exists to NAME a lot that
	// disappeared, exactly as LayIdentity.BomLineKey names a deleted slot — the price of SET NULL,
	// paid for by a настил that can still speak the missing roll's name instead of falling silent.
	LotCode string
	// MeasuredWidthCm is material_lot.measured_width_cm (0269) — the width that ARRIVED, i.e. the FULL
	// roll INCLUDING кромка, NOT a cutting width. Passed RAW on purpose: entity.NormWidthVsArticle
	// subtracts the selvedge, and subtracting it here as well would be invisible and always
	// permissive. INVALID = НЕ ЗАМЕРЕНО ⇒ UNKNOWN, никогда «влезает».
	MeasuredWidthCm decimal.NullDecimal
}

// LayWorkshopLimits are the settings of the ЦЕХА — one room, one set of numbers (Р5). NULL means «не
// настроено», and then there is NO VERDICT, not a zero.
type LayWorkshopLimits struct {
	// MaxStackHeightCm is workshop_settings.max_stack_height_cm (0283). ONE PER WORKSHOP, deliberately
	// (Р5): a per-fabric limit is a refinement there is no data for, and a column nobody can fill is a
	// column that answers NULL forever while looking like a setting.
	MaxStackHeightCm decimal.NullDecimal
	// CuttingTableLengthCm is workshop_settings.cutting_table_length_cm (0272).
	CuttingTableLengthCm decimal.NullDecimal
}

// LayQtyEntry is one entry of the quantity snapshot / of today's run lines for this colourway.
type LayQtyEntry struct {
	SizeId int
	Qty    int
}

// LayPieceCut is ONE cut-piece as the whole настил cut it, for the перекрой question.
type LayPieceCut struct {
	PieceLineKey string
	PieceName    string
	// Cut is instances PHYSICALLY cut by every section of the настил together (§6.2 шаг 2 summed) —
	// i.e. what LayerCutInstances returned, not what one layer places.
	Cut MarkerPieceCounts
	// Symmetry is tech_card_piece.cut_symmetry (0275). INVALID = НЕ РАЗМЕЧЕНО ⇒ the piece is UNKNOWN
	// and contributes an UNKNOWN, never an assumed `identical`.
	Symmetry sql.NullString
	// PiecesPerGarment is how many instances one garment consumes.
	PiecesPerGarment int
	// ChiralityKnown is MarkerYield.ChiralityKnown of the blob these instances were counted off —
	// false below layout schema 3, where «zero mirrored» is absence of evidence.
	ChiralityKnown bool
}

// LayCoveredQty is how many изделий the настил is judged to provision (§6.2 шаг 4), WITH the third
// answer in the type. ZERO VALUE IS UNKNOWN: перекрой is «выкроено сверх нужного на covered_qty», so
// a coverage nobody computed leaves the surplus undefined rather than equal to everything cut.
type LayCoveredQty struct {
	Qty   int
	Known bool
}

// LayCheckInput is the whole of what §8 needs about one настил.
type LayCheckInput struct {
	Lay      LayIdentity
	Mode     LayFaceMode
	Sections []LayCheckSection
	Article  LayArticleFacts
	// Lot is the РУЛОН this настил is spread from (0285). ZERO VALUE = лот не выбран ⇒ lay_lot_width is
	// UNKNOWN, which is the correct reading for every настил built before Ф5б and for every caller
	// that has not wired the join yet: «не сверяли», never «влезает».
	Lot    LayLotFacts
	Limits LayWorkshopLimits
	// BomLines are the card's ROLL-GOODS lines — the input entity.MarkerFabricScope resolves the
	// направление scope out of. Passed whole because «строжайшее побеждает» is a fact about the
	// назначение, not about one line (§8.1).
	BomLines []entity.FabricDirectionLine
	// PieceSymmetry is piece_line_key → tech_card_piece.cut_symmetry. A MISSING key and an INVALID
	// value mean the same thing here — «не размечено» ⇒ UNKNOWN — and neither may read as `identical`.
	PieceSymmetry map[string]sql.NullString
	QtySnapshot   []LayQtyEntry
	QtyCurrent    []LayQtyEntry
	PieceCuts     []LayPieceCut
	Covered       LayCoveredQty
}

// TotalPlies is Σ слоёв по секциям — the multiplier of the stack height.
func (in LayCheckInput) TotalPlies() int {
	total := 0
	for _, s := range in.Sections {
		if s.Plies > 0 {
			total += s.Plies
		}
	}
	return total
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// Предикаты.
// ─────────────────────────────────────────────────────────────────────────────────────────────────

// MarkerColorwayBinding is the THREE answers to «этот маркер про этот колорвей?». Two would not do:
// `colorway_id IS NULL` means ОБЩАЯ раскладка — usable by any colourway — and the value 0 arrives
// from exactly there (fk_tcm_colorway ON DELETE SET NULL, 0264:45, the named trade of §14 п.7). A
// reader that compares ints reads that absence as «this one» the moment the lay's own colourway is
// unset too, and then a раскладка measured for a colour nobody has any more is offered as the
// measurement of the colour in front of them.
type MarkerColorwayBinding int

const (
	// The zero value is deliberately NOT one of the three: nobody may get an answer by forgetting to
	// ask. See ColorwayBindingOf, which is the only constructor.
	_ MarkerColorwayBinding = iota
	// MarkerColorwayGeneral — маркер ОБЩИЙ (colorway_id IS NULL / 0). Годен для любого колорвея, and
	// §8 puts it inside lay_marker_scope's OK set on purpose.
	MarkerColorwayGeneral
	// MarkerColorwayMatches — маркер именно этого колорвея.
	MarkerColorwayMatches
	// MarkerColorwayForeign — маркер ДРУГОГО колорвея.
	MarkerColorwayForeign
)

func (b MarkerColorwayBinding) String() string {
	switch b {
	case MarkerColorwayGeneral:
		return "GENERAL"
	case MarkerColorwayMatches:
		return "MATCHES"
	case MarkerColorwayForeign:
		return "FOREIGN"
	}
	return fmt.Sprintf("MarkerColorwayBinding(%d)", int(b))
}

// ColorwayBindingOf reads a marker's colourway link against the настил's colourway. THE ONLY place
// in Ф4 allowed to interpret tech_card_marker.colorway_id, so «0 = общая» exists once.
func ColorwayBindingOf(markerColorwayId sql.NullInt64, layColorwayId int) MarkerColorwayBinding {
	if !markerColorwayId.Valid || markerColorwayId.Int64 <= 0 {
		return MarkerColorwayGeneral
	}
	if layColorwayId <= 0 {
		// production_run_lay.colorway_id is NOT NULL (0281), so a non-positive value is an unfilled
		// input, not a colourway. Answering MATCHES for it would pair two absences and call the pair
		// an agreement — the exact substitution this type exists to make unavailable.
		return MarkerColorwayForeign
	}
	if markerColorwayId.Int64 == int64(layColorwayId) {
		return MarkerColorwayMatches
	}
	return MarkerColorwayForeign
}

// LayModeParityCheck — `lay_mode_parity`. OK: режим FACE_UP; либо FACE_TO_FACE и `plies` чётно у
// КАЖДОЙ секции. FAIL: FACE_TO_FACE и есть секция с нечётным `plies`. UNKNOWN: НИКОГДА — the ply
// count and the mode are both on the настил itself, so there is never anything missing to ask.
//
// ЧЁТНОСТЬ СПРАШИВАЕТСЯ У LayerCutInstances, А НЕ У `plies%2`. The rule lives in
// production_lay_yield.go and the same call decides how many instances a section actually cuts; two
// copies of «чётно ли» would be two answers to one question, and the one that drifted would be the
// one nobody tested. The probe lays a single as-drawn instance — the counts are irrelevant, only the
// refusal is read.
func LayModeParityCheck(mode LayFaceMode, sections []LayCheckSection) LayCheck {
	c := LayCheck{Key: LayCheckKeyModeParity, Label: "ply parity against the lay mode"}
	if !ValidLayFaceModes[mode] {
		// Не UNKNOWN: chk_prlay_mode (0281) makes this unreachable through the app, so a mode outside
		// the dictionary is a corrupt row, and «не могу проверить» would let it through the save path
		// that calls this predicate to refuse.
		c.Status = LayCheckStatusBlocker
		c.Detail = fmt.Sprintf("lay mode %q is not in the dictionary (%s|%s)", mode, LayFaceModeFaceUp, LayFaceModeFaceToFace)
		return c
	}
	var odd, malformed []string
	for _, s := range sections {
		_, err := LayerCutInstances(MarkerPieceCounts{AsDrawn: 1}, mode, s.Plies)
		switch {
		case err == nil:
		case errors.Is(err, ErrLayModeParity):
			odd = append(odd, s.describe())
		default:
			malformed = append(malformed, fmt.Sprintf("%s: %v", s.describe(), err))
		}
	}
	switch {
	case len(odd) > 0 && len(malformed) > 0:
		c.Status = LayCheckStatusBlocker
		c.Detail = fmt.Sprintf("a face-to-face lay requires an even number of plies in every section; odd: %s; besides that: %s",
			strings.Join(odd, ", "), strings.Join(malformed, ", "))
	case len(odd) > 0:
		c.Status = LayCheckStatusBlocker
		c.Detail = fmt.Sprintf("a face-to-face lay requires an even number of plies in every section; odd: %s", strings.Join(odd, ", "))
	case len(malformed) > 0:
		c.Status = LayCheckStatusBlocker
		c.Detail = strings.Join(malformed, "; ")
	default:
		c.Status = LayCheckStatusOK
	}
	return c
}

// LayMarkerScopeCheck — `lay_marker_scope`. OK: маркер секции несёт `tech_card_id` прогона,
// `run_id` = этого прогона, `bom_item_id` = слот настила, `colorway_id ∈ {NULL, колорвей настила}`.
// FAIL иначе. UNKNOWN: НИКОГДА.
//
// РАБОТАЕТ НА ЧТЕНИИ, А НЕ ТОЛЬКО НА ЗАПИСИ, и это не удобство (§14 п.6). `fk_tcm_bom` is
// ON DELETE SET NULL (0257:63): deleting a BOM line from the card's tab detaches every marker on it,
// touching neither the настил nor the run. A check that ran only at save time would leave that
// настил rendering green until the next time somebody edited it, which may be never.
func LayMarkerScopeCheck(lay LayIdentity, marker LayMarkerFacts) LayCheck {
	c := LayCheck{Key: LayCheckKeyMarkerScope, Label: "the marker belongs to this lay", MarkerId: marker.Id}
	var faults []string

	if marker.TechCardId != lay.TechCardId {
		faults = append(faults, fmt.Sprintf("the marker was made from card #%d, while the run works off card #%d",
			marker.TechCardId, lay.TechCardId))
	}
	if marker.RunId != lay.RunId {
		if marker.RunId == 0 {
			faults = append(faults, "this is a CARD marker (a consumption norm or a free marker); a lay section may only reference the copy owned by the run")
		} else {
			faults = append(faults, fmt.Sprintf("the marker belongs to run #%d, while the lay belongs to run #%d", marker.RunId, lay.RunId))
		}
	}

	switch {
	case !lay.BomItemId.Valid:
		// Слот самого настила пропал. Не UNKNOWN: сравнивать не с чем — значит соответствие НЕ
		// доказано, а «доказать нечем» здесь совпало бы с «годен» на экране цеха.
		faults = append(faults, fmt.Sprintf("the lay's BOM slot has been deleted (line_key snapshot %s) — there is nothing to match the marker against",
			nameOrDash(lay.BomLineKey)))
	case !marker.BomItemId.Valid:
		faults = append(faults, "the marker lost its BOM slot (the BOM line was deleted from the card) — it is no longer about this lay's fabric")
	case marker.BomItemId.Int64 != lay.BomItemId.Int64:
		faults = append(faults, fmt.Sprintf("the marker was made for BOM slot #%d, while the lay is spread on slot #%d",
			marker.BomItemId.Int64, lay.BomItemId.Int64))
	}

	if ColorwayBindingOf(marker.ColorwayId, lay.ColorwayId) == MarkerColorwayForeign {
		faults = append(faults, fmt.Sprintf("the marker is bound to colourway #%d, while the lay is bound to colourway #%d",
			marker.ColorwayId.Int64, lay.ColorwayId))
	}

	if len(faults) == 0 {
		c.Status = LayCheckStatusOK
		return c
	}
	c.Status = LayCheckStatusBlocker
	c.Detail = strings.Join(faults, "; ")
	return c
}

// LayMirrorExpansionCheck — `lay_mirror_expansion`. Судится ТОЛЬКО деталь с `cut_symmetry = mirrored`:
//
//	FACE_UP        OK: число размещений flipped=false равно числу flipped=true — обе руки в маркере.
//	               FAIL: несовпадение (полкомплекта лекал разложено под другой режим).
//	FACE_TO_FACE   OK: flipped=true не встречается вовсе — вторую руку даёт сам настил.
//	               FAIL: есть отражённые размещения — маркер развёрнут под другой режим.
//
// UNKNOWN: `layout_schema_version < 3` (до Ф1 поля `flipped` не было, и «ноль отражённых» — не
// доказательство), `cut_symmetry IS NULL` (0275: «НЕ РАЗМЕЧЕНО», а не «обычная»), `piece_line_key = ""`
// (деталь неатрибутируема к карточке), либо блоб не прочитан вовсе.
//
// СЧЁТ ПО ДЕТАЛИ, А НЕ ПО РАЗМЕРУ, и это названная граница: instances are summed across the sizes of
// the состав, so an imbalance in size M that cancels against the opposite imbalance in size L is not
// visible HERE. It is visible in coverage, which asks PerLayerInstances per size. This check answers
// «нарезан ли маркер под этот режим», a property of the geometry as a whole.
func LayMirrorExpansionCheck(mode LayFaceMode, marker LayMarkerFacts, symmetry map[string]sql.NullString) LayCheck {
	c := LayCheck{Key: LayCheckKeyMirrorExpansion, Label: "mirrored-piece expansion under the lay mode", MarkerId: marker.Id}
	if !ValidLayFaceModes[mode] {
		// lay_mode_parity already carries the BLOCKER for a mode outside the dictionary; repeating it
		// here would double-count one fault, and answering OK would approve geometry against a rule
		// nobody could name.
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("lay mode %q is not in the dictionary — there is no rule to judge the expansion by", mode)
		return c
	}
	if marker.Yield == nil {
		c.Status = LayCheckStatusUnknown
		c.Detail = "the marker's layout was not read — there is nothing to check the piece expansion with"
		return c
	}
	y := *marker.Yield
	if !y.Attributable() {
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("the layout cannot be matched to the card's pieces (blob schema %d, unattributable pieces: %d)",
			y.SchemaVersion, y.UnattributedPieces)
		return c
	}

	// Свести размещения по ДЕТАЛИ, сложив размеры.
	byPiece := make(map[string]MarkerPieceCounts, len(y.Pieces))
	for key, counts := range y.Pieces {
		if key.PieceLineKey == "" {
			continue
		}
		agg := byPiece[key.PieceLineKey]
		agg.AsDrawn += counts.AsDrawn
		agg.Mirrored += counts.Mirrored
		byPiece[key.PieceLineKey] = agg
	}
	if len(byPiece) == 0 {
		c.Status = LayCheckStatusUnknown
		c.Detail = "the layout has no attributed pieces at all"
		return c
	}

	keys := make([]string, 0, len(byPiece))
	for k := range byPiece {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var statuses []LayCheckStatus
	var faults, unknowns, faultKeys []string
	for _, key := range keys {
		counts := byPiece[key]
		name := layCheckPieceLabel(key, y.PieceNames[key])
		sym, ok := symmetry[key]
		if !ok || !sym.Valid {
			statuses = append(statuses, LayCheckStatusUnknown)
			unknowns = append(unknowns, fmt.Sprintf("%s: cut symmetry is not set", name))
			continue
		}
		switch entity.TechCardPieceCutSymmetry(sym.String) {
		case entity.PieceCutSymmetryMirrored:
			if !y.ChiralityKnown() {
				statuses = append(statuses, LayCheckStatusUnknown)
				unknowns = append(unknowns, fmt.Sprintf("%s: a schema-%d layout does not distinguish flipped placements", name, y.SchemaVersion))
				continue
			}
			switch mode {
			case LayFaceModeFaceUp:
				if counts.AsDrawn != counts.Mirrored {
					statuses = append(statuses, LayCheckStatusBlocker)
					faults = append(faults, fmt.Sprintf("%s: as-drawn %d, flipped %d — in a face-up lay both hands are cut from the marker and their counts must be equal",
						name, counts.AsDrawn, counts.Mirrored))
					faultKeys = append(faultKeys, key)
					continue
				}
			case LayFaceModeFaceToFace:
				if counts.Mirrored > 0 {
					statuses = append(statuses, LayCheckStatusBlocker)
					faults = append(faults, fmt.Sprintf("%s: %d flipped placements — the marker is nested for face-up spreading, while the lay runs face to face",
						name, counts.Mirrored))
					faultKeys = append(faultKeys, key)
					continue
				}
			}
			statuses = append(statuses, LayCheckStatusOK)
		case entity.PieceCutSymmetryIdentical, entity.PieceCutSymmetryFold:
			// Ни та, ни другая не образует зеркальную пару: у identical отражение — это не эта деталь
			// (его считает lay_overcut), у fold контур симметричен по построению. Развёртке нечего
			// проверять, и молчание тут — ответ, а не пропуск.
			statuses = append(statuses, LayCheckStatusOK)
		default:
			// Значение вне словаря 0275. Не «наверное identical» — ровно та подстановка, ради
			// запрета которой 0275 и заводилась.
			statuses = append(statuses, LayCheckStatusUnknown)
			unknowns = append(unknowns, fmt.Sprintf("%s: cut symmetry = %q is not in the dictionary", name, sym.String))
		}
	}

	c.Status = WorstLayCheckStatus(statuses...)
	switch c.Status {
	case LayCheckStatusOK:
	case LayCheckStatusBlocker:
		c.Detail = strings.Join(faults, "; ")
		if len(faultKeys) == 1 {
			c.PieceLineKey = faultKeys[0]
		}
	default:
		c.Detail = strings.Join(append(append([]string{}, faults...), unknowns...), "; ")
	}
	return c
}

// LayDirectionModeCheck — `lay_direction_mode`. OK: направление скоупа `two_way`/`any`; либо
// `one_way` и режим FACE_UP. FAIL: `one_way` и FACE_TO_FACE — слои лицом вниз разворачивают ворс.
// UNKNOWN: направление NULL хоть на одной строке скоупа.
//
// НАПРАВЛЕНИЕ БЕРЁТСЯ ИЗ Ф1 ЦЕЛИКОМ (§8.1): entity.MarkerFabricScope resolves which lines the
// назначение owns (including «семпловая ярдажа в скоуп не входит»), entity.ScopeFabricDirection folds
// them СТРОЖАЙШИМ ПОБЕЖДАЕТ. Ни одной второй реализации: Ф1 поставила эту машинерию, Ф4 её ВЫЗЫВАЕТ.
//
// На бете направление NULL почти везде до кампании Д1, поэтому именно эта проверка будет самой
// громкой — что и есть причина, по которой её UNKNOWN обязан выглядеть как «не проверено», а не как
// отказ и не как «годен».
func LayDirectionModeCheck(mode LayFaceMode, bomLineKey string, lines []entity.FabricDirectionLine) LayCheck {
	c := LayCheck{Key: LayCheckKeyDirectionMode, Label: "fabric direction against the lay mode"}
	scope := entity.MarkerFabricScope(bomLineKey, lines)
	if !scope.Live() {
		// ScopeFabricDirection over an EMPTY scope answers `any` with no unknowns — truthfully, since
		// it folded nothing — and reading that as OK would approve directional cloth on evidence that
		// does not exist. A dangling scope is the state of a настил whose slot was deleted, i.e.
		// exactly the case lay_slot_detached is shouting about.
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("BOM line %s is not found among the card's fabric lines — there is nothing to ask for the direction",
			nameOrDash(bomLineKey))
		return c
	}
	dir, unknown, ok := entity.ScopeFabricDirection(scope, lines)
	if !ok {
		names := make([]string, 0, len(unknown))
		for _, l := range unknown {
			names = append(names, nameOrDash(l.LineKey))
		}
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("fabric direction is not set on BOM lines: %s — fill it in on the BOM tab and this check will start working",
			strings.Join(names, ", "))
		return c
	}
	if dir != entity.FabricDirectionOneWay {
		c.Status = LayCheckStatusOK
		return c
	}
	if mode == LayFaceModeFaceUp {
		c.Status = LayCheckStatusOK
		return c
	}
	c.Status = LayCheckStatusBlocker
	c.Detail = fmt.Sprintf("the fabric of purpose %s is directional (one_way), while a face-to-face lay puts every second ply face down — the nap and any directional print will end up reversed",
		nameOrDash(scope.Key))
	return c
}

// LayMarkerWidthCheck — `lay_marker_width`. OK: `marker.fabric_width_cm ≤` полезная ширина артикула,
// который колорвей пинит СЕГОДНЯ. FAIL: шире. UNKNOWN: у артикула нет ни ширины, ни измеренной ширины
// лота (Ф5а.1).
//
// Предикат — entity.NormWidthVsArticle целиком, включая вычитание кромки из измеренного лота и
// названную там же консервативность скалярного сравнения. Здесь только перевод вердикта в статус Ф4:
// вторая реализация сравнения ширин разошлась бы с гейтом Ф6, и разошлась бы молча.
func LayMarkerWidthCheck(marker LayMarkerFacts, article LayArticleFacts) LayCheck {
	c := LayCheck{Key: LayCheckKeyMarkerWidth, Label: "marker width against the article's usable width", MarkerId: marker.Id}
	v := entity.NormWidthVsArticle(marker.FabricWidthCm, article.NarrowestMeasuredLotCm, article.SelvedgeCm, article.NominalUsableWidthCm)
	c.Status = layCheckStatusFromSeverity(v.Severity)
	switch c.Status {
	case LayCheckStatusOK:
	case LayCheckStatusBlocker:
		c.Detail = fmt.Sprintf("the marker was made %s cm wide, while the usable width of article %s today is %s cm",
			marker.FabricWidthCm.String(), nameOrDash(article.Name), v.TodayCuttingCm.Decimal.String())
	default:
		c.Detail = fmt.Sprintf("article %s has neither a catalogue width nor a measured lot width — there is nothing to compare against",
			nameOrDash(article.Name))
	}
	return c
}

// LayLotWidthCheck — `lay_lot_width` (Ф5б.1, Р8). Ширина маркера против ИЗМЕРЕННОЙ ширины ТОГО
// РУЛОНА, с которого этот настил стелется:
//
//	ширина лота не задана (лот не выбран, лот удалён, лот не замерен) ⇒ UNKNOWN, а НЕ «влезает»
//	маркер уже лота                                                  ⇒ OK
//	маркер шире лота                                                 ⇒ BLOCKER
//
// BLOCKER, А НЕ WARNING, И ЭТО СОЗНАТЕЛЬНО РАСХОДИТСЯ С СОСЕДНЕЙ lay_table_length. НЕ «УНИФИЦИРУЙТЕ»
// ИХ. Стол короче маркера — это про то, СКОЛЬКО РАЗ придётся настилать: маркер длиннее стола просто
// делится на проходы, физика не нарушена, и запрещать раскройщику многопроходный настил сервер не
// вправе. Рулон уже маркера не делится ни на что: деталь, выходящая за кромку, не выкроится ни в
// один проход, ни в сто — поперёк ткани резать нечего. Одна проверка предупреждает, вторая
// отказывает, и любое их сведение к одному статусу — это либо запрет законного многопроходного
// настилания, либо разрешение резать воздух.
//
// СРАВНЕНИЕ — entity.NormWidthVsArticle ЦЕЛИКОМ, включая вычитание кромки из измеренного рулона и
// названную там же консервативность скалярного сравнения. Вторая реализация сравнения ширин
// разошлась бы с lay_marker_width и с гейтом Ф6, и разошлась бы молча.
//
// НОМИНАЛ АРТИКУЛА В ПРЕДИКАТ НЕ ПЕРЕДАЁТСЯ, И ЭТО НЕ ЗАБЫВЧИВОСТЬ. entity.NormWidthVsArticle падает
// на каталожную ширину, когда замера нет; передать сюда article.NominalUsableWidthCm значило бы
// превратить незамеренный лот в OK по ширине, которую этот рулон никому не обещал, — то есть ровно
// в тот ответ «влезает», против которого Р8 и написана. Вопрос «а что говорит каталог» уже задан, и
// на него отвечает lay_marker_width.
func LayLotWidthCheck(marker LayMarkerFacts, lot LayLotFacts, article LayArticleFacts) LayCheck {
	c := LayCheck{Key: LayCheckKeyLotWidth, Label: "marker width against the lot's measured width", MarkerId: marker.Id}

	if !lot.LotId.Valid || lot.LotId.Int64 <= 0 {
		c.Status = LayCheckStatusUnknown
		if code := strings.TrimSpace(lot.LotCode); code != "" {
			// Снимок кода пережил лот (Р6) — настил называет пропавший рулон вместо того, чтобы
			// молчать. Это и есть то, чем оплачен SET NULL.
			c.Detail = fmt.Sprintf("lot %s, which this lay was spread from, has been deleted from the register — there is nowhere to ask for its measured width", code)
			return c
		}
		c.Detail = "the lot this lay is spread from is not selected — there is nothing to compare the marker width against"
		return c
	}

	// Непозитивный замер — это не рулон нулевой ширины, в который ничего не влезает; это незаполненное
	// поле в чужой одежде. chk_material_lot_measured_width (0269) такую строку не пропустит, но
	// предикат здесь чистый и вход берёт значениями, а весь файл уже читает «не Valid или не
	// положительно» как «не задано» (LayStackHeightVerdict, LayTableLengthCheck).
	measured := lot.MeasuredWidthCm
	if measured.Valid && !measured.Decimal.IsPositive() {
		measured = decimal.NullDecimal{}
	}

	v := entity.NormWidthVsArticle(marker.FabricWidthCm, measured, article.SelvedgeCm, decimal.NullDecimal{})
	if v.Basis == entity.NormWidthBasisNone {
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("the width of lot %s is not measured — there is nothing to compare the marker width against; measure the roll and this check will start working",
			layLotLabel(lot))
		return c
	}
	switch layCheckStatusFromSeverity(v.Severity) {
	case LayCheckStatusOK:
		c.Status = LayCheckStatusOK
	case LayCheckStatusBlocker:
		c.Status = LayCheckStatusBlocker
		c.Detail = fmt.Sprintf("the marker was made %s cm wide, while lot %s measures %s cm — its cutting width is %s cm (selvedge %s cm on each edge); this marker cannot be cut on this roll",
			marker.FabricWidthCm.String(), layLotLabel(lot), v.MeasuredRollCm.Decimal.String(),
			v.TodayCuttingCm.Decimal.String(), article.SelvedgeCm.String())
	default:
		// Вердикт, которого этот файл прочитать не может. Не OK: нечитаемый ответ не бывает одобрением.
		c.Status = LayCheckStatusUnknown
		c.Detail = fmt.Sprintf("the width comparison for lot %s returned a verdict there is no way to read", layLotLabel(lot))
	}
	return c
}

// layLotLabel names a lot for a message: the code the склад calls it by, falling back to the id.
func layLotLabel(lot LayLotFacts) string {
	if code := strings.TrimSpace(lot.LotCode); code != "" {
		return code
	}
	if lot.LotId.Valid {
		return fmt.Sprintf("#%d", lot.LotId.Int64)
	}
	return "—"
}

// stackHeightMmPerCm — миллиметры в сантиметре. Named because the /10 in Σ plies × thickness_mm / 10
// is the unit conversion Ф4.8 is entirely about (30 слоёв шифона — 2 см, 30 слоёв драпа — 30 см), and
// a bare 10 in that expression is the kind of thing somebody later reads as a tolerance.
var stackHeightMmPerCm = decimal.NewFromInt(10)

// LayStackHeight is the Ф4.8 verdict WITH its numbers — what ProductionRunLay.stack_height_cm is
// filled from.
type LayStackHeight struct {
	Status     LayCheckStatus
	TotalPlies int
	// HeightCm is Σ plies × thickness_mm / 10. INVALID whenever the thickness is not measured, and
	// that is the acceptance probe of §13 п.13 word for word: «артикул без толщины ⇒ UNKNOWN, а не
	// „0 см, влезает"». The number is WITHHELD, not zeroed — a zero would travel to the client as a
	// stack that trivially fits, which is the single most confident wrong answer this file can give.
	HeightCm decimal.NullDecimal
	// LimitCm is the workshop's limit, INVALID when it is not configured (and cleared when it is
	// stored as a non-positive number, so «предел 0 см» can never be rendered as a limit).
	LimitCm decimal.NullDecimal
	Detail  string
}

// LayStackHeightVerdict — предел стопки (Ф4.8, §11):
//
//	stack_height_cm = Σ plies × fabric_thickness_mm / 10
//	вердикт: stack_height_cm ≤ workshop_settings.max_stack_height_cm
//
//	fabric_thickness_mm IS NULL  ⇒ UNKNOWN, высота НЕ ОТДАЁТСЯ ВОВСЕ, подсказка «замерьте толщину»
//	max_stack_height_cm IS NULL  ⇒ UNKNOWN, подсказка «предел стопки не настроен»
//	оба заданы                   ⇒ OK / BLOCKER с числами
//
// НИКАКОГО ДЕФОЛТА. «Нет толщины — нет проверки, не догадка»: a default per fabric class was rejected
// together with the class taxonomy itself, and any stand-in value here would turn an absence of data
// into a verdict — a confident one, on a knife that has a real stroke height.
//
// Предел — ОДИН НА ЦЕХ (Р5). NULL значит «не настроено», и тогда вердикта нет, а не ноль. Пер-тканевых
// пределов нет сознательно: колонка, которую сегодня нечем заполнить, отвечала бы NULL вечно, а
// выглядела бы настройкой.
func LayStackHeightVerdict(totalPlies int, fabricThicknessMm, maxStackHeightCm decimal.NullDecimal) LayStackHeight {
	out := LayStackHeight{TotalPlies: totalPlies}
	var missing []string

	thicknessKnown := fabricThicknessMm.Valid && fabricThicknessMm.Decimal.IsPositive()
	if !thicknessKnown {
		missing = append(missing, "fabric thickness is not set on the article — measure it and the stack height will start being checked")
	}
	if totalPlies < 1 {
		// Ноль слоёв — это не стопка нулевой высоты, влезающая в любой предел; это настил, о котором
		// сказать нечего. Тот же провал, что «нет толщины ⇒ 0 см», в другой одежде.
		missing = append(missing, "the lay has no plies at all — there is nothing to compute the stack height from")
	}
	if thicknessKnown && totalPlies >= 1 {
		out.HeightCm = decimal.NullDecimal{
			Decimal: decimal.NewFromInt(int64(totalPlies)).Mul(fabricThicknessMm.Decimal).Div(stackHeightMmPerCm),
			Valid:   true,
		}
	}

	if maxStackHeightCm.Valid && maxStackHeightCm.Decimal.IsPositive() {
		out.LimitCm = maxStackHeightCm
	} else {
		missing = append(missing, "the stack limit is not configured in the workshop settings")
	}

	if len(missing) > 0 {
		out.Status = LayCheckStatusUnknown
		out.Detail = strings.Join(missing, "; ")
		return out
	}
	if out.HeightCm.Decimal.GreaterThan(out.LimitCm.Decimal) {
		out.Status = LayCheckStatusBlocker
		out.Detail = fmt.Sprintf("the stack is %s cm (%d plies × %s mm), above the workshop limit of %s cm",
			out.HeightCm.Decimal.String(), totalPlies, fabricThicknessMm.Decimal.String(), out.LimitCm.Decimal.String())
		return out
	}
	out.Status = LayCheckStatusOK
	return out
}

// LayStackHeightCheck — `lay_stack_height` как находка. The numbers travel separately
// (LayStackHeightVerdict) because ProductionRunLay carries stack_height_cm as its own field and it
// must be ABSENT, not zero, in exactly the cases the finding is UNKNOWN.
func LayStackHeightCheck(totalPlies int, fabricThicknessMm, maxStackHeightCm decimal.NullDecimal) LayCheck {
	v := LayStackHeightVerdict(totalPlies, fabricThicknessMm, maxStackHeightCm)
	return LayCheck{
		Key:    LayCheckKeyStackHeight,
		Status: v.Status,
		// «ПО ТОЛЩИНЕ» СКАЗАНО В ИМЕНИ ПРОВЕРКИ, И ЭТО НЕ УТОЧНЕНИЕ РАДИ ТОЧНОСТИ.
		//
		// Формула §11 — Σ слоёв × толщина / 10 — считает СЛОЖЕННУЮ ТКАНЬ, а настил стелется с
		// воздухом между слоями. 00-model.md:44-46 приводит замеренные цифры цеха: 30 слоёв драпа —
		// 30 см, тогда как формула на тех же входах даёт 6. Расхождение вчетверо-вшестеро, и оно
		// целиком в РАЗРЕШАЮЩУЮ сторону: настил, посчитанный 6 см, физически может не влезть под
		// нож с ходом 15.
		//
		// Коэффициент распушения сюда не вписан НАМЕРЕННО: подобрать его не из чего, а число,
		// выведенное из двух строк документа, было бы «догадкой в одежде числа» — ровно то, что
		// решение Р4 отвергло у коэффициента раскроя. Правильный ответ тот же: отдавать
		// измеримое как есть, НАЗЫВАТЬ, что именно посчитано, и калибровать по факту в Ф5б.
		// Поэтому вердикт остаётся, а обещание из него убрано: цех читает «по толщине», а не
		// «высота стопки», и знает, что запас надо держать сверх этого числа.
		Label:  "stack height BY FABRIC THICKNESS (no lay loft included) against the workshop limit",
		Detail: v.Detail,
	}
}

// LayTableLengthCheck — `lay_table_length`. OK: `max(marker.used_length_cm) ≤ cutting_table_length_cm`.
// UNKNOWN: длина стола не настроена.
//
// ПРЕДУПРЕЖДЕНИЕ, А НЕ ОТКАЗ (§8): настил длиннее стола просто делится на проходы. The check exists so
// the раскройщик learns it before spreading, not so a server can decide it is impossible.
//
// Сравнивается ДЛИНА МАРКЕРА, а не длина настила: один проход настилания — это ровно один маркер в
// длину, сколько бы слоёв под него ни легло.
func LayTableLengthCheck(sections []LayCheckSection, cuttingTableLengthCm decimal.NullDecimal) LayCheck {
	c := LayCheck{Key: LayCheckKeyTableLength, Label: "marker length against the cutting table length"}
	if !cuttingTableLengthCm.Valid || !cuttingTableLengthCm.Decimal.IsPositive() {
		c.Status = LayCheckStatusUnknown
		c.Detail = "the cutting table length is not configured in the workshop settings"
		return c
	}
	if len(sections) == 0 {
		c.Status = LayCheckStatusUnknown
		c.Detail = "the lay has no sections at all — there is nothing to measure"
		return c
	}
	longest := sections[0]
	for _, s := range sections[1:] {
		if s.Marker.UsedLengthCm.GreaterThan(longest.Marker.UsedLengthCm) {
			longest = s
		}
	}
	if longest.Marker.UsedLengthCm.GreaterThan(cuttingTableLengthCm.Decimal) {
		c.Status = LayCheckStatusWarning
		c.MarkerId = longest.Marker.Id
		c.Detail = fmt.Sprintf("%s at %s cm long does not fit on a %s cm table — the lay will have to be spread in several passes",
			longest.describe(), longest.Marker.UsedLengthCm.String(), cuttingTableLengthCm.Decimal.String())
		return c
	}
	c.Status = LayCheckStatusOK
	return c
}

// LaySlotDetachedCheck — `lay_slot_detached`. OK: `bom_item_id` живой. FAIL: `bom_item_id IS NULL` —
// слот BOM удалён, настил вне потребности и покрытия. UNKNOWN: НИКОГДА.
//
// Цена решения «fk_prlay_bom — SET NULL» (0281), оплаченная НЕ-NULL-снимком bom_line_key: настил
// всегда может НАЗВАТЬ пропавший слот и потому никогда не молчит.
func LaySlotDetachedCheck(lay LayIdentity) LayCheck {
	c := LayCheck{Key: LayCheckKeySlotDetached, Label: "the lay's BOM slot is in place"}
	if lay.BomItemId.Valid {
		c.Status = LayCheckStatusOK
		return c
	}
	c.Status = LayCheckStatusBlocker
	c.Detail = fmt.Sprintf("the BOM line this lay was spread against has been deleted from the card (line_key snapshot %s) — the lay drops out of the requirement and coverage",
		nameOrDash(lay.BomLineKey))
	return c
}

// LayQuantitiesStaleCheck — `lay_quantities_stale`. OK: снимок совпадает с текущими строками прогона.
// WARNING иначе. UNKNOWN: НИКОГДА — обе стороны сравнения на руках у сервера.
//
// Сравнение НЕЧУВСТВИТЕЛЬНО К ПОРЯДКУ и складывает дубликаты: the snapshot is JSON written by the
// server and read back whole, so a reader that depended on entry order would report staleness for a
// re-serialisation. Нулевые количества выброшены с обеих сторон: размер с qty 0 и отсутствующий
// размер — одно и то же утверждение о плане.
func LayQuantitiesStaleCheck(snapshot, current []LayQtyEntry) LayCheck {
	c := LayCheck{Key: LayCheckKeyQuantitiesStale, Label: "quantity snapshot against the run's lines"}
	was, now := normaliseLayQty(snapshot), normaliseLayQty(current)
	var diffs []string
	seen := make(map[int]bool, len(was)+len(now))
	sizes := make([]int, 0, len(was)+len(now))
	for _, m := range []map[int]int{was, now} {
		for sizeID := range m {
			if !seen[sizeID] {
				seen[sizeID] = true
				sizes = append(sizes, sizeID)
			}
		}
	}
	sort.Ints(sizes)
	for _, sizeID := range sizes {
		if was[sizeID] != now[sizeID] {
			diffs = append(diffs, fmt.Sprintf("size #%d: was %d, now %d", sizeID, was[sizeID], now[sizeID]))
		}
	}
	if len(diffs) == 0 {
		c.Status = LayCheckStatusOK
		return c
	}
	c.Status = LayCheckStatusWarning
	c.Detail = fmt.Sprintf("the run's quantities changed after the lay was built (%s) — rebuild the lay or confirm the quantities",
		strings.Join(diffs, "; "))
	return c
}

func normaliseLayQty(entries []LayQtyEntry) map[int]int {
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		if e.SizeId <= 0 {
			continue
		}
		out[e.SizeId] += e.Qty
	}
	for sizeID, qty := range out {
		if qty == 0 {
			delete(out, sizeID)
		}
	}
	return out
}

// LayOvercutCheck — `lay_overcut`. По каждой детали выкроено ровно столько, сколько нужно на
// `covered_qty` изделий ⇒ OK; больше ⇒ WARNING с числом и деталью; деталь UNKNOWN по §6.3 ⇒ UNKNOWN.
//
// ЭТО ПРЕДУПРЕЖДЕНИЕ, А НЕ ЗАПРЕТ, И ЭТО НЕ СМЯГЧЕНИЕ (§8.2). Настил лицом к лицу арифметически
// СОГЛАСОВАН, но детали, не входящие в зеркальные пары, режутся с перекроем, и величина перекроя —
// свойство состава деталей конкретного изделия, а не режима как такового. Нормативное правило «лицом
// к лицу требует таких-то деталей» было бы догадкой о цехе; счёт по размещениям с честным числом —
// измерением. Поэтому Ф4 СЧИТАЕТ и ПОКАЗЫВАЕТ, а решает человек.
//
// ЧИСЛО НЕ ЗАВИСИТ ОТ cut_symmetry, А ЗНАНИЕ — ЗАВИСИТ, и разделять их обязательно. Перекрой — это
// `выкроено всего − covered_qty × на изделие`, одна вычитание для всех трёх симметрий (сверьте с
// §8.2: карман identical n=2 при p слоях даёт ровно p в отход, спинка fold — p/2). Но ПРАВО считать
// даёт PieceYieldGarments: деталь, у которой `cut_symmetry` не размечено или чьи руки блоб не
// различает, не имеет определённого перекроя — у неё не определено даже, что из выкроенного годно.
func LayOvercutCheck(pieces []LayPieceCut, covered LayCoveredQty) LayCheck {
	c := LayCheck{Key: LayCheckKeyOvercut, Label: "overcut: more cut than needed"}
	if !covered.Known {
		c.Status = LayCheckStatusUnknown
		c.Detail = "the lay's coverage is not determined — there is nothing to compare the cut counts against"
		return c
	}
	if len(pieces) == 0 {
		c.Status = LayCheckStatusUnknown
		c.Detail = "no pieces to compare"
		return c
	}
	coveredQty := covered.Qty
	if coveredQty < 0 {
		coveredQty = 0
	}

	sorted := append([]LayPieceCut(nil), pieces...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].PieceLineKey < sorted[j].PieceLineKey })

	var statuses []LayCheckStatus
	var overs, unknowns, overKeys []string
	for _, p := range sorted {
		name := layCheckPieceLabel(p.PieceLineKey, p.PieceName)
		y := PieceYieldGarments(p.Cut, p.Symmetry, p.PiecesPerGarment, p.ChiralityKnown)
		if !y.Known {
			statuses = append(statuses, LayCheckStatusUnknown)
			unknowns = append(unknowns, fmt.Sprintf("%s: how much of the cut is usable is undetermined (cut symmetry is not set, or the hands are indistinguishable)", name))
			continue
		}
		need := coveredQty * p.PiecesPerGarment
		over := p.Cut.Total() - need
		if over > 0 {
			statuses = append(statuses, LayCheckStatusWarning)
			overs = append(overs, fmt.Sprintf("%s: %d cut, %d units need %d — overcut of %d",
				name, p.Cut.Total(), coveredQty, need, over))
			overKeys = append(overKeys, p.PieceLineKey)
			continue
		}
		statuses = append(statuses, LayCheckStatusOK)
	}

	c.Status = WorstLayCheckStatus(statuses...)
	switch c.Status {
	case LayCheckStatusOK:
	case LayCheckStatusWarning:
		c.Detail = strings.Join(overs, "; ")
		if len(overKeys) == 1 {
			c.PieceLineKey = overKeys[0]
		}
	default:
		c.Detail = strings.Join(append(append([]string{}, overs...), unknowns...), "; ")
	}
	return c
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// Сборки.
// ─────────────────────────────────────────────────────────────────────────────────────────────────

// ProductionLayChecks runs the findings that are about the НАСТИЛ as a whole, in the order of §8's
// table. Order is stable so a client can diff two reads of one настил without sorting.
func ProductionLayChecks(in LayCheckInput) []LayCheck {
	return []LayCheck{
		LayModeParityCheck(in.Mode, in.Sections),
		LayDirectionModeCheck(in.Mode, in.Lay.BomLineKey, in.BomLines),
		LayStackHeightCheck(in.TotalPlies(), in.Article.FabricThicknessMm, in.Limits.MaxStackHeightCm),
		LayTableLengthCheck(in.Sections, in.Limits.CuttingTableLengthCm),
		LaySlotDetachedCheck(in.Lay),
		LayQuantitiesStaleCheck(in.QtySnapshot, in.QtyCurrent),
		LayOvercutCheck(in.PieceCuts, in.Covered),
	}
}

// ProductionLaySectionChecks runs the findings that are about ONE section's marker.
//
// lay_marker_scope is here — i.e. ON THE READ PATH — and not only inside ValidateLayForSave. §14 п.6:
// a marker loses its slot to somebody else's edit of the BOM, without any write to this настил, and a
// настил that has become unfit must stop looking fit on the very next read.
func ProductionLaySectionChecks(in LayCheckInput, section LayCheckSection) []LayCheck {
	return []LayCheck{
		LayMarkerScopeCheck(in.Lay, section.Marker),
		LayMarkerWidthCheck(section.Marker, in.Article),
		LayLotWidthCheck(section.Marker, in.Lot, in.Article),
		LayMirrorExpansionCheck(in.Mode, section.Marker, in.PieceSymmetry),
	}
}

// ErrLayMarkerScope is a section naming a marker that does not belong to this настил. Paired with
// ErrLayModeParity (production_lay_yield.go) as the two refusals SaveLay owes; both are exported so
// the handler can map them to a status code without matching on prose.
var ErrLayMarkerScope = errors.New("lay section names a marker that does not belong to this lay")

// ValidateLayForSave is the WRITE-PATH subset of §8.3 — the checks that must REFUSE rather than
// report: `lay_marker_scope` and `lay_mode_parity`. Everything else is computed on read and returned
// as findings, because it can go bad without a write and must therefore not be enforced by one.
//
// Приёмочная §13 п.11 целиком здесь: FACE_TO_FACE с plies = 7 не сохраняется, а сохранённый настил с
// чётными слоями, переведённый в FACE_TO_FACE, сохраняется, если ВСЕ секции чётные, и отказывает
// иначе — the mode is a property of the настил, so the parity of EVERY section is re-asked on every
// save, not only of the sections this request happens to touch.
//
// Ошибки СОЕДИНЯЮТСЯ, а не возвращается первая: an operator fixing a настил section by section would
// otherwise pay one round trip per fault, and the whole list is already computed.
func ValidateLayForSave(in LayCheckInput) error {
	var errs []error
	if c := LayModeParityCheck(in.Mode, in.Sections); c.Status.Blocks() {
		errs = append(errs, fmt.Errorf("%w: %s", ErrLayModeParity, c.Detail))
	}
	for _, s := range in.Sections {
		if c := LayMarkerScopeCheck(in.Lay, s.Marker); c.Status.Blocks() {
			errs = append(errs, fmt.Errorf("%w: %s: %s", ErrLayMarkerScope, s.describe(), c.Detail))
		}
	}
	return errors.Join(errs...)
}

// Pb renders one finding for the wire.
func (c LayCheck) Pb() *pb_common.ProductionLayCheck {
	return &pb_common.ProductionLayCheck{
		Key:          c.Key,
		Status:       c.Status.Pb(),
		Label:        c.Label,
		Detail:       c.Detail,
		MarkerId:     int32(c.MarkerId),
		PieceLineKey: c.PieceLineKey,
	}
}

// LayChecksPb renders a list of findings, preserving order — the order is part of the contract
// (ProductionLayChecks / ProductionLaySectionChecks return §8's table in §8's sequence).
func LayChecksPb(checks []LayCheck) []*pb_common.ProductionLayCheck {
	out := make([]*pb_common.ProductionLayCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.Pb())
	}
	return out
}

// layCheckPieceLabel names a piece for a message: the name an operator scans the карточка for, falling back
// to the line_key that at least deep-links. Same reasoning as FabricDirectionLine.label.
func layCheckPieceLabel(lineKey, name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return fmt.Sprintf("piece %q", n)
	}
	return fmt.Sprintf("piece %s", nameOrDash(lineKey))
}

func nameOrDash(s string) string {
	if t := strings.TrimSpace(s); t != "" {
		return t
	}
	return "—"
}
