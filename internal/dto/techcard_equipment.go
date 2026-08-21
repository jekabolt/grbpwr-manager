package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// The equipment half of a tech card: the card's machine / ВТО park (CARD DEFAULTS) and the
// per-step overrides that hang off it.
//
// Every map below is BUILT from the entity token slice by enumTokenMap, which panics at init on a
// token with no matching proto member. Nothing here is hand-listed: a vocabulary that is written
// out twice is a vocabulary that drifts, and the halves that drift are always the values nobody
// tested.

var machineTypePbToToken = enumTokenMap[pb_common.TechCardMachineType]("TECH_CARD_MACHINE_TYPE_", entity.MachineTypeTokens, pb_common.TechCardMachineType_value)
var machineTypeTokenToPb = invertTokenMap(machineTypePbToToken)

var pressEquipmentPbToToken = enumTokenMap[pb_common.TechCardPressEquipment]("TECH_CARD_PRESS_EQUIPMENT_", entity.PressEquipmentTokens, pb_common.TechCardPressEquipment_value)
var pressEquipmentTokenToPb = invertTokenMap(pressEquipmentPbToToken)

var needleTypePbToToken = enumTokenMap[pb_common.TechCardNeedleType]("TECH_CARD_NEEDLE_TYPE_", entity.NeedleTypeTokens, pb_common.TechCardNeedleType_value)
var needleTypeTokenToPb = invertTokenMap(needleTypePbToToken)

var bedTypePbToToken = enumTokenMap[pb_common.TechCardBedType]("TECH_CARD_BED_TYPE_", entity.BedTypeTokens, pb_common.TechCardBedType_value)
var bedTypeTokenToPb = invertTokenMap(bedTypePbToToken)

var automationPbToToken = enumTokenMap[pb_common.TechCardAutomationLevel]("TECH_CARD_AUTOMATION_LEVEL_", entity.AutomationLevelTokens, pb_common.TechCardAutomationLevel_value)
var automationTokenToPb = invertTokenMap(automationPbToToken)

var threadTensionPbToToken = enumTokenMap[pb_common.TechCardThreadTension]("TECH_CARD_THREAD_TENSION_", entity.ThreadTensionTokens, pb_common.TechCardThreadTension_value)
var threadTensionTokenToPb = invertTokenMap(threadTensionPbToToken)

// press_cloth carries NONE, and NONE is an ordinary token here ('none' in the column) — see
// parseEquipmentEnum for why that separation is the whole point of the pair.
var pressClothPbToToken = enumTokenMap[pb_common.TechCardPressCloth]("TECH_CARD_PRESS_CLOTH_", entity.PressClothTokens, pb_common.TechCardPressCloth_value)
var pressClothTokenToPb = invertTokenMap(pressClothPbToToken)

// parseEquipmentEnum turns an optional enum into a storage token, and it is the ONE place the
// UNKNOWN / NONE split lives:
//
//	UNKNOWN (the zero member) -> SQL NULL  = «inherit whatever the profile says»
//	NONE    (a real member)   -> 'none'    = «this step runs bare», cancelling the profile
//
// Until the card grew equipment profiles those two were one fact and attachment_kind collapsed both
// into NULL. They are not one fact any more: with something above the step, a step that cannot say
// «no foot» cannot contradict its profile, and the value it stores instead is the profile's foot.
// NONE reaches this function as an ordinary token because entity.AttachmentKindTokens /
// PressClothTokens list it — nothing special-cases it, which is exactly why it cannot be lost again.
func parseEquipmentEnum[E interface {
	comparable
	String() string
}](v E, m map[E]string, field, hint string) (sql.NullString, error) {
	var unset E // the zero member is UNKNOWN in every one of these enums
	if v == unset {
		return sql.NullString{}, nil
	}
	tok, ok := m[v]
	if !ok {
		return sql.NullString{}, entity.NewFieldViolation(field, "unknown_value", v.String(), hint)
	}
	return sql.NullString{String: tok, Valid: true}, nil
}

// parseRangedInt32 reads an optional int32 setting: 0 = unset (proto3 has no presence on a bare
// int32 and every one of these ranges starts at 1 or higher, so zero is free to mean «not stated»).
//
// The band is a SANITY bound, not a standard — it exists to catch «14 °C» typed for «140» before it
// reaches the shop floor. It also stands in the schema as a CHECK; the Go check exists because a
// CHECK alone answers 3819 with no field and no words, and this is a number an operator typed into
// a form control.
func parseRangedInt32(v int32, min, max int, field, hint string) (sql.NullInt32, error) {
	if v == 0 {
		return sql.NullInt32{}, nil
	}
	if int(v) < min || int(v) > max {
		return sql.NullInt32{}, entity.NewFieldViolation(field, "out_of_range", fmt.Sprint(v),
			fmt.Sprintf("%s (%d to %d); send 0 to leave it unset", hint, min, max))
	}
	return sql.NullInt32{Int32: v, Valid: true}, nil
}

