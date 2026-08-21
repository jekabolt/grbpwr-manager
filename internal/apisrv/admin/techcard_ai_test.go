package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
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

// --- the thread-tension qualifier, and the profile link the model cannot make ---------------------

// TestAIOperationToPb_ThreadTensionNote: the qualifier needs the scale to be legal at all.
//
// ЧЕРНОВИК ТЕПЕРЬ ГОВОРИТ `tighter`, А НЕ `other`: 0328 снял «другое» из УПОРЯДОЧЕННОЙ шкалы —
// «другое, чем слабее / нормально / туже» не бывает, а то, что имелось в виду («у меня есть
// конкретное число»), и есть эта самая записка. Записка законна рядом с ЛЮБОЙ ступенью и никуда
// не делась; исчез только повод выбирать ступень наугад.
func TestAIOperationToPb_ThreadTensionNote(t *testing.T) {
	var drafted openrouter.Operation
	if err := json.Unmarshal([]byte(`{
	  "operation_type":"machine","machine_type":"overlock","zone":"side",
	  "thread_tension":"tighter","thread_tension_note":"на 0.5 туже верхней"
	}`), &drafted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The field has to exist in the answer SHAPE first: without the json tag the decoder drops the
	// sentence and «other» arrives meaning nothing.
	if drafted.ThreadTensionNote != "на 0.5 туже верхней" {
		t.Fatalf("the note never reached the decoded answer: %q", drafted.ThreadTensionNote)
	}
	op := aiOperationToPb(drafted)
	if op.ThreadTension != pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_TIGHTER {
		t.Fatalf("tension = %v", op.ThreadTension)
	}
	if op.ThreadTensionNote != "на 0.5 туже верхней" {
		t.Errorf("note = %q", op.ThreadTensionNote)
	}

	// A note with no scale is refused by the save in exactly those words, so a draft that carried one
	// would be a step the technologist cannot save until they find the field that did it.
	bare := aiOperationToPb(openrouter.Operation{
		OperationType: "machine", MachineType: "overlock", Zone: "side",
		ThreadTensionNote: "чуть слабее",
	})
	if bare.ThreadTensionNote != "" {
		t.Errorf("a qualifier with no scale must not travel: %q", bare.ThreadTensionNote)
	}

	// Over the column's width the note is CUT and marked, not dropped and not passed on: passed on it
	// is a save the operator cannot complete, dropped it takes the number the scale cannot carry.
	long := aiOperationToPb(openrouter.Operation{
		OperationType: "machine", MachineType: "overlock", Zone: "side",
		ThreadTension: "tighter", ThreadTensionNote: strings.Repeat("я", entity.MaxThreadTensionNoteLen+20),
	})
	if n := len([]rune(long.ThreadTensionNote)); n != entity.MaxThreadTensionNoteLen {
		t.Errorf("truncated note is %d runes, want %d", n, entity.MaxThreadTensionNoteLen)
	}
	if !strings.HasSuffix(long.ThreadTensionNote, "…") {
		t.Errorf("a cut note must say it was cut: %q", long.ThreadTensionNote)
	}
}

func aiTestPark(machines []entity.TechCardMachineProfile, presses []entity.TechCardPressProfile) aiEquipmentPark {
	return newAIEquipmentPark(&entity.TechCardConstruction{
		EquipmentDefaults: &entity.TechCardEquipmentDefaults{Machines: machines, Presses: presses},
	})
}

// TestAIEquipmentParkAttachesTheSoleProfile is the whole point of the park in the prompt: the model
// is told to OMIT the settings that match a listed profile, and an omitted setting only inherits
// through a profile key — which the model has no field for and no way to choose. If the server does
// not attach it, every omission the prompt asked for becomes a blank on the sheet.
func TestAIEquipmentParkAttachesTheSoleProfile(t *testing.T) {
	const overlockKey = "01J0AIPARKOVERLOCK00000001"
	const ironKey = "01J0AIPARKIRON00000000001A"
	park := aiTestPark(
		[]entity.TechCardMachineProfile{{ProfileKey: overlockKey, MachineType: "overlock"}},
		[]entity.TechCardPressProfile{{ProfileKey: ironKey, PressEquipment: "iron"}},
	)

	machine := aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "overlock", Zone: "side"})
	park.attach(machine)
	if machine.MachineProfileKey != overlockKey {
		t.Errorf("machine step was not attached: %q", machine.MachineProfileKey)
	}
	if machine.PressProfileKey != "" {
		t.Errorf("a machine step must not gain a ВТО reference: %q", machine.PressProfileKey)
	}

	press := aiOperationToPb(openrouter.Operation{OperationType: "press", PressEquipment: "iron", Zone: "front"})
	park.attach(press)
	if press.PressProfileKey != ironKey {
		t.Errorf("press step was not attached: %q", press.PressProfileKey)
	}
	if press.MachineProfileKey != "" {
		t.Errorf("a ВТО step must not gain a machine reference: %q", press.MachineProfileKey)
	}

	// Equipment the card does not run has nothing to inherit from, and a step naming it is left
	// unattached rather than pointed at something plausible.
	other := aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "bartack", Zone: "pocket"})
	park.attach(other)
	if other.MachineProfileKey != "" {
		t.Errorf("a machine the card does not run must not be attached: %q", other.MachineProfileKey)
	}

	// A step whose machine the model never named is a draft with a blank, and a blank cannot pick a
	// profile either.
	blank := aiOperationToPb(openrouter.Operation{OperationType: "machine", Zone: "side"})
	park.attach(blank)
	if blank.MachineProfileKey != "" {
		t.Errorf("a step with no machine must not be attached: %q", blank.MachineProfileKey)
	}
}

