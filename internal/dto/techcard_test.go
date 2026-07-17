package dto

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func dec(s string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: s} }
func i32(v int32) *int32               { return &v }

func TestConvertPbTechCardInsertToEntity(t *testing.T) {
	revDate := timestamppb.New(time.Date(2026, 6, 19, 15, 30, 0, 0, time.UTC))
	valid := &pb_common.TechCardInsert{
		StyleNumber:      "ST-001",
		Name:             "Field Jacket",
		Brand:            "grbpwr",
		SkuSeason:        &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_FW, Year: 2025},
		Stage:            pb_common.TechCardStage_TECH_CARD_STAGE_FIT,
		ApprovalState:    pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_APPROVED,
		ApprovedBy:       "lead",
		ReleasedAt:       revDate,
		MeasurementUnit:  pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		TargetGender:     pb_common.GenderEnum_GENDER_ENUM_MALE,
		CategoryId:       3,
		BaseModelId:      7,
		BaseSampleSizeId: 4,
		Concept:          "boxy field jacket",
		RevisionDate:     revDate,
		SizeIds:          []int32{4, 5, 6},
		ProductIds:       []int32{100, 101},
		MoodboardMedia: []*pb_common.TechCardMediaItem{
			{MediaId: 20, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_REFERENCE},
			{MediaId: 21}, // unset kind in moodboard list -> defaults to moodboard
		},
		TechnicalMedia: []*pb_common.TechCardMediaItem{
			{MediaId: 11, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
			{MediaId: 12}, // unset kind in technical list -> defaults to preview
		},
		Callouts: []*pb_common.TechCardCallout{
			{Number: 1, Part: "collar", Description: "two-piece", Dimensions: "h=4cm", MediaId: 11},
		},
		Revisions: []*pb_common.TechCardRevision{
			{Version: "v2", RevisionDate: revDate, Author: "tech", Section: "materials", ChangeNote: "graded"},
		},
		Details: []*pb_common.TechCardDetail{
			{Key: "silhouette", Text: "boxy, hip length", MediaIds: []int32{11, 12}},
		},
	}

	got, err := ConvertPbTechCardInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.StyleNumber.String != "ST-001" || got.Name != "Field Jacket" {
		t.Errorf("identity mismatch: %+v", got)
	}
	if got.Stage != entity.TechCardStageFit {
		t.Errorf("stage mismatch: %v", got.Stage)
	}
	if !got.TargetGender.Valid || got.TargetGender.String != string(entity.Male) {
		t.Errorf("gender mismatch: %+v", got.TargetGender)
	}
	if !got.SeasonCode.Valid || got.SeasonCode.String != "FW" || !got.SeasonYear.Valid || got.SeasonYear.Int32 != 2025 {
		t.Errorf("sku season mismatch: code=%+v year=%+v", got.SeasonCode, got.SeasonYear)
	}
	if !got.CategoryId.Valid || got.CategoryId.Int32 != 3 || !got.BaseModelId.Valid || got.BaseSampleSizeId.Int32 != 4 {
		t.Errorf("fk fields mismatch: %+v", got)
	}
	if got.Concept.String != "boxy field jacket" {
		t.Errorf("concept mismatch: %+v", got.Concept)
	}
	if want := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC); !got.RevisionDate.Valid || !got.RevisionDate.Time.Equal(want) {
		t.Errorf("revision_date not normalized: %+v", got.RevisionDate)
	}
	if len(got.SizeIds) != 3 || got.SizeIds[2] != 6 || len(got.ProductIds) != 2 {
		t.Errorf("size/product ids mismatch: %+v %+v", got.SizeIds, got.ProductIds)
	}
	// media is concatenated moodboard-first, each tagged by category; unset kind defaults
	// per list (moodboard list → moodboard, technical list → preview).
	if len(got.Media) != 4 ||
		got.Media[0].Category != entity.TechCardMediaCategoryMoodboard || got.Media[0].Kind != entity.TechCardMediaReference ||
		got.Media[1].Category != entity.TechCardMediaCategoryMoodboard || got.Media[1].Kind != entity.TechCardMediaMoodboard ||
		got.Media[2].Category != entity.TechCardMediaCategoryTechnical || got.Media[2].Kind != entity.TechCardMediaFront ||
		got.Media[3].Category != entity.TechCardMediaCategoryTechnical || got.Media[3].Kind != entity.TechCardMediaPreview {
		t.Errorf("media split mismatch: %+v", got.Media)
	}
	if len(got.Callouts) != 1 || got.Callouts[0].Number != 1 || got.Callouts[0].MediaId.Int32 != 11 {
		t.Errorf("callouts mismatch: %+v", got.Callouts)
	}
	if len(got.Details) != 1 || got.Details[0].Key.String != "silhouette" || len(got.Details[0].MediaIds) != 2 || got.Details[0].MediaIds[1] != 12 {
		t.Errorf("details mismatch: %+v", got.Details)
	}
	if got.ApprovalState != entity.TechCardApprovalApproved || got.ApprovedBy.String != "lead" || !got.ReleasedAt.Valid {
		t.Errorf("approval mismatch: state=%v by=%+v released=%+v", got.ApprovalState, got.ApprovedBy, got.ReleasedAt)
	}
	if got.MeasurementUnit != entity.TechCardUnitMm {
		t.Errorf("measurement_unit mismatch: %v", got.MeasurementUnit)
	}

	// defaults: unset stage becomes proto; zero fk ids become NULL.
	def, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "ST-002", Name: "Tee"})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if def.Stage != entity.TechCardStageProto || def.ApprovalState != entity.TechCardApprovalDraft || def.MeasurementUnit != entity.TechCardUnitMm {
		t.Errorf("defaults mismatch: %+v", def)
	}
	if def.BaseModelId.Valid || def.CategoryId.Valid {
		t.Errorf("zero fk fields should be NULL: %+v", def)
	}

	// base_sample_size_id is allowed when the size range is still empty (early proto).
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "ST-004", Name: "Coat", BaseSampleSizeId: 9,
	}); err != nil {
		t.Errorf("base size with empty size range should be allowed: %v", err)
	}

	// NF-03: an `idea` draft may omit style_number (stored NULL); from proto onward it is required.
	idea, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name: "Just an idea", Stage: pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
	})
	if err != nil {
		t.Fatalf("idea draft without style_number should be allowed: %v", err)
	}
	if idea.Stage != entity.TechCardStageIdea || idea.StyleNumber.Valid {
		t.Errorf("idea draft: stage=%v style_number=%+v (want idea + NULL)", idea.Stage, idea.StyleNumber)
	}
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name: "Now sampling", Stage: pb_common.TechCardStage_TECH_CARD_STAGE_PROTO,
	}); err == nil {
		t.Error("proto stage without style_number must be rejected")
	}
	// an idea draft cannot be released.
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name: "Premature", Stage: pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		ApprovalState: pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
	}); err == nil {
		t.Error("releasing an idea draft must be rejected")
	}

	// invalid cases.
	bad := map[string]*pb_common.TechCardInsert{
		"nil":                     nil,
		"no style":                {Name: "x"},
		"no name":                 {StyleNumber: "x"},
		"neg category":            {StyleNumber: "x", Name: "y", CategoryId: -1},
		"dup size":                {StyleNumber: "x", Name: "y", SizeIds: []int32{4, 4}},
		"dup product":             {StyleNumber: "x", Name: "y", ProductIds: []int32{1, 1}},
		"size id zero":            {StyleNumber: "x", Name: "y", SizeIds: []int32{0}},
		"base not in range":       {StyleNumber: "x", Name: "y", BaseSampleSizeId: 9, SizeIds: []int32{4, 5}},
		"moodboard media id zero": {StyleNumber: "x", Name: "y", MoodboardMedia: []*pb_common.TechCardMediaItem{{MediaId: 0}}},
		"technical media id zero": {StyleNumber: "x", Name: "y", TechnicalMedia: []*pb_common.TechCardMediaItem{{MediaId: 0}}},
		"version too long":        {StyleNumber: "x", Name: "y", Version: string(make([]byte, 65))},
		"detail media zero":       {StyleNumber: "x", Name: "y", Details: []*pb_common.TechCardDetail{{Key: "k", MediaIds: []int32{0}}}},
		"detail media dup":        {StyleNumber: "x", Name: "y", Details: []*pb_common.TechCardDetail{{Key: "k", MediaIds: []int32{5, 5}}}},
		"dup colorway code":       {StyleNumber: "x", Name: "y", Colorways: []*pb_common.TechCardColorway{{Name: "a", Code: "BLK", ColorCode: "BLK"}, {Name: "b", Code: "BLK", ColorCode: "WHT"}}},
		"bad hex":                 {StyleNumber: "x", Name: "y", Colorways: []*pb_common.TechCardColorway{{Name: "a", ColorCode: "BLK", Hex: "red"}}},
		"bad pantone sys":         {StyleNumber: "x", Name: "y", Colorways: []*pb_common.TechCardColorway{{Name: "a", ColorCode: "BLK", PantoneSystem: "XXX"}}},
		"release unapproved": {StyleNumber: "x", Name: "y",
			ApprovalState: pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
			Colorways:     []*pb_common.TechCardColorway{{Name: "Black", ColorCode: "BLK"}}}, // lab dip defaults to pending
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestTechCardSkuSeasonIsAtomicAndValidated(t *testing.T) {
	tests := []struct {
		name    string
		season  *pb_common.SkuSeason
		wantErr string
	}{
		{name: "unset"},
		{name: "missing code", season: &pb_common.SkuSeason{Year: 2026}, wantErr: "code is required"},
		{name: "missing year", season: &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS}, wantErr: "year must be between"},
		{name: "year below range", season: &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_FW, Year: 1999}, wantErr: "year must be between"},
		{name: "year above range", season: &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_FW, Year: 2100}, wantErr: "year must be between"},
		{name: "valid", season: &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_PF, Year: 2027}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, year, err := ConvertPbSkuSeasonToEntity(tt.season)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.season != nil && (code != entity.SeasonPF || year != 2027) {
				t.Fatalf("got (%q,%d), want (PF,2027)", code, year)
			}
		})
	}
}