// parseRangedDecimal is the decimal twin of parseRangedInt32. Absent = unset; a present value has
// to fit both the column's scale and the sanity band. maxFrac mirrors the column (DECIMAL(_,1) for
// every equipment decimal but the density), because MySQL does not reject an over-precise value —
// it rounds it silently and hands a different number back on the next read.
func parseRangedDecimal(v *pb_decimal.Decimal, field string, maxFrac int32, min, max int64, hint string) (decimal.NullDecimal, error) {
	nd, err := nullDecimalFromPb(v)
	if err != nil {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "invalid_number", v.GetValue(),
			"enter a plain number, e.g. 4.5")
	}
	if !nd.Valid {
		return nd, nil
	}
	if nd.Decimal.Exponent() < -maxFrac {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "too_many_decimal_places", nd.Decimal.String(),
			fmt.Sprintf("round to at most %d decimal place(s) — the column stores no more, so the rest would be dropped silently", maxFrac))
	}
	if nd.Decimal.LessThan(decimal.NewFromInt(min)) || nd.Decimal.GreaterThan(decimal.NewFromInt(max)) {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "out_of_range", nd.Decimal.String(),
			fmt.Sprintf("%s (%d to %d); clear the field to leave it unset", hint, min, max))
	}
	return nd, nil
}

// trimmedText trims a free string and rejects it past the column width. Trimming is not cosmetic:
// a trailing space would move a section digest and mark every approval of that section as edited.
//
// Counted in RUNES, not bytes, because VARCHAR(n) in MySQL counts characters: a byte count would
// refuse a perfectly legal 40-character Russian label at «оверлок…» length and blame the operator.
func trimmedText(s string, max int, field, hint string) (string, error) {
	t := strings.TrimSpace(s)
	if utf8.RuneCountInString(t) > max {
		return "", entity.NewFieldViolation(field, "too_long", t,
			fmt.Sprintf("%s — at most %d characters", hint, max))
	}
	return t, nil
}

// pressProfileOperationTypes is the closed set a press profile may declare itself FOR. UNKNOWN
// (universal, stored NULL) plus the three ВТО verbs — a profile «for a lockstitch step» is not a
// thing a press can mean, so anything else is refused rather than stored and ignored.
var pressProfileOperationTypes = map[pb_common.TechCardOperationType]string{
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS:      string(entity.OpTypePress),
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN: string(entity.OpTypePressOpen),
	pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING:     string(entity.OpTypeFusing),
}

// parseTechCardEquipmentDefaults converts the CARD DEFAULTS equipment park.
//
// THE WRAPPER IS THE PRESENCE SIGNAL and nothing else is: nil (an older bundle that never heard of
// profiles) returns nil and the store then PRESERVES the stored profiles; a present wrapper — even
// with both lists empty — is a full replace, and empty means «delete them all». proto3 cannot tell
// an absent repeated field from an empty one, which is why the wrapper exists at all; a bool flag
// beside the lists would have had to be hashed into the section digest to be trustworthy, and a
// transport fact has no business in a signature.
func parseTechCardEquipmentDefaults(pb *pb_common.TechCardEquipmentDefaults) (*entity.TechCardEquipmentDefaults, error) {
	if pb == nil {
		return nil, nil
	}
	out := &entity.TechCardEquipmentDefaults{
		Machines: make([]entity.TechCardMachineProfile, 0, len(pb.Machines)),
		Presses:  make([]entity.TechCardPressProfile, 0, len(pb.Presses)),
	}
	// One key space across BOTH lists: the step's reference names a key, not a key-and-a-kind, and
	// UNIQUE(tech_card_id, profile_key) in the schema is over the whole table. Caught here so the
	// operator reads a sentence instead of a driver 1062.
	//
	// The comparison is BYTE-WISE, and the column agrees with it: 0306 declares profile_key
	// COLLATE utf8mb4_bin precisely so that «same key» means the same thing on both sides. Under the
	// schema's default (case-insensitive) collation these two halves disagreed, and disagreed in both
	// directions at once — two keys differing only in case passed this check and died on the UNIQUE
	// as a raw 1062, while a step whose reference differed from its profile's key by case passed the
	// format check, failed resolveProfileKey's byte-wise lookup and silently detached. The case is
	// never normalised here: the key is minted by the client and rewriting somebody's identity to
	// make a comparison work is how two keys end up under one name.
	seen := make(map[string]bool, len(pb.Machines)+len(pb.Presses))
	for i, m := range pb.Machines {
		field := fmt.Sprintf("construction.equipment_defaults.machines[%d]", i)
		p, err := parseTechCardMachineProfile(m, field)
		if err != nil {
			return nil, err
		}
		if seen[p.ProfileKey] {
			return nil, entity.NewFieldViolation(field+".profile_key", "duplicate", p.ProfileKey,
				"two equipment profiles on one card cannot share a key — the steps that point at it would have two answers")
		}
		seen[p.ProfileKey] = true
		out.Machines = append(out.Machines, p)
	}
	for i, pr := range pb.Presses {
		field := fmt.Sprintf("construction.equipment_defaults.presses[%d]", i)
		p, err := parseTechCardPressProfile(pr, field)
		if err != nil {
			return nil, err
		}
		if seen[p.ProfileKey] {
			return nil, entity.NewFieldViolation(field+".profile_key", "duplicate", p.ProfileKey,
				"two equipment profiles on one card cannot share a key — the steps that point at it would have two answers")
		}
		seen[p.ProfileKey] = true
		out.Presses = append(out.Presses, p)
	}
	return out, nil
}

