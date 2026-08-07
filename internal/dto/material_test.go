package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestMaterialPurposeEnumNoDrift asserts every non-UNKNOWN proto MaterialPurpose value maps (via
// materialPurposePbToEntity) to a valid entity purpose and the sets match in size (#40). Mirrors
// TestMaterialClassEnumNoDrift; the entity<->DB leg is TestMaterialPurposeDBCheckNoDrift.
func TestMaterialPurposeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.MaterialPurpose_name {
		if pb_common.MaterialPurpose(v) == pb_common.MaterialPurpose_MATERIAL_PURPOSE_UNKNOWN {
			continue
		}
		protoValues++
		p, ok := materialPurposePbToEntity[pb_common.MaterialPurpose(v)]
		if !ok {
			t.Errorf("proto MaterialPurpose %s has no entity mapping", name)
			continue
		}
		if !entity.ValidMaterialPurposes[p] {
			t.Errorf("proto MaterialPurpose %s maps to invalid entity purpose %q", name, p)
		}
	}
	if protoValues != len(materialPurposePbToEntity) {
		t.Errorf("proto material purpose values (%d) != entity mapping size (%d)", protoValues, len(materialPurposePbToEntity))
	}
	if protoValues != len(entity.ValidMaterialPurposes) {
		t.Errorf("proto material purpose values (%d) != entity.ValidMaterialPurposes (%d)", protoValues, len(entity.ValidMaterialPurposes))
	}
}

// TestConvertMaterialImageAndPurpose covers the #39/#40 DTO legs: image_id + purpose round-trip, an
// UNKNOWN purpose maps to the empty entity value (the store defaults it to 'both'), a resolved image
// is emitted as MediaFull, and a negative image_id is rejected.
func TestConvertMaterialImageAndPurpose(t *testing.T) {
	// Produce a valid section enum via an entity->pb round-trip (avoids hard-coding the constant).
	base := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		MaterialInsert: entity.MaterialInsert{Name: "img", Section: "fabric"},
	}})

	base.Purpose = pb_common.MaterialPurpose_MATERIAL_PURPOSE_SAMPLE
	base.ImageId = 7
	ins, err := ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if ins.Purpose != string(entity.MaterialPurposeSample) {
		t.Errorf("purpose = %q, want sample", ins.Purpose)
	}
	if !ins.ImageId.Valid || ins.ImageId.Int32 != 7 {
		t.Errorf("image_id = %+v, want {7 true}", ins.ImageId)
	}

	// UNKNOWN purpose -> empty entity value (the store normalises it to 'both').
	base.Purpose = pb_common.MaterialPurpose_MATERIAL_PURPOSE_UNKNOWN
	ins, err = ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert unknown: %v", err)
	}
	if ins.Purpose != "" {
		t.Errorf("unknown purpose = %q, want empty", ins.Purpose)
	}

	// A negative image_id is rejected.
	base.ImageId = -1
	if _, err := ConvertPbMaterialToEntityInsert(base); err == nil {
		t.Error("negative image_id should be rejected")
	}

	// Read side: a resolved image + purpose are emitted.
	out := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		Id: 1,
		MaterialInsert: entity.MaterialInsert{
			Name: "img", Section: "fabric",
			ImageId: sql.NullInt32{Int32: 42, Valid: true},
			Purpose: string(entity.MaterialPurposeProduction),
		},
		Image: &entity.MediaFull{Id: 42},
	}})
	if out.Purpose != pb_common.MaterialPurpose_MATERIAL_PURPOSE_PRODUCTION {
		t.Errorf("out.Purpose = %v, want PRODUCTION", out.Purpose)
	}
	if out.ImageId != 42 {
		t.Errorf("out.ImageId = %d, want 42", out.ImageId)
	}
	if out.Image == nil || out.Image.Id != 42 {
		t.Errorf("out.Image = %+v, want id 42", out.Image)
	}
}

func TestConvertMaterialPreservesUpdatePresenceMarkers(t *testing.T) {
	base := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		MaterialInsert: entity.MaterialInsert{Name: "legacy", Section: "fabric"},
	}})
	base.MaterialClass = pb_common.MaterialClass_MATERIAL_CLASS_UNKNOWN
	base.Attributes = nil
	base.CompositionEntries = nil

	ins, err := ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert absent fields: %v", err)
	}
	if ins.MaterialClass != "" || ins.FabricAttr != nil || ins.HardwareAttr != nil ||
		ins.ThreadAttr != nil || ins.PackagingAttr != nil || ins.CompositionEntries != nil {
		t.Fatalf("absence markers were not preserved: %+v", ins)
	}

	base.MaterialClass = pb_common.MaterialClass_MATERIAL_CLASS_FABRIC
	base.Attributes = &pb_common.Material_FabricAttrs{FabricAttrs: &pb_common.MaterialFabricAttrs{}}
	ins, err = ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert present empty attrs: %v", err)
	}
	if ins.FabricAttr == nil {
		t.Fatal("a present empty attrs message must remain distinguishable from absence")
	}
}