func TestConvertEntityTechCardToPb(t *testing.T) {
	tc := &entity.TechCard{
		Id: 9,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:     sql.NullString{String: "ST-001", Valid: true},
			Name:            "Field Jacket",
			SeasonCode:      sql.NullString{String: "FW", Valid: true},
			SeasonYear:      sql.NullInt32{Int32: 2025, Valid: true},
			Stage:           entity.TechCardStageProd,
			ApprovalState:   entity.TechCardApprovalReleased,
			MeasurementUnit: entity.TechCardUnitMm,
			TargetGender:    nullStringFromPb(string(entity.Female)),
			Concept:         nullStringFromPb("intent"),
			SizeIds:         []int{4, 5},
			ProductIds:      []int{100},
			Media: []entity.TechCardMediaItem{
				{MediaId: 11, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
				{MediaId: 20, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaReference},
			},
			Callouts:  []entity.TechCardCallout{{Number: 1, MediaId: nullInt32FromPb(11)}},
			Revisions: []entity.TechCardRevision{{Version: nullStringFromPb("v1")}},
			Details:   []entity.TechCardDetail{{Key: nullStringFromPb("collar"), Text: nullStringFromPb("two-piece"), MediaIds: []int{11}}},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ResolvedMedia: []entity.TechCardMediaFull{
			{Media: entity.MediaFull{Id: 11}, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
			{Media: entity.MediaFull{Id: 20}, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaReference, Caption: nullStringFromPb("mood")},
		},
	}

	pb := ConvertEntityTechCardToPb(tc, CostingFx{})
	if pb.Id != 9 || pb.TechCard.StyleNumber != "ST-001" {
		t.Errorf("id/style mismatch: %+v", pb)
	}
	if pb.TechCard.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_PROD ||
		pb.TechCard.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED {
		t.Errorf("stage/approval mismatch: %+v", pb.TechCard)
	}
	if pb.TechCard.MeasurementUnit != pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM ||
		pb.TechCard.TargetGender != pb_common.GenderEnum_GENDER_ENUM_FEMALE || pb.TechCard.Concept != "intent" {
		t.Errorf("unit/gender/concept mismatch: %+v", pb.TechCard)
	}
	if pb.TechCard.SkuSeason == nil || pb.TechCard.SkuSeason.Code != pb_common.SeasonEnum_SEASON_ENUM_FW || pb.TechCard.SkuSeason.Year != 2025 {
		t.Errorf("sku season mismatch: %+v", pb.TechCard.SkuSeason)
	}
	if len(pb.TechCard.Details) != 1 || pb.TechCard.Details[0].Key != "collar" || len(pb.TechCard.Details[0].MediaIds) != 1 {
		t.Errorf("details round-trip mismatch: %+v", pb.TechCard.Details)
	}
	// writable media splits into the two lists by category.
	if len(pb.TechCard.TechnicalMedia) != 1 || pb.TechCard.TechnicalMedia[0].MediaId != 11 ||
		len(pb.TechCard.MoodboardMedia) != 1 || pb.TechCard.MoodboardMedia[0].MediaId != 20 {
		t.Errorf("writable media split mismatch: technical=%+v moodboard=%+v", pb.TechCard.TechnicalMedia, pb.TechCard.MoodboardMedia)
	}
	// resolved media splits the same way, carrying kind + caption.
	if len(pb.ResolvedTechnicalMedia) != 1 || pb.ResolvedTechnicalMedia[0].Media.Id != 11 ||
		len(pb.ResolvedMoodboardMedia) != 1 || pb.ResolvedMoodboardMedia[0].Media.Id != 20 || pb.ResolvedMoodboardMedia[0].Caption != "mood" {
		t.Errorf("resolved media split mismatch: technical=%+v moodboard=%+v", pb.ResolvedTechnicalMedia, pb.ResolvedMoodboardMedia)
	}
}

// TestConvertTechCardColorwayUsages covers the colourway recipe: usages parse, bom_item_index
// is range-checked, placement is normalised (trim+lower), and index 0 survives as present.
func TestConvertTechCardColorwayUsages(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-010",
		Name:        "Parka",
		SizeIds:     []int32{4, 5},
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell", UnitPrice: dec("10"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip", UnitPrice: dec("3"), Currency: "EUR"},
		},
		Colorways: []*pb_common.TechCardColorway{
			{Code: "BLK", Name: "Black", ColorCode: "BLK", LabDipStatus: pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_APPROVED,
				Usages: []*pb_common.TechCardColorwayUsage{
					{BomItemIndex: i32(0), Placement: "  Outer Shell ", Color: "Jet", Consumption: dec("2")},
					{BomItemIndex: i32(1), Placement: "front", Color: "black", Quantity: dec("1")},
				}},
			{Name: "White", ColorCode: "WHT"}, // unset lab dip -> pending, no usages
		},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Colorways) != 2 || got.Colorways[0].LabDipStatus != entity.LabDipApproved || got.Colorways[1].LabDipStatus != entity.LabDipPending {
		t.Fatalf("colorways mismatch: %+v", got.Colorways)
	}
	us := got.Colorways[0].Usages
	if len(us) != 2 {
		t.Fatalf("usages mismatch: %+v", us)
	}
	if !us[0].BomItemIndex.Valid || us[0].BomItemIndex.Int32 != 0 {
		t.Errorf("usage bom_item_index 0 must be present: %+v", us[0].BomItemIndex)
	}
	if us[0].Placement.String != "outer shell" { // trim + lower
		t.Errorf("placement not normalised: %q", us[0].Placement.String)
	}

	// round-trip: usages emit with computed line_total resolved against the BOM article. The stored
	// colourway row id is emitted too (B-10) so a sample can link to it.
	got.Colorways[0].Id = 42
	pb := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	if pb.TechCard.Colorways[0].Id != 42 {
		t.Errorf("colorway id not emitted (B-10): %+v", pb.TechCard.Colorways[0].Id)
	}
	pus := pb.TechCard.Colorways[0].Usages
	if len(pus) != 2 || pus[0].Placement != "outer shell" {
		t.Fatalf("pb usages mismatch: %+v", pus)
	}
	// usage 0: consumption 2 × price 10 = 20 (no wastage).
	if pus[0].LineTotal == nil || pus[0].LineTotal.Value != "20" {
		t.Errorf("usage line_total mismatch: %+v", pus[0].LineTotal)
	}
	// usage 1: countable quantity 1 × price 3 = 3.
	if pus[1].LineTotal == nil || pus[1].LineTotal.Value != "3" {
		t.Errorf("countable usage line_total mismatch: %+v", pus[1].LineTotal)
	}

	// orphaned usage bom_item_index is rejected.
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "x", Name: "y",
		BomItems:  []*pb_common.TechCardBomItem{{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "f"}},
		Colorways: []*pb_common.TechCardColorway{{Name: "a", ColorCode: "BLK", Usages: []*pb_common.TechCardColorwayUsage{{BomItemIndex: i32(5)}}}},
	}); err == nil {
		t.Errorf("expected error for orphaned usage bom_item_index")
	}

	// new BOM sections decoration/other are accepted.
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "x", Name: "y",
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_DECORATION, Name: "print"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_OTHER, Name: "misc"},
		}}); err != nil {
		t.Errorf("decoration/other sections should be accepted: %v", err)
	}
}