// parseProfileKey validates the durable identity of a profile row and mints one when the client
// sent none — the pieces precedent exactly (a client that forgot to mint would otherwise get a row
// no step can ever reference).
func parseProfileKey(key, field string) (string, error) {
	k := strings.TrimSpace(key)
	if k == "" {
		minted, err := mintTechCardLineKey()
		if err != nil {
			return "", fmt.Errorf("%s.profile_key: %w", field, err)
		}
		return minted, nil
	}
	if err := validatePatternLineKey(k, field+".profile_key"); err != nil {
		return "", err
	}
	return k, nil
}

func parseTechCardMachineProfile(pb *pb_common.TechCardMachineProfile, field string) (entity.TechCardMachineProfile, error) {
	var out entity.TechCardMachineProfile
	if pb == nil {
		return out, entity.NewFieldViolation(field, "required", "", "an empty machine profile says nothing — remove the row")
	}
	key, err := parseProfileKey(pb.ProfileKey, field)
	if err != nil {
		return out, err
	}
	// The machine type IS the profile: a park row that does not say what machine it is cannot be
	// inherited from and cannot be offered on a step.
	if pb.MachineType == pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN {
		return out, entity.NewFieldViolation(field+".machine_type", "required", "",
			"pick the machine — a profile is «this style is sewn on such a machine, set up like this»")
	}
	machineType, err := parseEquipmentEnum(pb.MachineType, machineTypePbToToken, field+".machine_type",
		"pick a machine from the list")
	if err != nil {
		return out, err
	}
	label, err := trimmedText(pb.Label, entity.MaxEquipmentProfileLabelLen, field+".label",
		"the label is a NAME for the floor (“the overlock by the window”), not a description")
	if err != nil {
		return out, err
	}
	note, err := trimmedText(pb.Note, entity.MaxEquipmentProfileNoteLen, field+".note",
		"the note carries the machine model and the detail behind «other»")
	if err != nil {
		return out, err
	}
	threadCount, err := parseRangedInt32(pb.ThreadCount, entity.MinThreadCount, entity.MaxThreadCount,
		field+".thread_count", "threads on one machine")
	if err != nil {
		return out, err
	}
	needleType, err := parseEquipmentEnum(pb.NeedleType, needleTypePbToToken, field+".needle_type",
		"pick a needle point from the list")
	if err != nil {
		return out, err
	}
	needleSize, err := parseRangedInt32(pb.NeedleSizeNm, entity.MinNeedleSizeNm, entity.MaxNeedleSizeNm,
		field+".needle_size_nm", "needle size in Nm (Nm 90 = 0.90 mm blade)")
	if err != nil {
		return out, err
	}
	bedType, err := parseEquipmentEnum(pb.BedType, bedTypePbToToken, field+".bed_type",
		"pick a bed from the list")
	if err != nil {
		return out, err
	}
	automation, err := parseEquipmentEnum(pb.Automation, automationPbToToken, field+".automation",
		"pick an automation level from the list")
	if err != nil {
		return out, err
	}
	tension, tensionNote, err := parseThreadTension(pb.ThreadTension, pb.ThreadTensionNote, field)
	if err != nil {
		return out, err
	}
	attachment, err := parseEquipmentEnum(pb.AttachmentKind, attachmentKindPbToToken, field+".attachment_kind",
		"pick an attachment from the list")
	if err != nil {
		return out, err
	}
	stitches, err := nullDecimalFromPb(pb.StitchesPerCm)
	if err != nil {
		return out, fmt.Errorf("%s.stitches_per_cm: %w", field, err)
	}
	if err := entity.ValidateStitchesPerCm(field+".stitches_per_cm", stitches); err != nil {
		return out, err
	}
	stitchWidth, err := parseRangedDecimal(pb.StitchWidthMm, field+".stitch_width_mm", 1,
		entity.MinStitchWidthMm, entity.MaxStitchWidthMm, "stitch width in mm — the zigzag amplitude, NOT the topstitch width")
	if err != nil {
		return out, err
	}
	return entity.TechCardMachineProfile{
		ProfileKey:        key,
		Label:             nullStringFromPb(label),
		MachineType:       machineType.String,
		ThreadCount:       threadCount,
		NeedleType:        needleType,
		NeedleSizeNm:      needleSize,
		BedType:           bedType,
		Automation:        automation,
		ThreadTension:     tension,
		ThreadTensionNote: tensionNote,
		AttachmentKind:    attachment,
		StitchesPerCm:     stitches,
		StitchWidthMm:     stitchWidth,
		Note:              nullStringFromPb(note),
	}, nil
}

