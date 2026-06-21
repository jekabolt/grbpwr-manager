package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConvertPbTechCardInsertToEntity(t *testing.T) {
	revDate := timestamppb.New(time.Date(2026, 6, 19, 15, 30, 0, 0, time.UTC))
	valid := &pb_common.TechCardInsert{
		StyleNumber:       "ST-001",
		Name:              "Field Jacket",
		Brand:             "grbpwr",
		Season:            "FW25",
		Stage:             pb_common.TechCardStage_TECH_CARD_STAGE_FIT,
		ApprovalState:     pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_APPROVED,
		ApprovedBy:        "lead",
		ReleasedAt:        revDate,
		MeasurementUnit:   pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_IN,
		TargetGender:      pb_common.GenderEnum_GENDER_ENUM_MALE,
		CategoryId:        3,
		BaseModelId:       7,
		BaseSampleSizeId:  4,
		Currency:          "EUR",
		TargetCost:        &pb_decimal.Decimal{Value: "42.50"},
		TargetRetailPrice: &pb_decimal.Decimal{Value: "180.00"},
		RevisionDate:      revDate,
		Description:       "boxy field jacket",
		SizeIds:           []int32{4, 5, 6},
		ProductIds:        []int32{100, 101},
		Media: []*pb_common.TechCardMediaItem{
			{MediaId: 11, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
			{MediaId: 12}, // unset kind -> defaults to preview
		},
		Callouts: []*pb_common.TechCardCallout{
			{Number: 1, Part: "collar", Description: "two-piece", Dimensions: "h=4cm", MediaId: 11},
		},
		Revisions: []*pb_common.TechCardRevision{
			{Version: "v2", RevisionDate: revDate, Author: "tech", Section: "POM", ChangeNote: "graded"},
		},
	}

	got, err := ConvertPbTechCardInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.StyleNumber != "ST-001" || got.Name != "Field Jacket" {
		t.Errorf("identity mismatch: %+v", got)
	}
	if got.Stage != entity.TechCardStageFit {
		t.Errorf("stage mismatch: %v", got.Stage)
	}
	if !got.TargetGender.Valid || got.TargetGender.String != string(entity.Male) {
		t.Errorf("gender mismatch: %+v", got.TargetGender)
	}
	if !got.CategoryId.Valid || got.CategoryId.Int32 != 3 || !got.BaseModelId.Valid || got.BaseSampleSizeId.Int32 != 4 {
		t.Errorf("fk fields mismatch: %+v", got)
	}
	if !got.TargetCost.Valid || !got.TargetCost.Decimal.Equal(decimal.RequireFromString("42.50")) {
		t.Errorf("target_cost mismatch: %+v", got.TargetCost)
	}
	if want := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC); !got.RevisionDate.Valid || !got.RevisionDate.Time.Equal(want) {
		t.Errorf("revision_date not normalized: %+v", got.RevisionDate)
	}
	if len(got.SizeIds) != 3 || got.SizeIds[2] != 6 || len(got.ProductIds) != 2 {
		t.Errorf("size/product ids mismatch: %+v %+v", got.SizeIds, got.ProductIds)
	}
	if len(got.Media) != 2 || got.Media[0].Kind != entity.TechCardMediaFront || got.Media[1].Kind != entity.TechCardMediaPreview {
		t.Errorf("media mismatch: %+v", got.Media)
	}
	if len(got.Callouts) != 1 || got.Callouts[0].Number != 1 || !got.Callouts[0].Part.Valid {
		t.Errorf("callouts mismatch: %+v", got.Callouts)
	}
	if len(got.Revisions) != 1 || got.Revisions[0].Section.String != "POM" {
		t.Errorf("revisions mismatch: %+v", got.Revisions)
	}
	if got.ApprovalState != entity.TechCardApprovalApproved || got.ApprovedBy.String != "lead" || !got.ReleasedAt.Valid {
		t.Errorf("approval mismatch: state=%v by=%+v released=%+v", got.ApprovalState, got.ApprovedBy, got.ReleasedAt)
	}
	if got.Callouts[0].MediaId.Int32 != 11 {
		t.Errorf("callout media_id mismatch: %+v", got.Callouts[0].MediaId)
	}
	if got.MeasurementUnit != entity.TechCardUnitIn {
		t.Errorf("measurement_unit mismatch: %v", got.MeasurementUnit)
	}

	// defaults: unset stage becomes proto; zero fk ids become NULL.
	def, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "ST-002", Name: "Tee"})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if def.Stage != entity.TechCardStageProto {
		t.Errorf("stage default mismatch: %v", def.Stage)
	}
	if def.ApprovalState != entity.TechCardApprovalDraft {
		t.Errorf("approval_state default mismatch: %v", def.ApprovalState)
	}
	if def.MeasurementUnit != entity.TechCardUnitCm {
		t.Errorf("measurement_unit default mismatch: %v", def.MeasurementUnit)
	}
	if def.BaseModelId.Valid || def.CategoryId.Valid || def.TargetCost.Valid {
		t.Errorf("zero fields should be NULL: %+v", def)
	}

	// base_sample_size_id is allowed when the size range is still empty (early proto).
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "ST-004", Name: "Coat", BaseSampleSizeId: 9,
	}); err != nil {
		t.Errorf("base size with empty size range should be allowed: %v", err)
	}

	// invalid cases.
	bad := map[string]*pb_common.TechCardInsert{
		"nil":               nil,
		"no style":          {Name: "x"},
		"no name":           {StyleNumber: "x"},
		"neg category":      {StyleNumber: "x", Name: "y", CategoryId: -1},
		"bad currency":      {StyleNumber: "x", Name: "y", Currency: "EURO"},
		"dup size":          {StyleNumber: "x", Name: "y", SizeIds: []int32{4, 4}},
		"dup product":       {StyleNumber: "x", Name: "y", ProductIds: []int32{1, 1}},
		"size id zero":      {StyleNumber: "x", Name: "y", SizeIds: []int32{0}},
		"base not in range": {StyleNumber: "x", Name: "y", BaseSampleSizeId: 9, SizeIds: []int32{4, 5}},
		"media id zero":     {StyleNumber: "x", Name: "y", Media: []*pb_common.TechCardMediaItem{{MediaId: 0}}},
		"version too long":  {StyleNumber: "x", Name: "y", Version: string(make([]byte, 65))},
		"neg cost":          {StyleNumber: "x", Name: "y", TargetCost: &pb_decimal.Decimal{Value: "-1"}},
		"cost overflow":     {StyleNumber: "x", Name: "y", TargetCost: &pb_decimal.Decimal{Value: "100000000"}},
		"cost decimals":     {StyleNumber: "x", Name: "y", TargetRetailPrice: &pb_decimal.Decimal{Value: "1.999"}},
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertEntityTechCardToPb(t *testing.T) {
	tc := &entity.TechCard{
		Id: 9,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:     "ST-001",
			Name:            "Field Jacket",
			Stage:           entity.TechCardStageProd,
			ApprovalState:   entity.TechCardApprovalReleased,
			MeasurementUnit: entity.TechCardUnitIn,
			TargetGender:    nullStringFromPb(string(entity.Female)),
			TargetCost:      decimal.NullDecimal{Decimal: decimal.RequireFromString("42.50"), Valid: true},
			SizeIds:         []int{4, 5},
			ProductIds:      []int{100},
			Media:           []entity.TechCardMediaItem{{MediaId: 11, Kind: entity.TechCardMediaFront}},
			Callouts:        []entity.TechCardCallout{{Number: 1, MediaId: nullInt32FromPb(11)}},
			Revisions:       []entity.TechCardRevision{{Version: nullStringFromPb("v1")}},
		},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		ResolvedMedia: []entity.TechCardMediaFull{{Media: entity.MediaFull{Id: 11}, Kind: entity.TechCardMediaFront}},
	}

	pb := ConvertEntityTechCardToPb(tc)
	if pb.Id != 9 || pb.TechCard.StyleNumber != "ST-001" {
		t.Errorf("id/style mismatch: %+v", pb)
	}
	if pb.TechCard.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_PROD {
		t.Errorf("stage mismatch: %v", pb.TechCard.Stage)
	}
	if pb.TechCard.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED {
		t.Errorf("approval_state mismatch: %v", pb.TechCard.ApprovalState)
	}
	if pb.TechCard.Callouts[0].MediaId != 11 {
		t.Errorf("callout media_id round-trip mismatch: %v", pb.TechCard.Callouts[0].MediaId)
	}
	if pb.TechCard.MeasurementUnit != pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_IN {
		t.Errorf("measurement_unit round-trip mismatch: %v", pb.TechCard.MeasurementUnit)
	}
	if pb.TechCard.TargetGender != pb_common.GenderEnum_GENDER_ENUM_FEMALE {
		t.Errorf("gender mismatch: %v", pb.TechCard.TargetGender)
	}
	if pb.TechCard.TargetCost == nil || pb.TechCard.TargetCost.Value != "42.5" {
		t.Errorf("target_cost mismatch: %+v", pb.TechCard.TargetCost)
	}
	if len(pb.TechCard.Media) != 1 || pb.TechCard.Media[0].Kind != pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT {
		t.Errorf("media item mismatch: %+v", pb.TechCard.Media)
	}
	if len(pb.ResolvedMedia) != 1 || pb.ResolvedMedia[0].Media.Id != 11 {
		t.Errorf("resolved media mismatch: %+v", pb.ResolvedMedia)
	}
	if len(pb.TechCard.SizeIds) != 2 || len(pb.TechCard.Callouts) != 1 || len(pb.TechCard.Revisions) != 1 {
		t.Errorf("child sections mismatch: %+v", pb.TechCard)
	}
}