// baseTechCardWithPieces returns a valid card with 2 colourways, 2 BOM items (fabric + fusing
// hardware) and 1 callout, ready for a Pieces payload — the shared fixture for the piece cases.
func baseTechCardWithPieces(pieces []*pb_common.TechCardPiece) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber: "ST-P", Name: "Piece Coat", SizeIds: []int32{4},
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell", UnitPrice: dec("10"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "fusible", UnitPrice: dec("2"), Currency: "EUR"},
		},
		Colorways: []*pb_common.TechCardColorway{{Code: "BLK", Name: "Black", ColorCode: "BLK"}, {Code: "WHT", Name: "White", ColorCode: "WHT"}},
		Callouts:  []*pb_common.TechCardCallout{{Number: 1, Part: "body"}},
		Pieces:    pieces,
	}
}

// TestConvertTechCardPieces covers NF-05 cut-piece dto validation (parseTechCardPieces / pieceBomRef):
// the happy path plus one case per guard, since these piece×colourway→fabric mappings go to the
// factory and a dropped range-check would save a silently-wrong material (nf05-01/nf05-03).
func TestConvertTechCardPieces(t *testing.T) {
	// happy path: a piece with a per-colourway material referencing fabric (bom 0) fused with hardware
	// (bom 1), a callout, and a valid grainline.
	got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		{Name: "Body", PiecesPerGarment: 2, Grainline: "lengthwise", CalloutNumber: i32(1),
			Materials: []*pb_common.TechCardPieceColorwayMaterial{
				{ColorwayIndex: 0, BomItemIndex: i32(0), FusingBomItemIndex: i32(1)},
			}},
	}))
	if err != nil {
		t.Fatalf("valid pieces rejected: %v", err)
	}
	if len(got.Pieces) != 1 || got.Pieces[0].PiecesPerGarment != 2 || got.Pieces[0].Grainline != "lengthwise" {
		t.Fatalf("piece mismatch: %+v", got.Pieces)
	}
	if !got.Pieces[0].CalloutNumber.Valid || got.Pieces[0].CalloutNumber.Int32 != 1 {
		t.Errorf("callout_number not carried: %+v", got.Pieces[0].CalloutNumber)
	}
	pm := got.Pieces[0].Materials
	if len(pm) != 1 || pm[0].BomItemIndex.Int32 != 0 || !pm[0].FusingBomItemIndex.Valid || pm[0].FusingBomItemIndex.Int32 != 1 {
		t.Fatalf("piece material mismatch: %+v", pm)
	}
	// proto3 zero pieces_per_garment defaults to 1.
	got2, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		{Name: "Sleeve", Materials: []*pb_common.TechCardPieceColorwayMaterial{{ColorwayIndex: 0, BomItemIndex: i32(0)}}},
	}))
	if err != nil || got2.Pieces[0].PiecesPerGarment != 1 {
		t.Fatalf("zero pieces_per_garment should default to 1: %+v err=%v", got2.Pieces, err)
	}

	bad := map[string]*pb_common.TechCardInsert{
		"empty piece name": baseTechCardWithPieces([]*pb_common.TechCardPiece{{Name: ""}}),
		"negative pieces_per_garment": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", PiecesPerGarment: -2}}),
		"invalid grainline": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", Grainline: "diagonal"}}),
		"unknown callout_number": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", CalloutNumber: i32(7)}}),
		"colorway_index out of range": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", Materials: []*pb_common.TechCardPieceColorwayMaterial{{ColorwayIndex: 5, BomItemIndex: i32(0)}}}}),
		"duplicate colorway_index": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", Materials: []*pb_common.TechCardPieceColorwayMaterial{
				{ColorwayIndex: 0, BomItemIndex: i32(0)}, {ColorwayIndex: 0, BomItemIndex: i32(1)}}}}),
		"bom_item_index out of range": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", Materials: []*pb_common.TechCardPieceColorwayMaterial{{ColorwayIndex: 0, BomItemIndex: i32(9)}}}}),
		"fusing_bom_item_index out of range": baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "Body", Materials: []*pb_common.TechCardPieceColorwayMaterial{{ColorwayIndex: 0, FusingBomItemIndex: i32(9)}}}}),
	}
	for name, in := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}

	// usage piece_index is range-checked against the pieces in the same payload (1 piece → index 1 is
	// out of range).
	pieceIdxBad := baseTechCardWithPieces([]*pb_common.TechCardPiece{{Name: "Body"}})
	pieceIdxBad.Colorways[0].Usages = []*pb_common.TechCardColorwayUsage{{BomItemIndex: i32(0), PieceIndex: i32(1)}}
	if _, err := ConvertPbTechCardInsertToEntity(pieceIdxBad); err == nil {
		t.Errorf("out-of-range usage piece_index: expected error, got nil")
	}
}