// TestConvertMaterialCuttingCoefficientPresence pins Ф5а.2's THREE write states. A full replace on
// this column would let a stale admin tab — or any client in the window between the backend and the
// client deploy — erase a coefficient an operator set, and erase it without a trace, because the
// catalogue carries neither a signed digest nor an edit journal.
func TestConvertMaterialCuttingCoefficientPresence(t *testing.T) {
	base := func() *pb_common.Material {
		return &pb_common.Material{Name: "Twill", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC}
	}

	// ABSENT — the field was not sent at all. Leave the stored value alone.
	ins, err := ConvertPbMaterialToEntityInsert(base())
	if err != nil {
		t.Fatalf("absent coefficient: %v", err)
	}
	if !ins.CuttingCoefficientOmitted {
		t.Error("a nil cutting_coefficient must be reported as OMITTED, not as a clear")
	}
	if ins.CuttingCoefficient.Valid {
		t.Errorf("omitted must carry no value: %+v", ins.CuttingCoefficient)
	}

	// PRESENT but empty — the explicit "clear". This is the only way to unset the dial.
	cleared := base()
	cleared.CuttingCoefficient = &pb_decimal.Decimal{Value: ""}
	ins, err = ConvertPbMaterialToEntityInsert(cleared)
	if err != nil {
		t.Fatalf("cleared coefficient: %v", err)
	}
	if ins.CuttingCoefficientOmitted {
		t.Error("an explicitly sent empty decimal is a CLEAR, not an omission")
	}
	if ins.CuttingCoefficient.Valid {
		t.Errorf("a clear must store NULL: %+v", ins.CuttingCoefficient)
	}

	// PRESENT with a value — set it.
	set := base()
	set.CuttingCoefficient = dec("1.06")
	ins, err = ConvertPbMaterialToEntityInsert(set)
	if err != nil {
		t.Fatalf("set coefficient: %v", err)
	}
	if ins.CuttingCoefficientOmitted || !ins.CuttingCoefficient.Valid ||
		ins.CuttingCoefficient.Decimal.String() != "1.06" {
		t.Errorf("set coefficient = %+v (omitted=%v)", ins.CuttingCoefficient, ins.CuttingCoefficientOmitted)
	}

	// A multiplier below 1 shaves a requirement below the norm; above 3 is the fat-fingered "103".
	for _, bad := range []string{"0.9", "3.5"} {
		m := base()
		m.CuttingCoefficient = dec(bad)
		if _, err := ConvertPbMaterialToEntityInsert(m); err == nil {
			t.Errorf("cutting_coefficient %s must be rejected", bad)
		}
	}

	// DECIMAL(6,4) does not REJECT a fifth decimal place, it silently rounds it — so the operator's
	// number and the planner's number would differ with nothing saying so.
	overPrecise := base()
	overPrecise.CuttingCoefficient = dec("1.03456")
	if _, err := ConvertPbMaterialToEntityInsert(overPrecise); err == nil {
		t.Error("cutting_coefficient 1.03456 must be rejected, not silently rounded to 1.0346 by MySQL")
	}
}