func parseTechCardPressProfile(pb *pb_common.TechCardPressProfile, field string) (entity.TechCardPressProfile, error) {
	var out entity.TechCardPressProfile
	if pb == nil {
		return out, entity.NewFieldViolation(field, "required", "", "an empty press profile says nothing — remove the row")
	}
	key, err := parseProfileKey(pb.ProfileKey, field)
	if err != nil {
		return out, err
	}
	if pb.PressEquipment == pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN {
		return out, entity.NewFieldViolation(field+".press_equipment", "required", "",
			"pick the equipment — an iron, a press and a steamer are not interchangeable on a sheet")
	}
	equipment, err := parseEquipmentEnum(pb.PressEquipment, pressEquipmentPbToToken, field+".press_equipment",
		"pick the equipment from the list")
	if err != nil {
		return out, err
	}
	// WHICH PROCESS the profile is for. UNKNOWN = universal (NULL), and only the three ВТО verbs are
	// legal beyond it.
	var pressOpType sql.NullString
	if pb.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN {
		tok, ok := pressProfileOperationTypes[pb.OperationType]
		if !ok {
			return out, entity.NewFieldViolation(field+".operation_type", "not_applicable", pb.OperationType.String(),
				"a press profile is for pressing, open-pressing or fusing — leave it unset for a universal one")
		}
		pressOpType = sql.NullString{String: tok, Valid: true}
	}
	label, err := trimmedText(pb.Label, entity.MaxEquipmentProfileLabelLen, field+".label",
		"the label is a NAME for the floor, not a description")
	if err != nil {
		return out, err
	}
	note, err := trimmedText(pb.Note, entity.MaxEquipmentProfileNoteLen, field+".note",
		"the note carries the model and the detail behind «other»")
	if err != nil {
		return out, err
	}
	temperature, err := parseRangedInt32(pb.PressTemperatureC, entity.MinPressTemperatureC, entity.MaxPressTemperatureC,
		field+".press_temperature_c", "press temperature in °C")
	if err != nil {
		return out, err
	}
	dwell, err := parseRangedInt32(pb.PressDwellSec, entity.MinPressDwellSec, entity.MaxPressDwellSec,
		field+".press_dwell_sec", "dwell in seconds")
	if err != nil {
		return out, err
	}
	pressure, err := parseRangedDecimal(pb.PressPressureNCm2, field+".press_pressure_n_cm2", 1,
		entity.MinPressPressureNCm2, entity.MaxPressPressureNCm2, "pressure on the cloth in N/cm²")
	if err != nil {
		return out, err
	}
	cloth, err := parseEquipmentEnum(pb.PressCloth, pressClothPbToToken, field+".press_cloth",
		"pick a press cloth from the list, or «none» for none at all")
	if err != nil {
		return out, err
	}
	return entity.TechCardPressProfile{
		ProfileKey:         key,
		Label:              nullStringFromPb(label),
		PressEquipment:     equipment.String,
		PressOperationType: pressOpType,
		PressTemperatureC:  temperature,
		PressDwellSec:      dwell,
		PressPressureNCm2:  pressure,
		// Three-valued on purpose: absent = not stated, false = «без пара» — a real instruction that
		// a two-valued field would silently turn back into a default.
		PressSteam: nullBoolFromPbOptional(pb.PressSteam),
		PressCloth: cloth,
		Note:       nullStringFromPb(note),
	}, nil
}