// TestConvertTechCardCosting locks the per-colourway costing and the root rollup (= colourway
// index 0): per-currency buckets, currency-less fold, total_cost without labour, total_sam.
func TestConvertTechCardCosting(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-020",
		Name:        "Coat",
		SizeIds:     []int32{4},
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell", UnitPrice: dec("10"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip", UnitPrice: dec("3"), Currency: "USD"},
		},
		Colorways: []*pb_common.TechCardColorway{
			{Name: "Black", ColorCode: "BLK", Usages: []*pb_common.TechCardColorwayUsage{
				{BomItemIndex: i32(0), Quantity: dec("2")}, // 20 EUR
				{BomItemIndex: i32(1), Quantity: dec("1")}, // 3 USD
			}},
			{Name: "White", ColorCode: "WHT", Usages: []*pb_common.TechCardColorwayUsage{
				{BomItemIndex: i32(0), Quantity: dec("3")}, // 30 EUR
			}},
		},
		Operations: []*pb_common.TechCardOperation{
			{Node: "collar", TimeNorm: dec("2")},
			{Node: "side", TimeNorm: dec("3")},
		},
		SizeQuantities: []*pb_common.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		Costing:        &pb_common.TechCardCosting{CmtCost: dec("10"), DefectPercent: dec("10"), Currency: "EUR"},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pb := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	cost := pb.TechCard.Costing
	if cost == nil {
		t.Fatalf("costing not emitted")
	}

	// per-colourway costs. materials are per-garment; unit_cost folds in the shared manual
	// articles (× 1+defect%); order_cost = unit_cost × order_qty (Σ size_quantities = 100).
	if len(cost.ColorwayCosts) != 2 {
		t.Fatalf("expected 2 colorway_costs, got %d", len(cost.ColorwayCosts))
	}
	black := cost.ColorwayCosts[0]
	// Black: materials_per_unit 20 EUR (USD excluded), unit=(20+10)×1.1=33, order=33×100=3300.
	if black.ColorwayIndex != 0 || black.MaterialsPerUnit.Value != "20" || black.UnitCost.Value != "33" ||
		black.OrderQty != 100 || black.OrderCost.Value != "3300" || !black.HasUnconvertedCurrencies {
		t.Errorf("black colorway cost mismatch: %+v", black)
	}
	white := cost.ColorwayCosts[1]
	// White: materials_per_unit 30, unit=(30+10)×1.1=44, order=44×100=4400.
	if white.ColorwayIndex != 1 || white.MaterialsPerUnit.Value != "30" || white.UnitCost.Value != "44" ||
		white.OrderQty != 100 || white.OrderCost.Value != "4400" || white.HasUnconvertedCurrencies {
		t.Errorf("white colorway cost mismatch: %+v", white)
	}

	// root rollup = primary colourway (index 0 = Black).
	if cost.MaterialsPerUnit.Value != "20" || cost.UnitCost.Value != "33" ||
		cost.OrderQty != 100 || cost.OrderCost.Value != "3300" || !cost.HasUnconvertedCurrencies {
		t.Errorf("root rollup should mirror colourway 0: %+v", cost)
	}
	byCcy := map[string]string{}
	for _, l := range cost.MaterialsTotal {
		byCcy[l.Currency] = l.Amount.Value
	}
	if byCcy["EUR"] != "20" || byCcy["USD"] != "3" {
		t.Errorf("root materials_total buckets mismatch: %+v", byCcy)
	}
	// total_sam = 2 + 3 = 5.
	if cost.TotalSam == nil || cost.TotalSam.Value != "5" {
		t.Errorf("total_sam mismatch: %+v", cost.TotalSam)
	}

	// invalid costing cases.
	bad := map[string]*pb_common.TechCardInsert{
		"defect over 100": {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{DefectPercent: dec("150")}},
		"costing bad ccy": {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{Currency: "EURO"}},
		"neg cmt":         {StyleNumber: "x", Name: "y", Costing: &pb_common.TechCardCosting{CmtCost: dec("-1")}},
	}
	for name, bi := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(bi); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