// TestConvertMaterialFabricThicknessPresence pins Ф4.8's three write states on the article's half of
// the предел стопки. Same law as the coefficient above, same failure if it is missed — but the
// consequence is worse than a lost setting: an erased thickness silently turns a живая проверка
// высоты стопки back into UNKNOWN across every настил cut from that article, and nothing on the
// screen says the number used to be there.
func TestConvertMaterialFabricThicknessPresence(t *testing.T) {
	base := func() *pb_common.Material {
		return &pb_common.Material{Name: "Poplin", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC}
	}

	// ABSENT — the field was not sent at all. Leave the stored value alone.
	ins, err := ConvertPbMaterialToEntityInsert(base())
	if err != nil {
		t.Fatalf("absent thickness: %v", err)
	}
	if !ins.FabricThicknessMmOmitted {
		t.Error("a nil fabric_thickness_mm must be reported as OMITTED, not as a clear")
	}
	if ins.FabricThicknessMm.Valid {
		t.Errorf("omitted must carry no value: %+v", ins.FabricThicknessMm)
	}

	// PRESENT but empty — the explicit clear, i.e. «этот замер отозван». It must stay reachable, or an
	// operator who typed a wrong thickness could never take the wrong verdict back off the screen.
	cleared := base()
	cleared.FabricThicknessMm = &pb_decimal.Decimal{Value: ""}
	ins, err = ConvertPbMaterialToEntityInsert(cleared)
	if err != nil {
		t.Fatalf("cleared thickness: %v", err)
	}
	if ins.FabricThicknessMmOmitted {
		t.Error("an explicitly sent empty decimal is a CLEAR, not an omission")
	}
	if ins.FabricThicknessMm.Valid {
		t.Errorf("a clear must store NULL: %+v", ins.FabricThicknessMm)
	}

	// PRESENT with a value — set it. 0.3 mm поплин, straight out of the field's own hint text.
	set := base()
	set.FabricThicknessMm = dec("0.3")
	ins, err = ConvertPbMaterialToEntityInsert(set)
	if err != nil {
		t.Fatalf("set thickness: %v", err)
	}
	if ins.FabricThicknessMmOmitted || !ins.FabricThicknessMm.Valid ||
		ins.FabricThicknessMm.Decimal.String() != "0.3" {
		t.Errorf("set thickness = %+v (omitted=%v)", ins.FabricThicknessMm, ins.FabricThicknessMmOmitted)
	}

	// ZERO IS NOT A THICKNESS AND IS NOT «НЕ ЗАМЕРЕНО». Accepting it would make every stack 0 cm tall
	// and therefore within any limit — a confident verdict built out of missing data, which is the one
	// outcome «нет толщины — нет проверки, не догадка» exists to forbid. The way to say "unmeasured"
	// is the CLEAR above.
	zero := base()
	zero.FabricThicknessMm = dec("0")
	if _, err := ConvertPbMaterialToEntityInsert(zero); err == nil {
		t.Error("fabric_thickness_mm 0 must be rejected — clearing is how «не замерено» is recorded")
	}
	negative := base()
	negative.FabricThicknessMm = dec("-0.3")
	if _, err := ConvertPbMaterialToEntityInsert(negative); err == nil {
		t.Error("a negative fabric_thickness_mm must be rejected")
	}

	// Millimetres, one ply. 500 is a unit mistake (metres? the whole roll?), and the DB CHECK caps at
	// 50 — reaching it as a raw 500 instead of a readable message is the failure this guards.
	tooThick := base()
	tooThick.FabricThicknessMm = dec("500")
	if _, err := ConvertPbMaterialToEntityInsert(tooThick); err == nil {
		t.Error("fabric_thickness_mm 500 must be rejected before MySQL's CHECK turns it into a 500")
	}

	// DECIMAL(6,3) does not REJECT a fourth decimal place, it silently rounds it — so the operator's
	// measurement and the planner's would differ with nothing anywhere saying so.
	overPrecise := base()
	overPrecise.FabricThicknessMm = dec("0.1234")
	if _, err := ConvertPbMaterialToEntityInsert(overPrecise); err == nil {
		t.Error("fabric_thickness_mm 0.1234 must be rejected, not silently rounded to 0.123 by MySQL")
	}
}

// An article with no thickness must produce NO thickness on the wire — not "0". A zero would let the
// lay path compute a 0 cm stack that comfortably "fits", which is exactly the verdict-out-of-nothing
// Ф4.8 refuses; absence makes it say UNKNOWN and ask for the cloth to be measured.
func TestConvertMaterialUnmeasuredThicknessTravelsAbsent(t *testing.T) {
	pb := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		MaterialInsert: entity.MaterialInsert{Name: "Chiffon", Section: "fabric"},
	}})
	if pb.GetFabricThicknessMm() != nil {
		t.Errorf("unmeasured thickness must be absent on the wire, got %+v", pb.GetFabricThicknessMm())
	}
}

// EffectiveFabricThicknessMm is the ONE place the "unset" reading lives, and it must refuse to hand
// back a number that would be read as a measurement — including a zero that sneaked past the column
// CHECK in a hand-built fixture.
func TestEffectiveFabricThicknessMmRefusesNonMeasurements(t *testing.T) {
	nd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	for _, tc := range []struct {
		name  string
		in    decimal.NullDecimal
		valid bool
	}{
		{name: "never measured", in: decimal.NullDecimal{}},
		{name: "zero is not a measurement", in: nd("0")},
		{name: "negative is not a measurement", in: nd("-1")},
		{name: "chiffon", in: nd("0.15"), valid: true},
		{name: "melton", in: nd("2.5"), valid: true},
	} {
		m := &entity.Material{MaterialInsert: entity.MaterialInsert{FabricThicknessMm: tc.in}}
		got := m.EffectiveFabricThicknessMm()
		if got.Valid != tc.valid {
			t.Errorf("%s: valid = %v, want %v (%+v)", tc.name, got.Valid, tc.valid, got)
		}
	}
}
