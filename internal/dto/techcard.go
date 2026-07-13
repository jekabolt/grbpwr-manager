package dto

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
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
	maxVarchar32   = 32
	maxVarchar64   = 64
	maxVarchar1024 = 1024
	maxCurrency    = 3

	// Decimal bounds mirroring the Phase 2 column types so over-range input fails
	// as InvalidArgument, not a MySQL out-of-range Internal error.
	bomQtyMaxFrac   = 3 // consumption/quantity DECIMAL(10,3)
	bomQtyLimit     = 10_000_000
	bomPriceMaxFrac = 4 // unit_price DECIMAL(12,4)
	bomPriceLimit   = 100_000_000
)

var techCardStagePbToEntity = map[pb_common.TechCardStage]entity.TechCardStage{
	pb_common.TechCardStage_TECH_CARD_STAGE_IDEA:  entity.TechCardStageIdea,
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
	pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM: entity.TechCardUnitMm,
}

var techCardUnitEntityToPb = func() map[entity.TechCardMeasurementUnit]pb_common.TechCardMeasurementUnit {
	m := make(map[entity.TechCardMeasurementUnit]pb_common.TechCardMeasurementUnit, len(techCardUnitPbToEntity))
	for k, v := range techCardUnitPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardMediaKindPbToEntity = map[pb_common.TechCardMediaKind]entity.TechCardMediaKind{
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT:     entity.TechCardMediaFront,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_BACK:      entity.TechCardMediaBack,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_DETAIL:    entity.TechCardMediaDetail,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_LINING:    entity.TechCardMediaLining,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_PREVIEW:   entity.TechCardMediaPreview,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_MOODBOARD: entity.TechCardMediaMoodboard,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_REFERENCE: entity.TechCardMediaReference,
	pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SWATCH:    entity.TechCardMediaSwatch,
}

var techCardFabricDirectionPbToEntity = map[pb_common.TechCardFabricDirection]entity.TechCardFabricDirection{
	pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_ANY:     entity.FabricDirectionAny,
	pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_ONE_WAY: entity.FabricDirectionOneWay,
	pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_TWO_WAY: entity.FabricDirectionTwoWay,
}

var techCardFabricDirectionEntityToPb = func() map[entity.TechCardFabricDirection]pb_common.TechCardFabricDirection {
	m := make(map[entity.TechCardFabricDirection]pb_common.TechCardFabricDirection, len(techCardFabricDirectionPbToEntity))
	for k, v := range techCardFabricDirectionPbToEntity {
		m[v] = k
	}
	return m
}()

var techCardMediaKindEntityToPb = func() map[entity.TechCardMediaKind]pb_common.TechCardMediaKind {
	m := make(map[entity.TechCardMediaKind]pb_common.TechCardMediaKind, len(techCardMediaKindPbToEntity))
	for k, v := range techCardMediaKindPbToEntity {
		m[v] = k
	}
	return m
}()

// defaultTechCardMediaKind is the fallback kind for an item whose kind is unset, chosen
// per list so a moodboard item doesn't default to a technical "preview".
func defaultTechCardMediaKind(cat entity.TechCardMediaCategory) entity.TechCardMediaKind {
	if cat == entity.TechCardMediaCategoryMoodboard {
		return entity.TechCardMediaMoodboard
	}
	return entity.TechCardMediaPreview
}

// parseTechCardMediaItems validates one sketch-media list (moodboard or technical) and
// tags each item with its category. Media in the two lists share the same shape; the
// category is implied by which list the item arrived in.
func parseTechCardMediaItems(items []*pb_common.TechCardMediaItem, cat entity.TechCardMediaCategory) ([]entity.TechCardMediaItem, error) {
	out := make([]entity.TechCardMediaItem, 0, len(items))
	for _, m := range items {
		if m.MediaId <= 0 {
			return nil, fmt.Errorf("tech card media media_id must be positive")
		}
		kind := defaultTechCardMediaKind(cat)
		if m.Kind != pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_UNKNOWN {
			k, ok := techCardMediaKindPbToEntity[m.Kind]
			if !ok {
				return nil, fmt.Errorf("unknown tech card media kind: %v", m.Kind)
			}
			kind = k
		}
		if len(m.Caption) > maxVarchar255 {
			return nil, fmt.Errorf("media caption must be at most %d characters", maxVarchar255)
		}
		out = append(out, entity.TechCardMediaItem{
			MediaId:  int(m.MediaId),
			Category: cat,
			Kind:     kind,
			Caption:  nullStringFromPb(m.Caption),
		})
	}
	return out, nil
}

var techCardBomSectionPbToEntity = map[pb_common.TechCardBomSection]entity.TechCardBomSection{
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC:      entity.BomSectionFabric,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LINING:      entity.BomSectionLining,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_INTERLINING: entity.BomSectionInterlining,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_INSULATION:  entity.BomSectionInsulation,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE:    entity.BomSectionHardware,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD:      entity.BomSectionThread,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LABEL:       entity.BomSectionLabel,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING:   entity.BomSectionPackaging,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM:        entity.BomSectionTrim,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_DECORATION:  entity.BomSectionDecoration,
	pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_OTHER:       entity.BomSectionOther,
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
	stage := entity.TechCardStageProto
	if pb.Stage != pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN {
		s, ok := techCardStagePbToEntity[pb.Stage]
		if !ok {
			return nil, fmt.Errorf("unknown tech card stage: %v", pb.Stage)
		}
		stage = s
	}
	// style_number is optional for an `idea` draft (NF-03) but required to start sampling — every
	// stage from proto onward. This gates both create and update (both pass through here).
	styleNumber := strings.TrimSpace(pb.StyleNumber)
	if stage != entity.TechCardStageIdea && styleNumber == "" {
		return nil, fmt.Errorf("style_number is required from the proto stage onward")
	}

	approvalState := entity.TechCardApprovalDraft
	if pb.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_UNKNOWN {
		a, ok := techCardApprovalStatePbToEntity[pb.ApprovalState]
		if !ok {
			return nil, fmt.Errorf("unknown tech card approval state: %v", pb.ApprovalState)
		}
		approvalState = a
	}
	// An `idea` draft cannot be approved or released — advance it to a real stage first (NF-03).
	if stage == entity.TechCardStageIdea &&
		(approvalState == entity.TechCardApprovalApproved || approvalState == entity.TechCardApprovalReleased) {
		return nil, fmt.Errorf("an idea draft cannot be approved or released; advance the stage first")
	}

	// The brand works in mm: an unset measurement_unit defaults to mm (clients have
	// stopped sending cm, though the enum keeps cm for back-compat reads).
	unit := entity.TechCardUnitMm
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

	sizeIds, err := dedupePositiveIDs(pb.SizeIds, "size_ids")
	if err != nil {
		return nil, err
	}
	productIds, err := dedupePositiveIDs(pb.ProductIds, "product_ids")
	if err != nil {
		return nil, err
	}
	// tech_card_product (product_ids) is the CANON product↔style link — every external consumer
	// (cost_price seed, GetMarginByStyle, ListTechCards-by-product) reads it, never colorway
	// product_id. A colourway's product_id is an annotation obliged to be a subset. Rather than
	// reject a colourway whose product isn't yet listed (the old write-time 400), auto-seed it:
	// union colourway product_ids into product_ids so the link stays in sync with no manual step.
	// (The primary-for-costing card is a separate, deterministic pointer: product.primary_tech_card_id.)
	productIds = unionColorwayProductIds(productIds, pb.Colorways)

	// base_sample_size_id, when set, must be part of the declared size range: the
	// POM grade radiates from the base size, so a base outside the graded columns
	// would leave the future measurement chart without an origin. An empty size
	// range is allowed (the grade may not be defined yet at the proto stage).
	if pb.BaseSampleSizeId > 0 && len(sizeIds) > 0 && !slices.Contains(sizeIds, int(pb.BaseSampleSizeId)) {
		return nil, fmt.Errorf("base_sample_size_id %d must be one of size_ids", pb.BaseSampleSizeId)
	}

	// Sketch media arrives as two independent lists; concat into one internal slice,
	// each item tagged by its category (moodboard vs technical).
	moodboardMedia, err := parseTechCardMediaItems(pb.MoodboardMedia, entity.TechCardMediaCategoryMoodboard)
	if err != nil {
		return nil, err
	}
	technicalMedia, err := parseTechCardMediaItems(pb.TechnicalMedia, entity.TechCardMediaCategoryTechnical)
	if err != nil {
		return nil, err
	}
	media := make([]entity.TechCardMediaItem, 0, len(moodboardMedia)+len(technicalMedia))
	media = append(media, moodboardMedia...)
	media = append(media, technicalMedia...)

	callouts := make([]entity.TechCardCallout, 0, len(pb.Callouts))
	for _, c := range pb.Callouts {
		if len(c.Part) > maxVarchar255 || len(c.Dimensions) > maxVarchar255 {
			return nil, fmt.Errorf("callout part and dimensions must be at most %d characters", maxVarchar255)
		}
		if c.MediaId < 0 {
			return nil, fmt.Errorf("callout media_id must not be negative")
		}
		posX, err := nullDecimalFromPb(c.PosX)
		if err != nil {
			return nil, fmt.Errorf("callout pos_x: %w", err)
		}
		posY, err := nullDecimalFromPb(c.PosY)
		if err != nil {
			return nil, fmt.Errorf("callout pos_y: %w", err)
		}
		if err := validateUnitInterval(posX, "callout pos_x"); err != nil {
			return nil, err
		}
		if err := validateUnitInterval(posY, "callout pos_y"); err != nil {
			return nil, err
		}
		callouts = append(callouts, entity.TechCardCallout{
			Number:      int(c.Number),
			Part:        nullStringFromPb(c.Part),
			Description: nullStringFromPb(c.Description),
			Dimensions:  nullStringFromPb(c.Dimensions),
			MediaId:     nullInt32FromPb(c.MediaId),
			PosX:        posX,
			PosY:        posY,
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

	details, err := parseTechCardDetails(pb.Details)
	if err != nil {
		return nil, err
	}

	// materials (Phase 2). The BOM is parsed first (a pure article catalog); colourways
	// carry the usage recipe whose bom_item_index is range-checked against the BOM.
	bomItems, err := parseTechCardBomItems(pb.BomItems)
	if err != nil {
		return nil, err
	}
	colorways, err := parseTechCardColorways(pb.Colorways, productIds, len(bomItems), sizeIds)
	if err != nil {
		return nil, err
	}

	// production (Phase 3)
	construction, err := parseTechCardConstruction(pb.Construction)
	if err != nil {
		return nil, err
	}
	// Operations may reference a BOM material by index and a sketch callout by
	// number; both are validated against the same submitted payload (full-replace
	// has no stable ids to FK against on write).
	calloutNumbers := make(map[int]bool, len(callouts))
	for _, c := range callouts {
		calloutNumbers[c.Number] = true
	}
	operations, err := parseTechCardOperations(pb.Operations, calloutNumbers, len(bomItems))
	if err != nil {
		return nil, err
	}
	labels, err := parseTechCardLabels(pb.Labels)
	if err != nil {
		return nil, err
	}
	packaging, err := parseTechCardPackaging(pb.Packaging)
	if err != nil {
		return nil, err
	}
	costing, err := parseTechCardCosting(pb.Costing)
	if err != nil {
		return nil, err
	}
	issues, err := parseTechCardIssues(pb.Issues)
	if err != nil {
		return nil, err
	}
	sizeQuantities, err := parseTechCardSizeQuantities(pb.SizeQuantities, sizeIds)
	if err != nil {
		return nil, err
	}
	signoffs, err := parseTechCardSignoffs(pb.Signoffs)
	if err != nil {
		return nil, err
	}
	patterns, err := parseTechCardPatterns(pb.Patterns, sizeIds)
	if err != nil {
		return nil, err
	}

	// Release gate: a card cannot be RELEASED to a factory while any colourway's
	// lab dip is unapproved (bulk colour unsigned) or any high-severity maker issue
	// is still open (a known un-buildable operation).
	if approvalState == entity.TechCardApprovalReleased {
		for _, c := range colorways {
			if c.LabDipStatus != entity.LabDipApproved {
				return nil, fmt.Errorf("cannot release: colorway %q lab dip is %q, must be approved", c.Name, c.LabDipStatus)
			}
		}
		for _, is := range issues {
			if is.Severity == entity.IssueSeverityHigh && is.Status == entity.IssueStatusOpen {
				return nil, fmt.Errorf("cannot release: a high-severity issue is still open: %q", is.Description)
			}
		}
	}

	return &entity.TechCardInsert{
		StyleNumber:      nullStringFromPb(styleNumber),
		Name:             pb.Name,
		Brand:            nullStringFromPb(pb.Brand),
		Season:           nullStringFromPb(pb.Season),
		Collection:       nullStringFromPb(pb.Collection),
		CategoryId:       nullInt32FromPb(pb.CategoryId),
		TargetGender:     gender,
		Stage:            stage,
		Status:           nullStringFromPb(pb.Status),
		ApprovalState:    approvalState,
		ApprovedBy:       nullStringFromPb(pb.ApprovedBy),
		ApprovedAt:       nullTimeFromPbTimestamp(pb.ApprovedAt),
		ReleasedAt:       nullTimeFromPbTimestamp(pb.ReleasedAt),
		Version:          nullStringFromPb(pb.Version),
		RevisionDate:     nullDateFromPbTimestamp(pb.RevisionDate),
		BaseModelId:      nullInt32FromPb(pb.BaseModelId),
		BaseSampleSizeId: nullInt32FromPb(pb.BaseSampleSizeId),
		Designer:         nullStringFromPb(pb.Designer),
		Constructor:      nullStringFromPb(pb.Constructor),
		Technologist:     nullStringFromPb(pb.Technologist),
		MeasurementUnit:  unit,
		Concept:          nullStringFromPb(pb.Concept),
		Notes:            nullStringFromPb(pb.Notes),
		SizeIds:          sizeIds,
		ProductIds:       productIds,
		Media:            media,
		Callouts:         callouts,
		Revisions:        revisions,
		Details:          details,
		BomItems:         bomItems,
		Colorways:        colorways,
		Construction:     construction,
		Operations:       operations,
		Labels:           labels,
		Packaging:        packaging,
		Costing:          costing,
		Issues:           issues,
		SizeQuantities:   sizeQuantities,
		Signoffs:         signoffs,
		Patterns:         patterns,
	}, nil
}

// parseTechCardDetails parses the construction-description aspects, validating the key
// length and that each referenced media_id is positive (existence is enforced by the
// tech_card_detail_media FK → surfaces as InvalidArgument on write).
func parseTechCardDetails(pbs []*pb_common.TechCardDetail) ([]entity.TechCardDetail, error) {
	out := make([]entity.TechCardDetail, 0, len(pbs))
	for _, d := range pbs {
		if len(d.Key) > maxVarchar64 {
			return nil, fmt.Errorf("detail key must be at most %d characters", maxVarchar64)
		}
		mediaIds := make([]int, 0, len(d.MediaIds))
		seen := make(map[int]bool, len(d.MediaIds))
		for _, mid := range d.MediaIds {
			if mid <= 0 {
				return nil, fmt.Errorf("detail media_id must be positive")
			}
			if seen[int(mid)] {
				return nil, fmt.Errorf("detail has duplicate media_id %d", mid)
			}
			seen[int(mid)] = true
			mediaIds = append(mediaIds, int(mid))
		}
		out = append(out, entity.TechCardDetail{
			Key:      nullStringFromPb(d.Key),
			Text:     nullStringFromPb(d.Text),
			MediaIds: mediaIds,
		})
	}
	return out, nil
}

// parseTechCardPatterns parses the per-size PDF выкройки, validating each size is in the
// card's size range, the url is present, and the filename is not over-long.
func parseTechCardPatterns(pbs []*pb_common.TechCardSizePattern, sizeIds []int) ([]entity.TechCardSizePattern, error) {
	out := make([]entity.TechCardSizePattern, 0, len(pbs))
	for _, p := range pbs {
		sid := int(p.SizeId)
		if sid <= 0 || !slices.Contains(sizeIds, sid) {
			return nil, fmt.Errorf("pattern size_id %d must be one of size_ids", p.SizeId)
		}
		url := strings.TrimSpace(p.Url)
		if url == "" {
			return nil, fmt.Errorf("pattern url is required")
		}
		if len(url) > maxVarchar1024 {
			return nil, fmt.Errorf("pattern url must be at most %d characters", maxVarchar1024)
		}
		if !isHTTPURL(url) {
			return nil, fmt.Errorf("pattern url must be an http(s) URL")
		}
		if len(p.Filename) > maxVarchar255 {
			return nil, fmt.Errorf("pattern filename must be at most %d characters", maxVarchar255)
		}
		if p.SizeBytes < 0 {
			return nil, fmt.Errorf("pattern size_bytes must not be negative")
		}
		out = append(out, entity.TechCardSizePattern{
			SizeId:    sid,
			URL:       url,
			Filename:  nullStringFromPb(p.Filename),
			SizeBytes: nullInt64FromPb(p.SizeBytes),
		})
	}
	return out, nil
}

// ConvertEntityTechCardToPb converts an entity.TechCard to pb_common.TechCard. fx supplies the
// manual FX rates used to render the costing's base-currency rollup; pass a zero CostingFx to
// omit the *_base figures (e.g. in tests that don't exercise conversion).
func ConvertEntityTechCardToPb(tc *entity.TechCard, fx CostingFx) *pb_common.TechCard {
	if tc == nil {
		return nil
	}

	// Split the single internal media slice back into the two contract lists by category.
	var moodboardMedia, technicalMedia []*pb_common.TechCardMediaItem
	for _, m := range tc.Media {
		item := &pb_common.TechCardMediaItem{
			MediaId: int32(m.MediaId),
			Kind:    pbTechCardMediaKind(m.Kind),
			Caption: pbStringFromNull(m.Caption),
		}
		if m.Category == entity.TechCardMediaCategoryMoodboard {
			moodboardMedia = append(moodboardMedia, item)
		} else {
			technicalMedia = append(technicalMedia, item)
		}
	}

	var resolvedMoodboard, resolvedTechnical []*pb_common.TechCardMediaFull
	for i := range tc.ResolvedMedia {
		item := &pb_common.TechCardMediaFull{
			Media:   ConvertEntityToCommonMedia(&tc.ResolvedMedia[i].Media),
			Kind:    pbTechCardMediaKind(tc.ResolvedMedia[i].Kind),
			Caption: pbStringFromNull(tc.ResolvedMedia[i].Caption),
		}
		if tc.ResolvedMedia[i].Category == entity.TechCardMediaCategoryMoodboard {
			resolvedMoodboard = append(resolvedMoodboard, item)
		} else {
			resolvedTechnical = append(resolvedTechnical, item)
		}
	}

	callouts := make([]*pb_common.TechCardCallout, 0, len(tc.Callouts))
	for _, c := range tc.Callouts {
		callouts = append(callouts, &pb_common.TechCardCallout{
			Number:      int32(c.Number),
			Part:        pbStringFromNull(c.Part),
			Description: pbStringFromNull(c.Description),
			Dimensions:  pbStringFromNull(c.Dimensions),
			MediaId:     pbInt32FromNull(c.MediaId),
			PosX:        pbDecimalFromNull(c.PosX),
			PosY:        pbDecimalFromNull(c.PosY),
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

	// order quantity per size, used to compute each BOM line's size-run material cost.
	orderQtyBySize := make(map[int]int, len(tc.SizeQuantities))
	for _, q := range tc.SizeQuantities {
		orderQtyBySize[q.SizeId] = q.OrderQty
	}

	return &pb_common.TechCard{
		Id:          int32(tc.Id),
		LockVersion: int32(tc.LockVersion),
		CreatedAt:   timestamppb.New(tc.CreatedAt),
		UpdatedAt:   timestamppb.New(tc.UpdatedAt),
		TechCard: &pb_common.TechCardInsert{
			StyleNumber:      tc.StyleNumber.String,
			Name:             tc.Name,
			Brand:            pbStringFromNull(tc.Brand),
			Season:           pbStringFromNull(tc.Season),
			Collection:       pbStringFromNull(tc.Collection),
			CategoryId:       pbInt32FromNull(tc.CategoryId),
			TargetGender:     pbGenderFromNull(tc.TargetGender),
			Stage:            pbTechCardStage(tc.Stage),
			Status:           pbStringFromNull(tc.Status),
			ApprovalState:    pbTechCardApprovalState(tc.ApprovalState),
			ApprovedBy:       pbStringFromNull(tc.ApprovedBy),
			ApprovedAt:       pbTimestampFromNullTime(tc.ApprovedAt),
			ReleasedAt:       pbTimestampFromNullTime(tc.ReleasedAt),
			Version:          pbStringFromNull(tc.Version),
			RevisionDate:     pbTimestampFromNullTime(tc.RevisionDate),
			BaseModelId:      pbInt32FromNull(tc.BaseModelId),
			BaseSampleSizeId: pbInt32FromNull(tc.BaseSampleSizeId),
			Designer:         pbStringFromNull(tc.Designer),
			Constructor:      pbStringFromNull(tc.Constructor),
			Technologist:     pbStringFromNull(tc.Technologist),
			MeasurementUnit:  pbTechCardMeasurementUnit(tc.MeasurementUnit),
			Concept:          pbStringFromNull(tc.Concept),
			Notes:            pbStringFromNull(tc.Notes),
			SizeIds:          sizeIds,
			ProductIds:       productIds,
			MoodboardMedia:   moodboardMedia,
			TechnicalMedia:   technicalMedia,
			Callouts:         callouts,
			Revisions:        revisions,
			Details:          techCardDetailsToPb(tc.Details),
			BomItems:         techCardBomItemsToPb(tc.BomItems),
			Colorways:        techCardColorwaysToPb(tc.Colorways, tc.BomItems, orderQtyBySize),
			Construction:     techCardConstructionToPb(tc.Construction),
			Operations:       techCardOperationsToPb(tc.Operations),
			Labels:           techCardLabelsToPb(tc.Labels),
			Packaging:        techCardPackagingToPb(tc.Packaging),
			Costing:          techCardCostingToPb(tc, fx),
			Issues:           techCardIssuesToPb(tc.Issues),
			SizeQuantities:   techCardSizeQuantitiesToPb(tc.SizeQuantities),
			Signoffs:         techCardSignoffsToPb(tc.Signoffs),
			Patterns:         techCardPatternsToPb(tc.Patterns),
		},
		ResolvedMoodboardMedia: resolvedMoodboard,
		ResolvedTechnicalMedia: resolvedTechnical,
	}
}

// techCardDetailsToPb emits the construction-description aspects (+ media) for display.
func techCardDetailsToPb(details []entity.TechCardDetail) []*pb_common.TechCardDetail {
	out := make([]*pb_common.TechCardDetail, 0, len(details))
	for _, d := range details {
		out = append(out, &pb_common.TechCardDetail{
			Key:      pbStringFromNull(d.Key),
			Text:     pbStringFromNull(d.Text),
			MediaIds: intsToInt32(d.MediaIds),
		})
	}
	return out
}

// techCardPatternsToPb emits the per-size PDF выкройки for display.
func techCardPatternsToPb(ps []entity.TechCardSizePattern) []*pb_common.TechCardSizePattern {
	out := make([]*pb_common.TechCardSizePattern, 0, len(ps))
	for _, p := range ps {
		out = append(out, &pb_common.TechCardSizePattern{
			SizeId:    int32(p.SizeId),
			Url:       p.URL,
			Filename:  pbStringFromNull(p.Filename),
			SizeBytes: p.SizeBytes.Int64,
		})
	}
	return out
}

// ConvertEntityTechCardToListItemPb converts a header-only entity.TechCard to a
// lightweight pb_common.TechCardListItem for list views.
func ConvertEntityTechCardToListItemPb(tc *entity.TechCard) *pb_common.TechCardListItem {
	if tc == nil {
		return nil
	}
	return &pb_common.TechCardListItem{
		Id:            int32(tc.Id),
		StyleNumber:   tc.StyleNumber.String,
		Name:          tc.Name,
		Brand:         pbStringFromNull(tc.Brand),
		Stage:         pbTechCardStage(tc.Stage),
		Status:        pbStringFromNull(tc.Status),
		ApprovalState: pbTechCardApprovalState(tc.ApprovalState),
		TargetGender:  pbGenderFromNull(tc.TargetGender),
		Season:        pbStringFromNull(tc.Season),
		CreatedAt:     timestamppb.New(tc.CreatedAt),
		UpdatedAt:     timestamppb.New(tc.UpdatedAt),
		LockVersion:   int32(tc.LockVersion),
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
	return pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM
}

func pbTechCardMediaKind(k entity.TechCardMediaKind) pb_common.TechCardMediaKind {
	if v, ok := techCardMediaKindEntityToPb[k]; ok {
		return v
	}
	return pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_PREVIEW
}

// --- materials (Phase 2): parse pb -> entity ---

// unionColorwayProductIds appends any positive colourway product_id not already in productIds,
// preserving order (existing ids first, new ones in colourway order). This keeps tech_card_product
// (the canon) a superset of every colourway's annotated product, so linking a colour to a product
// never needs a separate edit to the product list. Deduplicates against the existing set.
func unionColorwayProductIds(productIds []int, colorways []*pb_common.TechCardColorway) []int {
	seen := make(map[int]bool, len(productIds))
	for _, id := range productIds {
		seen[id] = true
	}
	for _, c := range colorways {
		if c == nil || c.ProductId <= 0 || seen[int(c.ProductId)] {
			continue
		}
		seen[int(c.ProductId)] = true
		productIds = append(productIds, int(c.ProductId))
	}
	return productIds
}

func parseTechCardColorways(pbs []*pb_common.TechCardColorway, productIds []int, bomItemCount int, sizeIds []int) ([]entity.TechCardColorway, error) {
	out := make([]entity.TechCardColorway, 0, len(pbs))
	// Non-empty codes must be unique within the card (DB enforces
	// uniq_tech_card_colorway_code); dedupe here so a collision fails as a precise
	// InvalidArgument, not a misleading style_number/season unique-violation.
	seenCode := make(map[string]bool, len(pbs))
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
		if c.Code != "" {
			if seenCode[c.Code] {
				return nil, fmt.Errorf("colorways contain a duplicate code: %q", c.Code)
			}
			seenCode[c.Code] = true
		}
		if c.ProductId < 0 {
			return nil, fmt.Errorf("colorway product_id must not be negative")
		}
		// Invariant (post-auto-seed): a colourway's product must be in the card's product_ids.
		// unionColorwayProductIds already folded every colourway product into product_ids before
		// this parse, so this never fires on a normal payload — it is a defensive guard that the
		// annotation ⊆ canon invariant holds.
		if c.ProductId > 0 && !slices.Contains(productIds, int(c.ProductId)) {
			return nil, fmt.Errorf("colorway product_id %d must be one of the tech card's product_ids", c.ProductId)
		}
		status := entity.LabDipPending
		if c.LabDipStatus != pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_UNKNOWN {
			s, ok := techCardLabDipPbToEntity[c.LabDipStatus]
			if !ok {
				return nil, fmt.Errorf("unknown lab dip status: %v", c.LabDipStatus)
			}
			status = s
		}
		if len(c.Pantone) > maxVarchar64 || len(c.Hex) > 7 || len(c.LabDipDecidedBy) > maxVarchar255 {
			return nil, fmt.Errorf("colorway pantone/hex/lab_dip_decided_by too long")
		}
		// Validate format/membership in the DTO (not just lengths) so a bad value
		// fails as InvalidArgument instead of tripping the DB CHECK as Internal-500.
		if c.Hex != "" && !isHexColor(c.Hex) {
			return nil, fmt.Errorf("colorway hex must be #RRGGBB")
		}
		if c.PantoneSystem != "" && !validPantoneSystems[c.PantoneSystem] {
			return nil, fmt.Errorf("colorway pantone_system must be one of TCX, TPX, TPG, C, U")
		}
		if c.SwatchMediaId < 0 || c.LabDipRound < 0 {
			return nil, fmt.Errorf("colorway swatch_media_id/lab_dip_round must not be negative")
		}
		usages, err := parseTechCardColorwayUsages(c.Usages, bomItemCount, sizeIds)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardColorway{
			Code:               nullStringFromPb(c.Code),
			Name:               c.Name,
			LabDipStatus:       status,
			ProductId:          nullInt32FromPb(c.ProductId),
			Comment:            nullStringFromPb(c.Comment),
			Pantone:            nullStringFromPb(c.Pantone),
			PantoneSystem:      nullStringFromPb(c.PantoneSystem),
			Hex:                nullStringFromPb(c.Hex),
			SwatchMediaId:      nullInt32FromPb(c.SwatchMediaId),
			LabDipRound:        nullInt32FromPb(c.LabDipRound),
			LabDipSubmittedAt:  nullDateFromPbTimestamp(c.LabDipSubmittedAt),
			LabDipDecidedAt:    nullDateFromPbTimestamp(c.LabDipDecidedAt),
			LabDipDecidedBy:    nullStringFromPb(c.LabDipDecidedBy),
			LabDipRejectReason: nullStringFromPb(c.LabDipRejectReason),
			Usages:             usages,
		})
	}
	return out, nil
}

// parseTechCardColorwayUsages parses one colourway's material recipe. A usage's
// bom_item_index (when set) must point at a submitted BOM line; placement is normalised
// (trim+lower) so the construction resolver can match operation.placement to it (plan §3).
func parseTechCardColorwayUsages(pbs []*pb_common.TechCardColorwayUsage, bomItemCount int, sizeIds []int) ([]entity.TechCardColorwayUsage, error) {
	out := make([]entity.TechCardColorwayUsage, 0, len(pbs))
	for _, u := range pbs {
		var bomItemIndex sql.NullInt32
		if u.BomItemIndex != nil {
			idx := *u.BomItemIndex
			if idx < 0 || int(idx) >= bomItemCount {
				return nil, fmt.Errorf("usage bom_item_index %d out of range (have %d bom_items)", idx, bomItemCount)
			}
			bomItemIndex = sql.NullInt32{Int32: idx, Valid: true}
		}
		if len(u.Placement) > maxVarchar255 {
			return nil, fmt.Errorf("usage placement must be at most %d characters", maxVarchar255)
		}
		if len(u.Color) > maxVarchar255 || len(u.Pantone) > maxVarchar64 {
			return nil, fmt.Errorf("usage color/pantone too long")
		}
		consumption, err := nullDecimalFromPb(u.Consumption)
		if err != nil {
			return nil, fmt.Errorf("usage consumption: %w", err)
		}
		if err := validateDecimalScale(consumption, "usage consumption", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		quantity, err := nullDecimalFromPb(u.Quantity)
		if err != nil {
			return nil, fmt.Errorf("usage quantity: %w", err)
		}
		if err := validateDecimalScale(quantity, "usage quantity", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		sizeConsumptions, err := parseTechCardSizeConsumptions(u.SizeConsumptions, sizeIds)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardColorwayUsage{
			BomItemIndex:     bomItemIndex,
			Placement:        normalizedPlacementNull(u.Placement),
			Color:            nullStringFromPb(u.Color),
			Pantone:          nullStringFromPb(u.Pantone),
			Consumption:      consumption,
			Quantity:         quantity,
			SizeConsumptions: sizeConsumptions,
		})
	}
	return out, nil
}

func parseTechCardBomItems(pbs []*pb_common.TechCardBomItem) ([]entity.TechCardBomItem, error) {
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
		unitPrice, err := nullDecimalFromPb(b.UnitPrice)
		if err != nil {
			return nil, fmt.Errorf("bom unit_price: %w", err)
		}
		if err := validateDecimalScale(unitPrice, "bom unit_price", bomPriceMaxFrac, bomPriceLimit); err != nil {
			return nil, err
		}
		fabricWidth, err := nullDecimalFromPb(b.FabricWidth)
		if err != nil {
			return nil, fmt.Errorf("bom fabric_width: %w", err)
		}
		fabricGsm, err := nullDecimalFromPb(b.FabricWeightGsm)
		if err != nil {
			return nil, fmt.Errorf("bom fabric_weight_gsm: %w", err)
		}
		wastage, err := nullDecimalFromPb(b.WastagePercent)
		if err != nil {
			return nil, fmt.Errorf("bom wastage_percent: %w", err)
		}
		for _, v := range []struct {
			nd    decimal.NullDecimal
			field string
		}{{fabricWidth, "bom fabric_width"}, {fabricGsm, "bom fabric_weight_gsm"}} {
			if err := validateDecimalScale(v.nd, v.field, 2, 100_000); err != nil {
				return nil, err
			}
		}
		if err := validateDecimalScale(wastage, "bom wastage_percent", 2, 1_000); err != nil {
			return nil, err
		}
		if wastage.Valid && wastage.Decimal.GreaterThan(decimal.NewFromInt(100)) {
			return nil, fmt.Errorf("bom wastage_percent must be between 0 and 100")
		}
		direction := sql.NullString{}
		if b.FabricDirection != pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_UNKNOWN {
			d, ok := techCardFabricDirectionPbToEntity[b.FabricDirection]
			if !ok {
				return nil, fmt.Errorf("unknown bom fabric_direction: %v", b.FabricDirection)
			}
			direction = sql.NullString{String: string(d), Valid: true}
		}

		materialID := sql.NullInt64{}
		if b.MaterialId != 0 {
			materialID = sql.NullInt64{Int64: b.MaterialId, Valid: true}
		}

		out = append(out, entity.TechCardBomItem{
			MaterialId:      materialID,
			Section:         section,
			Name:            b.Name,
			Supplier:        nullStringFromPb(b.Supplier),
			SupplierRef:     nullStringFromPb(b.SupplierRef),
			Color:           nullStringFromPb(b.Color),
			Composition:     nullStringFromPb(b.Composition),
			Spec:            nullStringFromPb(b.Spec),
			Unit:            nullStringFromPb(b.Unit),
			UnitPrice:       unitPrice,
			Currency:        nullStringFromPb(b.Currency),
			Comment:         nullStringFromPb(b.Comment),
			FabricWidth:     fabricWidth,
			FabricWeightGsm: fabricGsm,
			FabricDirection: direction,
			WastagePercent:  wastage,
		})
	}
	return out, nil
}

// parseTechCardSizeConsumptions parses the per-size consumption of a colourway usage,
// validating each size is in the card's size range, consumption is present and
// non-negative, and no size repeats.
func parseTechCardSizeConsumptions(pbs []*pb_common.TechCardBomSizeConsumption, sizeIds []int) ([]entity.TechCardBomSizeConsumption, error) {
	out := make([]entity.TechCardBomSizeConsumption, 0, len(pbs))
	seen := make(map[int]bool, len(pbs))
	for _, sc := range pbs {
		sid := int(sc.SizeId)
		if sid <= 0 || !slices.Contains(sizeIds, sid) {
			return nil, fmt.Errorf("usage size_consumption size_id %d must be one of size_ids", sc.SizeId)
		}
		if seen[sid] {
			return nil, fmt.Errorf("duplicate usage size_consumption for size_id %d", sc.SizeId)
		}
		seen[sid] = true
		consumption, err := requiredDecimalFromPb(sc.Consumption, "usage size_consumption", bomQtyMaxFrac, bomQtyLimit)
		if err != nil {
			return nil, err
		}
		if consumption.IsNegative() {
			return nil, fmt.Errorf("usage size_consumption must not be negative")
		}
		out = append(out, entity.TechCardBomSizeConsumption{SizeId: sid, Consumption: consumption})
	}
	return out, nil
}

// --- materials (Phase 2): emit entity -> pb ---

// techCardColorwaysToPb emits colourways with their material recipe (usages). Each usage
// carries its computed per-garment line_total and whole-run size_run_total, resolved
// against the BOM article it points at.
func techCardColorwaysToPb(cws []entity.TechCardColorway, bomItems []entity.TechCardBomItem, orderQtyBySize map[int]int) []*pb_common.TechCardColorway {
	out := make([]*pb_common.TechCardColorway, 0, len(cws))
	for ci := range cws {
		c := &cws[ci]
		out = append(out, &pb_common.TechCardColorway{
			Code:               pbStringFromNull(c.Code),
			Name:               c.Name,
			LabDipStatus:       pbLabDipStatus(c.LabDipStatus),
			ProductId:          pbInt32FromNull(c.ProductId),
			Comment:            pbStringFromNull(c.Comment),
			Pantone:            pbStringFromNull(c.Pantone),
			PantoneSystem:      pbStringFromNull(c.PantoneSystem),
			Hex:                pbStringFromNull(c.Hex),
			SwatchMediaId:      pbInt32FromNull(c.SwatchMediaId),
			LabDipRound:        pbInt32FromNull(c.LabDipRound),
			LabDipSubmittedAt:  pbTimestampFromNullTime(c.LabDipSubmittedAt),
			LabDipDecidedAt:    pbTimestampFromNullTime(c.LabDipDecidedAt),
			LabDipDecidedBy:    pbStringFromNull(c.LabDipDecidedBy),
			LabDipRejectReason: pbStringFromNull(c.LabDipRejectReason),
			Usages:             techCardUsagesToPb(c.Usages, bomItems, orderQtyBySize),
		})
	}
	return out
}

// techCardUsagesToPb emits a colourway's usages, each with its computed per-garment
// line_total and whole-run size_run_total (resolved against the referenced BOM article).
func techCardUsagesToPb(usages []entity.TechCardColorwayUsage, bomItems []entity.TechCardBomItem, orderQtyBySize map[int]int) []*pb_common.TechCardColorwayUsage {
	out := make([]*pb_common.TechCardColorwayUsage, 0, len(usages))
	for i := range usages {
		u := &usages[i]
		bom := bomItemAtIndex(bomItems, u.BomItemIndex)
		var bomItemIndex *int32
		if u.BomItemIndex.Valid {
			v := u.BomItemIndex.Int32
			bomItemIndex = &v
		}
		sizeCons := make([]*pb_common.TechCardBomSizeConsumption, 0, len(u.SizeConsumptions))
		for _, sc := range u.SizeConsumptions {
			sizeCons = append(sizeCons, &pb_common.TechCardBomSizeConsumption{
				SizeId:      int32(sc.SizeId),
				Consumption: pbDecimalFromDecimal(sc.Consumption),
			})
		}
		out = append(out, &pb_common.TechCardColorwayUsage{
			BomItemIndex:     bomItemIndex,
			Placement:        pbStringFromNull(u.Placement),
			Color:            pbStringFromNull(u.Color),
			Pantone:          pbStringFromNull(u.Pantone),
			Consumption:      pbDecimalFromNull(u.Consumption),
			Quantity:         pbDecimalFromNull(u.Quantity),
			SizeConsumptions: sizeCons,
			LineTotal:        pbMoneyFromNull(u.LineTotal(bom)),
			SizeRunTotal:     pbMoneyFromNull(u.SizeRunTotal(bom, orderQtyBySize)),
		})
	}
	return out
}

// bomItemAtIndex returns the BOM article a usage/operation bom_item_index points at, or
// nil when unset or out of range (a draft can reference a not-yet-added article).
func bomItemAtIndex(bomItems []entity.TechCardBomItem, idx sql.NullInt32) *entity.TechCardBomItem {
	if !idx.Valid || idx.Int32 < 0 || int(idx.Int32) >= len(bomItems) {
		return nil
	}
	return &bomItems[idx.Int32]
}

func techCardBomItemsToPb(items []entity.TechCardBomItem) []*pb_common.TechCardBomItem {
	out := make([]*pb_common.TechCardBomItem, 0, len(items))
	for i := range items {
		b := &items[i]
		out = append(out, &pb_common.TechCardBomItem{
			MaterialId:      b.MaterialId.Int64,
			Section:         pbBomSection(b.Section),
			Name:            b.Name,
			Supplier:        pbStringFromNull(b.Supplier),
			SupplierRef:     pbStringFromNull(b.SupplierRef),
			Color:           pbStringFromNull(b.Color),
			Composition:     pbStringFromNull(b.Composition),
			Spec:            pbStringFromNull(b.Spec),
			Unit:            pbStringFromNull(b.Unit),
			UnitPrice:       pbDecimalFromNull(b.UnitPrice),
			Currency:        pbStringFromNull(b.Currency),
			Comment:         pbStringFromNull(b.Comment),
			FabricWidth:     pbDecimalFromNull(b.FabricWidth),
			FabricWeightGsm: pbDecimalFromNull(b.FabricWeightGsm),
			FabricDirection: pbFabricDirection(b.FabricDirection),
			WastagePercent:  pbDecimalFromNull(b.WastagePercent),
		})
	}
	return out
}

func pbFabricDirection(s sql.NullString) pb_common.TechCardFabricDirection {
	if !s.Valid {
		return pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_UNKNOWN
	}
	if v, ok := techCardFabricDirectionEntityToPb[entity.TechCardFabricDirection(s.String)]; ok {
		return v
	}
	return pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_UNKNOWN
}

// validPantoneSystems mirrors the tech_card_colorway.pantone_system CHECK.
var validPantoneSystems = map[string]bool{"TCX": true, "TPX": true, "TPG": true, "C": true, "U": true}

// isHTTPURL reports whether s is an http(s) URL — pattern PDFs are served over the CDN,
// so a non-http scheme (e.g. javascript:/data:) is rejected at the write boundary.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// isHexColor reports whether s is a #RRGGBB colour (mirrors the colorway.hex CHECK).
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// validateUnitInterval rejects a non-null decimal outside [0,1] (callout pos_x/y).
func validateUnitInterval(nd decimal.NullDecimal, field string) error {
	if !nd.Valid {
		return nil
	}
	if nd.Decimal.IsNegative() || nd.Decimal.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("%s must be between 0 and 1", field)
	}
	return nil
}

func parseTechCardSizeQuantities(pbs []*pb_common.TechCardSizeQuantity, sizeIds []int) ([]entity.TechCardSizeQuantity, error) {
	out := make([]entity.TechCardSizeQuantity, 0, len(pbs))
	seen := make(map[int]bool, len(pbs))
	for _, q := range pbs {
		sid := int(q.SizeId)
		if sid <= 0 || !slices.Contains(sizeIds, sid) {
			return nil, fmt.Errorf("size_quantity size_id %d must be one of size_ids", q.SizeId)
		}
		if seen[sid] {
			return nil, fmt.Errorf("duplicate size_quantity for size_id %d", q.SizeId)
		}
		seen[sid] = true
		if q.OrderQty < 0 {
			return nil, fmt.Errorf("size_quantity order_qty must not be negative")
		}
		out = append(out, entity.TechCardSizeQuantity{SizeId: sid, OrderQty: int(q.OrderQty)})
	}
	return out, nil
}

func techCardSizeQuantitiesToPb(qs []entity.TechCardSizeQuantity) []*pb_common.TechCardSizeQuantity {
	out := make([]*pb_common.TechCardSizeQuantity, 0, len(qs))
	for _, q := range qs {
		out = append(out, &pb_common.TechCardSizeQuantity{SizeId: int32(q.SizeId), OrderQty: int32(q.OrderQty)})
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

// normalizePlacement trims and lowercases a freeform garment-part string so usage and
// operation placements compare equal regardless of casing/whitespace (plan §3 resolver).
func normalizePlacement(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// normalizedPlacementNull returns the normalised placement, NULL when empty.
func normalizedPlacementNull(s string) sql.NullString {
	n := normalizePlacement(s)
	if n == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: n, Valid: true}
}

// pbMoneyFromNull emits a computed money amount rounded to 2 decimals (banker's rounding),
// or nil when absent. The frontend trusts these server totals and never re-sums.
func pbMoneyFromNull(nd decimal.NullDecimal) *pb_decimal.Decimal {
	if !nd.Valid {
		return nil
	}
	return &pb_decimal.Decimal{Value: roundMoney(nd.Decimal).String()}
}

// roundMoney rounds a money amount to 2 decimals (banker's rounding) for storage/emit.
func roundMoney(d decimal.Decimal) decimal.Decimal {
	return d.RoundBank(2)
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
// preserving the full time (the column is a TIMESTAMP, e.g. released_at). The
// grpc-gateway serialises an unset Go time.Time as "0001-01-01T00:00:00Z" — a
// non-nil timestamp holding the zero instant — so that is treated as NULL too,
// otherwise MySQL rejects it ("Incorrect date value: '0000-00-00'", err 1292).
func nullTimeFromPbTimestamp(ts *timestamppb.Timestamp) sql.NullTime {
	if ts == nil {
		return sql.NullTime{}
	}
	t := ts.AsTime().UTC()
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// nullDateFromPbTimestamp maps an optional timestamp to a nullable DATE value,
// normalised to UTC midnight (the column is a DATE). Like nullTimeFromPbTimestamp,
// the zero instant ("0001-01-01T00:00:00Z") is treated as NULL.
func nullDateFromPbTimestamp(ts *timestamppb.Timestamp) sql.NullTime {
	if ts == nil {
		return sql.NullTime{}
	}
	t := ts.AsTime().UTC()
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func pbTimestampFromNullTime(nt sql.NullTime) *timestamppb.Timestamp {
	if !nt.Valid {
		return nil
	}
	return timestamppb.New(nt.Time)
}

// ConvertTechCardReleaseMetaToPb converts an immutable release-snapshot header (task 11) to pb.
// The JSON blob itself is not carried here — it is parsed separately by the read handler.
func ConvertTechCardReleaseMetaToPb(m entity.TechCardReleaseMeta) *pb_common.TechCardReleaseMeta {
	return &pb_common.TechCardReleaseMeta{
		Id:         int32(m.Id),
		TechCardId: int32(m.TechCardId),
		Version:    pbStringFromNull(m.Version),
		ReleasedBy: pbStringFromNull(m.ReleasedBy),
		UnitCost:   pbDecimalFromNull(m.UnitCost),
		Currency:   pbStringFromNull(m.Currency),
		CreatedAt:  timestamppb.New(m.CreatedAt),
	}
}
