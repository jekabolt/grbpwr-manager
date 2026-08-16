package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

const (
	machineKey = "01J0MACHINEKEY000000000001"
	pressKey   = "01J0PRESSKEY0000000000001A"
)

// card wraps a construction + operations payload into the smallest insert the converter accepts.
func equipCard(c *pb_common.TechCardConstruction, aware bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber:        "EQ-1",
		Name:               "Jacket",
		Construction:       c,
		Operations:         ops,
		MachineFieldsAware: aware,
	}
}

func equipMachineOp(zone pb_common.TechCardGarmentZone, m pb_common.TechCardMachineType) *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:          zone,
		MachineType:   m,
	}
}

// TestEquipmentProfilesRoundTrip walks a fully-populated card out and back: every profile field and
// every one of the fifteen new operation columns has to survive pb -> entity -> pb -> entity
// unchanged. The round-trip (not just the parse) is what the seasonal clone does, and a field the
// mapper forgets to emit disappears there silently.
func TestEquipmentProfilesRoundTrip(t *testing.T) {
	in := equipCard(&pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{{
				ProfileKey:        machineKey,
				Label:             "  оверлок у окна  ",
				MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
				ThreadCount:       4,
				NeedleType:        pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_BALLPOINT,
				NeedleSizeNm:      90,
				BedType:           pb_common.TechCardBedType_TECH_CARD_BED_TYPE_CYLINDER_BED,
				Automation:        pb_common.TechCardAutomationLevel_TECH_CARD_AUTOMATION_LEVEL_SEMI_AUTO,
				ThreadTension:     pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_LOOSER,
				ThreadTensionNote: "на пол-оборота",
				AttachmentKind:    pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_BINDER,
				StitchesPerCm:     dec("4.5"),
				StitchWidthMm:     dec("5.5"),
				Note:              "Juki MO-6800",
			}},
			Presses: []*pb_common.TechCardPressProfile{{
				ProfileKey:        pressKey,
				Label:             "дублирующий",
				PressEquipment:    pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS,
				OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
				PressTemperatureC: 140,
				PressDwellSec:     12,
				PressPressureNCm2: dec("3.5"),
				PressSteam:        pbBool(false),
				PressCloth:        pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_TEFLON_SHEET,
				Note:              "Veit 2000",
			}},
		},
	}, true,
		&pb_common.TechCardOperation{
			OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:              zoneOuter,
			MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
			MachineProfileKey: machineKey,
			ThreadCount:       5,
			NeedleType:        pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_STRETCH,
			NeedleSizeNm:      80,
			ThreadTension:     pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_TIGHTER,
			ThreadTensionNote: "туже на 0.5",
			StitchWidthMm:     dec("6.5"),
		},
		&pb_common.TechCardOperation{
			OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
			Zone:              zoneCollar,
			PressEquipment:    pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS,
			PressProfileKey:   pressKey,
			PressTemperatureC: 150,
			PressDwellSec:     15,
			PressPressureNCm2: dec("4.5"),
			PressSteam:        pbBool(true),
			PressCloth:        pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_DAMP_PRESS_CLOTH,
		},
	)

	first, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *first}, CostingFx{})
	second, err := ConvertPbTechCardInsertToEntity(out.TechCard)
	if err != nil {
		t.Fatalf("re-parse of the emitted payload: %v", err)
	}

	for _, got := range []*entity.TechCardInsert{first, second} {
		d := got.Construction.EquipmentDefaults
		if d == nil || len(d.Machines) != 1 || len(d.Presses) != 1 {
			t.Fatalf("equipment defaults lost: %+v", d)
		}
		m := d.Machines[0]
		if m.ProfileKey != machineKey || m.Label.String != "оверлок у окна" || m.MachineType != "overlock" ||
			m.ThreadCount.Int32 != 4 || m.NeedleType.String != "ballpoint" || m.NeedleSizeNm.Int32 != 90 ||
			m.BedType.String != "cylinder_bed" || m.Automation.String != "semi_auto" ||
			m.ThreadTension.String != "looser" || m.ThreadTensionNote.String != "на пол-оборота" ||
			m.AttachmentKind.String != "binder" || m.StitchesPerCm.Decimal.String() != "4.5" ||
			m.StitchWidthMm.Decimal.String() != "5.5" || m.Note.String != "Juki MO-6800" {
			t.Errorf("machine profile mismatch: %+v", m)
		}
		p := d.Presses[0]
		if p.ProfileKey != pressKey || p.Label.String != "дублирующий" || p.PressEquipment != "fusing_press" ||
			p.PressOperationType.String != "fusing" || p.PressTemperatureC.Int32 != 140 ||
			p.PressDwellSec.Int32 != 12 || p.PressPressureNCm2.Decimal.String() != "3.5" ||
			!p.PressSteam.Valid || p.PressSteam.Bool || p.PressCloth.String != "teflon_sheet" ||
			p.Note.String != "Veit 2000" {
			t.Errorf("press profile mismatch: %+v", p)
		}
		mo := got.Operations[0]
		if mo.OperationType != entity.OpTypeMachine || mo.MachineType.String != "overlock" ||
			mo.MachineProfileKey.String != machineKey || mo.ThreadCount.Int32 != 5 ||
			mo.NeedleType.String != "stretch" || mo.NeedleSizeNm.Int32 != 80 ||
			mo.ThreadTension.String != "tighter" || mo.ThreadTensionNote.String != "туже на 0.5" ||
			mo.StitchWidthMm.Decimal.String() != "6.5" {
			t.Errorf("machine step mismatch: %+v", mo)
		}
		po := got.Operations[1]
		if po.OperationType != entity.OpTypeFusing || po.PressEquipment.String != "fusing_press" ||
			po.PressProfileKey.String != pressKey || po.PressTemperatureC.Int32 != 150 ||
			po.PressDwellSec.Int32 != 15 || po.PressPressureNCm2.Decimal.String() != "4.5" ||
			!po.PressSteam.Valid || !po.PressSteam.Bool || po.PressCloth.String != "damp_press_cloth" {
			t.Errorf("press step mismatch: %+v", po)
		}
		// The machine step must not have picked up press columns, or the reverse: a shadow value is
		// read by nothing, printed by nothing, and believed by the next editor.
		if mo.PressEquipment.Valid || po.MachineType.Valid {
			t.Errorf("blocks bled into each other: machine=%+v press=%+v", mo, po)
		}
	}
}