// TestAIEquipmentParkRefusesToGuessBetweenTwoIdenticalMachines: «два одинаковых станка» is a
// supported answer, not a duplicate — it is the reason the durable key exists. Picking one of them
// for the technologist would print settings from a machine nobody chose, so the server attaches
// nothing and the context stops promising the inheritance instead.
func TestAIEquipmentParkRefusesToGuessBetweenTwoIdenticalMachines(t *testing.T) {
	machines := []entity.TechCardMachineProfile{
		{ProfileKey: "01J0AIPARKOVERLOCK0000000A", MachineType: "overlock", Label: sql.NullString{String: "у окна", Valid: true}},
		{ProfileKey: "01J0AIPARKOVERLOCK0000000B", MachineType: "overlock", Label: sql.NullString{String: "у двери", Valid: true}},
		{ProfileKey: "01J0AIPARKBARTACK00000000C", MachineType: "bartack"},
	}
	park := aiTestPark(machines, nil)

	ambiguous := aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "overlock", Zone: "side"})
	park.attach(ambiguous)
	if ambiguous.MachineProfileKey != "" {
		t.Errorf("two overlocks cannot be told apart; the step must stay unattached, got %q", ambiguous.MachineProfileKey)
	}
	// The unambiguous neighbour is unaffected — ambiguity is per equipment, not per card.
	sole := aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "bartack", Zone: "pocket"})
	park.attach(sole)
	if sole.MachineProfileKey != "01J0AIPARKBARTACK00000000C" {
		t.Errorf("the card's only bartack must still be attached: %q", sole.MachineProfileKey)
	}

	// And the CONTEXT has to say the same thing the mapper does: the two overlock lines are marked,
	// the bartack line is not. A prompt that promised inheritance here would be asking for omissions
	// that inherit from nothing.
	lines := aiMachineProfileSummaries(&entity.TechCardEquipmentDefaults{Machines: machines})
	if len(lines) != 3 {
		t.Fatalf("lines = %v", lines)
	}
	for i, l := range lines[:2] {
		if !strings.Contains(l, "SEVERAL") {
			t.Errorf("overlock line %d must be marked: %q", i, l)
		}
	}
	if strings.Contains(lines[2], "SEVERAL") {
		t.Errorf("the card's only bartack must not be marked: %q", lines[2])
	}

	// Three presses of one equipment: the third must not look like a first one and re-enter the index.
	pressPark := aiTestPark(nil, []entity.TechCardPressProfile{
		{ProfileKey: "01J0AIPARKIRON0000000000A1", PressEquipment: "iron"},
		{ProfileKey: "01J0AIPARKIRON0000000000B2", PressEquipment: "iron"},
		{ProfileKey: "01J0AIPARKIRON0000000000C3", PressEquipment: "iron"},
	})
	third := aiOperationToPb(openrouter.Operation{OperationType: "fusing", PressEquipment: "iron", Zone: "front"})
	pressPark.attach(third)
	if third.PressProfileKey != "" {
		t.Errorf("three irons are no more attachable than two: %q", third.PressProfileKey)
	}
}

