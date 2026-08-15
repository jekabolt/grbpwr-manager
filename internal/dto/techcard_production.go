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

// CostingFx carries the manual FX rates used to fold a tech card's multi-currency costing into
// the base currency for the *_base rollup. ToBase maps an UPPERCASE ISO currency to how many
// base-currency units one unit is worth; the base currency itself is implicitly 1. A zero
// value (Base == "") means "no base configured" — the *_base fields are then left unset.
type CostingFx struct {
	ToBase map[string]decimal.Decimal
	Base   string
	// HouseTargetMarginPct is the house gross-margin target (alert_setting `target_margin_pct`), used
	// when a style names no target of its own. It rides along with the FX rates because it is the same
	// shape of thing: a global costing constant every tech-card read needs and no caller should have
	// to fetch separately. Invalid = no house default configured.
	HouseTargetMarginPct decimal.NullDecimal
	// VatCountry / VatRatePct are the market a margin on this read is being drawn for. Catalogue
	// prices are VAT-inclusive everywhere else in this system (the order snapshot, the accounting VAT
	// engine and the margin-by-style report all extract VAT out of them), so a costing margin that
	// compares them to a VAT-free unit cost overstates itself by the rate. These net them.
	// VatRatePct invalid = no rate on file for the country: nothing to net, and net_prices stays empty
	// rather than echoing the gross price back under a different name.
	VatCountry string
	VatRatePct decimal.NullDecimal
}

// netOfVat removes the VAT contained in a VAT-inclusive gross amount: net = gross × 100/(100+rate).
// Mirrors internal/store/metrics.netOfVat, which the realised-sales margin uses — the two figures
// must be derived identically or the two admin screens disagree again in the other direction.
func (fx CostingFx) netOfVat(gross decimal.Decimal) (decimal.Decimal, bool) {
	if !fx.VatRatePct.Valid || !fx.VatRatePct.Decimal.IsPositive() {
		return decimal.Zero, false
	}
	hundred := decimal.NewFromInt(100)
	return gross.Mul(hundred).Div(hundred.Add(fx.VatRatePct.Decimal)), true
}

// toBase converts amount from ccy into the base currency. An empty ccy is treated as the base
// currency (amounts with no currency are assumed already-base). Returns ok=false when no base
// is configured or the currency has no rate — the caller then leaves the base figure unset.
func (fx CostingFx) toBase(amount decimal.Decimal, ccy string) (decimal.Decimal, bool) {
	if fx.Base == "" {
		return decimal.Zero, false
	}
	if ccy == "" || strings.EqualFold(ccy, fx.Base) {
		return amount, true
	}
	r, ok := fx.ToBase[strings.ToUpper(ccy)]
	if !ok {
		return decimal.Zero, false
	}
	return amount.Mul(r), true
}

// Decimal bounds for the Phase 3 production/costing columns.
const (
	maxVarchar128 = 128

	costMaxFrac = 2 // cost articles DECIMAL(12,2)
	costLimit   = 10_000_000_000
	// weightGramsLimit caps packaging weight (INT grams). Generous — 1 tonne — so real parcels
	// (well above 750 g) are never rejected.
	weightGramsLimit = 1_000_000
)

var techCardLabelTypePbToEntity = map[pb_common.TechCardLabelType]entity.TechCardLabelType{
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_MAIN:    entity.LabelTypeMain,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_SIZE:    entity.LabelTypeSize,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_CARE:    entity.LabelTypeCare,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_ORIGIN:  entity.LabelTypeOrigin,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_FLAG:    entity.LabelTypeFlag,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_HANGTAG: entity.LabelTypeHangtag,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_BARCODE: entity.LabelTypeBarcode,
	pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_SPECIAL: entity.LabelTypeSpecial,
}

var techCardLabelTypeEntityToPb = func() map[entity.TechCardLabelType]pb_common.TechCardLabelType {
	m := make(map[entity.TechCardLabelType]pb_common.TechCardLabelType, len(techCardLabelTypePbToEntity))
	for k, v := range techCardLabelTypePbToEntity {
		m[v] = k
	}
	return m
}()

// --- parse pb -> entity ---

func parseTechCardConstruction(pb *pb_common.TechCardConstruction) (*entity.TechCardConstruction, error) {
	if pb == nil {
		return nil, nil
	}
	if len(pb.HemFinish) > maxVarchar255 {
		return nil, fmt.Errorf("construction hem_finish must be at most %d characters", maxVarchar255)
	}
	seamClass, err := parseSeamClass(pb.DefaultSeamClass, "construction default_seam_class")
	if err != nil {
		return nil, err
	}
	density, err := nullDecimalFromPb(pb.DefaultStitchesPerCm)
	if err != nil {
		return nil, fmt.Errorf("construction default_stitches_per_cm: %w", err)
	}
	if err := entity.ValidateStitchesPerCm("construction default_stitches_per_cm", density); err != nil {
		return nil, err
	}
	// The card's machine / ВТО park. nil wrapper -> nil here, and the store then preserves what is
	// stored; see parseTechCardEquipmentDefaults for why the presence lives in a wrapper.
	//
	// `pressing` (prose) and `overlock_thread_count` are NOT read and are not forgotten: both wire
	// fields are reserved and both columns are gone (0306). The thread count is a machine PROFILE
	// here — with its 1..20 range instead of the 3..5 this parser used to police, because the field
	// now describes any machine's threading and not only an overlock's.
	equipment, err := parseTechCardEquipmentDefaults(pb.EquipmentDefaults)
	if err != nil {
		return nil, err
	}
	return &entity.TechCardConstruction{
		HemFinish:            nullStringFromPb(pb.HemFinish),
		Notes:                nullStringFromPb(pb.Notes),
		DefaultSeamClass:     seamClass,
		DefaultStitchesPerCm: density,
		EquipmentDefaults:    equipment,
	}, nil
}

// The operation-type vocabulary AS IT IS STORED: the six choosable verbs plus "unknown". Derived
// from entity.OperationTypeTokens, so a verb added to the vocabulary without a proto member panics
// at init instead of failing on the one value nobody tested.
var operationTypePbToToken = enumTokenMap[pb_common.TechCardOperationType]("TECH_CARD_OPERATION_TYPE_", entity.OperationTypeTokens, pb_common.TechCardOperationType_value)
var operationTypeTokenToPb = invertTokenMap(operationTypePbToToken)

// legacyOperationTypePbToEntity is the NINE legacy wire values — the ones that answered «what» and
// «on what» with a single word. They are still accepted (a bundle that predates the split keeps
// working, and a release snapshot is protojson holding exactly these names, forever), and they are
// canonicalised into (machine, machine_type) BEFORE an entity exists. They are never emitted.
//
// Derived from entity.LegacyOperationMachineType by name, not typed out again: that map is the one
// canonicalisation table, shared with migration 0306 and the digest's compat projection.
var legacyOperationTypePbToEntity = func() map[pb_common.TechCardOperationType]entity.TechCardOperationType {
	m := make(map[pb_common.TechCardOperationType]entity.TechCardOperationType, len(entity.LegacyOperationMachineType))
	for legacy := range entity.LegacyOperationMachineType {
		name := "TECH_CARD_OPERATION_TYPE_" + strings.ToUpper(string(legacy))
		v, ok := pb_common.TechCardOperationType_value[name]
		if !ok {
			panic("legacy operation type without a proto enum value: " + string(legacy))
		}
		m[pb_common.TechCardOperationType(v)] = legacy
	}
	return m
}()

// The zone map is BUILT FROM entity.GarmentZoneTokens rather than written out, so a token added to
// the vocabulary cannot be silently absent here — the proto enum name is derived from the token by
// the same rule for all eighteen. A hand-written map is exactly where the fitting/operation copies
// would drift apart again.
var techCardGarmentZonePbToEntity = func() map[pb_common.TechCardGarmentZone]entity.TechCardGarmentZone {
	m := make(map[pb_common.TechCardGarmentZone]entity.TechCardGarmentZone, len(entity.GarmentZoneTokens))
	for _, tok := range entity.GarmentZoneTokens {
		if tok == string(entity.ZoneUnknown) {
			continue
		}
		name := "TECH_CARD_GARMENT_ZONE_" + strings.ToUpper(tok)
		v, ok := pb_common.TechCardGarmentZone_value[name]
		if !ok {
			panic("garment zone token without a proto enum value: " + tok)
		}
		m[pb_common.TechCardGarmentZone(v)] = entity.TechCardGarmentZone(tok)
	}
	return m
}()

var techCardGarmentZoneEntityToPb = func() map[entity.TechCardGarmentZone]pb_common.TechCardGarmentZone {
	m := make(map[entity.TechCardGarmentZone]pb_common.TechCardGarmentZone, len(techCardGarmentZonePbToEntity))
	for k, v := range techCardGarmentZonePbToEntity {
		m[v] = k
	}
	return m
}()

// The same derivation for the three dictionaries the break introduced. Each maps proto enum ->
// storage token by name, and each panics at init on a mismatch: a dictionary half-added is worse
// than one not added, because it fails on the one value nobody tested.
func enumTokenMap[E ~int32](prefix string, tokens []string, values map[string]int32) map[E]string {
	m := make(map[E]string, len(tokens))
	for _, tok := range tokens {
		v, ok := values[prefix+strings.ToUpper(tok)]
		if !ok {
			panic("token without a proto enum value: " + prefix + tok)
		}
		m[E(v)] = tok
	}
	return m
}

var seamClassPbToToken = enumTokenMap[pb_common.TechCardSeamClass]("TECH_CARD_SEAM_CLASS_", entity.SeamClassTokens, pb_common.TechCardSeamClass_value)
var seamClassTokenToPb = invertTokenMap(seamClassPbToToken)

var attachmentKindPbToToken = enumTokenMap[pb_common.TechCardAttachmentKind]("TECH_CARD_ATTACHMENT_KIND_", entity.AttachmentKindTokens, pb_common.TechCardAttachmentKind_value)
var attachmentKindTokenToPb = invertTokenMap(attachmentKindPbToToken)

var topstitchModePbToToken = enumTokenMap[pb_common.TechCardTopstitchMode]("TECH_CARD_TOPSTITCH_MODE_", entity.TopstitchModeTokens, pb_common.TechCardTopstitchMode_value)
var topstitchModeTokenToPb = invertTokenMap(topstitchModePbToToken)

