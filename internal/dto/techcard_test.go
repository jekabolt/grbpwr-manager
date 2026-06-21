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
		MeasurementUnit:   pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
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
	if got.MeasurementUnit != entity.TechCardUnitMm {
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
		"dup colorway code": {StyleNumber: "x", Name: "y", Colorways: []*pb_common.TechCardColorway{{Name: "a", Code: "BLK"}, {Name: "b", Code: "BLK"}}},
		"release unapproved": {StyleNumber: "x", Name: "y",
			ApprovalState: pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
			Colorways:     []*pb_common.TechCardColorway{{Name: "Black"}}}, // lab dip defaults to pending
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
			MeasurementUnit: entity.TechCardUnitMm,
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
	if pb.TechCard.MeasurementUnit != pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM {
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
				Name:          "Chest width",
				Code:          "A",
				Section:       "BODY",
				BaseValue:     &pb_decimal.Decimal{Value: "56"},
				TolerancePlus: &pb_decimal.Decimal{Value: "1"}, // minus unset -> mirrors to 1
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
	// tolerance_minus was unset, so it mirrors tolerance_plus.
	if tp := got.PomPoints[0].TolerancePlus; !tp.Valid || tp.Decimal.String() != "1" {
		t.Errorf("tolerance_plus mismatch: %+v", tp)
	}
	if tm := got.PomPoints[0].ToleranceMinus; !tm.Valid || tm.Decimal.String() != "1" {
		t.Errorf("tolerance_minus should mirror plus: %+v", tm)
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

func TestConvertTechCardProductionAndCosting(t *testing.T) {
	dec := func(s string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: s} }
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-020",
		Name:        "Coat",
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell", Quantity: dec("2"), UnitPrice: dec("10"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LINING, Name: "lining", Quantity: dec("1"), UnitPrice: dec("5"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip", Quantity: dec("1"), UnitPrice: dec("3"), Currency: "USD"},
		},
		Construction: &pb_common.TechCardConstruction{MainStitchType: "lockstitch", StitchDensity: "3-4"},
		Operations: []*pb_common.TechCardOperation{
			{Node: "collar", SeamType: "lockstitch", StitchesPerCm: dec("3.5")},
		},
		Labels: []*pb_common.TechCardLabel{
			{LabelType: pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_MAIN, Content: "grbpwr", Placement: "neck"},
		},
		Packaging: &pb_common.TechCardPackaging{FoldingMethod: "flat", UnitsPerBox: 10, WeightNet: dec("0.8")},
		Costing:   &pb_common.TechCardCosting{CmtCost: dec("10"), DefectPercent: dec("10"), Currency: "EUR"},
	}

	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Construction == nil || got.Construction.MainStitchType.String != "lockstitch" {
		t.Errorf("construction mismatch: %+v", got.Construction)
	}
	if len(got.Operations) != 1 || got.Operations[0].Node != "collar" {
		t.Errorf("operations mismatch: %+v", got.Operations)
	}
	if len(got.Labels) != 1 || got.Labels[0].LabelType != entity.LabelTypeMain {
		t.Errorf("labels mismatch: %+v", got.Labels)
	}
	if got.Packaging == nil || !got.Packaging.UnitsPerBox.Valid || got.Packaging.UnitsPerBox.Int32 != 10 {
		t.Errorf("packaging mismatch: %+v", got.Packaging)
	}
	if got.Costing == nil || !got.Costing.CmtCost.Valid {
		t.Fatalf("costing mismatch: %+v", got.Costing)
	}

	// round-trip to pb: production sections + computed costing rollup.
	pb := ConvertEntityTechCardToPb(&entity.TechCard{Id: 1, TechCardInsert: *got, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if pb.TechCard.Construction.MainStitchType != "lockstitch" || len(pb.TechCard.Operations) != 1 {
		t.Errorf("pb construction/operations mismatch: %+v", pb.TechCard)
	}
	if pb.TechCard.Labels[0].LabelType != pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_MAIN || pb.TechCard.Packaging.UnitsPerBox != 10 {
		t.Errorf("pb labels/packaging mismatch: %+v", pb.TechCard)
	}
	cost := pb.TechCard.Costing
	if cost == nil {
		t.Fatalf("costing not emitted")
	}
	// materials_cost = EUR lines only (20+5); USD line stays out of the single-currency subtotal.
	if cost.MaterialsCost == nil || cost.MaterialsCost.Value != "25" {
		t.Errorf("materials_cost mismatch: %+v", cost.MaterialsCost)
	}
	// total_cost = (25 materials + 10 cmt) * (1 + 10/100) = 38.5
	if cost.TotalCost == nil || cost.TotalCost.Value != "38.5" {
		t.Errorf("total_cost mismatch: %+v", cost.TotalCost)
	}
	// materials_total surfaces both currencies (no conversion).
	byCcy := map[string]string{}
	for _, l := range cost.MaterialsTotal {
		byCcy[l.Currency] = l.Amount.Value
	}
	if byCcy["EUR"] != "25" || byCcy["USD"] != "3" {
		t.Errorf("materials_total mismatch: %+v", byCcy)
	}
	// USD line against EUR costing → excluded from total_cost, flag must be set.
	if !cost.HasUnconvertedCurrencies {
		t.Errorf("expected has_unconverted_currencies (USD BOM line vs EUR costing)")
	}

	// invalid cases.
	bad := map[string]*pb_common.TechCardInsert{
		"label type unknown":  {StyleNumber: "x", Name: "y", Labels: []*pb_common.TechCardLabel{{Content: "x"}}},
		"operation no node":   {StyleNumber: "x", Name: "y", Operations: []*pb_common.TechCardOperation{{SeamType: "x"}}},
		"defect over 100":     {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{DefectPercent: dec("150")}},
		"costing bad ccy":     {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{Currency: "EURO"}},
		"neg cmt":             {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{CmtCost: dec("-1")}},
		"stitches too scaled": {StyleNumber: "x", Name: "y", Operations: []*pb_common.TechCardOperation{{Node: "n", StitchesPerCm: dec("1.234")}}},
	}
	for name, bi := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(bi); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertTechCardPomActualVerdict(t *testing.T) {
	dec := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	nd := func(s string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: dec(s), Valid: true} }
	pt := entity.TechCardPomPoint{
		Name:           "Chest",
		BaseValue:      nd("50"),
		TolerancePlus:  nd("1"),
		ToleranceMinus: nd("1"),
		Grades: []entity.TechCardPomGrade{
			{SizeId: 4, Value: dec("54")},
			{SizeId: 5, Value: dec("56")},
		},
		Actuals: []entity.TechCardPomActual{
			{SizeId: nullInt32FromPb(4), Value: dec("54.5")}, // in tolerance (54 ± 1)
			{SizeId: nullInt32FromPb(4), Value: dec("55.5")}, // over (> 55)
			{SizeId: nullInt32FromPb(4), Value: dec("52.5")}, // under (< 53)
			{Value: dec("50")}, // no size → base 50, in tolerance
			{SizeId: nullInt32FromPb(99), Value: dec("70")}, // size w/o grade → base 50 → over
		},
	}
	tc := &entity.TechCard{
		Id:             1,
		TechCardInsert: entity.TechCardInsert{StyleNumber: "x", Name: "y", PomPoints: []entity.TechCardPomPoint{pt}},
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	acts := ConvertEntityTechCardToPb(tc).TechCard.PomPoints[0].Actuals
	want := []pb_common.TechCardPomVerdict{
		pb_common.TechCardPomVerdict_TECH_CARD_POM_VERDICT_IN_TOLERANCE,
		pb_common.TechCardPomVerdict_TECH_CARD_POM_VERDICT_OVER,
		pb_common.TechCardPomVerdict_TECH_CARD_POM_VERDICT_UNDER,
		pb_common.TechCardPomVerdict_TECH_CARD_POM_VERDICT_IN_TOLERANCE,
		pb_common.TechCardPomVerdict_TECH_CARD_POM_VERDICT_OVER,
	}
	for i, w := range want {
		if acts[i].Verdict != w {
			t.Errorf("actual %d verdict = %v, want %v", i, acts[i].Verdict, w)
		}
	}
	if acts[0].Deviation == nil || acts[0].Deviation.Value != "0.5" {
		t.Errorf("actual 0 deviation = %+v, want 0.5", acts[0].Deviation)
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