func TestConvertTechCardMaterials(t *testing.T) {
	valid := &pb_common.TechCardInsert{
		StyleNumber: "ST-010",
		Name:        "Parka",
		SizeIds:     []int32{4, 5},
		Colorways: []*pb_common.TechCardColorway{
			{Code: "BLK", Name: "Black", LabDipStatus: pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_APPROVED},
			{Name: "White"}, // unset lab dip -> pending
		},
		BomItems: []*pb_common.TechCardBomItem{
			{
				Section:   pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Name:      "Main shell",
				Quantity:  &pb_decimal.Decimal{Value: "2"},
				UnitPrice: &pb_decimal.Decimal{Value: "10.5"},
				Currency:  "EUR",
				ColorwayColors: []*pb_common.TechCardBomColorwayColor{
					{ColorwayIndex: 0, Color: "black", Pantone: "Black C"},
					{ColorwayIndex: 1, Color: "white"},
				},
			},
		},
		PomPoints: []*pb_common.TechCardPomPoint{
			{
				Name:      "Chest width",
				Code:      "A",
				Section:   "BODY",
				BaseValue: &pb_decimal.Decimal{Value: "56"},
				Tolerance: &pb_decimal.Decimal{Value: "1"},
				Grades: []*pb_common.TechCardPomGrade{
					{SizeId: 4, Value: &pb_decimal.Decimal{Value: "54"}},
					{SizeId: 5, Value: &pb_decimal.Decimal{Value: "56"}},
				},
				Actuals: []*pb_common.TechCardPomActual{
					{FittingId: 3, Label: "fit1", Value: &pb_decimal.Decimal{Value: "55.5"}},
				},
			},
		},
	}

	got, err := ConvertPbTechCardInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Colorways) != 2 || got.Colorways[0].LabDipStatus != entity.LabDipApproved || got.Colorways[1].LabDipStatus != entity.LabDipPending {
		t.Errorf("colorways mismatch: %+v", got.Colorways)
	}
	if len(got.BomItems) != 1 || got.BomItems[0].Section != entity.BomSectionFabric || len(got.BomItems[0].ColorwayColors) != 2 {
		t.Fatalf("bom mismatch: %+v", got.BomItems)
	}
	if lt := got.BomItems[0].LineTotal(); !lt.Valid || !lt.Decimal.Equal(decimal.RequireFromString("21")) {
		t.Errorf("line_total mismatch: %+v", lt)
	}
	if got.BomItems[0].ColorwayColors[1].ColorwayIndex != 1 {
		t.Errorf("colorway_index mismatch: %+v", got.BomItems[0].ColorwayColors)
	}
	if len(got.PomPoints) != 1 || len(got.PomPoints[0].Grades) != 2 || got.PomPoints[0].Grades[0].SizeId != 4 {
		t.Fatalf("pom grades mismatch: %+v", got.PomPoints)
	}
	if len(got.PomPoints[0].Actuals) != 1 || !got.PomPoints[0].Actuals[0].FittingId.Valid || got.PomPoints[0].Actuals[0].FittingId.Int32 != 3 {
		t.Errorf("pom actuals mismatch: %+v", got.PomPoints[0].Actuals)
	}

	// round-trip back to pb: computed line_total + matrix + grades survive.
	pb := ConvertEntityTechCardToPb(&entity.TechCard{Id: 1, TechCardInsert: *got, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if len(pb.TechCard.Colorways) != 2 || pb.TechCard.Colorways[0].LabDipStatus != pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_APPROVED {
		t.Errorf("pb colorways mismatch: %+v", pb.TechCard.Colorways)
	}
	if len(pb.TechCard.BomItems) != 1 || pb.TechCard.BomItems[0].LineTotal == nil || pb.TechCard.BomItems[0].LineTotal.Value != "21" {
		t.Errorf("pb line_total mismatch: %+v", pb.TechCard.BomItems[0].GetLineTotal())
	}
	if pb.TechCard.BomItems[0].Section != pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC || len(pb.TechCard.BomItems[0].ColorwayColors) != 2 {
		t.Errorf("pb bom mismatch: %+v", pb.TechCard.BomItems[0])
	}
	if len(pb.TechCard.PomPoints) != 1 || len(pb.TechCard.PomPoints[0].Grades) != 2 || pb.TechCard.PomPoints[0].Grades[0].Value.Value != "54" {
		t.Errorf("pb pom mismatch: %+v", pb.TechCard.PomPoints)
	}

	// invalid cases.
	bad := map[string]*pb_common.TechCardInsert{
		"bom section unknown": {StyleNumber: "x", Name: "y", BomItems: []*pb_common.TechCardBomItem{{Name: "m"}}},
		"bom no name":         {StyleNumber: "x", Name: "y", BomItems: []*pb_common.TechCardBomItem{{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC}}},
		"colorway no name":    {StyleNumber: "x", Name: "y", Colorways: []*pb_common.TechCardColorway{{Code: "X"}}},
		"colorway idx range": {StyleNumber: "x", Name: "y",
			Colorways: []*pb_common.TechCardColorway{{Name: "a"}},
			BomItems:  []*pb_common.TechCardBomItem{{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "m", ColorwayColors: []*pb_common.TechCardBomColorwayColor{{ColorwayIndex: 5}}}}},
		"pom grade not in range": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			PomPoints: []*pb_common.TechCardPomPoint{{Name: "p", Grades: []*pb_common.TechCardPomGrade{{SizeId: 9, Value: &pb_decimal.Decimal{Value: "1"}}}}}},
		"pom grade value missing": {StyleNumber: "x", Name: "y", SizeIds: []int32{4},
			PomPoints: []*pb_common.TechCardPomPoint{{Name: "p", Grades: []*pb_common.TechCardPomGrade{{SizeId: 4}}}}},
		"bom price too many decimals": {StyleNumber: "x", Name: "y",
			BomItems: []*pb_common.TechCardBomItem{{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "m", UnitPrice: &pb_decimal.Decimal{Value: "1.23456"}}}},
		"colorway product not in card": {StyleNumber: "x", Name: "y",
			Colorways: []*pb_common.TechCardColorway{{Name: "a", ProductId: 999}}},
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}

	// a colourway whose product is one of the card's linked products is valid.
	okCw, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "ST-011", Name: "Coat", ProductIds: []int32{100},
		Colorways: []*pb_common.TechCardColorway{{Name: "Black", ProductId: 100}},
	})
	if err != nil {
		t.Fatalf("colorway linked to a card product should be valid: %v", err)
	}
	if !okCw.Colorways[0].ProductId.Valid || okCw.Colorways[0].ProductId.Int32 != 100 {
		t.Errorf("colorway product_id mismatch: %+v", okCw.Colorways[0].ProductId)
	}
}

// ListItem conversion produces a lightweight header.
func TestConvertEntityTechCardToListItemPb(t *testing.T) {
	tc := &entity.TechCard{
		Id: 5,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber: "ST-003",
			Name:        "Pants",
			Stage:       entity.TechCardStagePP,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	li := ConvertEntityTechCardToListItemPb(tc)
	if li.Id != 5 || li.StyleNumber != "ST-003" || li.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_PP {
		t.Errorf("list item mismatch: %+v", li)
	}
}