// TestEquipmentDefaultsWrapperPresence: the wrapper — not a flag, not an empty list — is what says
// «this payload speaks about equipment». nil preserves the stored park, present replaces it.
func TestEquipmentDefaultsWrapperPresence(t *testing.T) {
	absent, err := ConvertPbTechCardInsertToEntity(equipCard(&pb_common.TechCardConstruction{Notes: "x"}, true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if absent.Construction.EquipmentDefaults != nil {
		t.Errorf("an absent wrapper must stay nil so the store preserves the stored profiles: %+v",
			absent.Construction.EquipmentDefaults)
	}
	empty, err := ConvertPbTechCardInsertToEntity(equipCard(&pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{},
	}, true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if empty.Construction.EquipmentDefaults == nil {
		t.Errorf("an empty wrapper is «delete them all», not «say nothing»")
	}
	// A card with no profiles at all still emits the wrapper: the clone re-parses this payload, and a
	// nil there would read as «the payload did not speak» — the one reading that loses the park.
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *absent}, CostingFx{})
	if out.TechCard.Construction.EquipmentDefaults == nil {
		t.Errorf("the read wrapper must always be present, even when the card has no profiles")
	}
}

// TestLegacyOperationTypeCanonicalisation walks all nine legacy wire values. The expected machines
// are written out here on purpose: deriving them from entity.LegacyOperationMachineType would make
// the test agree with the map by construction and check nothing.
func TestLegacyOperationTypeCanonicalisation(t *testing.T) {
	cases := []struct {
		pb      pb_common.TechCardOperationType
		machine string
	}{
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH, "lockstitch"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_DOUBLE_NEEDLE, "lockstitch_double_needle"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OVERLOCK, "overlock"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_COVERSTITCH, "coverstitch"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_CHAINSTITCH, "chainstitch"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BLINDHEM, "blindstitch"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BARTACK, "bartack"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTONHOLE, "buttonhole"},
		{pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTON_ATTACH, "button_attach"},
	}
	for _, c := range cases {
		// The legacy payload is what an OLD bundle sends, so it is parsed NOT aware — that path must
		// stay open forever (release snapshots are protojson holding exactly these names).
		got, err := ConvertPbTechCardInsertToEntity(equipCard(nil, false,
			&pb_common.TechCardOperation{OperationType: c.pb, Zone: zoneOuter}))
		if err != nil {
			t.Fatalf("%v: %v", c.pb, err)
		}
		op := got.Operations[0]
		if op.OperationType != entity.OpTypeMachine || op.MachineType.String != c.machine {
			t.Errorf("%v canonicalised to (%s, %q), want (machine, %q)", c.pb, op.OperationType, op.MachineType.String, c.machine)
		}
		// And it goes back out in the NEW vocabulary only — a client has one set of words to render.
		out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
		emitted := out.TechCard.Operations[0]
		if emitted.OperationType != pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE ||
			emitted.MachineType.String() != "TECH_CARD_MACHINE_TYPE_"+upperToken(c.machine) {
			t.Errorf("%v emitted as (%v, %v), want (MACHINE, %s)", c.pb, emitted.OperationType, emitted.MachineType, c.machine)
		}
	}

	// A legacy type that AGREES with an explicitly sent machine goes through silently.
	agree, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, &pb_common.TechCardOperation{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OVERLOCK,
		Zone:          zoneOuter,
		MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
	}))
	if err != nil {
		t.Fatalf("a legacy type agreeing with the sent machine must be accepted: %v", err)
	}
	if agree.Operations[0].MachineType.String != "overlock" {
		t.Errorf("agreeing payload mismatch: %+v", agree.Operations[0])
	}

	// A legacy type that CONTRADICTS the sent machine is two answers to one question — refused,
	// because silently preferring either one puts a machine nobody chose on the printed sheet.
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, &pb_common.TechCardOperation{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH,
		Zone:          zoneOuter,
		MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
	})); err == nil {
		t.Errorf("expected a violation when the legacy type and machine_type disagree")
	}
}

