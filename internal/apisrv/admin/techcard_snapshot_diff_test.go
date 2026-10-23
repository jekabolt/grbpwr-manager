package admin

import (
	"reflect"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func snapshotDiffCard() *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		Name:            "shirt",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_PROTO,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		MoodNote:        proto.String("quiet blue"),
		SizeIds:         []int32{1, 2},
		BomItems: []*pb_common.TechCardBomItem{
			{LineKey: "shell", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "twill"},
			{LineKey: "zip", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip"},
		},
	}
}

func TestTechCardChangedSectionsHasStableLabelsAndBomLineCount(t *testing.T) {
	before := snapshotDiffCard()
	after := proto.Clone(before).(*pb_common.TechCardInsert)
	after.Name = "overshirt"
	after.MoodNote = proto.String("washed blue")
	after.SizeIds = append(after.SizeIds, 3)
	after.BomItems[0].Comment = "pre-wash"

	got := techCardChangedSections(before, after)
	want := []string{"name", "note", "sizes", "bom · 1 line"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("techCardChangedSections() = %#v, want %#v", got, want)
	}
}

func TestTechCardChangedSectionsCountsAddedAndRemovedBomLines(t *testing.T) {
	before := snapshotDiffCard()
	after := proto.Clone(before).(*pb_common.TechCardInsert)
	after.BomItems = []*pb_common.TechCardBomItem{
		proto.Clone(before.BomItems[0]).(*pb_common.TechCardBomItem),
		{LineKey: "button", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "button"},
	}
	after.BomItems[0].Name = "washed twill"

	got := techCardChangedSections(before, after)
	want := []string{"bom · 3 lines"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("techCardChangedSections() = %#v, want %#v", got, want)
	}
}

func TestTechCardChangedSectionsIgnoresReadOnlyProjections(t *testing.T) {
	before := snapshotDiffCard()
	before.BomItems[0].Id = 91
	before.BomItems[0].UnitCode = pb_common.MaterialUnit_MATERIAL_UNIT_M
	before.BomItems[0].PriceSource = "catalog"
	before.BomItems[0].PriceSnapshotAt = timestamppb.Now()
	before.BomItems[0].EffectiveFabricWidthCm = &pb_decimal.Decimal{Value: "145"}
	before.BomItems[0].CuttingCoefficient = &pb_decimal.Decimal{Value: "1.03"}
	before.Patterns = []*pb_common.TechCardSizePattern{{
		LineKey: "front", Url: "https://files.example/front.dxf", ViewUrl: "signed-view",
		DownloadUrl: "signed-download", History: []*pb_common.TechCardPatternRevision{{Revision: 1}},
	}}
	before.Operations = []*pb_common.TechCardOperation{{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		PieceLineKeys: []string{"front"}, PieceIds: []int64{88}, BomItemIds: []int64{91},
	}}
	before.Costing = &pb_common.TechCardCosting{
		CmtCost: &pb_decimal.Decimal{Value: "3"}, UnitCost: &pb_decimal.Decimal{Value: "20"},
		BaseCurrency: "EUR",
	}
	after := proto.Clone(before).(*pb_common.TechCardInsert)
	after.BomItems[0].Id = 0
	after.BomItems[0].UnitCode = pb_common.MaterialUnit_MATERIAL_UNIT_UNKNOWN
	after.BomItems[0].PriceSource = ""
	after.BomItems[0].PriceSnapshotAt = nil
	after.BomItems[0].EffectiveFabricWidthCm = nil
	after.BomItems[0].CuttingCoefficient = nil
	after.Patterns[0].ViewUrl, after.Patterns[0].DownloadUrl, after.Patterns[0].History = "", "", nil
	after.Operations[0].PieceIds, after.Operations[0].BomItemIds = nil, nil
	after.Costing.UnitCost, after.Costing.BaseCurrency = nil, ""

	if got := techCardChangedSections(before, after); len(got) != 0 {
		t.Fatalf("read-only projections produced changes: %#v", got)
	}
}

func TestTechCardChangedSectionsHonoursPreserveOnAbsence(t *testing.T) {
	before := snapshotDiffCard()
	before.CategoryId = 12
	before.Construction = &pb_common.TechCardConstruction{Notes: "keep"}
	before.Packaging = &pb_common.TechCardPackaging{FoldingMethod: "flat"}
	before.Costing = &pb_common.TechCardCosting{CmtCost: &pb_decimal.Decimal{Value: "4"}}
	before.GarmentLabels = []*pb_common.TechCardGarmentLabel{{Key: "brand"}}
	before.PackagingItems = []*pb_common.TechCardPackagingItem{{Key: "tissue"}}
	after := proto.Clone(before).(*pb_common.TechCardInsert)
	after.CategoryId = 0
	after.MeasurementUnit = pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_UNKNOWN
	after.MoodNote = nil
	after.Construction, after.Packaging, after.Costing = nil, nil, nil
	after.GarmentLabels, after.PackagingItems = nil, nil
	after.LabelsAware = false

	if got := techCardChangedSections(before, after); len(got) != 0 {
		t.Fatalf("preserve-on-absence fields produced changes: %#v", got)
	}
}
