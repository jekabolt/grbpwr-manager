package admin

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// The AI draft is where the two axes meet the model's vocabulary: it answers with the words it was
// trained on, and this mapper is the only place that can turn those into a step the technologist can
// actually save. Nothing here is allowed to REFUSE a step — a draft with a blank is worth more than
// no draft — but everything here has to leave the step in a state the save path accepts as shown.

func TestAIOperationToPb_CanonicalisesLegacyType(t *testing.T) {
	// The nine legacy words WERE the operation type until this phase and are in every sewing text;
	// a model will keep answering with them for as long as it exists. Each becomes (MACHINE, its
	// machine) rather than UNKNOWN — which is the difference between a complete drafted step and a
	// step with its type field blank.
	for word, wantMachine := range map[string]pb_common.TechCardMachineType{
		"overlock":      pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
		"double_needle": pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH_DOUBLE_NEEDLE,
		"blindhem":      pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BLINDSTITCH,
		"button_attach": pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTON_ATTACH,
		// A model writes prose, not tokens; the normaliser folds the case, the spaces and the hyphens.
		"Double Needle": pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH_DOUBLE_NEEDLE,
		"double-needle": pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH_DOUBLE_NEEDLE,
		"Overlock":      pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
	} {
		op := aiOperationToPb(openrouter.Operation{OperationType: word, Zone: "hem"})
		if op.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE {
			t.Errorf("%q: type = %v, want MACHINE", word, op.OperationType)
		}
		if op.MachineType != wantMachine {
			t.Errorf("%q: machine = %v, want %v", word, op.MachineType, wantMachine)
		}
	}
}

// Every verb the storage vocabulary offers has to survive the trip, and the list is walked rather
// than transcribed: the dictionary is derived from entity.OperationTypeTokens, so a verb added there
// and forgotten here would otherwise land as UNKNOWN on every drafted step that used it.
func TestAIOperationToPb_EverySelectableVerbResolves(t *testing.T) {
	for _, tok := range entity.OperationTypeTokens {
		if tok == "unknown" { // a storage placeholder, never an answer
			continue
		}
		op := aiOperationToPb(openrouter.Operation{OperationType: tok, Zone: "hem"})
		if op.OperationType == pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN {
			t.Errorf("verb %q does not resolve — the draft would carry a blank type", tok)
		}
	}
}

// A word from outside every vocabulary costs the step THAT FIELD and nothing else. Refusing the
// step, or the draft, over one guessed word would throw away the nine fields that were right — the
// technologist is the one who completes a draft, and a blank is what they complete.
func TestAIOperationToPb_UnknownWordCostsOnlyItsOwnField(t *testing.T) {
	op := aiOperationToPb(openrouter.Operation{
		OperationType: "мережка", Zone: "hem", SeamClass: "ss_plain", SmvMinutes: "0.5",
	})
	if op.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN {
		t.Errorf("type = %v, want UNKNOWN", op.OperationType)
	}
	if op.Zone != pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_HEM ||
		op.SeamClass != pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN ||
		op.Smv.GetValue() != "0.5" {
		t.Errorf("an unknown type must not cost the step its other fields: %+v", op)
	}
}

func TestAIOperationToPb_ExplicitMachineTypeWinsOverTheLegacyWord(t *testing.T) {
	// Two answers to one question. The save path refuses a payload that carries both and disagrees,
	// so the draft has to pick one — and the explicit field is the answer on the axis that asks.
	op := aiOperationToPb(openrouter.Operation{
		OperationType: "overlock", MachineType: "coverstitch", Zone: "hem",
	})
	if op.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE {
		t.Fatalf("type = %v, want MACHINE", op.OperationType)
	}
	if op.MachineType != pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_COVERSTITCH {
		t.Errorf("machine = %v, want COVERSTITCH (the explicit field)", op.MachineType)
	}
	// The legacy word in the MACHINE field is a legal spelling of the machine, not of the verb.
	op = aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "blindhem"})
	if op.MachineType != pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BLINDSTITCH {
		t.Errorf("machine from a legacy spelling = %v, want BLINDSTITCH", op.MachineType)
	}
}

func TestAIOperationToPb_MachineBlock(t *testing.T) {
	op := aiOperationToPb(openrouter.Operation{
		OperationType: "machine", MachineType: "overlock", Zone: "shoulder",
		ThreadCount: "4", NeedleType: "ballpoint", NeedleSizeNm: "90",
		ThreadTension: "looser", StitchWidthMm: "5.2",
	})
	if op.ThreadCount != 4 || op.NeedleSizeNm != 90 {
		t.Errorf("ints: threads=%d needle=%d", op.ThreadCount, op.NeedleSizeNm)
	}
	if op.NeedleType != pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_BALLPOINT ||
		op.ThreadTension != pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_LOOSER {
		t.Errorf("tokens: needle=%v tension=%v", op.NeedleType, op.ThreadTension)
	}
	if op.StitchWidthMm.GetValue() != "5.2" {
		t.Errorf("stitch width = %q", op.StitchWidthMm.GetValue())
	}
}