// A card with no park at all attaches nothing and panics on nothing — the ordinary case today.
func TestAIEquipmentParkEmptyCard(t *testing.T) {
	for _, park := range []aiEquipmentPark{{}, newAIEquipmentPark(nil), newAIEquipmentPark(&entity.TechCardConstruction{})} {
		op := aiOperationToPb(openrouter.Operation{OperationType: "machine", MachineType: "overlock", Zone: "side"})
		park.attach(op)
		if op.MachineProfileKey != "" {
			t.Errorf("an empty park attached %q", op.MachineProfileKey)
		}
		park.attach(nil)
	}
}

// --- the ВТО half of the same seam: a press profile belongs to a PROCESS, not just to a machine ----

const (
	aiIroningProfileKey  = "01J0AIPARKIRONINGPROG0001A"
	aiFusingProfileKey   = "01J0AIPARKFUSINGPROGR0002B"
	aiUniversalPressKey  = "01J0AIPARKUNIVERSALPR0003C"
	aiFusingPressMachine = "fusing_press"
)

func aiPressProfile(key, process string) entity.TechCardPressProfile {
	p := entity.TechCardPressProfile{
		ProfileKey:        key,
		PressEquipment:    aiFusingPressMachine,
		PressTemperatureC: sql.NullInt32{Int32: 150, Valid: true},
		PressDwellSec:     sql.NullInt32{Int32: 12, Valid: true},
	}
	if process != "" {
		p.PressOperationType = sql.NullString{String: process, Valid: true}
	}
	return p
}

func aiPressStep(process string) *pb_common.TechCardOperation {
	return aiOperationToPb(openrouter.Operation{
		OperationType: process, PressEquipment: aiFusingPressMachine, Zone: "front",
	})
}