// TestConvertTechCardPerSizeCosting checks a mixed colourway (one per-garment usage + one
// per-size usage) folds the size-run cost against per-size order quantities.
func TestConvertTechCardPerSizeCosting(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber:    "ST-021",
		Name:           "Parka",
		SizeIds:        []int32{4, 5},
		SizeQuantities: []*pb_common.TechCardSizeQuantity{{SizeId: 4, OrderQty: 10}, {SizeId: 5, OrderQty: 20}},
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "shell", UnitPrice: dec("2"), Currency: "EUR"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "zip", UnitPrice: dec("3"), Currency: "EUR"},
		},
		Colorways: []*pb_common.TechCardColorway{
			{Name: "Black", ColorCode: "BLK", Usages: []*pb_common.TechCardColorwayUsage{
				// per-size: (1.5×10 + 1.8×20) × 2 = 51 × 2 = 102.
				{BomItemIndex: i32(0), SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{
					{SizeId: 4, Consumption: dec("1.5")}, {SizeId: 5, Consumption: dec("1.8")}}},
				// per-garment countable: 1 × 3 = 3.
				{BomItemIndex: i32(1), Quantity: dec("1")},
			}},
		},
		Costing: &pb_common.TechCardCosting{Currency: "EUR"},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pb := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	cost := pb.TechCard.Costing
	cc := cost.ColorwayCosts[0]
	// Per-unit: the per-size usage normalises to 102/30 = 3.4, the per-garment zip is 3, so
	// materials_per_unit = 6.4. With no manual articles / defect, unit_cost = 6.4 and
	// order_cost = 6.4 × 30 = 192 — which equals size-run 102 + zip run 90, recovering the run.
	if cc.MaterialsPerUnit.Value != "6.4" || cc.UnitCost.Value != "6.4" ||
		cc.OrderQty != 30 || cc.OrderCost.Value != "192" {
		t.Errorf("mixed-scale per-unit/per-order mismatch: %+v", cc)
	}
	// the per-size usage emits size_run_total and an absent line_total.
	pus := pb.TechCard.Colorways[0].Usages
	if pus[0].SizeRunTotal == nil || pus[0].SizeRunTotal.Value != "102" || pus[0].LineTotal != nil {
		t.Errorf("per-size usage totals mismatch: line=%v run=%v", pus[0].LineTotal, pus[0].SizeRunTotal)
	}
}