func upperToken(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}

// TestNoneIsNotUnknown is the pair the inheritance model rests on: NULL means «take the profile's»,
// and without a NONE token a step could not contradict its profile at all.
func TestNoneIsNotUnknown(t *testing.T) {
	none, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true,
		&pb_common.TechCardOperation{
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:           zoneOuter,
			MachineType:    pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			AttachmentKind: pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_NONE,
		},
		&pb_common.TechCardOperation{
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
			Zone:           zoneHem,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			PressCloth:     pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_NONE,
		}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if none.Operations[0].AttachmentKind.String != attachmentKindNone {
		t.Errorf("NONE must store the 'none' token, not NULL: %+v", none.Operations[0].AttachmentKind)
	}
	if none.Operations[1].PressCloth.String != "none" {
		t.Errorf("NONE press cloth must store 'none': %+v", none.Operations[1].PressCloth)
	}

	unknown, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true,
		equipMachineOp(zoneOuter, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH),
		&pb_common.TechCardOperation{
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
			Zone:           zoneHem,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
		}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if unknown.Operations[0].AttachmentKind.Valid || unknown.Operations[1].PressCloth.Valid {
		t.Errorf("UNKNOWN must stay NULL («inherit»): %+v %+v",
			unknown.Operations[0].AttachmentKind, unknown.Operations[1].PressCloth)
	}

	// …and back out: 'none' is NONE again, NULL is UNKNOWN again. A read that collapsed them would
	// undo the distinction on the very next save.
	back := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *none}, CostingFx{})
	if back.TechCard.Operations[0].AttachmentKind != pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_NONE ||
		back.TechCard.Operations[1].PressCloth != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_NONE {
		t.Errorf("'none' must be emitted as NONE: %+v / %+v",
			back.TechCard.Operations[0].AttachmentKind, back.TechCard.Operations[1].PressCloth)
	}
	backUnknown := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *unknown}, CostingFx{})
	if backUnknown.TechCard.Operations[0].AttachmentKind != pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_UNKNOWN ||
		backUnknown.TechCard.Operations[1].PressCloth != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_UNKNOWN {
		t.Errorf("NULL must be emitted as UNKNOWN: %+v / %+v",
			backUnknown.TechCard.Operations[0].AttachmentKind, backUnknown.TechCard.Operations[1].PressCloth)
	}

	// A size next to «runs bare» measures a tool the step just said it does not use.
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, &pb_common.TechCardOperation{
		OperationType:    pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:             zoneOuter,
		MachineType:      pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		AttachmentKind:   pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_NONE,
		AttachmentSizeMm: dec("8"),
	})); err == nil {
		t.Errorf("expected a violation for an attachment size on a step that runs bare")
	}
}