// TestAIEquipmentParkWillNotAttachAPressingProfileToAFusingStep is the defect this pair of rules was
// split over, and it is not a hypothetical: a card carries ONE profile of the дублирующий пресс and
// it is declared for ВТО — the ironing program, not дублирование. Indexed by equipment alone, the
// SERVER wrote that profile's key onto a drafted fusing step; the sign gate then looked the profile
// up BY THE KEY THE SERVER HAD JUST WRITTEN, found a temperature and a dwell in it, and let the
// approval through. Дублирование on an ironing program, under a signature — the exact thing the gate
// exists to stop, reached through the one path no human ever chose.
func TestAIEquipmentParkWillNotAttachAPressingProfileToAFusingStep(t *testing.T) {
	presses := []entity.TechCardPressProfile{aiPressProfile(aiIroningProfileKey, string(entity.OpTypePress))}
	park := aiTestPark(nil, presses)

	fusing := aiPressStep("fusing")
	park.attach(fusing)
	if fusing.PressProfileKey != "" {
		t.Errorf("an ironing profile was hung on a fusing step: %q", fusing.PressProfileKey)
	}
	open := aiPressStep("press_open")
	park.attach(open)
	if open.PressProfileKey != "" {
		t.Errorf("разутюжка is not the process this profile declares either: %q", open.PressProfileKey)
	}
	// The step it IS for keeps inheriting — narrowing the rule must not cost the profile its own job.
	press := aiPressStep("press")
	park.attach(press)
	if press.PressProfileKey != aiIroningProfileKey {
		t.Errorf("the profile's own process no longer inherits it: %q", press.PressProfileKey)
	}

	// And the mirror: a profile declared for fusing is not an ironing setup.
	fusingPark := aiTestPark(nil, []entity.TechCardPressProfile{aiPressProfile(aiFusingProfileKey, string(entity.OpTypeFusing))})
	ironed := aiPressStep("press")
	fusingPark.attach(ironed)
	if ironed.PressProfileKey != "" {
		t.Errorf("a fusing recipe was hung on a ВТО step: %q", ironed.PressProfileKey)
	}
	fused := aiPressStep("fusing")
	fusingPark.attach(fused)
	if fused.PressProfileKey != aiFusingProfileKey {
		t.Errorf("the fusing step lost the card's only fusing profile: %q", fused.PressProfileKey)
	}

	// The CONTEXT keeps its half of the promise: the line is the sole answer for the process it names,
	// so it stays inheritable and the model is still told to omit the settings that match it.
	lines := aiPressProfileSummaries(&entity.TechCardEquipmentDefaults{Presses: presses})
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if strings.Contains(lines[0], "SEVERAL") {
		t.Errorf("the card's only ironing profile must stay inheritable for an ironing step: %q", lines[0])
	}
}

// A profile that declares NO process is universal, and universal has to keep meaning universal —
// narrowing the rule by process is worthless if it quietly narrows the profiles that declared none.
func TestAIEquipmentParkAttachesAUniversalPressProfileToEveryProcess(t *testing.T) {
	presses := []entity.TechCardPressProfile{aiPressProfile(aiUniversalPressKey, "")}
	park := aiTestPark(nil, presses)
	for _, process := range []string{"press", "press_open", "fusing"} {
		op := aiPressStep(process)
		park.attach(op)
		if op.PressProfileKey != aiUniversalPressKey {
			t.Errorf("a universal profile must be inherited by a %s step: %q", process, op.PressProfileKey)
		}
	}
	lines := aiPressProfileSummaries(&entity.TechCardEquipmentDefaults{Presses: presses})
	if len(lines) != 1 || strings.Contains(lines[0], "SEVERAL") {
		t.Errorf("a universal sole profile must stay inheritable: %v", lines)
	}
}

// Two profiles of ONE piece of equipment used to be «ambiguous» by counting alone. Once the question
// carries the process, two profiles that answer two different processes are not competing at all —
// each is the sole answer to its own question, and the prompt has to say so or the model would state
// settings it was meant to omit. The pair that DOES still compete — a universal profile beside a
// declared one — stays ambiguous for the process they share and only for that one.
func TestAIEquipmentParkTellsTwoProcessesApartOnOneMachine(t *testing.T) {
	split := []entity.TechCardPressProfile{
		aiPressProfile(aiIroningProfileKey, string(entity.OpTypePress)),
		aiPressProfile(aiFusingProfileKey, string(entity.OpTypeFusing)),
	}
	park := aiTestPark(nil, split)
	for process, want := range map[string]string{
		"press":      aiIroningProfileKey,
		"fusing":     aiFusingProfileKey,
		"press_open": "", // neither profile declares разутюжка, so there is nothing to inherit
	} {
		op := aiPressStep(process)
		park.attach(op)
		if op.PressProfileKey != want {
			t.Errorf("a %s step attached %q, want %q", process, op.PressProfileKey, want)
		}
	}
	for i, l := range aiPressProfileSummaries(&entity.TechCardEquipmentDefaults{Presses: split}) {
		if strings.Contains(l, "SEVERAL") {
			t.Errorf("line %d answers its own process alone and must stay inheritable: %q", i, l)
		}
	}

	// A universal profile beside a fusing one: the fusing step has two answers and gets none, while
	// the ironing step still has exactly one. The line for either must then be marked, because the
	// model may name that equipment for the process where the answer is not decided.
	overlap := []entity.TechCardPressProfile{
		aiPressProfile(aiUniversalPressKey, ""),
		aiPressProfile(aiFusingProfileKey, string(entity.OpTypeFusing)),
	}
	overlapPark := aiTestPark(nil, overlap)
	contested := aiPressStep("fusing")
	overlapPark.attach(contested)
	if contested.PressProfileKey != "" {
		t.Errorf("two profiles fit a fusing step; picking one invents an answer: %q", contested.PressProfileKey)
	}
	uncontested := aiPressStep("press")
	overlapPark.attach(uncontested)
	if uncontested.PressProfileKey != aiUniversalPressKey {
		t.Errorf("only the universal profile fits an ironing step here: %q", uncontested.PressProfileKey)
	}
	for i, l := range aiPressProfileSummaries(&entity.TechCardEquipmentDefaults{Presses: overlap}) {
		if !strings.Contains(l, "SEVERAL") {
			t.Errorf("line %d is contested for fusing and must not promise inheritance: %q", i, l)
		}
	}
}

