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
	maxVarchar64 = 64
	maxCurrency  = 3
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