func invertTokenMap[E comparable](m map[E]string) map[string]E {
	out := make(map[string]E, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// parseSeamClass turns the wire enum into the storage token. UNKNOWN is not an error: it means
// «inherit the card default», and the row stores NULL so the inherited value is never mistaken for
// a decision somebody made.
func parseSeamClass(v pb_common.TechCardSeamClass, field string) (sql.NullString, error) {
	if v == pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN {
		return sql.NullString{}, nil
	}
	tok, ok := seamClassPbToToken[v]
	if !ok {
		return sql.NullString{}, entity.NewFieldViolation(field, "unknown_value", v.String(),
			"pick a seam class from the list")
	}
	return sql.NullString{String: tok, Valid: true}, nil
}

// parseTechCardOperations validates and converts the assembly order. calloutNumbers is the set of
// TechCardCallout.number values in the same payload, so an operation's callout_number can be
// range-checked.
//
// TWO FIELDS ARE REQUIRED AND BOTH ARE CLOSED LISTS — operation_type and zone. That pairing is the
// lesson of the removed `node`: a mandatory field with free input has no right answer, so the
// operator invents one and two cards say the same thing differently. Everything else is optional,
// and unset means «inherit the card default», never «zero».
//
// park is the equipment park of the SAME payload (nil when the payload carried no wrapper), used to
// resolve a step's profile reference; aware says whether the client that sent this payload knows
// about the machine / ВТО fields at all — the two «required» rules below hold only for a client
// that could have filled them in, because a bundle that predates the split must keep saving exactly
// as it did.
func parseTechCardOperations(pbs []*pb_common.TechCardOperation, calloutNumbers map[int]bool, bomItemCount int, park *equipmentPark, aware bool) ([]entity.TechCardOperation, error) {
	_ = bomItemCount // the positional bom_item_index went with the break; the line keys are the reference
	out := make([]entity.TechCardOperation, 0, len(pbs))
	for i, o := range pbs {
		step := fmt.Sprintf("operations[%d]", i)
		if o.OperationType == pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN {
			return nil, entity.NewFieldViolation(step+".operation_type", "required", "",
				"pick what the step DOES (join, overlock, topstitch, …) — it names the step and drives its defaults")
		}
		// CANONICALISATION HAPPENS HERE, before any entity exists — and therefore before any section
		// digest is stamped off that entity (StampTechCardSignoffDigests runs at the end of the
		// conversion). Doing it later would hash the raw legacy token into a signature and then read
		// back the canonical one, and every card approved by an old bundle would read «edited since
		// sign-off» from the moment it was signed.
		opType, machineType, err := canonicaliseOperationType(o, step)
		if err != nil {
			return nil, err
		}
		if o.Zone == pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_UNKNOWN {
			return nil, entity.NewFieldViolation(step+".zone", "required", "",
				"pick WHERE on the garment the step works — the step heading is built from it, and «other» is a legitimate answer")
		}
		zone, ok := techCardGarmentZonePbToEntity[o.Zone]
		if !ok {
			return nil, entity.NewFieldViolation(step+".zone", "unknown_value", o.Zone.String(), "pick a zone from the list")
		}
		if o.CalloutNumber < 0 {
			return nil, entity.NewFieldViolation(step+".callout_number", "must_not_be_negative", fmt.Sprint(o.CalloutNumber), "clear the pin instead")
		}
		calloutNumber := nullInt32FromPb(o.CalloutNumber)
		if o.CalloutNumber > 0 && !calloutNumbers[int(o.CalloutNumber)] {
			// S8 parity with pieces (callout-sync rules): a reference to a callout that no longer
			// exists DETACHES — the link is cleared, the operation survives — instead of vetoing the
			// save. Sketch cleanup (K.31) must not be blocked by a stale display cross-ref.
			calloutNumber = sql.NullInt32{}
		}
		stitches, err := nullDecimalFromPb(o.StitchesPerCm)
		if err != nil {
			return nil, fmt.Errorf("%s.stitches_per_cm: %w", step, err)
		}
		// Same band as the card default it overrides. The step's column predates the break and its
		// CHECK is only `>= 0` (0070), so the schema would accept a zero here that the card default
		// refuses — the override and the thing it overrides have to answer the same question the same
		// way, and Go is the only layer where both are in view.
		if err := entity.ValidateStitchesPerCm(step+".stitches_per_cm", stitches); err != nil {
			return nil, err
		}
		smv, err := nullDecimalFromPb(o.Smv)
		if err != nil {
			return nil, fmt.Errorf("%s.smv: %w", step, err)
		}
		if err := validateDecimalScale(smv, step+".smv", 3, 10_000); err != nil {
			return nil, err
		}
		seamClass, err := parseSeamClass(o.SeamClass, step+".seam_class")
		if err != nil {
			return nil, err
		}
		// The step's allowance override. UNSET = inherit the card standard; ZERO IS A REAL VALUE
		// («the выкройки carry the cut line»), which is why presence and value are read separately
		// here exactly as they are on the card and in the workshop settings.
		var seamAllowanceMm decimal.NullDecimal
		if o.SeamAllowanceMm != nil {
			seamAllowanceMm, err = nullDecimalFromPb(o.SeamAllowanceMm)
			if err != nil {
				return nil, fmt.Errorf("%s.seam_allowance_mm: %w", step, err)
			}
			if err := entity.ValidateSeamAllowanceStandardMm(step+".seam_allowance_mm", seamAllowanceMm); err != nil {
				return nil, err
			}
		}
		topMode, topWidth, topRows, err := parseTopstitch(o.Topstitch, step)
		if err != nil {
			return nil, err
		}
		// UNKNOWN -> NULL («inherit the profile's foot»), NONE -> 'none' («this step runs bare»).
		// The two used to be one value here; see parseEquipmentEnum for why they had to come apart
		// the day something sat above the step to inherit from.
		attachKind, err := parseEquipmentEnum(o.AttachmentKind, attachmentKindPbToToken,
			step+".attachment_kind", "pick an attachment from the list")
		if err != nil {
			return nil, err
		}
		var attachSize decimal.NullDecimal
		if o.AttachmentSizeMm != nil {
			attachSize, err = nullDecimalFromPb(o.AttachmentSizeMm)
			if err != nil {
				return nil, fmt.Errorf("%s.attachment_size_mm: %w", step, err)
			}
			if err := validateDecimalScale(attachSize, step+".attachment_size_mm", 1, entity.MaxSeamAllowanceMm); err != nil {
				return nil, err
			}
			// A size with no attachment is a number describing nothing — and it prints on the sheet
			// next to a blank tool. 'none' counts as no attachment for exactly the same reason: a
			// binder size next to «runs bare» measures a tool the step just said it does not use.
			if attachSize.Valid && (!attachKind.Valid || attachKind.String == attachmentKindNone) {
				return nil, entity.NewFieldViolation(step+".attachment_size_mm", "needs_attachment_kind", attachSize.Decimal.String(),
					"pick the attachment first — a size on its own describes no tool")
			}
		}
		// --- «на чём»: the machine and ВТО blocks ---------------------------------------------------
		machine, press, err := parseOperationEquipment(o, opType, machineType, park, aware, step)
		if err != nil {
			return nil, err
		}
		// piece_line_keys (WS4): the cut-pieces this operation works on. Repeated because an
		// assembly operation spans as many pieces as it joins. Blanks are dropped and duplicates
		// collapsed here so the store's join-table write can stay a straight insert -- the table's
		// UNIQUE(operation_id, piece_id) would otherwise turn an accidental repeat into a 500.
		var pieceLineKeys []string
		seenPieceKey := make(map[string]bool, len(o.PieceLineKeys))
		for _, k := range o.PieceLineKeys {
			k = strings.TrimSpace(k)
			if k == "" || seenPieceKey[k] {
				continue
			}
			seenPieceKey[k] = true
			pieceLineKeys = append(pieceLineKeys, k)
		}
		// --- сборка (0307): что шаг берёт со стола и что производит ---------------------------
		// Здесь только СЫРОЙ разбор: классифицировать ключ («деталь или узел») на этом месте
		// физически нельзя — детали карточки разбираются ПОЗЖЕ операций, множества ещё нет.
		// Классификация и все правила графа живут одним пост-проходом в конвертере
		// (canonicalizeAssembly), после обоих разборов и до штампа подписей.
		//
		// Поля разбираются ВСЕГДА, независимо от aware: флаг объявляет способность бандла, а не
		// выключает разбор. Иначе серверный round-trip (клон сезона payload строит сам и флага
		// не несёт) молча вернул бы карточку без разметки.
		var inputKeys []string
		for _, k := range o.InputKeys {
			// Только trim и отбрасывание пустых. Дубли НЕ схлопываются, в отличие от легаси-поля
			// ниже: для объединения повтор — это нарушение правила 7, о котором технолог обязан
			// узнать, а не молча исправленная опечатка.
			if k = strings.TrimSpace(k); k != "" {
				inputKeys = append(inputKeys, k)
			}
		}
		outputUnitKey := strings.TrimSpace(o.OutputUnitKey)
		outputUnitName := strings.TrimSpace(o.OutputUnitName)

		// bom_line_keys: the materials this operation consumes. The legacy single bom_line_key went
		// with the break — the chip row was always the real answer, and the single field was a second
		// one that the printed sheet then had to subtract to stop printing it twice.
		var bomLineKeys []string
		seenBomKey := make(map[string]bool, len(o.BomLineKeys))
		for _, k := range o.BomLineKeys {
			k = strings.TrimSpace(k)
			if k == "" || seenBomKey[k] {
				continue
			}
			seenBomKey[k] = true
			bomLineKeys = append(bomLineKeys, k)
		}
		out = append(out, entity.TechCardOperation{
			// operation_number is server-assigned = (position+1)*10 («оп. 10, 20, …»);
			// any client value is ignored (plan §4). Reorder shifts numbers (Q6).
			OperationNumber:  sql.NullInt32{Int32: int32((i + 1) * 10), Valid: true},
			OperationType:    opType,
			Zone:             zone,
			StitchesPerCm:    stitches,
			SeamClass:        seamClass,
			SeamAllowanceMm:  seamAllowanceMm,
			TopstitchMode:    topMode,
			TopstitchWidthMm: topWidth,
			TopstitchRows:    topRows,
			AttachmentKind:   attachKind,
			AttachmentSizeMm: attachSize,
			SMV:              smv,
			Note:             nullStringFromPb(o.Note),
			CalloutNumber:    calloutNumber,
			PieceLineKeys:    pieceLineKeys,
			BomLineKeys:      bomLineKeys,
			InputKeys:        inputKeys,
			OutputUnitKey:    nullStringFromPb(outputUnitKey),
			OutputUnitName:   nullStringFromPb(outputUnitName),

			MachineType:       machine.machineType,
			MachineProfileKey: machine.profileKey,
			ThreadCount:       machine.threadCount,
			NeedleType:        machine.needleType,
			NeedleSizeNm:      machine.needleSizeNm,
			ThreadTension:     machine.threadTension,
			ThreadTensionNote: machine.threadTensionNote,
			StitchWidthMm:     machine.stitchWidthMm,

			PressEquipment:    press.equipment,
			PressProfileKey:   press.profileKey,
			PressTemperatureC: press.temperatureC,
			PressDwellSec:     press.dwellSec,
			PressPressureNCm2: press.pressureNCm2,
			PressSteam:        press.steam,
			PressCloth:        press.cloth,
		})
	}
	return out, nil
}

// parseTopstitch splits the sub-message into its three columns and enforces the one rule that makes
// the mode mean anything: a width belongs to WIDTH and nowhere else. Carrying «6 mm» alongside
// «edge» would leave a shadow value that nothing reads and the next editor believes.
// The violation paths here are FLAT (`topstitch_width_mm`), not the nested wire path
// (`topstitch.width_mm`), and that is deliberate: a field violation exists to point the admin at the
// CONTROL the operator must fix, and the admin routes it by camel-casing the path onto a form field.
// The form holds these three flat, so a nested path would map to `topstitch.widthMm`, match nothing,
// and demote a precise message into an unattributable toast — see field-errors.ts, which documents
// exactly this divergence between wire name and form name.
func parseTopstitch(t *pb_common.TechCardTopstitch, step string) (sql.NullString, decimal.NullDecimal, sql.NullInt32, error) {
	var mode sql.NullString
	var width decimal.NullDecimal
	var rows sql.NullInt32
	if t == nil || t.Mode == pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN {
		return mode, width, rows, nil
	}
	tok, ok := topstitchModePbToToken[t.Mode]
	if !ok {
		return mode, width, rows, entity.NewFieldViolation(step+".topstitch_mode", "unknown_value", t.Mode.String(), "pick edge or width")
	}
	mode = sql.NullString{String: tok, Valid: true}
	w, err := nullDecimalFromPb(t.WidthMm)
	if err != nil {
		return mode, width, rows, fmt.Errorf("%s.topstitch.width_mm: %w", step, err)
	}
	isWidth := t.Mode == pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_WIDTH
	switch {
	case isWidth && !w.Valid:
		return mode, width, rows, entity.NewFieldViolation(step+".topstitch_width_mm", "required", "",
			"a topstitch at a stated width needs the width; pick «edge» if it runs along the fold instead")
	case !isWidth && w.Valid:
		return mode, width, rows, entity.NewFieldViolation(step+".topstitch_width_mm", "not_applicable", w.Decimal.String(),
			"«edge» topstitching has no width — clear it, or switch the mode to width")
	}
	if err := validateDecimalScale(w, step+".topstitch_width_mm", 1, entity.MaxSeamAllowanceMm); err != nil {
		return mode, width, rows, err
	}
	width = w
	if t.Rows != 0 {
		if t.Rows < 1 || t.Rows > entity.MaxTopstitchRows {
			return mode, width, rows, entity.NewFieldViolation(step+".topstitch_rows", "out_of_range", fmt.Sprint(t.Rows),
				fmt.Sprintf("1 to %d rows; send 0 to leave it unset", entity.MaxTopstitchRows))
		}
		rows = sql.NullInt32{Int32: t.Rows, Valid: true}
	}
	return mode, width, rows, nil
}

func parseTechCardLabels(pbs []*pb_common.TechCardLabel) ([]entity.TechCardLabel, error) {
	out := make([]entity.TechCardLabel, 0, len(pbs))
	for _, l := range pbs {
		lt, ok := techCardLabelTypePbToEntity[l.LabelType]
		if !ok {
			return nil, fmt.Errorf("label label_type is required and must be valid")
		}
		if len(l.Content) > maxVarchar255 || len(l.Placement) > maxVarchar255 || len(l.Attachment) > maxVarchar255 {
			return nil, fmt.Errorf("label content/placement/attachment must be at most %d characters", maxVarchar255)
		}
		if len(l.Size) > maxVarchar64 {
			return nil, fmt.Errorf("label size must be at most %d characters", maxVarchar64)
		}
		out = append(out, entity.TechCardLabel{
			LabelType:  lt,
			Content:    nullStringFromPb(l.Content),
			Placement:  nullStringFromPb(l.Placement),
			Attachment: nullStringFromPb(l.Attachment),
			Size:       nullStringFromPb(l.Size),
			Note:       nullStringFromPb(l.Note),
			BomItemId:  nullInt32FromPb(l.BomItemId), // §2.8 link to the physical label material's BOM line
		})
	}
	return out, nil
}

func parseTechCardPackaging(pb *pb_common.TechCardPackaging) (*entity.TechCardPackaging, error) {
	if pb == nil {
		return nil, nil
	}
	for _, c := range []struct {
		field string
		val   string
		max   int
	}{
		{"packaging folding_method", pb.FoldingMethod, maxVarchar255},
		{"packaging polybag", pb.Polybag, maxVarchar255},
		{"packaging bag_sticker", pb.BagSticker, maxVarchar255},
		{"packaging inserts", pb.Inserts, maxVarchar255},
		{"packaging box_marking", pb.BoxMarking, maxVarchar255},
		{"packaging box_dimensions", pb.BoxDimensions, maxVarchar128},
	} {
		if len(c.val) > c.max {
			return nil, fmt.Errorf("%s must be at most %d characters", c.field, c.max)
		}
	}
	if pb.UnitsPerBox < 0 {
		return nil, fmt.Errorf("packaging units_per_box must not be negative")
	}
	if pb.WeightNetGrams < 0 || pb.WeightGrossGrams < 0 {
		return nil, fmt.Errorf("packaging weight must not be negative")
	}
	if pb.WeightNetGrams > weightGramsLimit || pb.WeightGrossGrams > weightGramsLimit {
		return nil, fmt.Errorf("packaging weight exceeds max %d grams", weightGramsLimit)
	}
	return &entity.TechCardPackaging{
		FoldingMethod:    nullStringFromPb(pb.FoldingMethod),
		Polybag:          nullStringFromPb(pb.Polybag),
		BagSticker:       nullStringFromPb(pb.BagSticker),
		Inserts:          nullStringFromPb(pb.Inserts),
		UnitsPerBox:      nullInt32FromPb(pb.UnitsPerBox),
		BoxMarking:       nullStringFromPb(pb.BoxMarking),
		BoxDimensions:    nullStringFromPb(pb.BoxDimensions),
		WeightNetGrams:   nullInt32FromPb(pb.WeightNetGrams),
		WeightGrossGrams: nullInt32FromPb(pb.WeightGrossGrams),
		Notes:            nullStringFromPb(pb.Notes),
	}, nil
}

func parseTechCardCosting(pb *pb_common.TechCardCosting) (*entity.TechCardCosting, error) {
	if pb == nil {
		return nil, nil
	}
	if pb.Currency != "" && !IsExpenseCurrency(pb.Currency) {
		return nil, fmt.Errorf("costing currency must be a supported currency or USDT")
	}
	cost := func(d *pb_decimal.Decimal, field string) (decimal.NullDecimal, error) {
		nd, err := nullDecimalFromPb(d)
		if err != nil {
			return nd, fmt.Errorf("costing %s: %w", field, err)
		}
		return nd, validateDecimalScale(nd, "costing "+field, costMaxFrac, costLimit)
	}
	cmt, err := cost(pb.CmtCost, "cmt_cost")
	if err != nil {
		return nil, err
	}
	logistics, err := cost(pb.LogisticsCost, "logistics_cost")
	if err != nil {
		return nil, err
	}
	overhead, err := cost(pb.OverheadCost, "overhead_cost")
	if err != nil {
		return nil, err
	}
	if pb.Currency == "" && (cmt.Valid || logistics.Valid || overhead.Valid) {
		return nil, entity.NewFieldViolation("costing.currency",
			"currency is required when a monetary costing amount is set", "",
			"select the currency used by these costing amounts")
	}
	defect, err := nullDecimalFromPb(pb.DefectPercent)
	if err != nil {
		return nil, fmt.Errorf("costing defect_percent: %w", err)
	}
	if err := validateDecimalScale(defect, "costing defect_percent", costMaxFrac, 1_000); err != nil {
		return nil, err
	}
	if defect.Valid && defect.Decimal.GreaterThan(decimal.NewFromInt(100)) {
		return nil, fmt.Errorf("costing defect_percent must be between 0 and 100")
	}
	targetMargin, err := nullDecimalFromPb(pb.TargetMarginPct)
	if err != nil {
		return nil, fmt.Errorf("costing target_margin_pct: %w", err)
	}
	if err := validateDecimalScale(targetMargin, "costing target_margin_pct", costMaxFrac, 1_000); err != nil {
		return nil, err
	}
	if targetMargin.Valid && (targetMargin.Decimal.IsNegative() || targetMargin.Decimal.GreaterThan(decimal.NewFromInt(100))) {
		return nil, fmt.Errorf("costing target_margin_pct must be between 0 and 100")
	}
	// 0 is "no style target" rather than "target a 0% margin" — nobody sets the latter, and treating
	// it as a real target would silently switch the house default off for any client that sends a
	// zero-valued decimal instead of omitting the field.
	if targetMargin.Valid && targetMargin.Decimal.IsZero() {
		targetMargin = decimal.NullDecimal{}
	}
	return &entity.TechCardCosting{
		CmtCost:         cmt,
		LogisticsCost:   logistics,
		OverheadCost:    overhead,
		DefectPercent:   defect,
		Currency:        nullStringFromPb(pb.Currency),
		Notes:           nullStringFromPb(pb.Notes),
		TargetMarginPct: targetMargin,
	}, nil
}

// --- emit entity -> pb ---

func techCardConstructionToPb(c *entity.TechCardConstruction) *pb_common.TechCardConstruction {
	if c == nil {
		return nil
	}
	return &pb_common.TechCardConstruction{
		HemFinish:            pbStringFromNull(c.HemFinish),
		Notes:                pbStringFromNull(c.Notes),
		DefaultSeamClass:     seamClassTokenToPb[c.DefaultSeamClass.String],
		DefaultStitchesPerCm: pbDecimalFromNull(c.DefaultStitchesPerCm),
		// ALWAYS non-nil — a read that omitted the wrapper would be re-read by the clone path as
		// «this payload did not speak about equipment», and the clone would lose the park.
		EquipmentDefaults: equipmentDefaultsToPb(c.EquipmentDefaults),
	}
}

func techCardOperationsToPb(ops []entity.TechCardOperation) []*pb_common.TechCardOperation {
	out := make([]*pb_common.TechCardOperation, 0, len(ops))
	for i := range ops {
		o := ops[i]
		pieceIds := make([]int64, 0, len(o.PieceIds))
		for _, id := range o.PieceIds {
			pieceIds = append(pieceIds, int64(id))
		}
		bomIds := make([]int64, 0, len(o.BomIds))
		for _, id := range o.BomIds {
			bomIds = append(bomIds, int64(id))
		}
		// The overrides go out with PRESENCE, not as zeros: an absent allowance means «inherit the
		// card standard» and a present 0 means «cut on the line as drawn». Emitting 0 for both would
		// hand the client the one confusion the whole cascade is built to avoid.
		var seamAllowanceMm *pb_decimal.Decimal
		if o.SeamAllowanceMm.Valid {
			seamAllowanceMm = pbDecimalFromNull(o.SeamAllowanceMm)
		}
		var attachmentSizeMm *pb_decimal.Decimal
		if o.AttachmentSizeMm.Valid {
			attachmentSizeMm = pbDecimalFromNull(o.AttachmentSizeMm)
		}
		var stitchWidthMm *pb_decimal.Decimal
		if o.StitchWidthMm.Valid {
			stitchWidthMm = pbDecimalFromNull(o.StitchWidthMm)
		}
		var pressPressure *pb_decimal.Decimal
		if o.PressPressureNCm2.Valid {
			pressPressure = pbDecimalFromNull(o.PressPressureNCm2)
		}
		opType, machineType := emitOperationType(o)
		out = append(out, &pb_common.TechCardOperation{
			OperationNumber:  pbInt32FromNull(o.OperationNumber),
			OperationType:    opType,
			Zone:             techCardGarmentZoneEntityToPb[o.Zone],
			StitchesPerCm:    pbDecimalFromNull(o.StitchesPerCm),
			SeamClass:        seamClassTokenToPb[o.SeamClass.String],
			SeamAllowanceMm:  seamAllowanceMm,
			Topstitch:        topstitchToPb(o),
			AttachmentKind:   attachmentKindTokenToPb[o.AttachmentKind.String],
			AttachmentSizeMm: attachmentSizeMm,
			Smv:              pbDecimalFromNull(o.SMV),
			Note:             pbStringFromNull(o.Note),
			CalloutNumber:    pbInt32FromNull(o.CalloutNumber),
			PieceLineKeys:    o.PieceLineKeys,
			PieceIds:         pieceIds,
			BomLineKeys:      o.BomLineKeys,
			BomItemIds:       bomIds,

			// The machine block. A NULL token maps to the enum's zero member (UNKNOWN) = «inherit»,
			// which is the same statement the column makes.
			MachineType:       machineType,
			MachineProfileKey: pbStringFromNull(o.MachineProfileKey),
			ThreadCount:       pbInt32FromNull(o.ThreadCount),
			NeedleType:        needleTypeTokenToPb[o.NeedleType.String],
			NeedleSizeNm:      pbInt32FromNull(o.NeedleSizeNm),
			ThreadTension:     threadTensionTokenToPb[o.ThreadTension.String],
			ThreadTensionNote: pbStringFromNull(o.ThreadTensionNote),
			StitchWidthMm:     stitchWidthMm,

			// The ВТО block. press_cloth 'none' goes out as NONE (an instruction), NULL as UNKNOWN
			// (inherit); press_steam goes out ABSENT when NULL, because false is «без пара» and a
			// two-valued field would turn that instruction back into a default.
			PressEquipment:    pressEquipmentTokenToPb[o.PressEquipment.String],
			PressProfileKey:   pbStringFromNull(o.PressProfileKey),
			PressTemperatureC: pbInt32FromNull(o.PressTemperatureC),
			PressDwellSec:     pbInt32FromNull(o.PressDwellSec),
			PressPressureNCm2: pressPressure,
			PressSteam:        pbOptionalBoolFromNull(o.PressSteam),
			PressCloth:        pressClothTokenToPb[o.PressCloth.String],

			// Сборка (0307). Эмитится ВСЕГДА и вместе с легаси-проекцией 21/22 выше — они не
			// заменяют друг друга: 21 остаётся «только детали» навсегда, 46 несёт объединение.
			//
			// Без этой эмиссии клон сезона молча стирал бы разметку: CloneStyleForSeason строит
			// payload именно здесь, и pb без 46-48 уходит в конвертер, где канонизация не видит
			// сборочных фактов и сохраняет карточку неразмеченной — без единой ошибки. Ровно та
			// катастрофа, ради которой флаг не фильтрует поля.
			InputKeys:      o.InputKeys,
			OutputUnitKey:  pbStringFromNull(o.OutputUnitKey),
			OutputUnitName: pbStringFromNull(o.OutputUnitName),
		})
	}
	return out
}

// emitOperationType emits the verb and the machine. ONLY the new values go out: the nine legacy
// members are accepted on the wire forever (snapshots) but never spoken, so a client has exactly one
// vocabulary to render.
//
// The legacy branch is a belt for a row this phase says cannot exist — migration 0306 rewrites every
// stored legacy token in the same deploy that ships this code. If one is ever read anyway (a rollback,
// a hand-written row), it goes out canonicalised rather than as UNKNOWN: a step that lost its type
// entirely is worse than one described in the new words, and it is the same canonicalisation the
// write path would apply the moment that card is saved.
func emitOperationType(o entity.TechCardOperation) (pb_common.TechCardOperationType, pb_common.TechCardMachineType) {
	machineType := machineTypeTokenToPb[o.MachineType.String]
	if machine, ok := entity.LegacyOperationMachineType[o.OperationType]; ok {
		if !o.MachineType.Valid {
			machineType = machineTypeTokenToPb[machine]
		}
		return pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE, machineType
	}
	return operationTypeTokenToPb[string(o.OperationType)], machineType
}

// topstitchToPb emits the sub-message only when there is topstitching at all — an always-present
// wrapper carrying MODE_UNKNOWN would read as «somebody considered it» on every step that has none.
func topstitchToPb(o entity.TechCardOperation) *pb_common.TechCardTopstitch {
	if !o.TopstitchMode.Valid {
		return nil
	}
	return &pb_common.TechCardTopstitch{
		Mode:    topstitchModeTokenToPb[o.TopstitchMode.String],
		WidthMm: pbDecimalFromNull(o.TopstitchWidthMm),
		Rows:    o.TopstitchRows.Int32,
	}
}

// --- issues (Phase 3.5b) ---

var techCardIssueSeverityPbToEntity = map[pb_common.TechCardIssueSeverity]entity.TechCardIssueSeverity{
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_LOW:    entity.IssueSeverityLow,
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_MEDIUM: entity.IssueSeverityMedium,
	pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_HIGH:   entity.IssueSeverityHigh,
}
var techCardIssueSeverityEntityToPb = func() map[entity.TechCardIssueSeverity]pb_common.TechCardIssueSeverity {
	m := make(map[entity.TechCardIssueSeverity]pb_common.TechCardIssueSeverity, len(techCardIssueSeverityPbToEntity))
	for k, v := range techCardIssueSeverityPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardIssueStatusPbToEntity = map[pb_common.TechCardIssueStatus]entity.TechCardIssueStatus{
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_OPEN:     entity.IssueStatusOpen,
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_RESOLVED: entity.IssueStatusResolved,
	pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_WONTFIX:  entity.IssueStatusWontfix,
}
var techCardIssueStatusEntityToPb = func() map[entity.TechCardIssueStatus]pb_common.TechCardIssueStatus {
	m := make(map[entity.TechCardIssueStatus]pb_common.TechCardIssueStatus, len(techCardIssueStatusPbToEntity))
	for k, v := range techCardIssueStatusPbToEntity {
		m[v] = k
	}
	return m
}()

func parseTechCardIssues(pbs []*pb_common.TechCardIssue, operationCount int, calloutNumbers map[int]bool) ([]entity.TechCardIssue, error) {
	out := make([]entity.TechCardIssue, 0, len(pbs))
	for issueIndex, i := range pbs {
		if i.Description == "" {
			return nil, fmt.Errorf("issue description is required")
		}
		operationField := fmt.Sprintf("issues[%d].operation_number", issueIndex)
		if i.OperationNumber < 0 {
			return nil, entity.NewFieldViolation(operationField, "must not be negative", "", "use 0 for no operation")
		}
		if i.OperationNumber > 0 {
			maxOperationNumber := operationCount * 10
			if i.OperationNumber%10 != 0 || int(i.OperationNumber) > maxOperationNumber {
				if operationCount == 0 {
					return nil, entity.NewFieldViolation(operationField,
						"must be 0 because the payload contains no operations", "", "add an operation or clear this reference")
				}
				return nil, entity.NewFieldViolation(operationField,
					fmt.Sprintf("must be 0 or an exact multiple of 10 in [10, %d]", maxOperationNumber), "",
					fmt.Sprintf("use one of the server-assigned operation numbers 10, 20, …, %d", maxOperationNumber))
			}
		}
		calloutField := fmt.Sprintf("issues[%d].callout_number", issueIndex)
		if i.CalloutNumber < 0 {
			return nil, entity.NewFieldViolation(calloutField, "must not be negative", "", "use 0 for no callout")
		}
		if i.CalloutNumber > 0 && !calloutNumbers[int(i.CalloutNumber)] {
			return nil, entity.NewFieldViolation(calloutField,
				"does not reference a callout in this payload", "",
				"use 0 for no callout, or reference an existing callout number")
		}
		if len(i.RaisedBy) > maxVarchar255 {
			return nil, fmt.Errorf("issue raised_by must be at most %d characters", maxVarchar255)
		}
		severity := entity.IssueSeverityMedium
		if i.Severity != pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_UNKNOWN {
			s, ok := techCardIssueSeverityPbToEntity[i.Severity]
			if !ok {
				return nil, fmt.Errorf("unknown issue severity: %v", i.Severity)
			}
			severity = s
		}
		st := entity.IssueStatusOpen
		if i.Status != pb_common.TechCardIssueStatus_TECH_CARD_ISSUE_STATUS_UNKNOWN {
			v, ok := techCardIssueStatusPbToEntity[i.Status]
			if !ok {
				return nil, fmt.Errorf("unknown issue status: %v", i.Status)
			}
			st = v
		}
		out = append(out, entity.TechCardIssue{
			OperationNumber: nullInt32FromPb(i.OperationNumber),
			CalloutNumber:   nullInt32FromPb(i.CalloutNumber),
			RaisedBy:        nullStringFromPb(i.RaisedBy),
			Severity:        severity,
			Status:          st,
			Description:     i.Description,
			ResolutionNote:  nullStringFromPb(i.ResolutionNote),
		})
	}
	return out, nil
}

func techCardIssuesToPb(issues []entity.TechCardIssue) []*pb_common.TechCardIssue {
	out := make([]*pb_common.TechCardIssue, 0, len(issues))
	for _, i := range issues {
		out = append(out, &pb_common.TechCardIssue{
			OperationNumber: pbInt32FromNull(i.OperationNumber),
			CalloutNumber:   pbInt32FromNull(i.CalloutNumber),
			RaisedBy:        pbStringFromNull(i.RaisedBy),
			Severity:        techCardIssueSeverityEntityToPb[i.Severity],
			Status:          techCardIssueStatusEntityToPb[i.Status],
			Description:     i.Description,
			ResolutionNote:  pbStringFromNull(i.ResolutionNote),
		})
	}
	return out
}

// --- sign-off (Phase 3.5a-2) ---

var techCardSignoffSectionPbToEntity = map[pb_common.TechCardSignoffSection]entity.TechCardSignoffSection{
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN:       entity.SignoffDesign,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION: entity.SignoffConstruction,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_MATERIALS:    entity.SignoffMaterials,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR:       entity.SignoffColour,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_LABELS:       entity.SignoffLabels,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_PACKAGING:    entity.SignoffPackaging,
	pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING:      entity.SignoffCosting,
}
var techCardSignoffSectionEntityToPb = func() map[entity.TechCardSignoffSection]pb_common.TechCardSignoffSection {
	m := make(map[entity.TechCardSignoffSection]pb_common.TechCardSignoffSection, len(techCardSignoffSectionPbToEntity))
	for k, v := range techCardSignoffSectionPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardSignoffStatePbToEntity = map[pb_common.TechCardSignoffState]entity.TechCardSignoffState{
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_PENDING:  entity.SignoffStatePending,
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED: entity.SignoffStateApproved,
	pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_REJECTED: entity.SignoffStateRejected,
}
var techCardSignoffStateEntityToPb = func() map[entity.TechCardSignoffState]pb_common.TechCardSignoffState {
	m := make(map[entity.TechCardSignoffState]pb_common.TechCardSignoffState, len(techCardSignoffStatePbToEntity))
	for k, v := range techCardSignoffStatePbToEntity {
		m[v] = k
	}
	return m
}()

func parseTechCardSignoffs(pbs []*pb_common.TechCardSignoff) ([]entity.TechCardSignoff, error) {
	out := make([]entity.TechCardSignoff, 0, len(pbs))
	seen := make(map[entity.TechCardSignoffSection]bool, len(pbs))
	for _, s := range pbs {
		section, ok := techCardSignoffSectionPbToEntity[s.Section]
		if !ok {
			return nil, fmt.Errorf("signoff section is required and must be valid")
		}
		if seen[section] {
			return nil, fmt.Errorf("duplicate signoff for section %q", section)
		}
		seen[section] = true
		if len(s.SignedBy) > maxVarchar255 {
			return nil, fmt.Errorf("signoff signed_by must be at most %d characters", maxVarchar255)
		}
		state := entity.SignoffStatePending
		if s.State != pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_UNKNOWN {
			v, ok := techCardSignoffStatePbToEntity[s.State]
			if !ok {
				return nil, fmt.Errorf("unknown signoff state: %v", s.State)
			}
			state = v
		}
		if len(s.SignedDigest) > 64 {
			return nil, fmt.Errorf("signoff signed_digest must be at most 64 characters")
		}
		out = append(out, entity.TechCardSignoff{
			Section:  section,
			State:    state,
			SignedBy: nullStringFromPb(s.SignedBy),
			SignedAt: nullTimeFromPbTimestamp(s.SignedAt),
			Note:     nullStringFromPb(s.Note),
			// A present digest is only a REQUEST to carry the approval. The admin update layer verifies
			// it against the stored sign-off and replaces all server-owned audit fields from storage;
			// create discards it. An empty digest asks the server to approve what is being written.
			SignedDigest: nullStringFromPb(s.SignedDigest),
		})
	}
	return out, nil
}

func techCardSignoffsToPb(signoffs []entity.TechCardSignoff) []*pb_common.TechCardSignoff {
	out := make([]*pb_common.TechCardSignoff, 0, len(signoffs))
	for _, s := range signoffs {
		out = append(out, &pb_common.TechCardSignoff{
			Section:      techCardSignoffSectionEntityToPb[s.Section],
			State:        techCardSignoffStateEntityToPb[s.State],
			SignedBy:     pbStringFromNull(s.SignedBy),
			SignedAt:     pbTimestampFromNullTime(s.SignedAt),
			Note:         pbStringFromNull(s.Note),
			SignedDigest: pbStringFromNull(s.SignedDigest),
		})
	}
	return out
}

func techCardLabelsToPb(labels []entity.TechCardLabel) []*pb_common.TechCardLabel {
	out := make([]*pb_common.TechCardLabel, 0, len(labels))
	for _, l := range labels {
		out = append(out, &pb_common.TechCardLabel{
			LabelType:  techCardLabelTypeEntityToPb[l.LabelType],
			Content:    pbStringFromNull(l.Content),
			Placement:  pbStringFromNull(l.Placement),
			Attachment: pbStringFromNull(l.Attachment),
			Size:       pbStringFromNull(l.Size),
			Note:       pbStringFromNull(l.Note),
			BomItemId:  l.BomItemId.Int32, // §2.8 link (0 = unlinked)
		})
	}
	return out
}

func techCardPackagingToPb(p *entity.TechCardPackaging) *pb_common.TechCardPackaging {
	if p == nil {
		return nil
	}
	return &pb_common.TechCardPackaging{
		FoldingMethod:    pbStringFromNull(p.FoldingMethod),
		Polybag:          pbStringFromNull(p.Polybag),
		BagSticker:       pbStringFromNull(p.BagSticker),
		Inserts:          pbStringFromNull(p.Inserts),
		UnitsPerBox:      pbInt32FromNull(p.UnitsPerBox),
		BoxMarking:       pbStringFromNull(p.BoxMarking),
		BoxDimensions:    pbStringFromNull(p.BoxDimensions),
		WeightNetGrams:   pbInt32FromNull(p.WeightNetGrams),
		WeightGrossGrams: pbInt32FromNull(p.WeightGrossGrams),
		Notes:            pbStringFromNull(p.Notes),
	}
}

// operationMinutes is the minute figure one operation contributes to a minute rollup: its standard
// minute value (smv, 0219) when set, the legacy time_norm otherwise. smv was added as time_norm's
// successor. The legacy `time_norm` it used to fall back to went with the operations break: two time
// fields on one form, with no rule saying which the operator should fill, is a guarantee that half
// the cards are timed in one column and half in the other. SMV is now the only answer, and an
// operation with none is untimed and contributes nothing.
func operationMinutes(o *entity.TechCardOperation) decimal.NullDecimal {
	return o.SMV
}

// techCardCostingToPb emits the stored per-unit cost articles plus the computed per-colourway
// costs and the root rollup. Root figures are the PRIMARY colourway = index 0. Cost is built
// per GARMENT (unit_cost = materials_per_unit + shared manual articles, × (1 + defect%)) on the
// style's costing basis — a size-graded norm enters as its simple average over the declared
// size range — then scaled to the whole run for display only (order_cost = unit_cost ×
// order_qty, order_qty = Σ size_quantities). Returns nil when no costing row exists.
func techCardCostingToPb(tc *entity.TechCard, fx CostingFx) *pb_common.TechCardCosting {
	pb, _ := techCardCostingWithRoot(tc, fx)
	return pb
}

// techCardCostingWithRoot is techCardCostingToPb's body, additionally handing back the PRIMARY
// colourway's raw cost result. Both completeness flags now travel on the wire (has_unconverted_currencies,
// has_unpriced), but the seed paths need the whole result — the base-currency rollup and its
// convertibility — and recomputing that beside the pb would be a second, drift-prone copy of the same math.
func techCardCostingWithRoot(tc *entity.TechCard, fx CostingFx) (*pb_common.TechCardCosting, colorwayCostResult) {
	if tc.Costing == nil {
		return nil, colorwayCostResult{}
	}
	c := tc.Costing
	// Two different questions, deliberately kept apart:
	//   basis         — WHAT the per-garment cost is computed on (the style default: the simple
	//                   average over the declared size range — entity.CostingBasis).
	//   totalOrderQty — the illustrative run size_quantities still declares, used ONLY to scale the
	//                   already-computed unit cost into the display order_qty / order_cost pair.
	// size_quantities is not a denominator anywhere: the range average divides by the SIZE COUNT,
	// never by the typical mix — feeding quantities back into the unit figure is the fiction the
	// base-size phase removed, and the range-average phase keeps it removed.
	basis := tc.CostingBasis()
	totalOrderQty := 0
	for _, q := range tc.SizeQuantities {
		if q.OrderQty > 0 {
			totalOrderQty += q.OrderQty
		}
	}
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}

	// Manual per-unit articles are shared across colourways; each colourway's unit cost is
	// its own materials plus these, grossed up by defect%.
	manualPerUnit := decimal.Zero
	for _, d := range []decimal.NullDecimal{c.CmtCost, c.LogisticsCost, c.OverheadCost} {
		if d.Valid {
			manualPerUnit = manualPerUnit.Add(d.Decimal)
		}
	}
	defectMul := decimal.NewFromInt(1)
	if c.DefectPercent.Valid {
		defectMul = decimal.NewFromInt(1).Add(c.DefectPercent.Decimal.Div(decimal.NewFromInt(100)))
	}
	qtyDec := decimal.NewFromInt(int64(totalOrderQty))
	unitAndOrder := func(materialsPerUnit decimal.Decimal) (unit, order decimal.Decimal) {
		unit = materialsPerUnit.Add(manualPerUnit).Mul(defectMul)
		return unit, unit.Mul(qtyDec)
	}

	// Per-colourway cost (OUTPUT-ONLY). The root rollup is the primary colourway (index 0).
	colorwayCosts := make([]*pb_common.TechCardColorwayCost, 0, len(tc.Colorways))
	root := colorwayCostResult{}
	for ci := range tc.Colorways {
		cc := colorwayCost(tc, &tc.Colorways[ci], tc.BomItems, tc.LinkedMaterials, costingCcy, basis, fx)
		unit, order := unitAndOrder(cc.materialsPerUnit)
		colorwayCosts = append(colorwayCosts, &pb_common.TechCardColorwayCost{
			ColorwayId:               int64(tc.Colorways[ci].Id),
			MaterialsTotal:           cc.materialsTotal,
			MaterialsPerUnit:         pbDecimalFromDecimal(roundMoney(cc.materialsPerUnit)),
			UnitCost:                 pbDecimalFromDecimal(roundMoney(unit)),
			OrderQty:                 int32(totalOrderQty),
			OrderCost:                pbDecimalFromDecimal(roundMoney(order)),
			HasUnconvertedCurrencies: cc.hasUnconverted,
			HasUnpriced:              cc.hasUnpriced,
			HasEstimate:              cc.hasEstimate,
		})
		if ci == 0 {
			root = cc
		}
	}
	rootMaterialsPerUnit := root.materialsPerUnit
	rootMaterialsPerUnitBase := root.materialsPerUnitBase
	rootBaseConvertible := root.baseConvertible

	rootUnit, rootOrder := unitAndOrder(rootMaterialsPerUnit)
	out := &pb_common.TechCardCosting{
		CmtCost:                  pbDecimalFromNull(c.CmtCost),
		LogisticsCost:            pbDecimalFromNull(c.LogisticsCost),
		OverheadCost:             pbDecimalFromNull(c.OverheadCost),
		DefectPercent:            pbDecimalFromNull(c.DefectPercent),
		Currency:                 pbStringFromNull(c.Currency),
		Notes:                    pbStringFromNull(c.Notes),
		MaterialsTotal:           root.materialsTotal,
		MaterialsPerUnit:         pbDecimalFromDecimal(roundMoney(rootMaterialsPerUnit)),
		UnitCost:                 pbDecimalFromDecimal(roundMoney(rootUnit)),
		OrderQty:                 int32(totalOrderQty),
		OrderCost:                pbDecimalFromDecimal(roundMoney(rootOrder)),
		HasUnconvertedCurrencies: root.hasUnconverted,
		HasUnpriced:              root.hasUnpriced,
		HasEstimate:              root.hasEstimate,
		ColorwayCosts:            colorwayCosts,
	}

	// Base-currency rollup (OUTPUT-ONLY): fold the primary colourway's materials and the manual
	// articles (in the costing currency) into the base currency via the FX rates. Set only when
	// every currency involved has a rate, so the seed can trust unit_cost_base as a complete
	// figure; otherwise it is left unset and callers fall back / skip.
	if manualBase, ok := fx.toBase(manualPerUnit, costingCcy); ok && rootBaseConvertible {
		unitBase := rootMaterialsPerUnitBase.Add(manualBase).Mul(defectMul)
		out.UnitCostBase = pbDecimalFromDecimal(roundMoney(unitBase))
		out.OrderCostBase = pbDecimalFromDecimal(roundMoney(unitBase.Mul(qtyDec)))
		out.BaseCurrency = fx.Base
	}

	// total_sam = Σ(operation minutes); informative, pricing-independent.
	totalSam := decimal.Zero
	for i := range tc.Operations {
		if m := operationMinutes(&tc.Operations[i]); m.Valid {
			totalSam = totalSam.Add(m.Decimal)
		}
	}
	if totalSam.IsPositive() {
		out.TotalSam = pbDecimalFromDecimal(totalSam.Round(3))
	}

	// The target this style's margin is judged against: its own if it names one, the house default
	// otherwise. Resolved here so the costing tab reads one already-authorised response instead of
	// making a second call into the analytics section just to learn a percentage.
	if c.TargetMarginPct.Valid {
		out.TargetMarginPct = pbDecimalFromDecimal(c.TargetMarginPct.Decimal)
		out.EffectiveTargetMarginPct = pbDecimalFromDecimal(c.TargetMarginPct.Decimal)
	} else if fx.HouseTargetMarginPct.Valid {
		out.EffectiveTargetMarginPct = pbDecimalFromDecimal(fx.HouseTargetMarginPct.Decimal)
	}

	// Which market the colourway net_prices beside this were netted for. Reported even when the rate
	// is unknown, so the tab can say "GB, no rate on file" instead of quietly showing gross figures.
	out.VatCountryCode = fx.VatCountry
	if fx.VatRatePct.Valid {
		out.VatRatePct = pbDecimalFromDecimal(fx.VatRatePct.Decimal)
	}
	return out, root
}

// ComputeTechCardUnitCost returns a tech card's per-garment unit cost and its currency,
// computed exactly as the read path renders unit_cost — it reuses techCardCostingToPb so
// there is a single source of truth for the math. Returns an invalid NullDecimal when there
// is no costing row, the computed unit cost is not positive, or the figure is INCOMPLETE
// (see completeForSeed) — this is the cost-seeding entry point, and an incomplete number
// here becomes product.cost_price, the margin chain and every downstream report.
func ComputeTechCardUnitCost(tc *entity.TechCard, fx CostingFx) (decimal.NullDecimal, string) {
	if tc == nil {
		return decimal.NullDecimal{}, ""
	}
	pb, root := techCardCostingWithRoot(tc, fx)
	if pb == nil {
		return decimal.NullDecimal{}, ""
	}
	// Prefer the base-currency rollup so a non-base costing can still seed the product cost;
	// it is set only when every currency involved has an FX rate.
	// hasEstimate здесь по той же причине, что hasUnpriced: ветка базовой валюты — это ВТОРОЙ вход в
	// посев, и пропустив её, мы бы запретили оценке сеять цену только в валюте костинга, а в базовой
	// пустили бы ровно то же заниженное число.
	if pb.UnitCostBase != nil && !root.hasUnpriced && !root.hasEstimate {
		if v, err := decimal.NewFromString(pb.UnitCostBase.Value); err == nil && v.IsPositive() {
			return decimal.NullDecimal{Decimal: v, Valid: true}, pb.BaseCurrency
		}
	}
	if pb.UnitCost == nil || !completeForSeed(root) {
		return decimal.NullDecimal{}, ""
	}
	v, err := decimal.NewFromString(pb.UnitCost.Value)
	if err != nil || !v.IsPositive() {
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: v, Valid: true}, pb.Currency
}

// completeForSeed reports whether a colourway's costing-currency material figure is the WHOLE
// recipe. The costing-currency bucket is only the lines already in that currency: a line in another
// currency is excluded from it (hasUnconverted) and an uncostable line contributes to no bucket at
// all (hasUnpriced). Falling back to that bucket when either is set publishes a cost with a material
// silently missing — a BDT fabric on an EUR (== base) costing came out as trims+CMT and, because the
// currency then reads as base, sailed straight through the seed's base-currency guard into
// product.cost_price, to be rewritten with the same wrong number on every save.
// ОЦЕНКА ПО ПЛОЩАДИ НЕ СЕЕТ КАТАЛОЖНУЮ СЕБЕСТОИМОСТЬ, И ЭТО НЕ ОСТОРОЖНОСТЬ.
//
// Ступень 0 — netto, нижняя граница: межлекальных выпадов и концов настила в ней нет и быть не
// может. По собственным замерам беты КПД раскладки 69.7–75%, то есть занижение на треть. Уйдя в
// product.cost_price, это занижение становится маржой по стилю, отчётами и проводками — и там оно
// уже неотличимо от факта. Показать на экране с подписью «оценка снизу» честно; записать в каталог
// как себестоимость — нет.
//
// Поэтому «посчитано» и «годно для посева» — РАЗНЫЕ вопросы, и разошлись они здесь.
func completeForSeed(cc colorwayCostResult) bool {
	return !cc.hasUnconverted && !cc.hasUnpriced && !cc.hasEstimate
}

// HasColorwayForProduct reports whether the card carries a colourway bound to productID. The seed
// uses it to tell "this product is not one of the card's colourways" (fall back to the style figure)
// apart from "this colourway's own cost cannot be computed" (seed nothing) — ComputeColorwayUnitCost
// returns the same invalid result for both.
func HasColorwayForProduct(tc *entity.TechCard, productID int) bool {
	return tc != nil && colorwayForProduct(tc, productID) != nil
}

// ComputeColorwayUnitCost returns ONE colourway's per-garment unit cost and its currency — the
// colourway's own materials (pins priced via LinkedMaterials) plus the card's shared manual
// articles, grossed by defect%. This is what a per-colourway cost seed must use:
// ComputeTechCardUnitCost is the PRIMARY colourway's figure, and writing it to every product
// erases exactly the divergence pinning exists to create. Mirrors ComputeTechCardUnitCost's
// preference order: the base-currency rollup first, else the costing-currency figure — and its
// completeness rule: an uncostable line invalidates BOTH, an unconvertible one invalidates the
// fallback (see completeForSeed).
// colorwayProductID is the colourway's product id; unknown id / no costing → invalid.
func ComputeColorwayUnitCost(tc *entity.TechCard, colorwayProductID int, fx CostingFx) (decimal.NullDecimal, string) {
	if tc == nil || tc.Costing == nil {
		return decimal.NullDecimal{}, ""
	}
	cw := colorwayForProduct(tc, colorwayProductID)
	if cw == nil {
		return decimal.NullDecimal{}, ""
	}
	// A colourway with NO recipe at all inherits the STYLE figure (the primary colourway's),
	// exactly as before per-colourway seeding: computing it "own materials" would mean manual
	// articles only — a silent price DROP by the whole material component on every legacy
	// multi-colourway style whose recipes were never authored. An authored recipe, however
	// sparse, stands on its own.
	//
	// Piece-bound rows do not count as a recipe here: they assign materials to pieces and carry
	// no norms (entity.IsPieceMaterialAssignment), so a colourway whose usages are ALL piece
	// assignments would otherwise be costed as manual articles only — the exact silent drop this
	// guard exists to prevent.
	//
	// SINCE Ф1 THERE IS A SECOND WAY TO HAVE A RECIPE: piece assignments on a slot whose areas are
	// MEASURED are costable. Such a colourway must be priced from ITSELF — inheriting the primary's
	// figure would hand a colourway pinned to a different article its neighbour's price while its
	// own, correct one sat computed and unused. See colorwayHasOwnRecipe for why the measured guard
	// is what keeps every existing (unmeasured) card on the old inheritance path.
	if !colorwayHasOwnRecipe(tc, cw) {
		return ComputeTechCardUnitCost(tc, fx)
	}
	c := tc.Costing
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}
	manualPerUnit := decimal.Zero
	for _, d := range []decimal.NullDecimal{c.CmtCost, c.LogisticsCost, c.OverheadCost} {
		if d.Valid {
			manualPerUnit = manualPerUnit.Add(d.Decimal)
		}
	}
	defectMul := decimal.NewFromInt(1)
	if c.DefectPercent.Valid {
		defectMul = defectMul.Add(c.DefectPercent.Decimal.Div(decimal.NewFromInt(100)))
	}
	cc := colorwayCost(tc, cw, tc.BomItems, tc.LinkedMaterials, costingCcy, tc.CostingBasis(), fx)
	if cc.hasUnpriced {
		return decimal.NullDecimal{}, ""
	}
	// ВТОРОЙ ВХОД В ПОСЕВ — ветка базовой валюты. Она стоит ДО completeForSeed и проверяла только
	// сходимость курсов, поэтому оценка, запрещённая к посеву в валюте костинга, проходила бы здесь
	// в базовой — то же заниженное число, другой дверью.
	if cc.hasEstimate {
		return decimal.NullDecimal{}, ""
	}
	if manualBase, ok := fx.toBase(manualPerUnit, costingCcy); ok && cc.baseConvertible {
		if unit := roundMoney(cc.materialsPerUnitBase.Add(manualBase).Mul(defectMul)); unit.IsPositive() {
			return decimal.NullDecimal{Decimal: unit, Valid: true}, fx.Base
		}
	}
	if !completeForSeed(cc) {
		return decimal.NullDecimal{}, ""
	}
	if unit := roundMoney(cc.materialsPerUnit.Add(manualPerUnit).Mul(defectMul)); unit.IsPositive() {
		return decimal.NullDecimal{Decimal: unit, Valid: true}, costingCcy
	}
	return decimal.NullDecimal{}, ""
}

// colorwayHasOwnRecipe reports whether the colourway can be costed FROM ITSELF. A colourway that
// cannot inherits the style figure, and the breakdown must inherit the same projection
// (ComputeColorwayCostBreakdownBase), or the pair (cost_price, cost_breakdown) contradicts itself.
//
// TWO WAYS TO HAVE A RECIPE, AND THE SECOND ONE IS NEW (Ф1):
//
//  1. a GARMENT-level row — a row that can carry a norm (the original rule);
//  2. piece assignments on a roll slot WHOSE AREAS ARE MEASURED — since Ф1 those are a costable
//     recipe, not an empty one.
//
// WHY THE SECOND CLAUSE IS NOT OPTIONAL. The old rule read «nothing but piece assignments» as «empty
// recipe» and handed the colourway its neighbour's price. That was defensible while such a recipe
// produced no number; it stopped being defensible the moment it produces its own. A second colourway
// pinned to a different, more expensive article would otherwise be SEEDED with the primary's cost —
// its own, correct, computable figure discarded in favour of another colour's.
//
// WHY IT IS GUARDED BY «measured» RATHER THAN BY «has piece rows». Every card in the database has
// piece assignments and no measured areas; treating those as a recipe would compute materials = 0
// for them and seed CMT-only as the whole cost — silently cheaper than the inheritance they get
// today. Unmeasured stays empty, exactly as before.
func colorwayHasOwnRecipe(tc *entity.TechCard, cw *entity.TechCardColorway) bool {
	for i := range cw.Usages {
		if !cw.Usages[i].IsPieceMaterialAssignment() {
			return true
		}
	}
	if tc == nil || len(tc.PieceAreaScopes) == 0 {
		return false
	}
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if !isRollGoodsSection(b.Section) {
			continue
		}
		// СПОРЯЩИЕ ПИНЫ РЕЦЕПТ НЕ ОТМЕНЯЮТ. Это ДВА РАЗНЫХ ВОПРОСА, и путать их дорого: «есть ли у
		// колорвея собственный рецепт» (да — детали назначены, слот измерен) и «считается ли он»
		// (нет — про это hasUnpriced). Ответив «рецепта нет», мы отправляли бы такой колорвей
		// наследовать цену ПЕРВИЧНОГО — и посев записал бы её продукту, а прогон спланировал по ней.
		// То есть карточка, которую мы только что отказались считать, всё равно называла бы цену,
		// просто чужую. Здесь поэтому только len(pieces) > 0: конфликт снимет уже расчёт.
		if pieces, _, _ := slotAssignedPieces(tc, cw, b); len(pieces) > 0 {
			scope := entity.FabricScopeKey(b.Purpose.String, b.LineKey)
			if sc, ok := tc.PieceAreaScopes[scope]; ok && len(sc.Rows) > 0 {
				return true
			}
		}
	}
	return false
}

// colorwayForProduct returns the card's colourway bound to productID, or nil.
func colorwayForProduct(tc *entity.TechCard, productID int) *entity.TechCardColorway {
	for i := range tc.Colorways {
		if tc.Colorways[i].ProductId.Valid && int(tc.Colorways[i].ProductId.Int32) == productID {
			return &tc.Colorways[i]
		}
	}
	return nil
}

// ComputeColorwayCostBreakdownBase is ComputeTechCardCostBreakdownBase for ONE colourway: its
// own (pin-priced) materials in base currency plus the shared manual articles. ok=false when the
// colourway is unknown or any component currency lacks an FX rate — the same contract as the
// card-level breakdown, so per-colourway cost_breakdown is written iff the seed is base-complete.
func ComputeColorwayCostBreakdownBase(tc *entity.TechCard, colorwayProductID int, fx CostingFx) (entity.CostBreakdown, bool) {
	if tc == nil || tc.Costing == nil {
		return entity.CostBreakdown{}, false
	}
	if cw := colorwayForProduct(tc, colorwayProductID); cw != nil {
		// A colourway with an EMPTY recipe (none, or piece assignments only) inherits the STYLE
		// unit cost in ComputeColorwayUnitCost — and the breakdown must decompose the SAME figure.
		// Decomposing the colourway's own empty recipe instead pairs an inherited cost_price
		// (fabric included) with materials = 0, and the COGS-structure metrics
		// (internal/store/metrics/style.go) then spread a real figure over wrong shares.
		if !colorwayHasOwnRecipe(tc, cw) {
			return ComputeTechCardCostBreakdownBase(tc, fx)
		}
		return techCardCostBreakdownBase(tc, cw, fx)
	}
	return entity.CostBreakdown{}, false
}

// ComputeTechCardUnitCostWithWastage is ComputeTechCardUnitCost with a production run's ACTUAL
// cutting wastage substituted for every BOM line's estimate wastage_percent — used to snapshot a
// run's planned unit cost from the run's real cutting wastage instead of the style's per-line
// estimate. When override is unset it is identical to ComputeTechCardUnitCost (each line keeps its
// own BOM estimate), so the "run actual else BOM estimate" fallback is expressed purely by whether
// override is valid. The override is applied to a shallow copy of the card; the caller's card is
// untouched.
func ComputeTechCardUnitCostWithWastage(tc *entity.TechCard, fx CostingFx, override decimal.NullDecimal) (decimal.NullDecimal, string) {
	return ComputeTechCardUnitCost(cardWithRunWastage(tc, override), fx)
}

// ComputeTechCardUnitCostOnSize is ComputeTechCardUnitCostWithWastage costed on a GIVEN size
// instead of the style's default basis (the range average) — the per-garment cost of this style
// AT sizeID, with the run's actual cutting wastage applied the same way.
//
// It exists for the ONE caller that legitimately knows a concrete size: a production run, whose
// lines say which sizes are actually being made and how many of each. Standard cost stays on the
// range average (entity.TechCardColorwayUsage.UnitTotal explains why); this is not a second
// basis for the style, it is the same basis machinery evaluated at a size somebody has really
// committed to cut.
//
// sizeID <= 0 means "no size", and that is deliberately NOT the style default: it produces an
// uncosted result for every size-graded norm, so a line with no size can never be priced off the
// range average — a price for sizes the line never named (see cardCostedOnSize).
func ComputeTechCardUnitCostOnSize(tc *entity.TechCard, fx CostingFx, override decimal.NullDecimal, sizeID int) (decimal.NullDecimal, string) {
	return ComputeTechCardUnitCost(cardCostedOnSize(cardWithRunWastage(tc, override), sizeID), fx)
}

// cardCostedOnSize returns a shallow copy of tc whose COSTING BASIS is sizeID: the whole costing
// path reads the basis through entity.TechCardInsert.CostingBasis, so setting the one override
// field re-evaluates every graded norm at sizeID and leaves the rest of the math untouched. Doing
// it this way rather than threading a size through colorwayCost is the point — a parallel size
// parameter would be a second place for the "which size do we cost on" rule to live, and the two
// would drift the first time one of them grew a fallback.
//
// sizeID <= 0 sets the override to 0 = «no basis», NEVER back to the style default. This zero is
// the whole reason the override is three-valued: a run line with no size must stay uncosted, and
// if «no size» relaxed to the default it would silently take the range-average price — a figure
// for sizes the line never named, i.e. exactly the forbidden fallback wearing the new basis.
//
// The caller's card is never mutated: only the override pointer differs (it points at a fresh
// local), and every slice is shared read-only (cardWithRunWastage already cloned BomItems when
// it had to). No other code may set CostingSizeOverride — this is the one legitimate re-basing
// point, and a second writer is how two "which size" rules start to drift.
func cardCostedOnSize(tc *entity.TechCard, sizeID int) *entity.TechCard {
	if tc == nil {
		return nil
	}
	cp := *tc
	override := sizeID
	if override < 0 {
		override = 0
	}
	cp.CostingSizeOverride = &override
	return &cp
}

// cardWithRunWastage returns tc unchanged when override is unset; otherwise a shallow copy whose
// every BOM line's wastage_percent is replaced by override. Marker-sourced usages are immune by
// construction (Ф9.4): entity LineTotal/SizeRunTotal never read wastage_percent for them, so the
// substitution is inert exactly where the measured length already contains the waste. Only measured/per-size usage is grossed
// by wastage in the costing math (countable trims ignore it), so overriding every line applies the
// run's single cutting-wastage figure to all cut materials and is inert for the rest. Only the
// BomItems slice is cloned — the field the costing reads through the usage→bom index.
func cardWithRunWastage(tc *entity.TechCard, override decimal.NullDecimal) *entity.TechCard {
	if tc == nil || !override.Valid {
		return tc
	}
	cp := *tc
	cp.BomItems = make([]entity.TechCardBomItem, len(tc.BomItems))
	copy(cp.BomItems, tc.BomItems)
	for i := range cp.BomItems {
		cp.BomItems[i].WastagePercent = override
	}
	return &cp
}

// ComputeTechCardCostBreakdownBase decomposes a tech card's per-garment cost into base-currency
// (EUR) components — the same articles ComputeTechCardUnitCost rolls into one number — so the
// seed can snapshot them onto product.cost_breakdown for COGS-structure analytics. Components
// are the primary colourway's materials plus each manual cost article, each folded to base via
// the FX rates; defect_pct is carried raw (unit cost = (Σ components) × (1 + defect_pct/100)).
// Returns ok=false when there is no costing, no colourway, or any component currency lacks an FX
// rate — i.e. exactly when ComputeTechCardUnitCost's base rollup is unset — so cost_breakdown is
// written iff cost_price is seeded from a base-convertible cost, and the two never disagree.
func ComputeTechCardCostBreakdownBase(tc *entity.TechCard, fx CostingFx) (entity.CostBreakdown, bool) {
	if tc == nil || tc.Costing == nil || len(tc.Colorways) == 0 {
		return entity.CostBreakdown{}, false
	}
	return techCardCostBreakdownBase(tc, &tc.Colorways[0], fx)
}

// techCardCostBreakdownBase is the colourway-parameterised body shared by the card-level
// (primary colourway) and per-colourway breakdown entry points.
func techCardCostBreakdownBase(tc *entity.TechCard, cw *entity.TechCardColorway, fx CostingFx) (entity.CostBreakdown, bool) {
	c := tc.Costing
	costingCcy := ""
	if c.Currency.Valid {
		costingCcy = c.Currency.String
	}
	// The chosen colourway's materials, folded to base — the rollup's basis. An uncostable line
	// blocks the decomposition for the same reason it blocks the unit cost: the Materials component
	// would be short by that material while the report presents it as the whole recipe.
	cc := colorwayCost(tc, cw, tc.BomItems, tc.LinkedMaterials, costingCcy, tc.CostingBasis(), fx)
	// hasEstimate здесь по тем же основаниям, что и в цене: разбивка едет в product.cost_breakdown
	// рядом с cost_price, и разрешить ей оценку значило бы опубликовать заниженную структуру
	// себестоимости под ценой, которую мы только что отказались сеять.
	if !cc.baseConvertible || cc.hasUnpriced || cc.hasEstimate {
		return entity.CostBreakdown{}, false
	}
	// Each manual article is in the costing currency; fold individually. An absent (invalid)
	// article contributes 0 and never blocks convertibility.
	fold := func(d decimal.NullDecimal) (decimal.Decimal, bool) {
		if !d.Valid {
			return decimal.Zero, true
		}
		return fx.toBase(d.Decimal, costingCcy)
	}
	cmt, ok1 := fold(c.CmtCost)
	logi, ok4 := fold(c.LogisticsCost)
	ovh, ok5 := fold(c.OverheadCost)
	if !(ok1 && ok4 && ok5) {
		return entity.CostBreakdown{}, false
	}
	defect := decimal.Zero
	if c.DefectPercent.Valid {
		defect = c.DefectPercent.Decimal
	}
	return entity.CostBreakdown{
		Materials: roundMoney(cc.materialsPerUnitBase),
		Cmt:       roundMoney(cmt),
		Logistics: roundMoney(logi),
		Overhead:  roundMoney(ovh),
		DefectPct: defect,
	}, true
}

// colorwayCostResult holds one colourway's computed PER-GARMENT material cost.
type colorwayCostResult struct {
	materialsTotal   []*pb_common.TechCardCostLine // per-unit material cost grouped by article currency
	materialsPerUnit decimal.Decimal               // Σ per-garment usage cost in costingCcy (and currency-less)
	hasUnconverted   bool                          // a usage currency ≠ costingCcy (excluded from materialsPerUnit)
	// hasUnpriced is true when at least one authored usage could not be costed at all — its article
	// carries no price, its pin does not resolve (or its unit disagrees with the slot's), it points
	// at no BOM line, it has no norm, or it is graded per size while the basis cannot answer: the
	// norm does not cover the whole declared size range, the card declares no range, or the
	// computation runs with no basis at all (a run line naming no size). Such a line contributes
	// NOTHING to any bucket, so no currency flag catches it and every rollup silently understates
	// the recipe by that material. It blocks the cost seed, never the display figures.
	hasUnpriced bool
	// hasEstimate is true when at least one roll slot was costed by AREA ESTIMATE (Ф1, ступень 0)
	// rather than by an authored norm. The figure is a NETTO lower bound — inter-piece waste and lay
	// ends are not in it and cannot be — so it may be shown, but it may not seed product.cost_price
	// and it may not stand under a release. Kept apart from hasUnpriced: that one means «a line was
	// dropped from the total», this one means «a line is in the total, at its floor».
	hasEstimate bool
	// baseConvertible is true when every usage currency could be folded into the base currency
	// via the FX rates; materialsPerUnitBase is that Σ in base currency (valid only when true).
	baseConvertible      bool
	materialsPerUnitBase decimal.Decimal
	// estimates — тот самый срез, из которого сложены деньги выше. Он же уезжает на провод как
	// норма на изделие: держать его в результате дешевле, чем доверять двум вызовам совпасть, и
	// именно на этом равенстве стоит TestPublishedNormIsTheNumberTheMoneyUsed.
	estimates []slotEstimate
}

// pinShadowBom substitutes the PINNED article's latest price and currency for the slot default's
// when the colourway pins a different article (usage.material_id ≠ bom_item.material_id). The
// slot's own wastage stays — cutting loss is a property of how the slot is cut, not of which
// article fills it. A pin that cannot be resolved in linked (nil map on list reads, or a missing
// id) or that has no price yields a shadow with NO price, so UnitTotal skips the line exactly
// like an unpriced BOM line — never silently costed at the wrong (default) article's price.
func pinShadowBom(bom *entity.TechCardBomItem, u *entity.TechCardColorwayUsage, linked map[int]entity.MaterialWithPrice, costingCurrency, baseCurrency string) *entity.TechCardBomItem {
	if bom == nil || !u.MaterialId.Valid || u.MaterialId.Int64 <= 0 {
		return bom
	}
	if bom.MaterialId.Valid && bom.MaterialId.Int64 == u.MaterialId.Int64 {
		return bom // the pin IS the default — the slot's own (already-resolved) price is right
	}
	sh := *bom
	sh.UnitPrice = decimal.NullDecimal{}
	sh.Currency = sql.NullString{}
	if m, ok := linked[int(u.MaterialId.Int64)]; ok {
		price := m.LatestPriceForCurrencies(costingCurrency, baseCurrency)
		// A catalog price is per the MATERIAL's unit; the usage's norm is in the SLOT's unit.
		// When the two disagree (slot metres, article priced per cone), norm × price is off by
		// the whole conversion factor — leave the line unpriced instead, same rule as an
		// unpriced pin: never a silently wrong number into product.cost_price.
		//
		// Compared through the unit vocabulary (Ф5а.3), not EqualFold: «м» and "m" ARE the same
		// unit, and treating them as a disagreement did not produce a wrong number — it produced a
		// silently MISSING one (an unpriced line, which also blocks the cost seed).
		slotUnit := strings.TrimSpace(bom.Unit.String)
		stockUnit := strings.TrimSpace(m.Unit.String)
		if price != nil && (slotUnit == "" || stockUnit == "" || entity.SameMaterialUnit(slotUnit, stockUnit)) {
			sh.UnitPrice = decimal.NullDecimal{Decimal: price.Price, Valid: true}
			sh.Currency = sql.NullString{String: price.Currency, Valid: price.Currency != ""}
		}
	}
	return &sh
}

// withCuttingCoefficient stamps the EFFECTIVE article's roll-reality coefficient
// (material.cutting_coefficient, 0270) onto a COPY of the BOM line, so the four money-of-the-norm
// methods — the single definition of what this row costs — can apply it themselves. ЕДИНСТВЕННЫЙ
// резолвер коэффициента на денежном пути: кто считает деньги нормы мимо него, считает без рулона.
//
// ЭФФЕКТИВНЫЙ АРТИКУЛ, А НЕ «ПИН, ЕСЛИ ОН ОТЛИЧАЕТСЯ». Это ловушка, на которую очень легко сесть,
// повторив форму pinShadowBom: та возвращает строку БЕЗ ИЗМЕНЕНИЙ, когда пина нет или пин равен
// умолчанию слота, — и правильно, ей в этих случаях нечего подменять, цена умолчания уже верна.
// Коэффициент же нужен во ВСЕХ трёх случаях, потому что артикул есть всегда, а не только когда он
// расходится с умолчанием. Поэтому резолв идёт через entity.EffectiveMaterialId — то самое одно
// правило («пин, иначе умолчание; пин, равный умолчанию, ведёт себя как умолчание»), которое уже
// решает, чей это рулон, — а не через сравнение «пин ≠ умолчание».
//
// КОПИЯ, И ЭТО НЕ ГИГИЕНА, А ГРАНИЦА. Строку из tc.BomItems штамповать на месте нельзя: тот же
// указатель читают план настила и обе калибровки, где коэффициента быть не должно ни при каких
// условиях (иначе калибровка калибрует сама себя — шапка material_coefficient_calibration.go).
// Штамп на месте протёк бы туда молча: числа продолжили бы считаться.
//
// НЕТ КАРТЫ, НЕТ АРТИКУЛА, НЕТ КОЭФФИЦИЕНТА — возвращается ИСХОДНЫЙ указатель, и деньги строки
// получаются ровно те же, что до этой врезки (списочные чтения приходят с nil linked). Границу
// «только рулонные секции» держит entity.TechCardBomItem.EffectiveCuttingCoefficient, а не этот
// резолвер: там её нельзя обойти, забыв позвать сюда.
func withCuttingCoefficient(bom *entity.TechCardBomItem, u *entity.TechCardColorwayUsage, linked map[int]entity.MaterialWithPrice) *entity.TechCardBomItem {
	if bom == nil || u == nil {
		return bom
	}
	id, _ := u.EffectiveMaterialId(bom)
	return withArticleCuttingCoefficient(bom, id, linked)
}

// withArticleCuttingCoefficient is the article-keyed half of the stamp: for readers that already
// KNOW which article they are pricing and have no usage to resolve it from — the LAY path, where the
// pair (колорвей, слот) was resolved once by ResolveLayArticle and there is no single recipe row
// behind the number at all.
//
// ОДНО МЕСТО ШТАМПА НА ВСЕ ПУТИ. Резолв артикула у нормового и настильного путей разный по
// необходимости (строка рецепта против пары), а вот граница применения — нет: она целиком в
// entity.EffectiveCuttingCoefficient, куда обе дороги приходят через эту функцию. Второй штамп
// «прямо здесь, у меня же есть linked» — это ровно то место, где рулонная граница однажды
// разъедется с этой.
func withArticleCuttingCoefficient(bom *entity.TechCardBomItem, materialID int, linked map[int]entity.MaterialWithPrice) *entity.TechCardBomItem {
	if bom == nil || materialID <= 0 || len(linked) == 0 {
		return bom
	}
	m, ok := linked[materialID]
	if !ok {
		return bom
	}
	coeff := m.EffectiveCuttingCoefficient()
	if !coeff.Valid {
		return bom
	}
	sh := *bom
	sh.CuttingCoefficient = coeff
	return &sh
}

// colorwayCost computes one colourway's PER-GARMENT material cost from its usages. Each usage
// contributes its per-garment UnitTotal on the caller's resolved basis (the style default: a
// size-graded usage enters as the simple average of its norms over the declared size range —
// incomplete range coverage, or no declared range, leaves it uncosted), resolved against the
// BOM article it points at — with the colourway's pinned article's price substituted when the
// usage pins one (linked is the card's LinkedMaterials map; nil degrades to slot-default
// prices). Buckets are per-currency (no FX conversion); currency-less lines fold into the costing
// currency, and a line in another currency is flagged (and left out of materialsPerUnit).
func colorwayCost(tc *entity.TechCard, cw *entity.TechCardColorway, bomItems []entity.TechCardBomItem, linked map[int]entity.MaterialWithPrice, costingCcy string, basis entity.CostingBasis, fx CostingFx) colorwayCostResult {
	byCcy := map[string]decimal.Decimal{}
	order := make([]string, 0)
	hasUnconverted := false
	hasUnpriced := false
	for i := range cw.Usages {
		u := &cw.Usages[i]
		// A piece-bound row (entity.IsPieceMaterialAssignment) assigns a material to a cut-piece
		// and carries no norm: no contribution — a legacy number typed on such a row must not add
		// to the garment's cost — and no hasUnpriced — an empty piece row must not veto the seed.
		if u.IsPieceMaterialAssignment() {
			continue
		}
		// resolveUsageBom, not bomItemAtIndex: a usage authored via bom_line_key carries no
		// positional index, and a nil bom here silently zeroes the whole colourway's material cost.
		// ЦЕНА И КОЭФФИЦИЕНТ БЕРУТСЯ У ОДНОГО АРТИКУЛА — у эффективного (пин колорвея, иначе
		// умолчание слота). pinShadowBom подменяет цену, когда пин расходится с умолчанием;
		// withCuttingCoefficient проставляет коэффициент ВСЕГДА, потому что артикул есть и тогда,
		// когда подменять цену нечего. Две функции, один артикул: EffectiveMaterialId у обеих.
		bom := withCuttingCoefficient(
			pinShadowBom(resolveUsageBom(bomItems, u), u, linked, costingCcy, fx.Base), u, linked)
		ut := u.UnitTotal(bom, basis)
		if !ut.Valid {
			hasUnpriced = true
			continue
		}
		ccy := ""
		if bom != nil && bom.Currency.Valid {
			ccy = bom.Currency.String
		}
		if _, ok := byCcy[ccy]; !ok {
			order = append(order, ccy)
		}
		byCcy[ccy] = byCcy[ccy].Add(ut.Decimal)
		if ccy != "" && ccy != costingCcy {
			hasUnconverted = true
		}
	}

	// ОЦЕНКА ПО ПЛОЩАДИ (Ф1, ступень 0) — для рулонного слота, на который детали назначены, а норму
	// никто не вписал. До этой врезки такой слот стоил ноль и молчал: строка, привязанная к детали,
	// нормы не несёт (T8), а другой строки на слоте нет.
	//
	// Оценка НЕ поднимает hasUnpriced: она не «строка без цены», а посчитанная нижняя граница. Её
	// собственный флаг — hasEstimate, и он запрещает ровно одно: сеять каталожную себестоимость.
	//
	// СЧИТАЕТСЯ ОДНИМ ВЫЗОВОМ colorwayAreaEstimates — тем же, из которого карточка публикует норму на
	// изделие (AdminColorwayRef.area_estimates). Деньги здесь — это money ЭТОГО среза, то есть
	// perGarment × цена той же строки: рецепт и заголовок костинга не могут назвать разные числа,
	// потому что число одно.
	hasEstimate := false
	estimates := colorwayAreaEstimates(tc, cw, bomItems, linked, basis, fx.Base)
	for _, e := range estimates {
		amount, ccy, ok, refusal := e.money, e.currency, e.ok, e.refusal
		if !ok {
			// НАЗНАЧЕННЫЙ, НО НЕПОСЧИТАННЫЙ СЛОТ — ЭТО hasUnpriced, А НЕ ТИШИНА. Правило целиком
			// живёт в areaRefusalBlocksTheCost: у него теперь второй читатель — чек-лист готовности
			// к релизу, который называет ровно эти слоты словами.
			if areaRefusalBlocksTheCost(len(tc.PieceAreaScopes) > 0, refusal) {
				hasUnpriced = true
			}
			continue
		}
		hasEstimate = true
		if _, seen := byCcy[ccy]; !seen {
			order = append(order, ccy)
		}
		byCcy[ccy] = byCcy[ccy].Add(amount)
		if ccy != "" && ccy != costingCcy {
			hasUnconverted = true
		}
	}

	lines := make([]*pb_common.TechCardCostLine, 0, len(order))
	for _, ccy := range order {
		lines = append(lines, &pb_common.TechCardCostLine{
			Currency: ccy,
			Amount:   pbDecimalFromDecimal(roundMoney(byCcy[ccy])),
		})
	}

	materialsPerUnit := decimal.Zero
	if v, ok := byCcy[costingCcy]; ok {
		materialsPerUnit = materialsPerUnit.Add(v)
	}
	if costingCcy != "" {
		if v, ok := byCcy[""]; ok { // currency-less lines fold into the costing currency
			materialsPerUnit = materialsPerUnit.Add(v)
		}
	}

	// Base-currency rollup: fold every bucket into the base currency via the FX rates. A
	// currency-less bucket is treated as the costing currency first. If any bucket cannot be
	// converted (no rate), the base figure is incomplete and marked not convertible.
	baseSum := decimal.Zero
	baseConvertible := true
	for _, ccy := range order {
		eff := ccy
		if eff == "" {
			eff = costingCcy
		}
		b, ok := fx.toBase(byCcy[ccy], eff)
		if !ok {
			baseConvertible = false
			continue
		}
		baseSum = baseSum.Add(b)
	}

	return colorwayCostResult{
		materialsTotal:       lines,
		materialsPerUnit:     materialsPerUnit,
		hasUnconverted:       hasUnconverted,
		hasUnpriced:          hasUnpriced,
		hasEstimate:          hasEstimate,
		baseConvertible:      baseConvertible,
		materialsPerUnitBase: baseSum,
		estimates:            estimates,
	}
}