// TestConvertTechCardOperations covers server-assigned operation numbers ((i+1)*10, client
// value ignored), placement normalisation, and the classification refs.
func TestConvertTechCardOperations(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-030",
		Name:        "Jacket",
		Callouts:    []*pb_common.TechCardCallout{{Number: 1}, {Number: 2}},
		BomItems: []*pb_common.TechCardBomItem{
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD, Name: "thread"},
			{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM, Name: "binding"},
		},
		Operations: []*pb_common.TechCardOperation{
			{Node: "bind hem", OperationNumber: 999, Placement: "  Outer Hem", // client number ignored
				OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_COVERSTITCH,
				Zone:          pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_OUTER,
				BomItemIndex:  i32(1), CalloutNumber: 2},
			{Node: "lay thread", BomItemIndex: i32(0)}, // index 0 must survive as present
		},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// server-assigned numbers: (0+1)*10=10, (1+1)*10=20, regardless of client value.
	if got.Operations[0].OperationNumber.Int32 != 10 || got.Operations[1].OperationNumber.Int32 != 20 {
		t.Errorf("operation numbers not server-assigned: %v, %v", got.Operations[0].OperationNumber, got.Operations[1].OperationNumber)
	}
	if got.Operations[0].Placement.String != "outer hem" {
		t.Errorf("operation placement not normalised: %q", got.Operations[0].Placement.String)
	}
	if got.Operations[0].OperationType != entity.OpTypeCoverstitch || got.Operations[0].Zone != entity.ZoneOuter ||
		got.Operations[0].BomItemIndex.Int32 != 1 || got.Operations[0].CalloutNumber.Int32 != 2 {
		t.Errorf("operation classification mismatch: %+v", got.Operations[0])
	}
	if !got.Operations[1].BomItemIndex.Valid || got.Operations[1].BomItemIndex.Int32 != 0 {
		t.Errorf("bom_item_index 0 should be present: %+v", got.Operations[1].BomItemIndex)
	}

	pb := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	if pb.TechCard.Operations[0].OperationNumber != 10 || pb.TechCard.Operations[0].Placement != "outer hem" {
		t.Errorf("pb operation mismatch: %+v", pb.TechCard.Operations[0])
	}
	if pop := pb.TechCard.Operations[1]; pop.BomItemIndex == nil || *pop.BomItemIndex != 0 {
		t.Errorf("pb op1 bom_item_index 0 should round-trip as present: %+v", pop.BomItemIndex)
	}

	// a placement that matches no usage is a soft case — drafts are incomplete, never rejected.
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "x", Name: "y",
		Operations: []*pb_common.TechCardOperation{{Node: "n", Placement: "nonexistent part"}}}); err != nil {
		t.Errorf("placement mismatch must be accepted (soft): %v", err)
	}

	// hard errors: out-of-range bom_item_index and unmatched callout_number.
	bad := map[string]*pb_common.TechCardInsert{
		"op no node": {StyleNumber: "x", Name: "y", Operations: []*pb_common.TechCardOperation{{SeamType: "x"}}},
		"bom idx range": {StyleNumber: "x", Name: "y",
			BomItems:   []*pb_common.TechCardBomItem{{Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD, Name: "t"}},
			Operations: []*pb_common.TechCardOperation{{Node: "n", BomItemIndex: i32(3)}}},
		"callout unmatched": {StyleNumber: "x", Name: "y",
			Callouts:   []*pb_common.TechCardCallout{{Number: 1}},
			Operations: []*pb_common.TechCardOperation{{Node: "n", CalloutNumber: 9}}},
	}
	for name, bi := range bad {
		if _, err := ConvertPbTechCardInsertToEntity(bi); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertTechCardIssuesAndRelease(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-035",
		Name:        "Jacket",
		Issues: []*pb_common.TechCardIssue{
			{OperationNumber: 10, Severity: pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_HIGH, Description: "collar too tight to turn"},
		},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Issues) != 1 || got.Issues[0].Severity != entity.IssueSeverityHigh || got.Issues[0].Status != entity.IssueStatusOpen {
		t.Errorf("issues mismatch: %+v", got.Issues)
	}

	// issue without a description is rejected.
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "x", Name: "y",
		Issues: []*pb_common.TechCardIssue{{Severity: pb_common.TechCardIssueSeverity_TECH_CARD_ISSUE_SEVERITY_LOW}}}); err == nil {
		t.Errorf("expected error for issue without description")
	}
	// releasing while a high-severity issue is open is blocked.
	in.ApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED
	if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
		t.Errorf("expected release to be blocked by an open high-severity issue")
	}
}