// parseThreadTension reads the scale and its free qualifier together, because the qualifier alone
// states nothing: «чуть слабее» with no scale is not a setting anybody can reproduce. Same shape as
// attachment_size_mm needing an attachment_kind.
func parseThreadTension(v pb_common.TechCardThreadTension, note, field string) (sql.NullString, sql.NullString, error) {
	tension, err := parseEquipmentEnum(v, threadTensionPbToToken, field+".thread_tension",
		"pick a tension from the scale")
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	trimmed, err := trimmedText(note, entity.MaxThreadTensionNoteLen, field+".thread_tension_note",
		"the note qualifies the scale (“0.5 tighter”), it does not replace it")
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	if trimmed != "" && !tension.Valid {
		return sql.NullString{}, sql.NullString{}, entity.NewFieldViolation(field+".thread_tension_note",
			"needs_thread_tension", trimmed,
			"pick the tension first — a note on its own describes no setting the next machine can be set to")
	}
	return tension, nullStringFromPb(trimmed), nil
}

func nullBoolFromPbOptional(v *bool) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *v, Valid: true}
}

// --- emit entity -> pb -----------------------------------------------------------------------

// equipmentDefaultsToPb ALWAYS returns a non-nil wrapper, and that is load-bearing rather than
// tidy: seasonal cloning round-trips a stored card through pb and straight back into the write
// converter (style.go CloneStyleForSeason), so a nil wrapper there would read as «this payload did
// not speak about equipment» and the clone would silently inherit nothing. An empty pair of lists
// is the honest reading of a card with no profiles.
func equipmentDefaultsToPb(d *entity.TechCardEquipmentDefaults) *pb_common.TechCardEquipmentDefaults {
	out := &pb_common.TechCardEquipmentDefaults{}
	if d == nil {
		return out
	}
	out.Machines = make([]*pb_common.TechCardMachineProfile, 0, len(d.Machines))
	for i := range d.Machines {
		m := d.Machines[i]
		out.Machines = append(out.Machines, &pb_common.TechCardMachineProfile{
			ProfileKey:        m.ProfileKey,
			Label:             pbStringFromNull(m.Label),
			MachineType:       machineTypeTokenToPb[m.MachineType],
			ThreadCount:       pbInt32FromNull(m.ThreadCount),
			NeedleType:        needleTypeTokenToPb[m.NeedleType.String],
			NeedleSizeNm:      pbInt32FromNull(m.NeedleSizeNm),
			BedType:           bedTypeTokenToPb[m.BedType.String],
			Automation:        automationTokenToPb[m.Automation.String],
			ThreadTension:     threadTensionTokenToPb[m.ThreadTension.String],
			ThreadTensionNote: pbStringFromNull(m.ThreadTensionNote),
			// 'none' goes out as NONE, NULL as UNKNOWN — the map holds the first, the zero value of
			// the lookup gives the second.
			AttachmentKind: attachmentKindTokenToPb[m.AttachmentKind.String],
			StitchesPerCm:  pbDecimalFromNull(m.StitchesPerCm),
			StitchWidthMm:  pbDecimalFromNull(m.StitchWidthMm),
			Note:           pbStringFromNull(m.Note),
		})
	}
	out.Presses = make([]*pb_common.TechCardPressProfile, 0, len(d.Presses))
	for i := range d.Presses {
		p := d.Presses[i]
		out.Presses = append(out.Presses, &pb_common.TechCardPressProfile{
			ProfileKey:        p.ProfileKey,
			Label:             pbStringFromNull(p.Label),
			PressEquipment:    pressEquipmentTokenToPb[p.PressEquipment],
			OperationType:     operationTypeTokenToPb[p.PressOperationType.String],
			PressTemperatureC: pbInt32FromNull(p.PressTemperatureC),
			PressDwellSec:     pbInt32FromNull(p.PressDwellSec),
			PressPressureNCm2: pbDecimalFromNull(p.PressPressureNCm2),
			// NULL goes out ABSENT, not as false: «not stated» and «без пара» are two answers and the
			// optional bool is the only way the wire can hold both.
			PressSteam: pbOptionalBoolFromNull(p.PressSteam),
			PressCloth: pressClothTokenToPb[p.PressCloth.String],
			Note:       pbStringFromNull(p.Note),
		})
	}
	return out
}