// TestPressSteamIsThreeValued: absent, false and true are three different answers, and false is a
// real instruction («без пара») that must not read back as «not stated».
func TestPressSteamIsThreeValued(t *testing.T) {
	steam := func(v *bool) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN,
			Zone:           zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			PressSteam:     v,
		}
	}
	got, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, steam(nil), steam(pbBool(false)), steam(pbBool(true))))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Operations[0].PressSteam.Valid ||
		!got.Operations[1].PressSteam.Valid || got.Operations[1].PressSteam.Bool ||
		!got.Operations[2].PressSteam.Valid || !got.Operations[2].PressSteam.Bool {
		t.Fatalf("press_steam collapsed: %+v %+v %+v",
			got.Operations[0].PressSteam, got.Operations[1].PressSteam, got.Operations[2].PressSteam)
	}
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	ops := out.TechCard.Operations
	if ops[0].PressSteam != nil || ops[1].PressSteam == nil || *ops[1].PressSteam ||
		ops[2].PressSteam == nil || !*ops[2].PressSteam {
		t.Errorf("press_steam emitted wrong: %v %v %v", ops[0].PressSteam, ops[1].PressSteam, ops[2].PressSteam)
	}
}

// TestMachineFieldsRequiredOnlyWhenAware is the capability half of the contract in the parser: a
// bundle that knows the fields must fill them in, and a bundle that does not must keep saving
// exactly as before — including the FUSING step it has always been able to send, which knows
// nothing about press equipment.
func TestMachineFieldsRequiredOnlyWhenAware(t *testing.T) {
	fusing := func() *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
			Zone:          zoneCollar,
		}
	}
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, fusing())); err == nil {
		t.Errorf("an aware payload must name the press equipment on a fusing step")
	}
	legacy, err := ConvertPbTechCardInsertToEntity(equipCard(nil, false, fusing()))
	if err != nil {
		t.Fatalf("a NOT-aware legacy fusing step must save exactly as it did: %v", err)
	}
	if legacy.Operations[0].OperationType != entity.OpTypeFusing || legacy.Operations[0].PressEquipment.Valid {
		t.Errorf("legacy fusing step mismatch: %+v", legacy.Operations[0])
	}
	if legacy.MachineFieldsAware {
		t.Errorf("the transport flag must ride through onto the entity as sent")
	}

	// The same asymmetry on the machine axis.
	bareMachine := func() *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:          zoneOuter,
		}
	}
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, bareMachine())); err == nil {
		t.Errorf("an aware payload must name the machine on a machine step")
	}
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, false, bareMachine())); err != nil {
		t.Errorf("a not-aware payload must not be judged on fields its bundle cannot see: %v", err)
	}

	aware, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true,
		equipMachineOp(zoneOuter, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ZIGZAG)))
	if err != nil {
		t.Fatalf("a complete aware payload must pass: %v", err)
	}
	if !aware.MachineFieldsAware || aware.Operations[0].MachineType.String != "zigzag" {
		t.Errorf("aware payload mismatch: %+v", aware.Operations[0])
	}
}

// TestBlocksBelongToTheirStepType: a setting on a step that cannot use it is a shadow value —
// nothing reads it, nothing prints it, and the next editor believes it.
func TestBlocksBelongToTheirStepType(t *testing.T) {
	bad := map[string]*pb_common.TechCardOperation{
		"machine on a handwork step": {
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK,
			Zone:          zoneOuter,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		},
		"thread count on a fusing step": {
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
			Zone:           zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS,
			ThreadCount:    4,
		},
		"stitch width on a press step": {
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
			Zone:           zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			StitchWidthMm:  dec("3"),
		},
		"press temperature on a machine step": {
			OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:              zoneOuter,
			MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			PressTemperatureC: 140,
		},
		"press steam on an other step": {
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OTHER,
			Zone:          zoneOuter,
			PressSteam:    pbBool(false),
		},
		"press cloth on a machine step": {
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:          zoneOuter,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			PressCloth:    pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_NONE,
		},
	}
	for name, op := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, op)); err == nil {
			t.Errorf("case %q: expected a violation, got nil", name)
		}
	}
}