// The two halves of the seam, end to end over ONE park: what the mapper attaches is what the sign
// gate resolves. The mapper writes the key; the gate reads it back. They were two rules and the
// looser one was reachable by machine, which is how an ironing recipe came to sign off дублирование.
func TestAIAttachmentAndTheSignGateAnswerTheSameQuestion(t *testing.T) {
	presses := []entity.TechCardPressProfile{aiPressProfile(aiIroningProfileKey, string(entity.OpTypePress))}
	drafted := aiPressStep("fusing")
	aiTestPark(nil, presses).attach(drafted)

	stepAsSaved := &entity.TechCardOperation{
		OperationType:   entity.OpTypeFusing,
		PressEquipment:  sql.NullString{String: aiFusingPressMachine, Valid: true},
		PressProfileKey: sql.NullString{String: drafted.PressProfileKey, Valid: drafted.PressProfileKey != ""},
	}
	profile, ambiguous := resolveFusingPressProfile(stepAsSaved, presses)
	if ambiguous || profile != nil {
		t.Fatalf("the gate resolved an ironing profile for a fusing step: %+v (ambiguous=%v)", profile, ambiguous)
	}

	// The same step with the key put there BY HAND — a technologist picking the wrong row in the
	// client — has to be refused by the gate on its own, without the mapper's help.
	byHand := *stepAsSaved
	byHand.PressProfileKey = sql.NullString{String: aiIroningProfileKey, Valid: true}
	if profile, ambiguous := resolveFusingPressProfile(&byHand, presses); ambiguous || profile != nil {
		t.Fatalf("a hand-picked ironing profile still resolved for a fusing step: %+v (ambiguous=%v)", profile, ambiguous)
	}

	// Universal, both ways: the mapper attaches it and the gate resolves the very same key.
	universal := []entity.TechCardPressProfile{aiPressProfile(aiUniversalPressKey, "")}
	draftedUniversal := aiPressStep("fusing")
	aiTestPark(nil, universal).attach(draftedUniversal)
	if draftedUniversal.PressProfileKey != aiUniversalPressKey {
		t.Fatalf("the universal profile was not attached: %q", draftedUniversal.PressProfileKey)
	}
	resolved, ambiguous := resolveFusingPressProfile(&entity.TechCardOperation{
		OperationType:   entity.OpTypeFusing,
		PressEquipment:  sql.NullString{String: aiFusingPressMachine, Valid: true},
		PressProfileKey: sql.NullString{String: draftedUniversal.PressProfileKey, Valid: true},
	}, universal)
	if ambiguous || resolved == nil || resolved.ProfileKey != aiUniversalPressKey {
		t.Fatalf("the gate did not resolve the key the mapper wrote: %+v (ambiguous=%v)", resolved, ambiguous)
	}
}