// --- the step: canonicalisation and the two blocks ----------------------------------------------

// attachmentKindNone is the token behind TECH_CARD_ATTACHMENT_KIND_NONE. Named rather than spelled
// inline because it is the ONE value of that vocabulary that means «cancel the inherited one», and
// a rule keyed off a bare "none" string is a rule the next reader deletes as a typo.
const attachmentKindNone = "none"

// canonicaliseOperationType splits the wire's operation type into the two axes the step now has:
// the verb (what the step DOES) and the machine (what it does it ON). The nine legacy values
// answered both with one word, so each of them lands as (machine, <its machine>).
//
// The conflict case is not pedantry: a payload that says LOCKSTITCH and also sends
// machine_type=OVERLOCK holds two answers to one question, and silently preferring either one puts
// a machine on the printed sheet that nobody chose. It is refused, and a payload that agrees is
// canonicalised without a word.
func canonicaliseOperationType(o *pb_common.TechCardOperation, step string) (entity.TechCardOperationType, sql.NullString, error) {
	sent, err := parseEquipmentEnum(o.MachineType, machineTypePbToToken, step+".machine_type",
		"pick a machine from the list")
	if err != nil {
		return "", sql.NullString{}, err
	}
	if legacy, ok := legacyOperationTypePbToEntity[o.OperationType]; ok {
		machine := entity.LegacyOperationMachineType[legacy]
		if sent.Valid && sent.String != machine {
			return "", sql.NullString{}, entity.NewFieldViolation(step+".machine_type",
				"conflicts_with_operation_type", sent.String,
				fmt.Sprintf("the step type %q already names the machine (%s) — send the type as MACHINE with the machine you mean, or leave machine_type unset",
					string(legacy), machine))
		}
		return entity.OpTypeMachine, sql.NullString{String: machine, Valid: true}, nil
	}
	tok, ok := operationTypePbToToken[o.OperationType]
	if !ok {
		return "", sql.NullString{}, entity.NewFieldViolation(step+".operation_type", "unknown_value",
			o.OperationType.String(), "pick a type from the list")
	}
	return entity.TechCardOperationType(tok), sent, nil
}

// operationMachineFields / operationPressFields are the two blocks of a step, carried as one value
// each so the parse can hand back «nothing set» for the block that does not apply without
// scattering fifteen zero literals through the operation loop.
type operationMachineFields struct {
	machineType       sql.NullString
	profileKey        sql.NullString
	threadCount       sql.NullInt32
	needleType        sql.NullString
	needleSizeNm      sql.NullInt32
	threadTension     sql.NullString
	threadTensionNote sql.NullString
	stitchWidthMm     decimal.NullDecimal
}

type operationPressFields struct {
	equipment    sql.NullString
	profileKey   sql.NullString
	temperatureC sql.NullInt32
	dwellSec     sql.NullInt32
	pressureNCm2 decimal.NullDecimal
	steam        sql.NullBool
	cloth        sql.NullString
}

// populatedField pairs a wire field name with whether the payload said anything in it, so a
// «this block does not belong on this step» refusal can name the actual control the operator has to
// clear instead of gesturing at a block.
type populatedField struct {
	name string
	set  bool
}

func firstPopulated(fields []populatedField) string {
	for _, f := range fields {
		if f.set {
			return f.name
		}
	}
	return ""
}

func decimalSent(d *pb_decimal.Decimal) bool { return d != nil && strings.TrimSpace(d.Value) != "" }