// TestThreadTensionNoteNeedsTheScale: the free qualifier alone is not a setting anybody can
// reproduce, exactly as an attachment size with no attachment is not a tool.
func TestThreadTensionNoteNeedsTheScale(t *testing.T) {
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, &pb_common.TechCardOperation{
		OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:              zoneOuter,
		MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		ThreadTensionNote: "чуть слабее",
	})); err == nil {
		t.Errorf("expected a violation for a tension note with no tension")
	}
	if _, err := ConvertPbTechCardInsertToEntity(equipCard(&pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{{
				MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
				ThreadTensionNote: "на 0.5 туже",
			}},
		},
	}, true)); err == nil {
		t.Errorf("the same rule holds on a profile")
	}
	// With the scale, the note is kept — and trimmed, because a trailing space would move the
	// section digest and mark every approval of it as edited.
	got, err := ConvertPbTechCardInsertToEntity(equipCard(nil, true, &pb_common.TechCardOperation{
		OperationType:     pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:              zoneOuter,
		MachineType:       pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		ThreadTension:     pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_OTHER,
		ThreadTensionNote: "  дил 3.5  ",
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Operations[0].ThreadTensionNote.String != "дил 3.5" {
		t.Errorf("tension note not trimmed: %q", got.Operations[0].ThreadTensionNote.String)
	}
}

// TestProfileKeyMintAndDuplicates: identity is the durable key, exactly as for BOM lines and pieces.
func TestProfileKeyMintAndDuplicates(t *testing.T) {
	minted, err := ConvertPbTechCardInsertToEntity(equipCard(&pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{
				{MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
				// Two profiles of the SAME type are normal — «two identical machines» is an answer,
				// not a duplicate to collapse.
				{MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
			},
			Presses: []*pb_common.TechCardPressProfile{
				{PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON},
			},
		},
	}, true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := minted.Construction.EquipmentDefaults
	keys := map[string]bool{}
	for _, k := range []string{d.Machines[0].ProfileKey, d.Machines[1].ProfileKey, d.Presses[0].ProfileKey} {
		if len(k) != 26 {
			t.Errorf("a minted profile key must be 26 characters: %q", k)
		}
		if keys[k] {
			t.Errorf("minted keys collided: %q", k)
		}
		keys[k] = true
	}

	// One key space across both lists — a step's reference names a key, not a key-and-a-kind.
	dup := func(a, b string) *pb_common.TechCardInsert {
		return equipCard(&pb_common.TechCardConstruction{
			EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
				Machines: []*pb_common.TechCardMachineProfile{
					{ProfileKey: a, MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
				},
				Presses: []*pb_common.TechCardPressProfile{
					{ProfileKey: b, PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON},
				},
			},
		}, true)
	}
	if _, err := ConvertPbTechCardInsertToEntity(dup(machineKey, machineKey)); err == nil {
		t.Errorf("expected a violation for one key claimed by two profiles")
	}
	if _, err := ConvertPbTechCardInsertToEntity(dup(machineKey, pressKey)); err != nil {
		t.Errorf("distinct keys must be accepted: %v", err)
	}
	// A malformed key is refused here rather than reaching CHAR(26) as a driver-level 1406.
	if _, err := ConvertPbTechCardInsertToEntity(dup("short", pressKey)); err == nil {
		t.Errorf("expected a violation for a malformed profile key")
	}
}

// TestProfileRequiredFieldsAndRanges: the type IS the profile, and every band is a sanity bound that
// catches a typo before it reaches the shop floor («14 °C» for «140»).
func TestProfileRequiredFieldsAndRanges(t *testing.T) {
	withMachine := func(m *pb_common.TechCardMachineProfile) *pb_common.TechCardInsert {
		return equipCard(&pb_common.TechCardConstruction{
			EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{Machines: []*pb_common.TechCardMachineProfile{m}},
		}, true)
	}
	withPress := func(p *pb_common.TechCardPressProfile) *pb_common.TechCardInsert {
		return equipCard(&pb_common.TechCardConstruction{
			EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{Presses: []*pb_common.TechCardPressProfile{p}},
		}, true)
	}
	lock := pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH
	iron := pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON
	bad := map[string]*pb_common.TechCardInsert{
		"machine profile without a machine": withMachine(&pb_common.TechCardMachineProfile{ThreadCount: 4}),
		"press profile without equipment":   withPress(&pb_common.TechCardPressProfile{PressTemperatureC: 140}),
		"thread count over the band":        withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, ThreadCount: 21}),
		"needle finer than made":            withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, NeedleSizeNm: 9}),
		"stitch width off the scale":        withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, StitchWidthMm: dec("25")}),
		"stitch width over-precise":         withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, StitchWidthMm: dec("5.55")}),
		"label past the column":             withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, Label: xChars(65)}),
		"note past the column":              withMachine(&pb_common.TechCardMachineProfile{MachineType: lock, Note: xChars(256)}),
		"140 typed as 14":                   withPress(&pb_common.TechCardPressProfile{PressEquipment: iron, PressTemperatureC: 14}),
		"dwell past five minutes":           withPress(&pb_common.TechCardPressProfile{PressEquipment: iron, PressDwellSec: 301}),
		"pressure off the band":             withPress(&pb_common.TechCardPressProfile{PressEquipment: iron, PressPressureNCm2: dec("101")}),
		// A press profile declares WHICH PROCESS it is for, and only the three ВТО verbs are answers.
		"press profile for a machine step": withPress(&pb_common.TechCardPressProfile{PressEquipment: iron,
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE}),
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected a violation, got nil", name)
		}
	}
	// A universal press profile (no operation type) is legal and stores NULL.
	got, err := ConvertPbTechCardInsertToEntity(withPress(&pb_common.TechCardPressProfile{PressEquipment: iron}))
	if err != nil {
		t.Fatalf("a universal press profile must be accepted: %v", err)
	}
	if got.Construction.EquipmentDefaults.Presses[0].PressOperationType.Valid {
		t.Errorf("a universal profile must store NULL, not a token: %+v",
			got.Construction.EquipmentDefaults.Presses[0].PressOperationType)
	}
}