func TestConvertTechCardSignoffs(t *testing.T) {
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-050", Name: "Tee",
		Signoffs: []*pb_common.TechCardSignoff{
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING, State: pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED, SignedBy: "finance"},
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR},
		},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Signoffs) != 2 || got.Signoffs[0].Section != entity.SignoffCosting || got.Signoffs[0].State != entity.SignoffStateApproved {
		t.Errorf("signoffs mismatch: %+v", got.Signoffs)
	}
	if got.Signoffs[1].State != entity.SignoffStatePending {
		t.Errorf("signoff default state mismatch: %+v", got.Signoffs[1])
	}
	pb := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	if len(pb.TechCard.Signoffs) != 2 || pb.TechCard.Signoffs[0].Section != pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COSTING {
		t.Errorf("pb signoffs mismatch: %+v", pb.TechCard.Signoffs)
	}

	// duplicate sign-off section is rejected (the POM section is gone from the enum).
	if _, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "x", Name: "y",
		Signoffs: []*pb_common.TechCardSignoff{
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR},
			{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_COLOUR}}}); err == nil {
		t.Errorf("expected error for duplicate signoff section")
	}
}

// TestConvertTechCardZeroTimestampsAreNull guards the grpc-gateway behaviour where an unset
// Go time.Time serialises as "0001-01-01T00:00:00Z" (a non-nil zero-instant timestamp); these
// must map to NULL or MySQL rejects the DATE/TIMESTAMP (err 1292).
func TestConvertTechCardZeroTimestampsAreNull(t *testing.T) {
	zero := timestamppb.New(time.Time{})
	in := &pb_common.TechCardInsert{
		StyleNumber:  "ST-060",
		Name:         "Hoodie",
		RevisionDate: zero,
		ReleasedAt:   zero,
		ApprovedAt:   zero,
		Revisions:    []*pb_common.TechCardRevision{{Version: "1", RevisionDate: zero}},
		Colorways:    []*pb_common.TechCardColorway{{Name: "Ecru", ColorCode: "OFW", LabDipSubmittedAt: zero, LabDipDecidedAt: zero}},
	}
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RevisionDate.Valid || got.ReleasedAt.Valid || got.ApprovedAt.Valid {
		t.Errorf("header zero timestamps should be NULL: rev=%+v rel=%+v app=%+v", got.RevisionDate, got.ReleasedAt, got.ApprovedAt)
	}
	if got.Revisions[0].RevisionDate.Valid {
		t.Errorf("revision zero date should be NULL: %+v", got.Revisions[0].RevisionDate)
	}
	if got.Colorways[0].LabDipSubmittedAt.Valid || got.Colorways[0].LabDipDecidedAt.Valid {
		t.Errorf("colorway zero lab-dip dates should be NULL: %+v", got.Colorways[0])
	}
}