// parseOperationEquipment reads the machine block (31-38) and the ВТО block (39-45) under three
// rules:
//
//   - a block belongs to its own step type and nowhere else. A thread count on a handwork step is a
//     shadow value: nothing reads it, nothing prints it, and the next editor believes it. The one
//     verb sharing a block is PRINT, which presses on the same equipment as ВТО does.
//   - the «на чём» of a step is REQUIRED — but only from a client that knows the fields exist.
//     A bundle that predates the split cannot fill in a machine, and its FUSING step (which it has
//     always been able to send) has to keep saving exactly as it did; the capability gate in the
//     service, not this parser, is what stops such a bundle from erasing machine facts it cannot see.
//   - unset is NULL and stays NULL. Nothing inherited is ever written into the row.
func parseOperationEquipment(o *pb_common.TechCardOperation, opType entity.TechCardOperationType,
	machineType sql.NullString, park *equipmentPark, aware bool, step string,
) (operationMachineFields, operationPressFields, error) {
	var machine operationMachineFields
	var press operationPressFields

	isMachine := opType == entity.OpTypeMachine
	isPress := opType == entity.OpTypePress || opType == entity.OpTypePressOpen || opType == entity.OpTypeFusing
	// ВТО-БЛОК ЛЕГАЛЕН ТАКЖЕ ПРИ PRINT (0324): термопресс — то же самое оборудование, и второй
	// комплект тех же семи полей внутри блока печати был бы дублём, который немедленно разъедется.
	// РАЗНИЦА ОДНА: press_equipment остаётся REQUIRED-if-aware только у press / press_open / fusing —
	// у печати он опционален, потому что печать бывает и без прижима вовсе (лазер), а там весь блок
	// как раз отвергается — правилом волны, рядом с print_method.
	pressAllowed := isPress || opType == entity.OpTypePrint

	if !isMachine {
		if f := firstPopulated([]populatedField{
			{"machine_type", machineType.Valid},
			{"machine_profile_key", strings.TrimSpace(o.MachineProfileKey) != ""},
			{"thread_count", o.ThreadCount != 0},
			{"needle_type", o.NeedleType != pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_UNKNOWN},
			{"needle_size_nm", o.NeedleSizeNm != 0},
			{"thread_tension", o.ThreadTension != pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_UNKNOWN},
			{"thread_tension_note", strings.TrimSpace(o.ThreadTensionNote) != ""},
			{"stitch_width_mm", decimalSent(o.StitchWidthMm)},
		}); f != "" {
			return machine, press, entity.NewFieldViolation(step+"."+f, "not_applicable", string(opType),
				fmt.Sprintf("the machine settings belong to a machine step; this one is %q — clear the field or switch the step type", string(opType)))
		}
	}
	if !pressAllowed {
		if f := firstPopulated([]populatedField{
			{"press_equipment", o.PressEquipment != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN},
			{"press_profile_key", strings.TrimSpace(o.PressProfileKey) != ""},
			{"press_temperature_c", o.PressTemperatureC != 0},
			{"press_dwell_sec", o.PressDwellSec != 0},
			{"press_pressure_n_cm2", decimalSent(o.PressPressureNCm2)},
			{"press_steam", o.PressSteam != nil},
			{"press_cloth", o.PressCloth != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_UNKNOWN},
		}); f != "" {
			return machine, press, entity.NewFieldViolation(step+"."+f, "not_applicable", string(opType),
				fmt.Sprintf("the pressing settings belong to a press / press_open / fusing / print step; this one is %q — clear the field or switch the step type", string(opType)))
		}
	}

	if isMachine {
		if aware && !machineType.Valid {
			return machine, press, entity.NewFieldViolation(step+".machine_type", "required", "",
				"pick the machine — «machine» says what the step does, the machine says what it does it on")
		}
		machine.machineType = machineType
		var err error
		machine.profileKey, err = resolveProfileKey(o.MachineProfileKey, machineType, parkMachines(park),
			step+".machine_profile_key", "machine")
		if err != nil {
			return machine, press, err
		}
		machine.threadCount, err = parseRangedInt32(o.ThreadCount, entity.MinThreadCount, entity.MaxThreadCount,
			step+".thread_count", "threads on one machine")
		if err != nil {
			return machine, press, err
		}
		machine.needleType, err = parseEquipmentEnum(o.NeedleType, needleTypePbToToken, step+".needle_type",
			"pick a needle point from the list")
		if err != nil {
			return machine, press, err
		}
		machine.needleSizeNm, err = parseRangedInt32(o.NeedleSizeNm, entity.MinNeedleSizeNm, entity.MaxNeedleSizeNm,
			step+".needle_size_nm", "needle size in Nm (Nm 90 = 0.90 mm blade)")
		if err != nil {
			return machine, press, err
		}
		machine.threadTension, machine.threadTensionNote, err = parseThreadTension(o.ThreadTension, o.ThreadTensionNote, step)
		if err != nil {
			return machine, press, err
		}
		machine.stitchWidthMm, err = parseRangedDecimal(o.StitchWidthMm, step+".stitch_width_mm", 1,
			entity.MinStitchWidthMm, entity.MaxStitchWidthMm,
			"stitch width in mm — the zigzag amplitude / overlock bite, NOT the topstitch width")
		if err != nil {
			return machine, press, err
		}
	}

	if pressAllowed {
		if isPress && aware && o.PressEquipment == pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN {
			return machine, press, entity.NewFieldViolation(step+".press_equipment", "required", "",
				"pick the equipment — an iron, a fusing press and a steamer are three different instructions")
		}
		var err error
		press.equipment, err = parseEquipmentEnum(o.PressEquipment, pressEquipmentPbToToken,
			step+".press_equipment", "pick the equipment from the list")
		if err != nil {
			return machine, press, err
		}
		press.profileKey, err = resolveProfileKey(o.PressProfileKey, press.equipment, parkPresses(park),
			step+".press_profile_key", "press equipment")
		if err != nil {
			return machine, press, err
		}
		press.temperatureC, err = parseRangedInt32(o.PressTemperatureC, entity.MinPressTemperatureC, entity.MaxPressTemperatureC,
			step+".press_temperature_c", "press temperature in °C")
		if err != nil {
			return machine, press, err
		}
		press.dwellSec, err = parseRangedInt32(o.PressDwellSec, entity.MinPressDwellSec, entity.MaxPressDwellSec,
			step+".press_dwell_sec", "dwell in seconds")
		if err != nil {
			return machine, press, err
		}
		press.pressureNCm2, err = parseRangedDecimal(o.PressPressureNCm2, step+".press_pressure_n_cm2", 1,
			entity.MinPressPressureNCm2, entity.MaxPressPressureNCm2, "pressure on the cloth in N/cm²")
		if err != nil {
			return machine, press, err
		}
		press.steam = nullBoolFromPbOptional(o.PressSteam)
		press.cloth, err = parseEquipmentEnum(o.PressCloth, pressClothPbToToken, step+".press_cloth",
			"pick a press cloth from the list, or «none» for none at all")
		if err != nil {
			return machine, press, err
		}
	}
	return machine, press, nil
}