// Decoded from JSON rather than built as a literal, because press_steam carries presence and only
// the wire can express «the model said nothing» — which is the state the whole three-valued field
// exists for.
func TestAIOperationToPb_PressBlock(t *testing.T) {
	var drafted openrouter.Operation
	if err := json.Unmarshal([]byte(`{
	  "operation_type":"press_open","zone":"front","press_equipment":"iron",
	  "press_temperature_c":150,"press_dwell_sec":"12","press_pressure_n_cm2":"3.75",
	  "press_steam":false,"press_cloth":"damp_press_cloth"
	}`), &drafted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	op := aiOperationToPb(drafted)
	if op.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN {
		t.Fatalf("type = %v", op.OperationType)
	}
	if op.PressEquipment != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON ||
		op.PressCloth != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_DAMP_PRESS_CLOTH {
		t.Errorf("tokens: equipment=%v cloth=%v", op.PressEquipment, op.PressCloth)
	}
	if op.PressTemperatureC != 150 || op.PressDwellSec != 12 {
		t.Errorf("ints: t=%d dwell=%d", op.PressTemperatureC, op.PressDwellSec)
	}
	// Rounded to the column's one decimal place: the save refuses an over-precise number outright,
	// so 3.75 left alone would be a drafted field the technologist cannot save.
	if op.PressPressureNCm2.GetValue() != "3.8" {
		t.Errorf("pressure = %q, want it rounded to the column scale", op.PressPressureNCm2.GetValue())
	}
	// «без пара» is an instruction, and it has to reach the wire as an explicit false. A two-valued
	// field would turn it back into «not stated» and the step would silently inherit steam.
	if op.PressSteam == nil || *op.PressSteam {
		t.Errorf("press_steam = %v, want an explicit false", op.PressSteam)
	}
}

func TestAIOperationToPb_BlocksBelongToTheirOwnStepType(t *testing.T) {
	// The save path refuses a machine setting on a ВТО step and vice versa. Carrying one through
	// would not be a stray field — it would be a card that cannot be saved until someone finds it.
	press := aiOperationToPb(openrouter.Operation{
		OperationType: "fusing", Zone: "front", PressEquipment: "fusing_press",
		MachineType: "overlock", ThreadCount: "4", NeedleType: "jeans", StitchWidthMm: "5",
	})
	if press.MachineType != pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN ||
		press.ThreadCount != 0 || press.NeedleType != pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_UNKNOWN ||
		press.StitchWidthMm != nil {
		t.Errorf("machine settings survived onto a fusing step: %+v", press)
	}

	machine := aiOperationToPb(openrouter.Operation{
		OperationType: "machine", MachineType: "lockstitch", Zone: "hem",
		PressEquipment: "iron", PressTemperatureC: "150", PressCloth: "teflon_sheet",
	})
	if machine.PressEquipment != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN ||
		machine.PressTemperatureC != 0 ||
		machine.PressCloth != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_UNKNOWN {
		t.Errorf("ВТО settings survived onto a machine step: %+v", machine)
	}

	handwork := aiOperationToPb(openrouter.Operation{
		OperationType: "handwork", Zone: "hem", ThreadCount: "4", PressTemperatureC: "150",
	})
	if handwork.ThreadCount != 0 || handwork.PressTemperatureC != 0 {
		t.Errorf("a handwork step took equipment settings: %+v", handwork)
	}
}

// A press step with no equipment is INCOMPLETE, and incomplete is what a draft is for. It must map
// without panicking and hand the technologist the blank to fill — refusing it here would throw away
// the zone, the note and the SMV that were right.
func TestAIOperationToPb_PressStepWithoutEquipmentIsStillADraft(t *testing.T) {
	op := aiOperationToPb(openrouter.Operation{
		OperationType: "press", Zone: "collar", Note: "приутюжить воротник", SmvMinutes: "0.4",
	})
	if op.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS {
		t.Fatalf("type = %v, want PRESS", op.OperationType)
	}
	if op.PressEquipment != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN {
		t.Errorf("equipment = %v, want UNKNOWN for a human to fill", op.PressEquipment)
	}
	if op.Zone != pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_COLLAR ||
		op.Note != "приутюжить воротник" || op.Smv.GetValue() != "0.4" {
		t.Errorf("the rest of the step must survive the missing equipment: %+v", op)
	}
	// Likewise a machine step that names no machine.
	m := aiOperationToPb(openrouter.Operation{OperationType: "machine", Zone: "hem"})
	if m.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE ||
		m.MachineType != pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN {
		t.Errorf("machine step without a machine = %+v", m)
	}
}

func TestAIOperationToPb_DropsValuesTheSaveWouldRefuse(t *testing.T) {
	// A hallucinated number is not a fact worth carrying: it reaches the editor as a field that
	// blocks the save until it is cleared, which is strictly worse than the blank it replaces.
	op := aiOperationToPb(openrouter.Operation{
		OperationType: "machine", MachineType: "lockstitch", Zone: "hem",
		ThreadCount: "40", NeedleSizeNm: "9", StitchWidthMm: "200",
	})
	if op.ThreadCount != 0 || op.NeedleSizeNm != 0 || op.StitchWidthMm != nil {
		t.Errorf("out-of-range machine settings survived: %+v", op)
	}
	press := aiOperationToPb(openrouter.Operation{
		OperationType: "press", Zone: "front", PressEquipment: "iron",
		PressTemperatureC: "1800", PressDwellSec: "0", PressPressureNCm2: "500",
	})
	if press.PressTemperatureC != 0 || press.PressDwellSec != 0 || press.PressPressureNCm2 != nil {
		t.Errorf("out-of-range ВТО settings survived: %+v", press)
	}
	if press.PressEquipment != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON {
		t.Error("a bad number must not cost the step the equipment that was right")
	}
}

func TestAIProfileSummaries(t *testing.T) {
	park := &entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{{
			ProfileKey:     "01J0000000000000000000MACH",
			Label:          sql.NullString{String: "оверлок у окна", Valid: true},
			MachineType:    "overlock",
			ThreadCount:    sql.NullInt32{Int32: 4, Valid: true},
			NeedleType:     sql.NullString{String: "ballpoint", Valid: true},
			NeedleSizeNm:   sql.NullInt32{Int32: 90, Valid: true},
			ThreadTension:  sql.NullString{String: "normal", Valid: true},
			StitchesPerCm:  decimal.NewNullDecimal(decimal.RequireFromString("4")),
			AttachmentKind: sql.NullString{String: "none", Valid: true},
		}},
		Presses: []entity.TechCardPressProfile{{
			ProfileKey:         "01J000000000000000000PRESS",
			PressEquipment:     "fusing_press",
			PressOperationType: sql.NullString{String: "fusing", Valid: true},
			PressTemperatureC:  sql.NullInt32{Int32: 150, Valid: true},
			PressDwellSec:      sql.NullInt32{Int32: 12, Valid: true},
			PressPressureNCm2:  decimal.NewNullDecimal(decimal.RequireFromString("3.5")),
			PressSteam:         sql.NullBool{Bool: false, Valid: true},
			PressCloth:         sql.NullString{String: "teflon_sheet", Valid: true},
		}},
	}

	machines := aiMachineProfileSummaries(park)
	if len(machines) != 1 {
		t.Fatalf("machine summaries = %v", machines)
	}
	for _, want := range []string{"overlock", `"оверлок у окна"`, "4 threads", "ballpoint needle Nm 90",
		"tension normal", "4 st/cm", "no attachment"} {
		if !strings.Contains(machines[0], want) {
			t.Errorf("machine summary %q is missing %q", machines[0], want)
		}
	}
	// The park is context, not a link: the model names the TYPE and never an identifier.
	if strings.Contains(machines[0], "01J0000000000000000000MACH") {
		t.Errorf("the profile key must not reach the model: %q", machines[0])
	}

	presses := aiPressProfileSummaries(park)
	if len(presses) != 1 {
		t.Fatalf("press summaries = %v", presses)
	}
	for _, want := range []string{"fusing_press for fusing", "150 °C", "12 s", "3.5 N/cm²",
		"no steam", "press cloth: teflon_sheet"} {
		if !strings.Contains(presses[0], want) {
			t.Errorf("press summary %q is missing %q", presses[0], want)
		}
	}
	if strings.Contains(presses[0], "01J000000000000000000PRESS") {
		t.Errorf("the profile key must not reach the model: %q", presses[0])
	}

	// A card whose park was never hydrated says nothing, rather than «this style is sewn on nothing».
	if aiMachineProfileSummaries(nil) != nil || aiPressProfileSummaries(nil) != nil {
		t.Error("a nil park must produce no lines at all")
	}
}