func TestConvertEntityTechCardToListItemPb(t *testing.T) {
	tc := &entity.TechCard{
		Id:             5,
		TechCardInsert: entity.TechCardInsert{StyleNumber: sql.NullString{String: "ST-003", Valid: true}, Name: "Pants", Stage: entity.TechCardStagePP, Purpose: entity.TechCardPurposeAuxiliary},
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	li := ConvertEntityTechCardToListItemPb(tc)
	if li.Id != 5 || li.StyleNumber != "ST-003" || li.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_PP {
		t.Errorf("list item mismatch: %+v", li)
	}
	// #8: purpose is surfaced on the light card so a board can badge auxiliary cards without an N+1 GetTechCard.
	if li.Purpose != pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY {
		t.Errorf("list item purpose = %v, want auxiliary", li.Purpose)
	}
}

// TestColorwayProductAutoSeed covers task 17: a colourway whose product_id is not yet in the
// card's product_ids is auto-unioned into product_ids (rather than rejected), keeping
// tech_card_product a superset of every colourway's annotated product. Already-listed and unset
// (0) colourway products don't add duplicates.
func TestColorwayProductAutoSeed(t *testing.T) {
	card := &pb_common.TechCardInsert{
		StyleNumber:     "ST-AUTOSEED",
		Name:            "n",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_PROTO,
		ApprovalState:   pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		ProductIds:      []int32{100},
		Colorways: []*pb_common.TechCardColorway{
			{Name: "Black", ColorCode: "BLK", ProductId: 100}, // already listed
			{Name: "White", ColorCode: "WHT", ProductId: 200}, // NOT listed → auto-seeded
			{Name: "Sample", ColorCode: "UNK"},                // product_id 0 → ignored
		},
	}
	got, err := ConvertPbTechCardInsertToEntity(card)
	if err != nil {
		t.Fatalf("unexpected error (auto-seed should not reject): %v", err)
	}
	if len(got.ProductIds) != 2 || got.ProductIds[0] != 100 || got.ProductIds[1] != 200 {
		t.Fatalf("product_ids should be [100 200] after auto-seed, got %v", got.ProductIds)
	}
}