func parkMachines(p *equipmentPark) map[string]string {
	if p == nil {
		return nil
	}
	return p.machines
}

func parkPresses(p *equipmentPark) map[string]string {
	if p == nil {
		return nil
	}
	return p.presses
}

// --- the step's view of the park ---------------------------------------------------------------

// equipmentPark indexes the profiles of the payload BEING WRITTEN by key, so a step's reference can
// be resolved against the same full-replace payload it arrived in (there are no stable ids to FK
// against on write — the same reason callout numbers are checked this way).
//
// A nil park means the payload carried no wrapper at all: there is then nothing to resolve against
// and the keys pass through untouched. That is not a hole — the reference is soft by design (no FK,
// because the profile list is deleted and re-inserted on every save), and a key naming nothing
// simply resolves to «not set» at the point of use.
type equipmentPark struct {
	machines map[string]string // profile_key -> machine token
	presses  map[string]string // profile_key -> press equipment token
}

func newEquipmentPark(d *entity.TechCardEquipmentDefaults) *equipmentPark {
	if d == nil {
		return nil
	}
	p := &equipmentPark{
		machines: make(map[string]string, len(d.Machines)),
		presses:  make(map[string]string, len(d.Presses)),
	}
	for _, m := range d.Machines {
		p.machines[m.ProfileKey] = m.MachineType
	}
	for _, pr := range d.Presses {
		p.presses[pr.ProfileKey] = pr.PressEquipment
	}
	return p
}

// resolveProfileKey applies the two rules a step's profile reference lives by:
//
//   - a key naming NO profile of that kind DETACHES to unset — the S8 callout precedent. Tidying the
//     card's defaults must never block saving the steps, and the alternative («delete the profile,
//     the save is refused until you visit every step») is how an editor becomes unusable.
//   - a key naming a profile of a DIFFERENT type is refused, because that is two answers to one
//     question: the step says overlock, the profile it points at is a buttonholer, and whichever the
//     sheet prints, half of it is wrong.
//
// index nil (no wrapper in the payload) means «nothing to resolve against» and the key is kept.
func resolveProfileKey(key string, stepType sql.NullString, index map[string]string, field, kind string) (sql.NullString, error) {
	k := strings.TrimSpace(key)
	if k == "" {
		return sql.NullString{}, nil
	}
	// Validated even when there is nothing to resolve against: the column is CHAR(26) and an
	// over-long key would come back as a driver-level 1406, not a sentence.
	if err := validatePatternLineKey(k, field); err != nil {
		return sql.NullString{}, err
	}
	if index == nil {
		return sql.NullString{String: k, Valid: true}, nil
	}
	profileType, ok := index[k]
	if !ok {
		return sql.NullString{}, nil // detach
	}
	if stepType.Valid && profileType != stepType.String {
		return sql.NullString{}, entity.NewFieldViolation(field, "profile_type_mismatch", k,
			fmt.Sprintf("the step says %s %q but the profile it points at is %q — pick one", kind, stepType.String, profileType))
	}
	return sql.NullString{String: k, Valid: true}, nil
}