func xChars(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// TestStepProfileReference: a stale key detaches (tidying the defaults must not block saving the
// steps), a key naming a profile of a different type is refused, and with no wrapper in the payload
// there is nothing to resolve against so the key is kept.
func TestStepProfileReference(t *testing.T) {
	park := &pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{
				{ProfileKey: machineKey, MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
			},
			Presses: []*pb_common.TechCardPressProfile{
				{ProfileKey: pressKey, PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON},
			},
		},
	}
	step := func(key string, m pb_common.TechCardMachineType) *pb_common.TechCardOperation {
		op := equipMachineOp(zoneOuter, m)
		op.MachineProfileKey = key
		return op
	}
	linked, err := ConvertPbTechCardInsertToEntity(equipCard(park, true,
		step(machineKey, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK)))
	if err != nil {
		t.Fatalf("a matching reference must be accepted: %v", err)
	}
	if linked.Operations[0].MachineProfileKey.String != machineKey {
		t.Errorf("reference lost: %+v", linked.Operations[0].MachineProfileKey)
	}

	stale, err := ConvertPbTechCardInsertToEntity(equipCard(park, true,
		step("01J0GONEKEY00000000000001A", pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK)))
	if err != nil {
		t.Fatalf("a stale reference must detach, not fail the save: %v", err)
	}
	if stale.Operations[0].MachineProfileKey.Valid {
		t.Errorf("a stale reference must be cleared: %+v", stale.Operations[0].MachineProfileKey)
	}

	if _, err := ConvertPbTechCardInsertToEntity(equipCard(park, true,
		step(machineKey, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ZIGZAG))); err == nil {
		t.Errorf("expected a violation when the step's machine and its profile disagree")
	}

	// No wrapper: nothing to resolve against, and the reference is soft by design.
	kept, err := ConvertPbTechCardInsertToEntity(equipCard(&pb_common.TechCardConstruction{Notes: "x"}, true,
		step(machineKey, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if kept.Operations[0].MachineProfileKey.String != machineKey {
		t.Errorf("with no park in the payload the key must pass through: %+v", kept.Operations[0].MachineProfileKey)
	}
}

// TestOperationRangeBands covers the step-side bands, which are the same numbers as the profile's —
// an override and the thing it overrides have to answer the same question the same way.
func TestOperationRangeBands(t *testing.T) {
	machine := func(mut func(*pb_common.TechCardOperation)) *pb_common.TechCardInsert {
		op := equipMachineOp(zoneOuter, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH)
		mut(op)
		return equipCard(nil, true, op)
	}
	press := func(mut func(*pb_common.TechCardOperation)) *pb_common.TechCardInsert {
		op := &pb_common.TechCardOperation{
			OperationType:  pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
			Zone:           zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_PRESS,
		}
		mut(op)
		return equipCard(nil, true, op)
	}
	bad := map[string]*pb_common.TechCardInsert{
		"threads":     machine(func(o *pb_common.TechCardOperation) { o.ThreadCount = 21 }),
		"needle":      machine(func(o *pb_common.TechCardOperation) { o.NeedleSizeNm = 301 }),
		"width":       machine(func(o *pb_common.TechCardOperation) { o.StitchWidthMm = dec("20.5") }),
		"temperature": press(func(o *pb_common.TechCardOperation) { o.PressTemperatureC = 251 }),
		"dwell":       press(func(o *pb_common.TechCardOperation) { o.PressDwellSec = 301 }),
		"pressure":    press(func(o *pb_common.TechCardOperation) { o.PressPressureNCm2 = dec("0.5") }),
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected a violation, got nil", name)
		}
	}
	// Zero stitch width is a LEGAL setting (a straight stitch on a machine that can swing) — unlike
	// the density, where zero would mean a seam with no stitches in it.
	got, err := ConvertPbTechCardInsertToEntity(machine(func(o *pb_common.TechCardOperation) { o.StitchWidthMm = dec("0") }))
	if err != nil {
		t.Fatalf("a zero stitch width must be accepted: %v", err)
	}
	if !got.Operations[0].StitchWidthMm.Valid || !got.Operations[0].StitchWidthMm.Decimal.IsZero() {
		t.Errorf("a zero stitch width must survive as a PRESENT zero: %+v", got.Operations[0].StitchWidthMm)
	}
}

// TestProfileKeyIdentityIsCaseSensitive pins the side of the disagreement Go has always been on, so
// that the column's collation cannot drift back to the other one unnoticed.
//
// The durable key IS the identity of a profile row, and this converter reads that identity byte for
// byte — twice: the park's duplicate check, and the step's reference resolution. Until 0306 spelled
// `COLLATE utf8mb4_bin` on the column, the database read it case-INSENSITIVELY (utf8mb3_general_ci on
// prod, utf8mb4_0900_ai_ci in the container), and the two halves broke in opposite directions from
// the same cause: two keys differing only in case passed the check here and died on
// uq_equipment_profile_key as a driver 1062, while a step whose reference differed by case passed the
// format check, matched nothing here and silently detached into NULL — which the full replace then
// preserved forever.
//
// The case is NEVER normalised. The key is minted by the client and round-tripped verbatim; folding
// it to make a comparison work is how one name ends up covering two identities (the scope_key trap).
func TestProfileKeyIdentityIsCaseSensitive(t *testing.T) {
	const upper = "01J0CASEKEY0000000000000AB"
	lower := strings.ToLower(upper)
	if upper == lower || strings.ToUpper(lower) != upper {
		t.Fatalf("the fixture must differ ONLY in case: %q vs %q", upper, lower)
	}

	park := &pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{
				{ProfileKey: upper, MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
				{ProfileKey: lower, MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
			},
		},
	}
	two, err := ConvertPbTechCardInsertToEntity(equipCard(park, true))
	if err != nil {
		t.Fatalf("two keys differing only in case are two profiles: %v", err)
	}
	got := []string{two.Construction.EquipmentDefaults.Machines[0].ProfileKey,
		two.Construction.EquipmentDefaults.Machines[1].ProfileKey}
	if got[0] != upper || got[1] != lower {
		t.Errorf("the keys were rewritten: %q", got)
	}

	// The step's reference is resolved with the same eye. It names ONE of the two rows, and the one
	// it names is the one it spelled.
	step := func(key string) *pb_common.TechCardOperation {
		op := equipMachineOp(zoneOuter, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK)
		op.MachineProfileKey = key
		return op
	}
	for _, want := range []string{upper, lower} {
		linked, err := ConvertPbTechCardInsertToEntity(equipCard(park, true, step(want)))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if linked.Operations[0].MachineProfileKey.String != want {
			t.Errorf("reference %q resolved to %q", want, linked.Operations[0].MachineProfileKey.String)
		}
	}

	// And against a park that holds only one spelling, the other spelling is a key naming nothing —
	// which detaches, exactly as any other stale key does. That is the honest answer only because the
	// column agrees the two are different; under a CI collation the row would have matched and this
	// detachment would have been a lie about storage.
	onlyUpper := &pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{
				{ProfileKey: upper, MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK},
			},
		},
	}
	detached, err := ConvertPbTechCardInsertToEntity(equipCard(onlyUpper, true, step(lower)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if detached.Operations[0].MachineProfileKey.Valid {
		t.Errorf("a key naming no profile must detach: %+v", detached.Operations[0].MachineProfileKey)
	}
}