// aiOpsServer builds a Server whose ONLY live parts are the tech-card store and the AI client. The
// card carries an invalid CategoryId on purpose: resolveCategoryName returns early on it, so the
// dictionary cache never enters the picture and the test stays about the refusal.
func aiOpsServer(t *testing.T, client *openrouter.Client) *Server {
	t.Helper()
	cards := mocks.NewMockTechCards(t)
	cards.EXPECT().GetTechCardById(mock.Anything, 7).
		Return(&entity.TechCard{Id: 7}, nil).Maybe()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	return &Server{repo: repo, aiOps: client}
}

// TestGenerateTechCardOperationsModelUnavailable — СИММЕТРИЯ С ЗАМЕТКОЙ, И ОНА НЕ ФОРМАЛЬНОСТЬ.
//
// Черновик операций ходит тем же клиентом и тем же слугом, что помощник разметки: когда провайдер
// снял слуг с обслуживания, он умер тогда же и настолько же молча — по нему просто никто не жал
// кнопку. Ветка здесь зеркальная, но до этого теста её удаление или сдвиг не заметил бы никто:
// доказательство жило на уровне openrouter, а на уровне RPC — только на чтение.
//
// Проверяется пара: 404 — FailedPrecondition со словами про настройку, 503 — по-прежнему
// Unavailable. Одна половина без другой означала бы либо прежнюю ложь, либо новую.
func TestGenerateTechCardOperationsModelUnavailable(t *testing.T) {
	req := &pb_admin.GenerateTechCardOperationsRequest{TechCardId: 7, Description: "sew it"}

	t.Run("слуг не обслуживается: настройка, а не погода", func(t *testing.T) {
		client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"No endpoints found for anthropic/claude-3.5-sonnet.","code":404}}`))
		})
		resp, err := aiOpsServer(t, client).GenerateTechCardOperations(context.Background(), req)
		require.Nil(t, resp)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		msg := status.Convert(err).Message()
		require.Contains(t, msg, "OPENROUTER_MODEL")
		require.NotContains(t, msg, "try again")
		// Сырой текст провайдера остаётся в логе, а не едет клиенту: в нём бывают идентификаторы
		// провайдера и подсказки про ключ.
		require.NotContains(t, msg, "No endpoints found")
	})

	t.Run("обычный сбой провайдера остаётся Unavailable", func(t *testing.T) {
		client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream is having a moment"}}`))
		})
		resp, err := aiOpsServer(t, client).GenerateTechCardOperations(context.Background(), req)
		require.Nil(t, resp)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})

	t.Run("ключа нет: до стора дело не доходит", func(t *testing.T) {
		// Ожиданий на repo нет вовсе — mockery роняет тест на любом неожиданном вызове, и это и
		// есть доказательство, что пре-чек стоит раньше загрузки карточки.
		s := &Server{repo: mocks.NewMockRepository(t), aiOps: openrouter.New(openrouter.Config{})}
		resp, err := s.GenerateTechCardOperations(context.Background(), req)
		require.Nil(t, resp)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, aiOpsNotConfiguredMsg, status.Convert(err).Message())
	})

	t.Run("карточки нет: NotFound, и модель не зовут", func(t *testing.T) {
		client, calls := newFakeOpenRouter(t, orReplyWithContent(`{"operations":[]}`))
		cards := mocks.NewMockTechCards(t)
		cards.EXPECT().GetTechCardById(mock.Anything, 7).Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().TechCards().Return(cards)

		resp, err := (&Server{repo: repo, aiOps: client}).
			GenerateTechCardOperations(context.Background(), req)
		require.Nil(t, resp)
		require.Equal(t, codes.NotFound, status.Code(err))
		require.Empty(t, *calls, "платный вызов не имеет права случиться ради несуществующей карточки")
	})
}
