package dto

import (
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Column length bounds for tech-card varchar fields, mirroring the schema so that
// over-length input fails as InvalidArgument rather than a MySQL 1406 Internal error.
const (
	maxVarchar32 = 32
	maxVarchar64 = 64
	maxCurrency  = 3

	// Decimal bounds mirroring the Phase 2 column types so over-range input fails
	// as InvalidArgument, not a MySQL out-of-range Internal error.
	bomQtyMaxFrac   = 3 // consumption/quantity DECIMAL(10,3)
	bomQtyLimit     = 10_000_000
	bomPriceMaxFrac = 4 // unit_price DECIMAL(12,4)
	bomPriceLimit   = 100_000_000
	pomMaxFrac      = 2 // POM values DECIMAL(8,2)
	pomLimit        = 1_000_000
)

var techCardStagePbToEntity = map[pb_common.TechCardStage]entity.TechCardStage{
	pb_common.TechCardStage_TECH_CARD_STAGE_PROTO: entity.TechCardStageProto,
	pb_common.TechCardStage_TECH_CARD_STAGE_FIT:   entity.TechCardStageFit,
	pb_common.TechCardStage_TECH_CARD_STAGE_SMS:   entity.TechCardStageSMS,
	pb_common.TechCardStage_TECH_CARD_STAGE_PP:    entity.TechCardStagePP,
	pb_common.TechCardStage_TECH_CARD_STAGE_PROD:  entity.TechCardStageProd,
}

var techCardStageEntityToPb = func() map[entity.TechCardStage]pb_common.TechCardStage {
	m := make(map[entity.TechCardStage]pb_common.TechCardStage, len(techCardStagePbToEntity))
	for k, v := range techCardStagePbToEntity {
		m[v] = k
	}
	return m
}()

var techCardApprovalStatePbToEntity = map[pb_common.TechCardApprovalState]entity.TechCardApprovalState{
	pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT:     entity.TechCardApprovalDraft,
	pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_IN_REVIEW: entity.TechCardApprovalInReview,
	pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_APPROVED:  entity.TechCardApprovalApproved,
	pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED:  entity.TechCardApprovalReleased,
	pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_OBSOLETE:  entity.TechCardApprovalObsolete,
}

var techCardApprovalStateEntityToPb = func() map[entity.TechCardApprovalState]pb_common.TechCardApprovalState {
	m := make(map[entity.TechCardApprovalState]pb_common.TechCardApprovalState, len(techCardApprovalStatePbToEntity))
	for k, v := range techCardApprovalStatePbToEntity {
		m[v] = k
	}
	return m
}()

var techCardUnitPbToEntity = map[pb_common.TechCardMeasurementUnit]entity.TechCardMeasurementUnit{
	pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_CM: entity.TechCardUnitCm,
	pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_IN: entity.TechCardUnitIn,
}

var techCardUnitEntityToPb = func() map[entity.TechCardMeasurementUnit]pb_common.TechCardMeasurementUnit {
	m := make(map[entity.TechCardMeasurementUnit]pb_common.TechCardMeasurementUnit, len(techCardUnitPbToEntity))
	for k, v := range techCardUnitPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardMediaKindPbToEntity = map[pb_common.TechCardMediaKind]entity.TechCardMediaKind{
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT:   entity.TechCardMediaFront,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_BACK:    entity.TechCardMediaBack,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_DETAIL:  entity.TechCardMediaDetail,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_LINING:  entity.TechCardMediaLining,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_PREVIEW: entity.TechCardMediaPreview,
}

var techCardMediaKindEntityToPb = func() map[entity.TechCardMediaKind]pb_common.TechCardMediaKind {
	m := make(map[entity.TechCardMediaKind]pb_common.TechCardMediaKind, len(techCardMediaKindPbToEntity))
	for k, v := range techCardMediaKindPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardBomSectionPbToEntity = map[pb_common.TechCardBomSection]entity.TechCardBomSection{
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC:      entity.BomSectionFabric,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LINING:      entity.BomSectionLining,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_INTERLINING: entity.BomSectionInterlining,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_INSULATION:  entity.BomSectionInsulation,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE:    entity.BomSectionHardware,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD:      entity.BomSectionThread,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LABEL:       entity.BomSectionLabel,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING:   entity.BomSectionPackaging,
}

var techCardBomSectionEntityToPb = func() map[entity.TechCardBomSection]pb_common.TechCardBomSection {
	m := make(map[entity.TechCardBomSection]pb_common.TechCardBomSection, len(techCardBomSectionPbToEntity))
	for k, v := range techCardBomSectionPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardLabDipPbToEntity = map[pb_common.TechCardLabDipStatus]entity.TechCardLabDipStatus{
	pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_PENDING:   entity.LabDipPending,
	pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_SUBMITTED: entity.LabDipSubmitted,
	pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_APPROVED:  entity.LabDipApproved,
	pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_REJECTED:  entity.LabDipRejected,
}

var techCardLabDipEntityToPb = func() map[entity.TechCardLabDipStatus]pb_common.TechCardLabDipStatus {
	m := make(map[entity.TechCardLabDipStatus]pb_common.TechCardLabDipStatus, len(techCardLabDipPbToEntity))
	for k, v := range techCardLabDipPbToEntity {
		m[v] = k
	}
	return m
}()

// ConvertPbTechCardInsertToEntity converts a pb_common.TechCardInsert to an
// entity.TechCardInsert, validating identifiers, lengths, enums and child lists.
func ConvertPbTechCardInsertToEntity(pb *pb_common.TechCardInsert) (*entity.TechCardInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("tech card insert is nil")
	}
	if pb.StyleNumber == "" {
		return nil, fmt.Errorf("style_number is required")
	}
	if pb.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	for _, c := range []struct {
		field string
		val   string
		max   int
	}{
		{"style_number", pb.StyleNumber, maxVarchar255},
		{"name", pb.Name, maxVarchar255},
		{"brand", pb.Brand, maxVarchar255},
		{"season", pb.Season, maxVarchar255},
		{"collection", pb.Collection, maxVarchar255},
		{"status", pb.Status, maxVarchar255},
		{"version", pb.Version, maxVarchar64},
		{"designer", pb.Designer, maxVarchar255},
		{"constructor", pb.Constructor, maxVarchar255},
		{"technologist", pb.Technologist, maxVarchar255},
		{"approved_by", pb.ApprovedBy, maxVarchar255},
	} {
		if len(c.val) > c.max {
			return nil, fmt.Errorf("%s must be at most %d characters", c.field, c.max)
		}
	}
	if pb.Currency != "" && len(pb.Currency) != maxCurrency {
		return nil, fmt.Errorf("currency must be a 3-letter ISO 4217 code")
	}

	stage := entity.TechCardStageProto
	if pb.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN {
		s, ok := techCardStagePbToEntity[pb.Stage]
		if !ok {
			return nil, fmt.Errorf("unknown tech card stage: %v", pb.Stage)
		}
		stage = s
	}

	approvalState := entity.TechCardApprovalDraft
	if pb.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_UNKNOWN {
		a, ok := techCardApprovalStatePbToEntity[pb.ApprovalState]
		if !ok {
			return nil, fmt.Errorf("unknown tech card approval state: %v", pb.ApprovalState)
		}
		approvalState = a
	}

	unit := entity.TechCardUnitCm
	if pb.MeasurementUnit != pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_UNKNOWN {
		u, ok := techCardUnitPbToEntity[pb.MeasurementUnit]
		if !ok {
			return nil, fmt.Errorf("unknown tech card measurement unit: %v", pb.MeasurementUnit)
		}
		unit = u
	}

	gender, err := nullGenderFromPb(pb.TargetGender)
	if err != nil {
		return nil, err
	}

	if pb.CategoryId < 0 || pb.BaseModelId < 0 || pb.BaseSampleSizeId < 0 {
		return nil, fmt.Errorf("category_id, base_model_id and base_sample_size_id must not be negative")
	}

	targetCost, err := nullDecimalFromPb(pb.TargetCost)
	if err != nil {
		return nil, fmt.Errorf("target_cost: %w", err)
	}
	if err := validateMoney(targetCost, "target_cost"); err != nil {
		return nil, err
	}
	targetRetail, err := nullDecimalFromPb(pb.TargetRetailPrice)
	if err != nil {
		return nil, fmt.Errorf("target_retail_price: %w", err)
	}
	if err := validateMoney(targetRetail, "target_retail_price"); err != nil {
		return nil, err
	}

	sizeIds, err := dedupePositiveIDs(pb.SizeIds, "size_ids")
	if err != nil {
		return nil, err
	}
	productIds, err := dedupePositiveIDs(pb.ProductIds, "product_ids")
	if err != nil {
		return nil, err
	}

	// base_sample_size_id, when set, must be part of the declared size range: the
	// POM grade radiates from the base size, so a base outside the graded columns
	// would leave the future measurement chart without an origin. An empty size
	// range is allowed (the grade may not be defined yet at the proto stage).
	if pb.BaseSampleSizeId > 0 && len(sizeIds) > 0 && !slices.Contains(sizeIds, int(pb.BaseSampleSizeId)) {
		return nil, fmt.Errorf("base_sample_size_id %d must be one of size_ids", pb.BaseSampleSizeId)
	}

	media := make([]entity.TechCardMediaItem, 0, len(pb.Media))
	for _, m := range pb.Media {
		if m.MediaId <= 0 {
			return nil, fmt.Errorf("tech card media media_id must be positive")
		}
		kind := entity.TechCardMediaPreview
		if m.Kind != pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_UNKNOWN {
			k, ok := techCardMediaKindPbToEntity[m.Kind]
			if !ok {
				return nil, fmt.Errorf("unknown tech card media kind: %v", m.Kind)
			}
			kind = k
		}
		media = append(media, entity.TechCardMediaItem{MediaId: int(m.MediaId), Kind: kind})
	}

	callouts := make([]entity.TechCardCallout, 0, len(pb.Callouts))
	for _, c := range pb.Callouts {
		if len(c.Part) > maxVarchar255 || len(c.Dimensions) > maxVarchar255 {
			return nil, fmt.Errorf("callout part and dimensions must be at most %d characters", maxVarchar255)
		}
		if c.MediaId < 0 {
			return nil, fmt.Errorf("callout media_id must not be negative")
		}
		callouts = append(callouts, entity.TechCardCallout{
			Number:      int(c.Number),
			Part:        nullStringFromPb(c.Part),
			Description: nullStringFromPb(c.Description),
			Dimensions:  nullStringFromPb(c.Dimensions),
			MediaId:     nullInt32FromPb(c.MediaId),
		})
	}

	revisions := make([]entity.TechCardRevision, 0, len(pb.Revisions))
	for _, r := range pb.Revisions {
		if len(r.Version) > maxVarchar64 {
			return nil, fmt.Errorf("revision version must be at most %d characters", maxVarchar64)
		}
		if len(r.Author) > maxVarchar255 || len(r.Section) > maxVarchar255 {
			return nil, fmt.Errorf("revision author and section must be at most %d characters", maxVarchar255)
		}
		revisions = append(revisions, entity.TechCardRevision{
			Version:      nullStringFromPb(r.Version),
			RevisionDate: nullDateFromPbTimestamp(r.RevisionDate),
			Author:       nullStringFromPb(r.Author),
			Section:      nullStringFromPb(r.Section),
			ChangeNote:   nullStringFromPb(r.ChangeNote),
		})
	}

	// materials (Phase 2). Colorways are parsed first so BOM colorway_index can be
	// range-checked against them.
	colorways, err := parseTechCardColorways(pb.Colorways)
	if err != nil {
		return nil, err
	}
	bomItems, err := parseTechCardBomItems(pb.BomItems, len(colorways))
	if err != nil {
		return nil, err
	}
	pomPoints, err := parseTechCardPomPoints(pb.PomPoints, sizeIds)
	if err != nil {
		return nil, err
	}

	return &entity.TechCardInsert{
		StyleNumber:       pb.StyleNumber,
		Name:              pb.Name,
		Brand:             nullStringFromPb(pb.Brand),
		Season:            nullStringFromPb(pb.Season),
		Collection:        nullStringFromPb(pb.Collection),
		CategoryId:        nullInt32FromPb(pb.CategoryId),
		TargetGender:      gender,
		Stage:             stage,
		Status:            nullStringFromPb(pb.Status),
		ApprovalState:     approvalState,
		ApprovedBy:        nullStringFromPb(pb.ApprovedBy),
		ReleasedAt:        nullTimeFromPbTimestamp(pb.ReleasedAt),
		Version:           nullStringFromPb(pb.Version),
		RevisionDate:      nullDateFromPbTimestamp(pb.RevisionDate),
		BaseModelId:       nullInt32FromPb(pb.BaseModelId),
		BaseSampleSizeId:  nullInt32FromPb(pb.BaseSampleSizeId),
		Designer:          nullStringFromPb(pb.Designer),
		Constructor:       nullStringFromPb(pb.Constructor),
		Technologist:      nullStringFromPb(pb.Technologist),
		TargetCost:        targetCost,
		TargetRetailPrice: targetRetail,
		Currency:          nullStringFromPb(pb.Currency),
		MeasurementUnit:   unit,
		Description:       nullStringFromPb(pb.Description),
		Silhouette:        nullStringFromPb(pb.Silhouette),
		Collar:            nullStringFromPb(pb.Collar),
		Fastening:         nullStringFromPb(pb.Fastening),
		Pockets:           nullStringFromPb(pb.Pockets),
		SleeveCuff:        nullStringFromPb(pb.SleeveCuff),
		ExtraDetails:      nullStringFromPb(pb.ExtraDetails),
		Topstitching:      nullStringFromPb(pb.Topstitching),
		AuxMaterials:      nullStringFromPb(pb.AuxMaterials),
		Notes:             nullStringFromPb(pb.Notes),
		SizeIds:           sizeIds,
		ProductIds:        productIds,
		Media:             media,
		Callouts:          callouts,
		Revisions:         revisions,
		BomItems:          bomItems,
		Colorways:         colorways,
		PomPoints:         pomPoints,
	}, nil
}

// ConvertEntityTechCardToPb converts an entity.TechCard to pb_common.TechCard.
func ConvertEntityTechCardToPb(tc *entity.TechCard) *pb_common.TechCard {
	if tc == nil {
		return nil
	}

	media := make([]*pb_common.TechCardMediaItem, 0, len(tc.Media))
	for _, m := range tc.Media {
		media = append(media, &pb_common.TechCardMediaItem{
			MediaId: int32(m.MediaId),
			Kind:    pbTechCardMediaKind(m.Kind),
		})
	}

	resolved := make([]*pb_common.TechCardMediaFull, 0, len(tc.ResolvedMedia))
	for i := range tc.ResolvedMedia {
		resolved = append(resolved, &pb_common.TechCardMediaFull{
			Media: ConvertEntityToCommonMedia(&tc.ResolvedMedia[i].Media),
			Kind:  pbTechCardMediaKind(tc.ResolvedMedia[i].Kind),
		})
	}

	callouts := make([]*pb_common.TechCardCallout, 0, len(tc.Callouts))
	for _, c := range tc.Callouts {
		callouts = append(callouts, &pb_common.TechCardCallout{
			Number:      int32(c.Number),
			Part:        pbStringFromNull(c.Part),
			Description: pbStringFromNull(c.Description),
			Dimensions:  pbStringFromNull(c.Dimensions),
			MediaId:     pbInt32FromNull(c.MediaId),
		})
	}

	revisions := make([]*pb_common.TechCardRevision, 0, len(tc.Revisions))
	for _, r := range tc.Revisions {
		revisions = append(revisions, &pb_common.TechCardRevision{
			Version:      pbStringFromNull(r.Version),
			RevisionDate: pbTimestampFromNullTime(r.RevisionDate),
			Author:       pbStringFromNull(r.Author),
			Section:      pbStringFromNull(r.Section),
			ChangeNote:   pbStringFromNull(r.ChangeNote),
		})
	}

	sizeIds := intsToInt32(tc.SizeIds)
	productIds := intsToInt32(tc.ProductIds)

	return &pb_common.TechCard{
		Id:        int32(tc.Id),
		CreatedAt: timestamppb.New(tc.CreatedAt),
		UpdatedAt: timestamppb.New(tc.UpdatedAt),
		TechCard: &pb_common.TechCardInsert{
			StyleNumber:       tc.StyleNumber,
			Name:              tc.Name,
			Brand:             pbStringFromNull(tc.Brand),
			Season:            pbStringFromNull(tc.Season),
			Collection:        pbStringFromNull(tc.Collection),
			CategoryId:        pbInt32FromNull(tc.CategoryId),
			TargetGender:      pbGenderFromNull(tc.TargetGender),
			Stage:             pbTechCardStage(tc.Stage),
			Status:            pbStringFromNull(tc.Status),
			ApprovalState:     pbTechCardApprovalState(tc.ApprovalState),
			ApprovedBy:        pbStringFromNull(tc.ApprovedBy),
			ReleasedAt:        pbTimestampFromNullTime(tc.ReleasedAt),
			Version:           pbStringFromNull(tc.Version),
			RevisionDate:      pbTimestampFromNullTime(tc.RevisionDate),
			BaseModelId:       pbInt32FromNull(tc.BaseModelId),
			BaseSampleSizeId:  pbInt32FromNull(tc.BaseSampleSizeId),
			Designer:          pbStringFromNull(tc.Designer),
			Constructor:       pbStringFromNull(tc.Constructor),
			Technologist:      pbStringFromNull(tc.Technologist),
			TargetCost:        pbDecimalFromNull(tc.TargetCost),
			TargetRetailPrice: pbDecimalFromNull(tc.TargetRetailPrice),
			Currency:          pbStringFromNull(tc.Currency),
			MeasurementUnit:   pbTechCardMeasurementUnit(tc.MeasurementUnit),
			Description:       pbStringFromNull(tc.Description),
			Silhouette:        pbStringFromNull(tc.Silhouette),
			Collar:            pbStringFromNull(tc.Collar),
			Fastening:         pbStringFromNull(tc.Fastening),
			Pockets:           pbStringFromNull(tc.Pockets),
			SleeveCuff:        pbStringFromNull(tc.SleeveCuff),
			ExtraDetails:      pbStringFromNull(tc.ExtraDetails),
			Topstitching:      pbStringFromNull(tc.Topstitching),
			AuxMaterials:      pbStringFromNull(tc.AuxMaterials),
			Notes:             pbStringFromNull(tc.Notes),
			SizeIds:           sizeIds,
			ProductIds:        productIds,
			Media:             media,
			Callouts:          callouts,
			Revisions:         revisions,
			BomItems:          techCardBomItemsToPb(tc.BomItems),
			Colorways:         techCardColorwaysToPb(tc.Colorways),
			PomPoints:         techCardPomPointsToPb(tc.PomPoints),
		},
		ResolvedMedia: resolved,
	}
}

// ConvertEntityTechCardToListItemPb converts a header-only entity.TechCard to a
// lightweight pb_common.TechCardListItem for list views.
func ConvertEntityTechCardToListItemPb(tc *entity.TechCard) *pb_common.TechCardListItem {
	if tc == nil {
		return nil
	}
	return &pb_common.TechCardListItem{
		Id:            int32(tc.Id),
		StyleNumber:   tc.StyleNumber,
		Name:          tc.Name,
		Brand:         pbStringFromNull(tc.Brand),
		Stage:         pbTechCardStage(tc.Stage),
		Status:        pbStringFromNull(tc.Status),
		ApprovalState: pbTechCardApprovalState(tc.ApprovalState),
		TargetGender:  pbGenderFromNull(tc.TargetGender),
		Season:        pbStringFromNull(tc.Season),
		CreatedAt:     timestamppb.New(tc.CreatedAt),
		UpdatedAt:     timestamppb.New(tc.UpdatedAt),
	}
}

// ConvertPbTechCardStageToEntityString maps a stage filter enum to its entity
// string, returning "" for UNKNOWN (no filter).
func ConvertPbTechCardStageToEntityString(s pb_common.TechCardStage) (string, error) {
	if s == pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN {
		return "", nil
	}
	e, ok := techCardStagePbToEntity[s]
	if !ok {
		return "", fmt.Errorf("unknown tech card stage: %v", s)
	}
	return string(e), nil
}

func pbTechCardStage(s entity.TechCardStage) pb_common.TechCardStage {
	if v, ok := techCardStageEntityToPb[s]; ok {
		return v
	}
	return pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN
}

func pbTechCardApprovalState(s entity.TechCardApprovalState) pb_common.TechCardApprovalState {
	if v, ok := techCardApprovalStateEntityToPb[s]; ok {
		return v
	}
	return pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_UNKNOWN
}

func pbTechCardMeasurementUnit(u entity.TechCardMeasurementUnit) pb_common.TechCardMeasurementUnit {
	if v, ok := techCardUnitEntityToPb[u]; ok {
		return v
	}
	return pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_CM
}

func pbTechCardMediaKind(k entity.TechCardMediaKind) pb_common.TechCardMediaKind {
	if v, ok := techCardMediaKindEntityToPb[k]; ok {
		return v
	}
	return pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_PREVIEW
}

// --- materials (Phase 2): parse pb -> entity ---

func parseTechCardColorways(pbs []*pb_common.TechCardColorway) ([]entity.TechCardColorway, error) {
	out := make([]entity.TechCardColorway, 0, len(pbs))
	for _, c := range pbs {
		if c.Name == "" {
			return nil, fmt.Errorf("colorway name is required")
		}
		if len(c.Name) > maxVarchar255 {
			return nil, fmt.Errorf("colorway name must be at most %d characters", maxVarchar255)
		}
		if len(c.Code) > maxVarchar64 {
			return nil, fmt.Errorf("colorway code must be at most %d characters", maxVarchar64)
		}
		if c.ProductId < 0 {
			return nil, fmt.Errorf("colorway product_id must not be negative")
		}
		status := entity.LabDipPending
		if c.LabDipStatus != pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_UNKNOWN {
			s, ok := techCardLabDipPbToEntity[c.LabDipStatus]
			if !ok {
				return nil, fmt.Errorf("unknown lab dip status: %v", c.LabDipStatus)
			}
			status = s
		}
		out = append(out, entity.TechCardColorway{
			Code:         nullStringFromPb(c.Code),
			Name:         c.Name,
			LabDipStatus: status,
			ProductId:    nullInt32FromPb(c.ProductId),
			Comment:      nullStringFromPb(c.Comment),
		})
	}
	return out, nil
}

func parseTechCardBomItems(pbs []*pb_common.TechCardBomItem, colorwayCount int) ([]entity.TechCardBomItem, error) {
	out := make([]entity.TechCardBomItem, 0, len(pbs))
	for _, b := range pbs {
		section, ok := techCardBomSectionPbToEntity[b.Section]
		if !ok {
			return nil, fmt.Errorf("bom item section is required and must be valid")
		}
		if b.Name == "" {
			return nil, fmt.Errorf("bom item name is required")
		}
		for _, c := range []struct {
			field string
			val   string
			max   int
		}{
			{"bom name", b.Name, maxVarchar255},
			{"bom placement", b.Placement, maxVarchar255},
			{"bom supplier", b.Supplier, maxVarchar255},
			{"bom supplier_ref", b.SupplierRef, maxVarchar255},
			{"bom color", b.Color, maxVarchar255},
			{"bom composition", b.Composition, maxVarchar255},
			{"bom spec", b.Spec, maxVarchar255},
			{"bom unit", b.Unit, maxVarchar32},
		} {
			if len(c.val) > c.max {
				return nil, fmt.Errorf("%s must be at most %d characters", c.field, c.max)
			}
		}
		if b.Currency != "" && len(b.Currency) != maxCurrency {
			return nil, fmt.Errorf("bom currency must be a 3-letter ISO 4217 code")
		}
		consumption, err := nullDecimalFromPb(b.Consumption)
		if err != nil {
			return nil, fmt.Errorf("bom consumption: %w", err)
		}
		if err := validateDecimalScale(consumption, "bom consumption", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		quantity, err := nullDecimalFromPb(b.Quantity)
		if err != nil {
			return nil, fmt.Errorf("bom quantity: %w", err)
		}
		if err := validateDecimalScale(quantity, "bom quantity", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		unitPrice, err := nullDecimalFromPb(b.UnitPrice)
		if err != nil {
			return nil, fmt.Errorf("bom unit_price: %w", err)
		}
		if err := validateDecimalScale(unitPrice, "bom unit_price", bomPriceMaxFrac, bomPriceLimit); err != nil {
			return nil, err
		}

		colors := make([]entity.TechCardBomColorwayColor, 0, len(b.ColorwayColors))
		seen := make(map[int]bool, len(b.ColorwayColors))
		for _, cc := range b.ColorwayColors {
			idx := int(cc.ColorwayIndex)
			if idx < 0 || idx >= colorwayCount {
				return nil, fmt.Errorf("bom colorway_index %d out of range (have %d colorways)", idx, colorwayCount)
			}
			if seen[idx] {
				return nil, fmt.Errorf("bom item has duplicate colorway_index %d", idx)
			}
			seen[idx] = true
			if len(cc.Color) > maxVarchar255 || len(cc.Pantone) > maxVarchar64 {
				return nil, fmt.Errorf("bom colorway color/pantone too long")
			}
			colors = append(colors, entity.TechCardBomColorwayColor{
				ColorwayIndex: idx,
				Color:         nullStringFromPb(cc.Color),
				Pantone:       nullStringFromPb(cc.Pantone),
			})
		}

		out = append(out, entity.TechCardBomItem{
			Section:        section,
			Name:           b.Name,
			Placement:      nullStringFromPb(b.Placement),
			Supplier:       nullStringFromPb(b.Supplier),
			SupplierRef:    nullStringFromPb(b.SupplierRef),
			Color:          nullStringFromPb(b.Color),
			Composition:    nullStringFromPb(b.Composition),
			Spec:           nullStringFromPb(b.Spec),
			Consumption:    consumption,
			Unit:           nullStringFromPb(b.Unit),
			Quantity:       quantity,
			UnitPrice:      unitPrice,
			Currency:       nullStringFromPb(b.Currency),
			Comment:        nullStringFromPb(b.Comment),
			ColorwayColors: colors,
		})
	}
	return out, nil
}

func parseTechCardPomPoints(pbs []*pb_common.TechCardPomPoint, sizeIds []int) ([]entity.TechCardPomPoint, error) {
	sizeSet := make(map[int]bool, len(sizeIds))
	for _, s := range sizeIds {
		sizeSet[s] = true
	}
	out := make([]entity.TechCardPomPoint, 0, len(pbs))
	for _, p := range pbs {
		if p.Name == "" {
			return nil, fmt.Errorf("pom point name is required")
		}
		if len(p.Name) > maxVarchar255 || len(p.Section) > maxVarchar255 {
			return nil, fmt.Errorf("pom point name/section must be at most %d characters", maxVarchar255)
		}
		if len(p.Code) > maxVarchar32 {
			return nil, fmt.Errorf("pom code must be at most %d characters", maxVarchar32)
		}
		baseValue, err := nullDecimalFromPb(p.BaseValue)
		if err != nil {
			return nil, fmt.Errorf("pom base_value: %w", err)
		}
		if err := validateDecimalScale(baseValue, "pom base_value", pomMaxFrac, pomLimit); err != nil {
			return nil, err
		}
		tolerance, err := nullDecimalFromPb(p.Tolerance)
		if err != nil {
			return nil, fmt.Errorf("pom tolerance: %w", err)
		}
		if err := validateDecimalScale(tolerance, "pom tolerance", pomMaxFrac, pomLimit); err != nil {
			return nil, err
		}

		grades := make([]entity.TechCardPomGrade, 0, len(p.Grades))
		seenSize := make(map[int]bool, len(p.Grades))
		for _, g := range p.Grades {
			sid := int(g.SizeId)
			if !sizeSet[sid] {
				return nil, fmt.Errorf("pom grade size_id %d must be one of size_ids", sid)
			}
			if seenSize[sid] {
				return nil, fmt.Errorf("pom point has duplicate grade for size_id %d", sid)
			}
			seenSize[sid] = true
			val, err := requiredDecimalFromPb(g.Value, "pom grade value", pomMaxFrac, pomLimit)
			if err != nil {
				return nil, err
			}
			grades = append(grades, entity.TechCardPomGrade{SizeId: sid, Value: val})
		}

		actuals := make([]entity.TechCardPomActual, 0, len(p.Actuals))
		for _, a := range p.Actuals {
			if a.FittingId < 0 {
				return nil, fmt.Errorf("pom actual fitting_id must not be negative")
			}
			if len(a.Label) > maxVarchar64 {
				return nil, fmt.Errorf("pom actual label must be at most %d characters", maxVarchar64)
			}
			val, err := requiredDecimalFromPb(a.Value, "pom actual value", pomMaxFrac, pomLimit)
			if err != nil {
				return nil, err
			}
			actuals = append(actuals, entity.TechCardPomActual{
				FittingId: nullInt32FromPb(a.FittingId),
				Label:     nullStringFromPb(a.Label),
				Value:     val,
			})
		}

		out = append(out, entity.TechCardPomPoint{
			Section:      nullStringFromPb(p.Section),
			Code:         nullStringFromPb(p.Code),
			Name:         p.Name,
			HowToMeasure: nullStringFromPb(p.HowToMeasure),
			BaseValue:    baseValue,
			Tolerance:    tolerance,
			Grades:       grades,
			Actuals:      actuals,
		})
	}
	return out, nil
}

// --- materials (Phase 2): emit entity -> pb ---

func techCardColorwaysToPb(cws []entity.TechCardColorway) []*pb_common.TechCardColorway {
	out := make([]*pb_common.TechCardColorway, 0, len(cws))
	for _, c := range cws {
		out = append(out, &pb_common.TechCardColorway{
			Code:         pbStringFromNull(c.Code),
			Name:         c.Name,
			LabDipStatus: pbLabDipStatus(c.LabDipStatus),
			ProductId:    pbInt32FromNull(c.ProductId),
			Comment:      pbStringFromNull(c.Comment),
		})
	}
	return out
}

func techCardBomItemsToPb(items []entity.TechCardBomItem) []*pb_common.TechCardBomItem {
	out := make([]*pb_common.TechCardBomItem, 0, len(items))
	for i := range items {
		b := &items[i]
		colors := make([]*pb_common.TechCardBomColorwayColor, 0, len(b.ColorwayColors))
		for _, cc := range b.ColorwayColors {
			colors = append(colors, &pb_common.TechCardBomColorwayColor{
				ColorwayIndex: int32(cc.ColorwayIndex),
				Color:         pbStringFromNull(cc.Color),
				Pantone:       pbStringFromNull(cc.Pantone),
			})
		}
		out = append(out, &pb_common.TechCardBomItem{
			Section:        pbBomSection(b.Section),
			Name:           b.Name,
			Placement:      pbStringFromNull(b.Placement),
			Supplier:       pbStringFromNull(b.Supplier),
			SupplierRef:    pbStringFromNull(b.SupplierRef),
			Color:          pbStringFromNull(b.Color),
			Composition:    pbStringFromNull(b.Composition),
			Spec:           pbStringFromNull(b.Spec),
			Consumption:    pbDecimalFromNull(b.Consumption),
			Unit:           pbStringFromNull(b.Unit),
			Quantity:       pbDecimalFromNull(b.Quantity),
			UnitPrice:      pbDecimalFromNull(b.UnitPrice),
			Currency:       pbStringFromNull(b.Currency),
			Comment:        pbStringFromNull(b.Comment),
			ColorwayColors: colors,
			LineTotal:      pbDecimalFromNull(b.LineTotal()),
		})
	}
	return out
}

func techCardPomPointsToPb(points []entity.TechCardPomPoint) []*pb_common.TechCardPomPoint {
	out := make([]*pb_common.TechCardPomPoint, 0, len(points))
	for i := range points {
		p := &points[i]
		grades := make([]*pb_common.TechCardPomGrade, 0, len(p.Grades))
		for _, g := range p.Grades {
			grades = append(grades, &pb_common.TechCardPomGrade{
				SizeId: int32(g.SizeId),
				Value:  pbDecimalFromDecimal(g.Value),
			})
		}
		actuals := make([]*pb_common.TechCardPomActual, 0, len(p.Actuals))
		for _, a := range p.Actuals {
			actuals = append(actuals, &pb_common.TechCardPomActual{
				FittingId: pbInt32FromNull(a.FittingId),
				Label:     pbStringFromNull(a.Label),
				Value:     pbDecimalFromDecimal(a.Value),
			})
		}
		out = append(out, &pb_common.TechCardPomPoint{
			Section:      pbStringFromNull(p.Section),
			Code:         pbStringFromNull(p.Code),
			Name:         p.Name,
			HowToMeasure: pbStringFromNull(p.HowToMeasure),
			BaseValue:    pbDecimalFromNull(p.BaseValue),
			Tolerance:    pbDecimalFromNull(p.Tolerance),
			Grades:       grades,
			Actuals:      actuals,
		})
	}
	return out
}

func pbBomSection(s entity.TechCardBomSection) pb_common.TechCardBomSection {
	if v, ok := techCardBomSectionEntityToPb[s]; ok {
		return v
	}
	return pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN
}

func pbLabDipStatus(s entity.TechCardLabDipStatus) pb_common.TechCardLabDipStatus {
	if v, ok := techCardLabDipEntityToPb[s]; ok {
		return v
	}
	return pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_PENDING
}

// --- shared helpers ---

func intsToInt32(in []int) []int32 {
	out := make([]int32, 0, len(in))
	for _, v := range in {
		out = append(out, int32(v))
	}
	return out
}

func dedupePositiveIDs(ids []int32, field string) ([]int, error) {
	out := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, v := range ids {
		if v <= 0 {
			return nil, fmt.Errorf("%s must be positive", field)
		}
		if seen[int(v)] {
			return nil, fmt.Errorf("%s contains a duplicate: %d", field, v)
		}
		seen[int(v)] = true
		out = append(out, int(v))
	}
	return out, nil
}

// validateMoney rejects values that won't fit DECIMAL(10,2): negative, more than
// 2 fraction digits, or 100000000 and up — so they fail as InvalidArgument
// instead of a MySQL out-of-range Internal error (mirroring the varchar length
// checks above).
func validateMoney(nd decimal.NullDecimal, field string) error {
	if !nd.Valid {
		return nil
	}
	if nd.Decimal.IsNegative() {
		return fmt.Errorf("%s must not be negative", field)
	}
	if nd.Decimal.Exponent() < -2 {
		return fmt.Errorf("%s must have at most 2 decimal places", field)
	}
	if nd.Decimal.Abs().GreaterThanOrEqual(decimal.NewFromInt(100_000_000)) {
		return fmt.Errorf("%s must be less than 100000000", field)
	}
	return nil
}

func nullDecimalFromPb(d *pb_decimal.Decimal) (decimal.NullDecimal, error) {
	if d == nil || d.Value == "" {
		return decimal.NullDecimal{}, nil
	}
	v, err := decimal.NewFromString(d.Value)
	if err != nil {
		return decimal.NullDecimal{}, fmt.Errorf("invalid decimal %q: %w", d.Value, err)
	}
	return decimal.NullDecimal{Decimal: v, Valid: true}, nil
}

func pbDecimalFromNull(nd decimal.NullDecimal) *pb_decimal.Decimal {
	if !nd.Valid {
		return nil
	}
	return &pb_decimal.Decimal{Value: nd.Decimal.String()}
}

func pbDecimalFromDecimal(d decimal.Decimal) *pb_decimal.Decimal {
	return &pb_decimal.Decimal{Value: d.String()}
}

// validateDecimalScale rejects a non-null value that won't fit its column:
// negative, more than maxFrac fraction digits, or >= limit (mirrors validateMoney
// but parameterised for the Phase 2 decimal columns).
func validateDecimalScale(nd decimal.NullDecimal, field string, maxFrac int, limit int64) error {
	if !nd.Valid {
		return nil
	}
	if nd.Decimal.IsNegative() {
		return fmt.Errorf("%s must not be negative", field)
	}
	if nd.Decimal.Exponent() < int32(-maxFrac) {
		return fmt.Errorf("%s must have at most %d decimal places", field, maxFrac)
	}
	if nd.Decimal.Abs().GreaterThanOrEqual(decimal.NewFromInt(limit)) {
		return fmt.Errorf("%s must be less than %d", field, limit)
	}
	return nil
}

// requiredDecimalFromPb parses a required decimal column (NOT NULL), erroring when
// absent or out of range.
func requiredDecimalFromPb(d *pb_decimal.Decimal, field string, maxFrac int, limit int64) (decimal.Decimal, error) {
	nd, err := nullDecimalFromPb(d)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%s: %w", field, err)
	}
	if !nd.Valid {
		return decimal.Decimal{}, fmt.Errorf("%s is required", field)
	}
	if err := validateDecimalScale(nd, field, maxFrac, limit); err != nil {
		return decimal.Decimal{}, err
	}
	return nd.Decimal, nil
}

// nullTimeFromPbTimestamp maps an optional timestamp to a nullable instant,
// preserving the full time (the column is a TIMESTAMP, e.g. released_at).
func nullTimeFromPbTimestamp(ts *timestamppb.Timestamp) sql.NullTime {
	if ts == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: ts.AsTime().UTC(), Valid: true}
}

// nullDateFromPbTimestamp maps an optional timestamp to a nullable DATE value,
// normalised to UTC midnight (the column is a DATE).
func nullDateFromPbTimestamp(ts *timestamppb.Timestamp) sql.NullTime {
	if ts == nil {
		return sql.NullTime{}
	}
	t := ts.AsTime().UTC()
	return sql.NullTime{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func pbTimestampFromNullTime(nt sql.NullTime) *timestamppb.Timestamp {
	if !nt.Valid {
		return nil
	}
	return timestamppb.New(nt.Time)
}
