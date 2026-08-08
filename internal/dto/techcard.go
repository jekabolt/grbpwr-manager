package dto

import (
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"unicode/utf8"
)

// Column length bounds for tech-card varchar fields, mirroring the schema so that
// over-length input fails as InvalidArgument rather than a MySQL 1406 Internal error.
const (
	maxVarchar32   = 32
	maxVarchar64   = 64
	maxVarchar191  = 191
	maxVarchar512  = 512
	maxVarchar1024 = 1024
	// maxPieceDxfAliases bounds the machine-generated DXF-block alias set (marker bounds precedent).
	maxPieceDxfAliases = 2000

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

// КАК КРОИТСЯ (0275). UNKNOWN is deliberately absent from the table: it is not a value but the
// absence of one («не размечено»), so it can only ever become a NULL column — the same discipline
// TechCardBomPurpose's UNSET follows.
var techCardPieceCutSymmetryPbToEntity = map[pb_common.TechCardPieceCutSymmetry]entity.TechCardPieceCutSymmetry{
	pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_IDENTICAL: entity.PieceCutSymmetryIdentical,
	pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED:  entity.PieceCutSymmetryMirrored,
	pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_FOLD:      entity.PieceCutSymmetryFold,
}

var techCardPieceCutSymmetryEntityToPb = func() map[entity.TechCardPieceCutSymmetry]pb_common.TechCardPieceCutSymmetry {
	m := make(map[entity.TechCardPieceCutSymmetry]pb_common.TechCardPieceCutSymmetry, len(techCardPieceCutSymmetryPbToEntity))
	for k, v := range techCardPieceCutSymmetryPbToEntity {
		m[v] = k
	}
	return m
}()

// PieceCutSymmetryToPb maps a stored marking to the wire enum; an unset column reads as UNKNOWN, which
// is exactly «не размечено». An unrecognised stored value also reads as UNKNOWN rather than failing
// the whole card read — the DB CHECK makes that unreachable through this app, and a read is the wrong
// place to discover it. Exported because the cut-list projection (apisrv/admin/style_cutlist.go)
// speaks the same vocabulary from its own response message.
func PieceCutSymmetryToPb(s sql.NullString) pb_common.TechCardPieceCutSymmetry {
	if !s.Valid {
		return pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN
	}
	if v, ok := techCardPieceCutSymmetryEntityToPb[entity.TechCardPieceCutSymmetry(s.String)]; ok {
		return v
	}
	return pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN
}

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

// techCardBomPurposePbToEntity maps the closed НАЗНАЧЕНИЕ vocabulary (0265). UNSET is deliberately
// absent: it is not a value, it is the absence of one ("not sorted yet"), and it maps to a NULL
// column rather than to a string.
var techCardBomPurposePbToEntity = map[pb_common.TechCardBomPurpose]entity.TechCardBomPurpose{
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN:        entity.BomPurposeMain,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_LINING:      entity.BomPurposeLining,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_POCKETING:   entity.BomPurposePocketing,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_INTERFACING: entity.BomPurposeInterfacing,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_INSULATION:  entity.BomPurposeInsulation,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_CONTRAST:    entity.BomPurposeContrast,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MESH:        entity.BomPurposeMesh,
	pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_OTHER:       entity.BomPurposeOther,
}

var techCardBomPurposeEntityToPb = func() map[entity.TechCardBomPurpose]pb_common.TechCardBomPurpose {
	m := make(map[entity.TechCardBomPurpose]pb_common.TechCardBomPurpose, len(techCardBomPurposePbToEntity))
	for k, v := range techCardBomPurposePbToEntity {
		m[v] = k
	}
	return m
}()

// techCardBomKindPbToEntity maps the closed ЧТО ЭТО ЗА ПОЗИЦИЯ vocabulary (0278). UNSET is
// deliberately absent, on the same rule as TechCardBomPurpose's: it is not a value but the absence
// of one ("not classified yet"), and it must stay out of the table so it can only ever become a NULL
// column. The kind↔section pairing is NOT enforced here — it needs the roll-goods complement, which
// lives in the store beside the list it is derived from.
var techCardBomKindPbToEntity = map[pb_common.TechCardBomKind]entity.TechCardBomKind{
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER:            entity.BomKindZipper,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER_SLIDER:     entity.BomKindZipperSlider,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUTTON:            entity.BomKindButton,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_SNAP:              entity.BomKindSnap,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_RIVET:             entity.BomKindRivet,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_EYELET:            entity.BomKindEyelet,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HOOK_AND_BAR:      entity.BomKindHookAndBar,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_SNAP_HOOK:         entity.BomKindSnapHook,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUCKLE:            entity.BomKindBuckle,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_STRAP_ADJUSTER:    entity.BomKindStrapAdjuster,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_RING:              entity.BomKindRing,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_TOGGLE:            entity.BomKindToggle,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_CORD_STOPPER:      entity.BomKindCordStopper,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_CORD_END:          entity.BomKindCordEnd,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_MAGNET:            entity.BomKindMagnet,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_CHAIN:             entity.BomKindChain,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ELASTIC:           entity.BomKindElastic,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_DRAWCORD:          entity.BomKindDrawcord,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BINDING:           entity.BomKindBinding,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_TAPE:              entity.BomKindTape,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_PIPING:            entity.BomKindPiping,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_WEBBING:           entity.BomKindWebbing,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HOOK_LOOP:         entity.BomKindHookLoop,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BONING:            entity.BomKindBoning,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_LACE:              entity.BomKindLace,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_RIBBING:           entity.BomKindRibbing,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_PRINT:             entity.BomKindPrint,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_EMBROIDERY:        entity.BomKindEmbroidery,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_APPLIQUE:          entity.BomKindApplique,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_PATCH:             entity.BomKindPatch,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HEAT_TRANSFER:     entity.BomKindHeatTransfer,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_RHINESTONE:        entity.BomKindRhinestone,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_SEQUIN:            entity.BomKindSequin,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_STUD:              entity.BomKindStud,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_FOIL:              entity.BomKindFoil,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_LASER:             entity.BomKindLaser,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_SEWING_THREAD:     entity.BomKindSewingThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_TOPSTITCH_THREAD:  entity.BomKindTopstitchThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_OVERLOCK_THREAD:   entity.BomKindOverlockThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUTTONHOLE_THREAD: entity.BomKindButtonholeThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_EMBROIDERY_THREAD: entity.BomKindEmbroideryThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ELASTIC_THREAD:    entity.BomKindElasticThread,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_POLYBAG:           entity.BomKindPolybag,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_CARTON:            entity.BomKindCarton,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HANGER:            entity.BomKindHanger,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HANGTAG_STRING:    entity.BomKindHangtagString,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_STICKER:           entity.BomKindSticker,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_TISSUE:            entity.BomKindTissue,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_DUST_BAG:          entity.BomKindDustBag,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_GARMENT_CASE:      entity.BomKindGarmentCase,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_INSERT_CARD:       entity.BomKindInsertCard,
	pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_OTHER:             entity.BomKindOther,
}

var techCardBomKindEntityToPb = func() map[entity.TechCardBomKind]pb_common.TechCardBomKind {
	m := make(map[entity.TechCardBomKind]pb_common.TechCardBomKind, len(techCardBomKindPbToEntity))
	for k, v := range techCardBomKindPbToEntity {
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

// ConvertPbSkuSeasonToEntity validates the atomic style-owned season pair. A nil message means
// explicitly unset (allowed for early draft/idea cards); once present, both fields are required.
func ConvertPbSkuSeasonToEntity(pb *pb_common.SkuSeason) (entity.SeasonEnum, int, error) {
	if pb == nil {
		return "", 0, nil
	}
	code, err := ConvertPbSeasonEnumToEntitySeasonEnum(pb.Code)
	if err != nil {
		return "", 0, fmt.Errorf("sku_season code is required and must be SS, FW, PF, or RC")
	}
	if pb.Year < 2000 || pb.Year > 2099 {
		return "", 0, fmt.Errorf("sku_season year must be between 2000 and 2099")
	}
	return code, int(pb.Year), nil
}

func skuSeasonToPb(code sql.NullString, year sql.NullInt32) *pb_common.SkuSeason {
	if !code.Valid || !year.Valid {
		return nil
	}
	pbCode, _ := ConvertEntitySeasonToPbSeasonEnum(entity.SeasonEnum(code.String))
	if pbCode == pb_common.SeasonEnum_SEASON_ENUM_UNKNOWN || year.Int32 < 2000 || year.Int32 > 2099 {
		return nil
	}
	return &pb_common.SkuSeason{Code: pbCode, Year: year.Int32}
}

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
		{"collection", pb.Collection, maxVarchar255},
		{"status", pb.Status, maxVarchar255},
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
	seasonCode, seasonYear, err := ConvertPbSkuSeasonToEntity(pb.SkuSeason)
	if err != nil {
		return nil, err
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
	// stopped sending cm, though the enum keeps cm for back-compat reads). Presence is carried
	// alongside the value so an UPDATE can preserve a card's stored unit instead of defaulting it
	// — the default is a create-time choice, not a licence to re-unit an existing chart.
	unit := entity.TechCardUnitMm
	unitSet := pb.MeasurementUnit != pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_UNKNOWN
	if unitSet {
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
	// PR6 R1/R4: product_ids and colorways are no longer part of the style write contract — the
	// product↔style link is product.style_id (single source) and a colourway's recipe lives on the
	// colourway (ColorwayDevelopmentInsert.usages). The tech_card_product mirror is re-derived from
	// product.style_id in the store, not from this payload.

	// NF-07 purpose: sellable (default) produces a product; auxiliary produces a packaging material.
	// A sellable card must not carry an output material. output_material_id is only required before
	// the first run (the service/receive path enforces that), not at save time — a card can be
	// drafted first. PR6 R1/R4: "auxiliary cards link no products" is enforced where the link is
	// created — product/colorway_write.go requireSellableStyle, called by CreateColorway — not here;
	// product_ids left this payload. (That check was claimed here long before it existed; it does now.)
	purpose := entity.TechCardPurposeSellable
	if pb.Purpose != pb_common.TechCardPurpose_TECH_CARD_PURPOSE_UNKNOWN {
		purpose = techCardPurposeFromPb(pb.Purpose)
		if !entity.ValidTechCardPurposes[purpose] {
			return nil, fmt.Errorf("purpose must be one of sellable|auxiliary")
		}
	}
	var outputMaterialId sql.NullInt64
	if purpose == entity.TechCardPurposeAuxiliary {
		if pb.OutputMaterialId < 0 {
			return nil, fmt.Errorf("output_material_id must not be negative")
		}
		if pb.OutputMaterialId > 0 {
			outputMaterialId = sql.NullInt64{Int64: int64(pb.OutputMaterialId), Valid: true}
		}
	} else if pb.OutputMaterialId != 0 {
		return nil, fmt.Errorf("output_material_id is only for auxiliary cards")
	}

	// WS7 aux_subtype: only an auxiliary card may carry one; a sellable card must leave it UNKNOWN. An
	// auxiliary card with UNKNOWN is allowed (unclassified) and stored NULL — the DB gate
	// (chk_tech_card_aux_subtype_purpose) enforces the same invariant.
	var auxSubtype sql.NullString
	if pb.AuxSubtype != pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_UNKNOWN {
		if purpose != entity.TechCardPurposeAuxiliary {
			return nil, fmt.Errorf("aux_subtype is only for auxiliary cards")
		}
		st := techCardAuxSubtypeFromPb(pb.AuxSubtype)
		if !entity.ValidTechCardAuxSubtypes[st] {
			return nil, fmt.Errorf("aux_subtype is invalid")
		}
		auxSubtype = sql.NullString{String: string(st), Valid: true}
	}

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

	// Q1: revisions are a server-stamped auto-journal, not a client input — they are not parsed here.

	details, err := parseTechCardDetails(pb.Details)
	if err != nil {
		return nil, err
	}

	// materials (Phase 2). The BOM is the article catalog. PR6 R1: colourways are no longer style
	// children — a colourway's material recipe lives on the colourway (ColorwayDevelopmentInsert.usages).
	bomItems, err := parseTechCardBomItems(pb.BomItems)
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
	// Cut-pieces (NF-05): each material addresses its colourway by explicit colorway_id (validated in
	// the store against product.style_id); bom refs and callout_number are range-checked here against
	// the same full-replace payload.
	pieces, err := parseTechCardPieces(pb.Pieces, len(bomItems), calloutNumbers)
	if err != nil {
		return nil, err
	}
	pieceDxfAliases, pieceDxfAliasesSet, err := parseTechCardPieceDxfAliases(pb.PieceDxfAliases)
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
	issues, err := parseTechCardIssues(pb.Issues, len(operations), calloutNumbers)
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

	// Release gate: a card cannot be RELEASED to a factory while any high-severity maker issue is
	// still open (a known un-buildable operation).
	// TODO(pr6-B): the colourway lab-dip release gate (no release while any colourway's bulk colour is
	// unsigned) was a parse-time check over the style's colourways, which are no longer a write child
	// (R1 merge). lab_dip_status now lives on product; re-add the gate at the style-release path
	// (reading persisted colourways) once its post-merge semantics (draft/NULL handling) are settled
	// with live-DB verification (T-E / R4 style lifecycle). Not silently lost — data is preserved.
	if approvalState == entity.TechCardApprovalReleased {
		for _, is := range issues {
			if is.Severity == entity.IssueSeverityHigh && is.Status == entity.IssueStatusOpen {
				return nil, fmt.Errorf("cannot release: a high-severity issue is still open: %q", is.Description)
			}
		}
	}

	// Validated at the boundary rather than left to the CHECK: chk_tc_required_seam_allowance would
	// answer 3819 with no field name, and this is an operator-typed number on a form control.
	requiredSeamAllowance, err := nullDecimalFromPb(pb.RequiredSeamAllowanceMm)
	if err != nil {
		return nil, fmt.Errorf("required_seam_allowance_mm: %w", err)
	}
	if err := entity.ValidateSeamAllowanceStandardMm("required_seam_allowance_mm", requiredSeamAllowance); err != nil {
		return nil, err
	}
	insert := &entity.TechCardInsert{
		StyleNumber:        nullStringFromPb(styleNumber),
		StyleNumberSource:  styleNumberSourceFromPb(pb.StyleNumberSource),
		Purpose:            purpose,
		OutputMaterialId:   outputMaterialId,
		AuxSubtype:         auxSubtype,
		Name:               pb.Name,
		Brand:              nullStringFromPb(pb.Brand),
		SeasonCode:         sql.NullString{String: string(seasonCode), Valid: seasonCode != ""},
		SeasonYear:         sql.NullInt32{Int32: int32(seasonYear), Valid: seasonCode != ""},
		Collection:         nullStringFromPb(pb.Collection),
		CategoryId:         nullInt32FromPb(pb.CategoryId),
		TargetGender:       gender,
		Stage:              stage,
		Status:             nullStringFromPb(pb.Status),
		ApprovalState:      approvalState,
		ApprovedAt:         nullTimeFromPbTimestamp(pb.ApprovedAt),
		ReleasedAt:         nullTimeFromPbTimestamp(pb.ReleasedAt),
		TargetDropDate:     nullDateFromPbTimestamp(pb.TargetDropDate),
		BaseModelId:        nullInt32FromPb(pb.BaseModelId),
		BaseSampleSizeId:   nullInt32FromPb(pb.BaseSampleSizeId),
		MeasurementUnit:    unit,
		MeasurementUnitSet: unitSet,
		Concept:            nullStringFromPb(pb.Concept),
		Notes:              nullStringFromPb(pb.Notes),
		SizeIds:            sizeIds,
		Media:              media,
		Callouts:           callouts,
		Details:            details,
		BomItems:           bomItems,
		Construction:       construction,
		Operations:         operations,
		Labels:             labels,
		Packaging:          packaging,
		Costing:            costing,
		Issues:             issues,
		SizeQuantities:     sizeQuantities,
		Signoffs:           signoffs,
		Patterns:           patterns,
		Pieces:             pieces,
		PieceDxfAliases:    pieceDxfAliases,
		PieceDxfAliasesSet: pieceDxfAliasesSet,

		// ТРЕБУЕМЫЙ ПРИПУСК (Ф3.2). ABSENT is carried through as INVALID — «take the workshop
		// default» — and an explicit 0 is carried through as a set zero. Deliberately NOT folded into
		// any section digest projection: adding a field to one marks every signed-off approval of that
		// section as edited-since-signing, on every card at once.
		RequiredSeamAllowanceMm: requiredSeamAllowance,
	}
	// Fingerprint each APPROVED section from the payload being written, so "changed since sign-off"
	// is a durable fact rather than something the browser remembers until the next reload. Runs last:
	// it needs every child section already parsed onto the insert.
	StampTechCardSignoffDigests(insert)
	return insert, nil
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
// validatePatternLineKey admits an empty key or a 26-char alphanumeric one — wide enough for client
// ULIDs (Crockford), server base32 mints and the LEGACY-prefixed backfill, tight enough for CHAR(26).
func validatePatternLineKey(key, field string) error {
	if key == "" {
		return nil
	}
	if len(key) != 26 {
		return fmt.Errorf("%s must be a 26-character key", field)
	}
	for _, r := range key {
		alnum := (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !alnum {
			return fmt.Errorf("%s must be alphanumeric", field)
		}
	}
	return nil
}

// parseTechCardPieceDxfAliases parses the DXF block → cut-piece alias set. The wrapper message IS
// the presence signal (proto3 cannot tell empty-repeated from absent): nil wrapper → (nil, false) —
// the store preserves stored aliases; present wrapper → its items are the new full set. Block names
// are normalized here (trim + collapse inner whitespace); duplicates within the payload are
// rejected case-insensitively, mirroring the DB's CI UNIQUE, so the save fails with a readable
// message instead of a driver 1062.
func parseTechCardPieceDxfAliases(pb *pb_common.TechCardPieceDxfAliasSet) ([]entity.TechCardPieceDxfAlias, bool, error) {
	if pb == nil {
		return nil, false, nil
	}
	// Machine-generated from parsed DXF files — bounded like marker pieces/placements, so a
	// pathological file cannot turn one save into thousands of INSERTs.
	if len(pb.Items) > maxPieceDxfAliases {
		return nil, false, fmt.Errorf("piece_dxf_aliases has %d items, max %d", len(pb.Items), maxPieceDxfAliases)
	}
	out := make([]entity.TechCardPieceDxfAlias, 0, len(pb.Items))
	seen := make(map[string]bool, len(pb.Items))
	for i, a := range pb.Items {
		if a == nil {
			continue
		}
		// Uppercased like pattern keys: legitimate mints are uppercase, the column collates CI.
		slot := strings.ToUpper(strings.TrimSpace(a.BomLineKey))
		if err := validatePatternLineKey(slot, fmt.Sprintf("piece_dxf_aliases[%d].bom_line_key", i)); err != nil {
			return nil, false, err
		}
		// НАЗНАЧЕНИЕ is the scope since 0267; bom_line_key is its legacy half and its compatibility
		// echo. Exactly one of the two has to name something — a row naming neither would file under
		// the empty scope, where every unbound alias of the card would collide with every other.
		purpose, err := aliasFabricPurposeFromPb(a.FabricPurpose, fmt.Sprintf("piece_dxf_aliases[%d].fabric_purpose", i))
		if err != nil {
			return nil, false, err
		}
		if purpose == "" && slot == "" {
			return nil, false, fmt.Errorf(
				"piece_dxf_aliases[%d]: give it a назначение (fabric_purpose) or a BOM line (bom_line_key) — one of the two must say which cloth the block is cut from", i)
		}
		block := strings.Join(strings.Fields(a.BlockName), " ")
		if block == "" {
			return nil, false, fmt.Errorf("piece_dxf_aliases[%d].block_name is required", i)
		}
		if utf8.RuneCountInString(block) > 255 {
			return nil, false, fmt.Errorf("piece_dxf_aliases[%d].block_name must be at most 255 characters", i)
		}
		pieceKey := strings.ToUpper(strings.TrimSpace(a.PieceLineKey))
		if pieceKey == "" {
			return nil, false, fmt.Errorf("piece_dxf_aliases[%d].piece_line_key is required", i)
		}
		if err := validatePatternLineKey(pieceKey, fmt.Sprintf("piece_dxf_aliases[%d].piece_line_key", i)); err != nil {
			return nil, false, err
		}
		// Deduped on the SCOPE, not on the slot — the same key the generated column scope_key and the
		// store's diff use, so the three cannot disagree. This is the alias-collapse case the whole
		// 0267 design turns on: sorting two BOM lines into ONE назначение merges their alias sets, and
		// if both held a «полочка» the merged set has two rows under one scope. Caught HERE, before
		// the transaction opens, it is a readable refusal naming the block; caught by the UNIQUE index
		// it would be a driver 1062 that fails the entire card save. The client warns earlier still.
		dupKey := strings.ToLower(entity.FabricScopeKey(purpose, slot)) + "|" + strings.ToLower(block)
		if seen[dupKey] {
			return nil, false, fmt.Errorf(
				"piece_dxf_aliases[%d]: блок %q заявлен двумя деталями кроя под одним назначением — открой «детали кроя» на вкладке ВЫКРОЙКИ и оставь для этого блока одну связь", i, block)
		}
		seen[dupKey] = true
		out = append(out, entity.TechCardPieceDxfAlias{
			BomLineKey:    slot,
			FabricPurpose: purpose,
			BlockName:     block,
			PieceLineKey:  pieceKey,
		})
	}
	return out, true, nil
}

// techCardPieceDxfAliasesToPb emits the alias set. The wrapper is ALWAYS present on read so a new
// client round-trips presence and its saves carry the full set explicitly.
func techCardPieceDxfAliasesToPb(aliases []entity.TechCardPieceDxfAlias) *pb_common.TechCardPieceDxfAliasSet {
	out := &pb_common.TechCardPieceDxfAliasSet{Items: make([]*pb_common.TechCardPieceDxfAlias, 0, len(aliases))}
	for _, a := range aliases {
		out.Items = append(out.Items, &pb_common.TechCardPieceDxfAlias{
			BomLineKey:    a.BomLineKey,
			FabricPurpose: pbBomPurpose(sql.NullString{String: a.FabricPurpose, Valid: a.FabricPurpose != ""}),
			BlockName:     a.BlockName,
			PieceLineKey:  a.PieceLineKey,
		})
	}
	return out
}

func parseTechCardPatterns(pbs []*pb_common.TechCardSizePattern, sizeIds []int) ([]entity.TechCardSizePattern, error) {
	out := make([]entity.TechCardSizePattern, 0, len(pbs))
	// One key names one ROW: the same sheet hung on two sizes is two rows with two keys. A duplicate
	// here is a client bug that the store's diff would otherwise resolve by silently DELETING the
	// second stored row — so it is rejected before the transaction, like BOM/piece/run line keys.
	seenLineKeys := make(map[string]struct{}, len(pbs))
	// Two KEYED rows sharing one (size, url) are the dup-key reject's blind spot: distinct keys,
	// same sheet — the store's diff would keep both, but no client legitimately produces it and a
	// buggy one is about to lose a row somewhere; reject like the key dupe. Keyless rows keep the
	// lossless keep-first dedupe in the store.
	seenKeyedPairs := make(map[string]struct{}, len(pbs))
	for _, p := range pbs {
		// size_id 0 = the sheet is filed under NO size (stored NULL since 0281), which is the honest
		// value for a graded DXF: the sizes are in the file's block names and only the browser reads
		// them. It is also the only value available while the card's size range is still empty —
		// patterns arrive from the конструктор before anybody fixes the grade, and rejecting them
		// until a range exists locked the upload behind a decision the file has already made.
		// A NON-ZERO size still has to be one of the card's, so a stale row cannot name a dropped size.
		sid := int(p.SizeId)
		if sid < 0 || (sid > 0 && !slices.Contains(sizeIds, sid)) {
			return nil, fmt.Errorf("pattern size_id %d must be one of size_ids (or 0 — размеры в самом файле)", p.SizeId)
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
		// The url must name a MANAGED pattern object (Ф7): everything a pattern row can
		// point at is produced by Admin.UploadPattern under the dedicated bucket folder.
		// This closes two holes at once — a client echoing the output-only view_url back
		// into url, and an arbitrary https url that the admin would render in <object>.
		if _, ok := managedPatternObjectKey(url); !ok {
			return nil, fmt.Errorf("pattern url must be an uploaded pattern object url")
		}
		if len(p.Filename) > maxVarchar255 {
			return nil, fmt.Errorf("pattern filename must be at most %d characters", maxVarchar255)
		}
		if p.SizeBytes < 0 {
			return nil, fmt.Errorf("pattern size_bytes must not be negative")
		}
		if p.Version < 0 {
			return nil, fmt.Errorf("pattern version must not be negative")
		}
		// name keeps its proto presence — Valid=false (absent) tells the store to carry the
		// stored name forward, so a stale client cannot wipe names it never saw; an explicit
		// empty string clears.
		var name sql.NullString
		if p.Name != nil {
			trimmed := strings.TrimSpace(p.GetName())
			if len(trimmed) > maxVarchar255 {
				return nil, fmt.Errorf("pattern name must be at most %d characters", maxVarchar255)
			}
			name = sql.NullString{String: trimmed, Valid: true}
		}
		// line_key is validated but NEVER minted here, unlike BOM/piece line keys: an empty key IS
		// the legacy signal the store's upsert-diff matches by (size_id, url) on — minting in the
		// dto would make every stale-client save read as all-new rows and drop the bindings.
		// Uppercased: every legitimate source (client ULID, server base32, LEGACY backfill) is
		// uppercase, while the CHAR(26) column collates case-insensitively — a lowercase spelling
		// would miss the Go-side maps yet collide in MySQL (a 500, not a field violation).
		lineKey := strings.ToUpper(strings.TrimSpace(p.LineKey))
		if err := validatePatternLineKey(lineKey, "pattern line_key"); err != nil {
			return nil, err
		}
		if lineKey != "" {
			if _, dup := seenLineKeys[lineKey]; dup {
				return nil, fmt.Errorf("pattern line_key %q is used by two rows; one key names one row — the same sheet on two sizes is two rows with two keys", lineKey)
			}
			seenLineKeys[lineKey] = struct{}{}
			// SIZELESS rows are exempt, and it is not a loophole: the rule below reads «a sheet
			// appears once per size», and rows filed under NO size are not a size. The card that
			// legitimately hangs one combined sheet on XS and on S — the case the store documents by
			// name — collapses both rows into the 0 bucket the moment they are re-filed sizeless, and
			// with the rule applied there, every subsequent save of that card would 400, blocking
			// edits that have nothing to do with выкройки. Two keyed sizeless rows on one url are two
			// distinct sheets pointing at one object, and the line_key diff handles that losslessly.
			if sid > 0 {
				pair := fmt.Sprintf("%d|%s", sid, url)
				if _, dup := seenKeyedPairs[pair]; dup {
					return nil, fmt.Errorf("two keyed pattern rows carry the same size and url; a sheet appears once per size")
				}
				seenKeyedPairs[pair] = struct{}{}
			}
		}
		// bom_line_key keeps proto presence like name: absent → carry the stored binding forward.
		var bomLineKey sql.NullString
		if p.BomLineKey != nil {
			trimmed := strings.ToUpper(strings.TrimSpace(p.GetBomLineKey()))
			if err := validatePatternLineKey(trimmed, "pattern bom_line_key"); err != nil {
				return nil, err
			}
			bomLineKey = sql.NullString{String: trimmed, Valid: true}
		}
		// fabric_purpose (0267) — the binding proper; bom_line_key above is now its legacy half.
		// Same proto presence, for the same reason: a client that predates the field must not wipe
		// what it never saw.
		fabricPurpose, err := fabricPurposeFromPb(p.FabricPurpose,
			fmt.Sprintf("patterns[%d].fabric_purpose", len(out)))
		if err != nil {
			return nil, err
		}
		// uploaded_at is server-owned and deliberately dropped here: the store carries the original
		// forward by url, so accepting a client value would only let a save rewrite history.
		out = append(out, entity.TechCardSizePattern{
			SizeId:        sid,
			LineKey:       lineKey,
			BomLineKey:    bomLineKey,
			FabricPurpose: fabricPurpose,
			URL:           url,
			Filename:      nullStringFromPb(p.Filename),
			Name:          name,
			SizeBytes:     nullInt64FromPb(p.SizeBytes),
			Version:       int(p.Version),
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

	sizeIds := intsToInt32(tc.SizeIds)

	// orderQtyBySize resolves each colourway usage's size_run_total the same way the cost estimate
	// does (style_cost_estimate.go) — the style's declared per-size order quantity (size_quantities),
	// 0/absent when the card has none yet.
	orderQtyBySize := make(map[int]int, len(tc.SizeQuantities))
	for _, q := range tc.SizeQuantities {
		orderQtyBySize[q.SizeId] = q.OrderQty
	}

	return &pb_common.TechCard{
		Id:              int32(tc.Id),
		LockVersion:     int32(tc.LockVersion),
		CreatedAt:       timestamppb.New(tc.CreatedAt),
		UpdatedAt:       timestamppb.New(tc.UpdatedAt),
		CreatedBy:       tc.CreatedBy,
		UpdatedBy:       tc.UpdatedBy,
		RoleAssignments: techCardRoleAssignmentsToPb(tc.RoleAssignments),
		Revisions:       techCardRevisionsToPb(tc.Revisions),
		TechCard: &pb_common.TechCardInsert{
			StyleNumber:       tc.StyleNumber.String,
			StyleNumberSource: styleNumberSourceToPb(tc.StyleNumberSource),
			Purpose:           techCardPurposeToPb(tc.Purpose),
			OutputMaterialId:  int32(tc.OutputMaterialId.Int64),
			AuxSubtype:        techCardAuxSubtypeToPb(tc.AuxSubtype),
			Name:              tc.Name,
			Brand:             pbStringFromNull(tc.Brand),
			SkuSeason:         skuSeasonToPb(tc.SeasonCode, tc.SeasonYear),
			Collection:        pbStringFromNull(tc.Collection),
			CategoryId:        pbInt32FromNull(tc.CategoryId),
			TargetGender:      pbGenderFromNull(tc.TargetGender),
			Stage:             pbTechCardStage(tc.Stage),
			Status:            pbStringFromNull(tc.Status),
			ApprovalState:     pbTechCardApprovalState(tc.ApprovalState),
			ApprovedAt:        pbTimestampFromNullTime(tc.ApprovedAt),
			ReleasedAt:        pbTimestampFromNullTime(tc.ReleasedAt),
			TargetDropDate:    pbTimestampFromNullTime(tc.TargetDropDate),
			BaseModelId:       pbInt32FromNull(tc.BaseModelId),
			BaseSampleSizeId:  pbInt32FromNull(tc.BaseSampleSizeId),
			MeasurementUnit:   pbTechCardMeasurementUnit(tc.MeasurementUnit),
			Concept:           pbStringFromNull(tc.Concept),
			Notes:             pbStringFromNull(tc.Notes),
			SizeIds:           sizeIds,
			MoodboardMedia:    moodboardMedia,
			TechnicalMedia:    technicalMedia,
			Callouts:          callouts,
			Details:           techCardDetailsToPb(tc.Details),
			BomItems:          techCardBomItemsToPb(tc.BomItems),
			Construction:      techCardConstructionToPb(tc.Construction),
			Operations:        techCardOperationsToPb(tc.Operations),
			Labels:            techCardLabelsToPb(tc.Labels),
			Packaging:         techCardPackagingToPb(tc.Packaging),
			Costing:           techCardCostingToPb(tc, fx),
			Issues:            techCardIssuesToPb(tc.Issues),
			SizeQuantities:    techCardSizeQuantitiesToPb(tc.SizeQuantities),
			Signoffs:          techCardSignoffsToPb(tc.Signoffs),
			Patterns:          techCardPatternsToPb(tc.Patterns),
			Pieces:            techCardPiecesToPb(tc.Pieces),
			PieceDxfAliases:   techCardPieceDxfAliasesToPb(tc.PieceDxfAliases),

			// Ф3.2: absent on the wire when the card sets no requirement of its own, so a client can
			// tell «take the workshop default» from a card that requires exactly 0.
			RequiredSeamAllowanceMm: pbDecimalFromNull(tc.RequiredSeamAllowanceMm),
		},
		ResolvedMoodboardMedia: resolvedMoodboard,
		ResolvedTechnicalMedia: resolvedTechnical,
		// Derived, output-only (R1/§3.3): a style's colourways are its products. Each ref carries its
		// recipe (H1 fix) resolved against this style's own BOM items.
		Colorways: techCardColorwayRefsToPb(tc.Colorways, tc.BomItems, tc.Pieces, orderQtyBySize, fx),
		// Structured fibre composition (S17/M1 fix), alongside — never instead of — the legacy
		// free-text Composition below.
		CompositionEntries: compositionEntriesToPb(tc.CompositionEntries),
		// Style catalogue facts stored on tech_card but written via UpdateStyle — read-only
		// projections for the constructor (the admin edits them in-place, saving through UpdateStyle).
		// Composition is already normalized to plain text on read (normalizeLegacyComposition, M1).
		Fit:              pbStringFromNull(tc.Fit),
		Composition:      pbStringFromNull(tc.Composition),
		CareInstructions: pbStringFromNull(tc.CareInstructions),
		// Resolved against the care dictionary so the constructor renders symbols and names without
		// shipping its own copy of the vocabulary. Language 0 = the English base: the admin is
		// English-only. Empty for a row still holding pre-ISO free text, which is the client's cue to
		// fall back to CareInstructions above.
		CareEntries: CareEntriesToPb(cache.GetCareIndex().Resolve(tc.CareInstructions.String, 0)),
		// Current fingerprint per sign-off section: compare against each signoff's signed_digest to
		// tell an approval that still holds from one whose sheet moved underneath it.
		SectionDigests: TechCardSectionDigestsToPb(&tc.TechCardInsert),
		// The fit reference the studio shoots against — written through UpdateStyle like the catalogue
		// facts above, and until now readable only off the product/storefront messages.
		ModelWearsHeightCm: tc.ModelWearsHeightCm.Int32,
		ModelWearsSizeId:   tc.ModelWearsSizeId.Int32,
		// The taxonomy path the store derives from category_id. TechCardListItem has always carried it;
		// a card opened directly had to infer its own category path from the leaf tag alone.
		TopCategoryId: tc.TopCategoryId.Int32,
		SubCategoryId: tc.SubCategoryId.Int32,
		TypeId:        tc.TypeId.Int32,
		// Colour variants of an AUXILIARY card's warehouse output (0252). Empty for a sellable card
		// and for an aux card still in legacy single-output mode. Output-only here: they are written
		// through their own RPCs because a variant owns warehouse stock.
		OutputVariants: TechCardOutputVariantsToPb(tc.OutputVariants),
		// Saved раскладки (0257), summaries only — written through Save/DeleteTechCardMarker, the
		// blob rides GetTechCardMarker.
		Markers: TechCardMarkerSummariesToPb(tc.Markers),
	}
}

// TechCardMarkerSummariesToPb emits a card's saved раскладки for display, BOM link resolved
// (line key + name + unit) and the derived consumption-per-unit included. Unlinked markers (or
// markers whose BOM slot was deleted — bom_item_id went NULL) carry empty strings.
func TechCardMarkerSummariesToPb(ms []entity.TechCardMarkerSummary) []*pb_common.TechCardMarkerSummary {
	if len(ms) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardMarkerSummary, 0, len(ms))
	for i := range ms {
		out = append(out, TechCardMarkerSummaryToPb(ms[i]))
	}
	return out
}

// markerCompositionToPb emits a состав in the order the store read it (size_id ascending), each
// line carrying its PER-SIZE НОРМА (Ф2.4) beside the quantity.
//
// It takes the derived slice and not the состав, so the size, the quantity, the расход and the area
// it was derived from leave the server as one row that was computed once. The alternative — emitting
// the состав here and the norms from a second call — is how two adjacent fields end up describing
// two different раскладки.
//
// РАСХОД ОКРУГЛЯЕТСЯ ДО СОТЫХ, как и всё остальное на этой строке (см. used_length_cm,
// consumption_per_unit_cm), и это единственное место, где сходимость перестаёт быть точной:
// Σ(quantity × round(расход, 2)) отличается от used_length_cm не более чем на 0.005 × total_units.
// The unrounded distribution converges exactly (entity.MarkerPerSizeConsumption), and rounding a
// per-garment norm to a hundredth of a centimetre is not where a costing error comes from.
//
// THE AREA IS NOT ROUNDED HERE because it is not derived — it is stored at scale 2 already, and
// re-rounding a stored number is how a display convention silently becomes a second truncation.
func markerCompositionToPb(cs []entity.MarkerSizeConsumption) []*pb_common.TechCardMarkerCompositionEntry {
	if len(cs) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardMarkerCompositionEntry, 0, len(cs))
	for _, c := range cs {
		e := &pb_common.TechCardMarkerCompositionEntry{
			SizeId:            int32(c.SizeId),
			Quantity:          int32(c.Quantity),
			AreaPerGarmentCm2: pbDecimalFromNull(c.AreaPerGarmentCm2),
		}
		if c.ConsumptionCm.Valid {
			e.ConsumptionPerUnitCm = pbDecimalFromDecimal(c.ConsumptionCm.Decimal.Round(2))
		}
		out = append(out, e)
	}
	return out
}

// TechCardMarkerSummaryToPb emits one marker summary. Both consumption figures are derived HERE and
// never stored, so neither can drift from its inputs: the scalar (used_length_cm / total_units, and
// withheld on a mixed состав) and the PER-SIZE норма on each состав line (Ф2.4 — the measured length
// distributed by the area each size occupies, which is the number a mixed раскладка is applied from).
//
// size_id and sets ride as 0 when the row carries a состав. That is the contract the proto now
// states, and it is the honest answer: a mixed раскладка has no single size and no комплекты, and a
// plausible substitute (say, the largest size of the состав) would be read by every existing
// consumer as «the size this marker нормирует» and be wrong in a way that looks right.
func TechCardMarkerSummaryToPb(m entity.TechCardMarkerSummary) *pb_common.TechCardMarkerSummary {
	composition := m.CompositionOrLegacy()
	// ОДИН СРЕЗ НА ВСЁ: the состав, its per-size норма, total_units and the refusal below are every
	// one of them read off `perSize`, which is itself derived from `composition`. Two of these
	// computed from two reads of the row is how a summary ends up saying «4 изделия» beside a состав
	// that adds to 3.
	perSize := entity.MarkerPerSizeConsumption(composition, m.UsedLengthCm)
	// THE SCALAR IS WITHHELD, NOT LABELLED, on a mixed состав (orchestrator decision Р2). The server
	// is the only place that can refuse: there is no server-side marker-apply — the client copies this
	// figure into tech_card_colorway_usage.consumption with consumption_source='marker' and the row
	// stops being distinguishable from a measured norm — and the release snapshot then freezes
	// whatever was emitted, forever. An absent number cannot be copied; a labelled one is.
	//
	// Withholding is deliberately NOT gated on the client understanding Ф2: a stale bundle that falls
	// back to used_length/max(1,sets) will produce a visibly absurd figure (the whole spread as one
	// garment's norm) instead of a plausible mean, and visibly absurd is the failure mode this
	// codebase prefers — see the release-snapshot note above.
	// Ф2.4 did not repeal the withholding: it gave the refusal a REMEDY to name. A mixed раскладка
	// still has no sizeless per-garment number, and this is still the field that becomes
	// tech_card_colorway_usage.consumption; what changed is that the prose can now say «примените по
	// размерам» — but only for a раскладка whose per-size figures actually reached this slice.
	refusal := entity.MarkerScalarNormRefusal(m.Name, perSize)
	var consumption *pb_decimal.Decimal
	if refusal == "" {
		consumption = pbDecimalFromDecimal(m.ConsumptionPerUnitCm().Round(2))
	}
	return &pb_common.TechCardMarkerSummary{
		Id:         int32(m.Id),
		TechCardId: int32(m.TechCardId),
		SizeId:     int32(m.SizeId.Int64),
		// Derived from the SAME slice as the refusal and the consumption, so the three cannot describe
		// three different раскладки. It is 0 exactly when the состав is missing — the honest answer,
		// and the one the refusal beside it explains; TotalUnitsOrLegacy's arithmetic fallback of 1
		// must never surface here, because «1 garment» is a claim and this row makes none.
		Composition:        markerCompositionToPb(perSize),
		TotalUnits:         int32(entity.TotalUnitsOf(composition)),
		ScalarApplyRefusal: refusal,
		Name:               m.Name,
		Source:             m.Source,
		BomLineKey:         pbStringFromNull(m.BomLineKey),
		ColorwayId:         int32(m.ColorwayId.Int64),
		BomItemName:        pbStringFromNull(m.BomItemName),
		BomItemUnit:        pbStringFromNull(m.BomItemUnit),
		// 0 = КАРТОЧНАЯ раскладка, which is every marker a card list can currently show. Emitted from
		// every producer of this summary rather than only from ListProductionRunLays.run_markers,
		// because the client filters its card lists on exactly this field: a summary that reported 0
		// for a run's marker would put a one-off раскладка into the карточка's costing band.
		ProductionRunId:      int32(m.RunId.Int64),
		FabricWidthCm:        pbDecimalFromDecimal(m.FabricWidthCm),
		GapCm:                pbDecimalFromDecimal(m.GapCm),
		EdgeMarginCm:         pbDecimalFromDecimal(m.EdgeMarginCm),
		SelvedgeCm:           pbDecimalFromDecimal(m.SelvedgeCm),
		AllowCrossGrain:      m.AllowCrossGrain,
		Sets:                 int32(m.Sets.Int64),
		UsedLengthCm:         pbDecimalFromDecimal(m.UsedLengthCm),
		EfficiencyPct:        pbDecimalFromNull(m.EfficiencyPct),
		PlacedCount:          int32(m.PlacedCount),
		TotalCount:           int32(m.TotalCount),
		ConsumptionPerUnitCm: consumption,
		// УСЛОВИЯ СЪЁМКИ (Ф3), each emitted only when RECORDED. An unrecorded condition must reach the
		// client as an ABSENT field and never as a zero: «припуск 0» is a measurement («we laid the line
		// as drawn») and «не записано» is the absence of one, and a screen that could not tell them
		// apart would show every раскладка taken before Ф3 as a confidently-measured zero.
		SeamAllowanceMm:    pbDecimalFromNull(m.SeamAllowanceMm),
		ContourAllowanceMm: pbDecimalFromNull(m.ContourAllowanceMm),
		ContourLayer:       pbOptionalStringFromNull(m.ContourLayer),
		GrainLayer:         pbOptionalStringFromNull(m.GrainLayer),
		AllowFlip:          pbOptionalBoolFromNull(m.AllowFlip),
		IsNorm:             m.IsNorm,
		// Derived, never stored: the comparison is against the card AS IT IS NOW, and a stored verdict
		// would be a fact about a card state that has since moved on.
		PieceSetStatus: markerPieceSetStatusToPb(m.PieceSetStatus()),
		// Stamped by the store from the whole card's markers — one row cannot see a conflict between
		// two. Empty on a healthy card.
		NormConflict: m.NormConflict,
		CreatedBy:    m.CreatedBy,
		UpdatedBy:    m.UpdatedBy,
		CreatedAt:    timestamppb.New(m.CreatedAt),
		UpdatedAt:    timestamppb.New(m.UpdatedAt),
	}
}

// markerPieceSetStatusToPb maps the domain's three-valued piece-set verdict onto the wire. The
// default arm returns UNKNOWN rather than panicking, and that is the safe direction: an unmapped
// value must read as «нечего сказать», never as «набор изменился».
func markerPieceSetStatusToPb(s entity.MarkerPieceSetStatus) pb_common.TechCardMarkerPieceSetStatus {
	switch s {
	case entity.MarkerPieceSetMatches:
		return pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_MATCHES
	case entity.MarkerPieceSetChanged:
		return pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_CHANGED
	default:
		return pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_UNKNOWN
	}
}

// pbOptionalStringFromNull / pbOptionalBoolFromNull carry PRESENCE onto a proto3 `optional` field.
// They exist because the Ф3 conditions have three states and the ordinary helpers only carry two:
// grain_layer = "" means «do not orient» and is a DECISION, while an absent grain_layer means nobody
// recorded one. Collapsing them would, on rebuild, orient the very pieces an operator forbade
// orienting.
func pbOptionalStringFromNull(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func pbOptionalBoolFromNull(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

// Column bounds of tech_card_marker (0257), mirrored for readable refusals before the driver's.
const (
	markerNameMaxChars  = 191
	markerDimMaxFrac    = 2
	markerWidthLimit    = 100000000 // DECIMAL(10,2)
	markerSmallDimLimit = 10000     // DECIMAL(6,2) — gap / edge margin
)

// ConvertPbTechCardMarkerInsertToEntity parses the writable half of a marker payload — everything
// except the layout blob, which the API layer marshals separately (idiom of the release snapshot).
// Form checks only; facts the database has to witness (size membership, BOM line identity, the
// card's approval state, name uniqueness) are the store's.
func ConvertPbTechCardMarkerInsertToEntity(pb *pb_common.TechCardMarkerInsert) (entity.TechCardMarkerInsert, error) {
	var out entity.TechCardMarkerInsert
	if pb == nil {
		return out, fmt.Errorf("marker is required")
	}
	name := strings.TrimSpace(pb.Name)
	if name == "" {
		return out, fmt.Errorf("name is required")
	}
	// Rune count, matching the column: VARCHAR(191) counts CHARACTERS, so a byte cap would be
	// 4x stricter than the schema for Cyrillic names — and the prefill is Russian.
	if utf8.RuneCountInString(name) > markerNameMaxChars {
		return out, fmt.Errorf("name must be at most %d characters", markerNameMaxChars)
	}
	source := entity.MarkerSource(strings.TrimSpace(pb.Source))
	if source == "" {
		source = entity.MarkerSourceAuto
	}
	if !entity.ValidMarkerSources[source] {
		return out, fmt.Errorf("source must be one of auto|manual|imported, got %q", pb.Source)
	}
	width, err := requiredDecimalFromPb(pb.FabricWidthCm, "fabric_width_cm", markerDimMaxFrac, markerWidthLimit)
	if err != nil {
		return out, err
	}
	if !width.IsPositive() {
		return out, fmt.Errorf("fabric_width_cm must be positive")
	}
	gap, err := nullDecimalFromPb(pb.GapCm)
	if err != nil {
		return out, fmt.Errorf("gap_cm: %w", err)
	}
	if gap.Valid && gap.Decimal.IsNegative() {
		return out, fmt.Errorf("gap_cm must not be negative")
	}
	if err := validateDecimalScale(gap, "gap_cm", markerDimMaxFrac, markerSmallDimLimit); err != nil {
		return out, err
	}
	margin, err := nullDecimalFromPb(pb.EdgeMarginCm)
	if err != nil {
		return out, fmt.Errorf("edge_margin_cm: %w", err)
	}
	if margin.Valid && margin.Decimal.IsNegative() {
		return out, fmt.Errorf("edge_margin_cm must not be negative")
	}
	if err := validateDecimalScale(margin, "edge_margin_cm", markerDimMaxFrac, markerSmallDimLimit); err != nil {
		return out, err
	}
	// Selvedge is client-derived from the article (may carry engine-float dust) — round like
	// used_length rather than reject. Two selvedges cannot exceed the fabric width.
	selvedge, err := nullDecimalFromPb(pb.SelvedgeCm)
	if err != nil {
		return out, fmt.Errorf("selvedge_cm: %w", err)
	}
	if selvedge.Valid && selvedge.Decimal.IsNegative() {
		return out, fmt.Errorf("selvedge_cm must not be negative")
	}
	if selvedge.Valid {
		selvedge.Decimal = selvedge.Decimal.Round(markerDimMaxFrac)
		if selvedge.Decimal.Mul(decimal.NewFromInt(2)).GreaterThan(width) {
			return out, fmt.Errorf("selvedge_cm: two selvedges (%s cm) exceed fabric_width_cm (%s)",
				selvedge.Decimal.Mul(decimal.NewFromInt(2)), width)
		}
	}
	// used_length_cm is ENGINE-computed float64 — round to the column scale instead of
	// rejecting float dust (512.4370000000001 must save, not 400). Width/gap/margin stay
	// strict: those are operator inputs.
	usedLengthN, err := nullDecimalFromPb(pb.UsedLengthCm)
	if err != nil {
		return out, fmt.Errorf("used_length_cm: %w", err)
	}
	if !usedLengthN.Valid || !usedLengthN.Decimal.IsPositive() {
		return out, fmt.Errorf("used_length_cm must be positive")
	}
	usedLength := usedLengthN.Decimal.Round(markerDimMaxFrac)
	if err := validateDecimalScale(decimal.NullDecimal{Decimal: usedLength, Valid: true},
		"used_length_cm", markerDimMaxFrac, markerWidthLimit); err != nil {
		return out, err
	}
	efficiency, err := nullDecimalFromPb(pb.EfficiencyPct)
	if err != nil {
		return out, fmt.Errorf("efficiency_pct: %w", err)
	}
	if efficiency.Valid && (efficiency.Decimal.IsNegative() || efficiency.Decimal.GreaterThan(decimal.NewFromInt(100))) {
		return out, fmt.Errorf("efficiency_pct must be between 0 and 100")
	}
	if efficiency.Valid {
		// Engine-computed like used_length — round explicitly rather than letting MySQL
		// truncate into DECIMAL(5,2) silently.
		efficiency.Decimal = efficiency.Decimal.Round(2)
	}
	if pb.PlacedCount < 0 {
		return out, fmt.Errorf("placed_count must not be negative")
	}
	if pb.TotalCount < 1 {
		return out, fmt.Errorf("total_count must be at least 1")
	}
	// 0 is «not colourway-specific», the legacy shape; a NEGATIVE id is a client bug, and letting
	// it through would reach the store as an id that simply resolves to nothing — a refusal about
	// a colourway that «is not on this card» rather than about a malformed number.
	if pb.ColorwayId < 0 {
		return out, fmt.Errorf("colorway_id must not be negative")
	}
	// Same shape of guard, same reason (Ф4, 0282): 0 is «карточный маркер» and a negative id is a
	// client bug. Left alone it would reach the store as a run id that resolves to nothing and come
	// back as «this run is not a run of this tech card» — a refusal about the wrong thing entirely.
	if pb.ProductionRunId < 0 {
		return out, fmt.Errorf("production_run_id must not be negative")
	}
	sizeID, sets, composition, err := markerCompositionOfInsert(pb)
	if err != nil {
		return out, err
	}
	conditions, err := markerConditionsFromPb(pb)
	if err != nil {
		return out, err
	}
	return entity.TechCardMarkerInsert{
		// The reader for the geometry ALREADY ON FILE travels with the payload, set HERE rather than
		// by the caller: fail-closed is right (a nil distiller withholds every exemption) but a
		// fail-closed default nobody notices is a silent regression, and an injection sitting fifty
		// lines away in another package is exactly the kind of statement that gets dropped in a
		// refactor. Built where the struct is built, it cannot go missing for a wire-borne save.
		DistilStoredLayout: MarkerLayoutFactsFromBlob,
		SizeId:             sizeID,
		Name:               name,
		Source:             source,
		BomLineKey:         strings.TrimSpace(pb.BomLineKey),
		ColorwayId:         int(pb.ColorwayId),
		ProductionRunId:    int(pb.ProductionRunId),
		FabricWidthCm:      width,
		GapCm:              gap.Decimal,
		EdgeMarginCm:       margin.Decimal,
		SelvedgeCm:         selvedge.Decimal,
		AllowCrossGrain:    pb.AllowCrossGrain,
		Sets:               sets,
		Composition:        composition,
		UsedLengthCm:       usedLength,
		EfficiencyPct:      efficiency,
		PlacedCount:        int(pb.PlacedCount),
		TotalCount:         int(pb.TotalCount),
		// УСЛОВИЯ СЪЁМКИ (Ф3). PieceSetFp is deliberately NOT here: the fingerprint is the store's, taken
		// off the rows its own transaction sees, and a payload-supplied one would be forgeable and stale.
		SeamAllowanceMm:    conditions.SeamAllowanceMm,
		ContourAllowanceMm: conditions.ContourAllowanceMm,
		ContourLayer:       conditions.ContourLayer,
		GrainLayer:         conditions.GrainLayer,
		AllowFlip:          conditions.AllowFlip,
	}, nil
}

// markerLayerMaxChars mirrors the VARCHAR(64) of contour_layer / grain_layer — a readable refusal
// before the driver's 1406. Rune count, matching the column: VARCHAR counts CHARACTERS.
const markerLayerMaxChars = 64

// markerConditions is the parsed Ф3 half of a marker payload, kept as one value so the conversion
// above reads as five fields rather than fifteen lines of parsing.
type markerConditions struct {
	SeamAllowanceMm    decimal.NullDecimal
	ContourAllowanceMm decimal.NullDecimal
	ContourLayer       sql.NullString
	GrainLayer         sql.NullString
	AllowFlip          sql.NullBool
}

// markerConditionsFromPb reads the УСЛОВИЯ СЪЁМКИ off a payload.
//
// ABSENCE IS NEVER AN ERROR HERE, and that is the design, not laxity. The admin is an SPA: an open
// tab survives a deploy and a bundle built before Ф3 sends none of these fields. Such a save is
// stored with NULLs and the row honestly becomes «старая норма», which the readiness gate declines
// to count — Ф1 reached the same conclusion on fabric_direction, and the alternative (refusing the
// save) only stops the geometry being stored at all.
//
// BOTH ALLOWANCES ARE ROUNDED rather than refused for scale, unlike gap/edge_margin above. Those are
// operator inputs; these are MEASUREMENTS — the offset is prefilled in the modal from the distance
// measured between the two contours in the file, and the file-measured half is a median of sampled
// distances. Both arrive as float64 and both are stored at 2 decimal places, so refusing
// 1.0000000000002 would fail a save over dust in a number the column truncates anyway.
//
// THE LAYER NAMES ARE NOT TRIMMED. They are matched literally against the layer names parsed out of
// the DXF when the piece drawing is rebuilt at export; trimming here would silently break that
// comparison for any file whose layer name really does carry a space.
//
// THE DOUBLE-ALLOWANCE REFUSAL IS NOT HERE. It is raised in the API layer (entity.MarkerAllowanceRefusal)
// because it must reach the client as a FIELD violation, and errors returned from this function are
// flattened into a bare InvalidArgument by SaveTechCardMarker — the same reason
// CompositionPredatesSchema is checked there.
func markerConditionsFromPb(pb *pb_common.TechCardMarkerInsert) (markerConditions, error) {
	var out markerConditions
	seam, err := nullDecimalFromPb(pb.SeamAllowanceMm)
	if err != nil {
		return out, fmt.Errorf("seam_allowance_mm: %w", err)
	}
	if seam.Valid {
		if seam.Decimal.IsNegative() {
			return out, fmt.Errorf("seam_allowance_mm must not be negative")
		}
		seam.Decimal = seam.Decimal.Round(markerDimMaxFrac)
		if err := validateDecimalScale(seam, "seam_allowance_mm", markerDimMaxFrac, markerSmallDimLimit); err != nil {
			return out, err
		}
	}
	contour, err := nullDecimalFromPb(pb.ContourAllowanceMm)
	if err != nil {
		return out, fmt.Errorf("contour_allowance_mm: %w", err)
	}
	if contour.Valid {
		if contour.Decimal.IsNegative() {
			return out, fmt.Errorf("contour_allowance_mm must not be negative")
		}
		contour.Decimal = contour.Decimal.Round(markerDimMaxFrac)
		if err := validateDecimalScale(contour, "contour_allowance_mm", markerDimMaxFrac, markerSmallDimLimit); err != nil {
			return out, err
		}
	}
	contourLayer, err := markerLayerFromPb(pb.ContourLayer, "contour_layer")
	if err != nil {
		return out, err
	}
	grainLayer, err := markerLayerFromPb(pb.GrainLayer, "grain_layer")
	if err != nil {
		return out, err
	}
	out = markerConditions{
		SeamAllowanceMm:    seam,
		ContourAllowanceMm: contour,
		ContourLayer:       contourLayer,
		GrainLayer:         grainLayer,
	}
	if pb.AllowFlip != nil {
		out.AllowFlip = sql.NullBool{Bool: *pb.AllowFlip, Valid: true}
	}
	return out, nil
}

// markerLayerFromPb carries a proto3 `optional string` layer name onto a sql.NullString WITHOUT
// collapsing "" into NULL. The empty string is a real answer on grain_layer — «не разворачивать» —
// and folding it into «not recorded» would, on rebuild, orient pieces the operator forbade orienting.
func markerLayerFromPb(v *string, field string) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	if utf8.RuneCountInString(*v) > markerLayerMaxChars {
		return sql.NullString{}, fmt.Errorf("%s must be at most %d characters", field, markerLayerMaxChars)
	}
	return sql.NullString{String: *v, Valid: true}, nil
}

// markerCompositionOfInsert resolves the СОСТАВ of a save, plus the legacy (size_id, sets) pair the
// row will carry. Exactly one rule, three outcomes:
//
//	layout.composition non-empty  ->  a client that speaks Ф2: size_id/sets go NULL, состав as sent
//	empty, but size_id>0 & sets>=1 ->  a STALE ADMIN BUNDLE: stored byte-for-byte in the legacy shape
//	                                  and projected into a one-entry состав so readers see one format
//	neither                        ->  REFUSED
//
// FAIL-CLOSED is the point of the third branch. Assuming «one комплект» would be the cheap option
// and it is the dangerous one: total_units would be 1, consumption_per_unit_cm would report the whole
// spread as one garment's norm, and a client copies that straight into a recipe as a persistent fact
// with consumption_source='marker'. There is no reader downstream that could later tell it was a
// guess.
//
// The состав is taken ONLY from the layout blob and never from a field of its own on the insert
// (there is deliberately none). Two copies on the wire would raise «which one wins if they differ»,
// a question with no free answer, and would split a fact that belongs together: the blob's pieces
// carry sizes, and the состав is the header of that same geometry.
// ПЛОЩАДИ ПО РАЗМЕРАМ СНИМАЮТСЯ ЗДЕСЬ ЖЕ (Ф2.4), off the same layout, so the состав cannot leave
// this function without them. Computing them a step later — in the API layer, next to
// MarkerLayoutFactsFromPb — was the alternative, and it fails in the way this codebase keeps meeting:
// the merge is one line somebody can drop in a refactor, and dropping it produces markers that store
// no areas and hand out no per-size норма, which looks exactly like a раскладка taken before Ф2.4.
// Built where the состав is built, it cannot go missing.
//
// The area computation is TOTAL — it never errors — because the cross-checks between the pieces and
// the состав (a piece pointing at a size the состав does not cut, and a состав size with no pieces)
// belong to MarkerLayoutFactsFromPb, where they reach the client as a refusal naming the field. A
// payload that would fail them simply gets no areas here and is rejected moments later.
func markerCompositionOfInsert(pb *pb_common.TechCardMarkerInsert) (sizeID, sets sql.NullInt64,
	composition []entity.MarkerCompositionEntry, err error) {
	composition, err = markerCompositionFromPb(pb.GetLayout().GetComposition())
	if err != nil {
		return sql.NullInt64{}, sql.NullInt64{}, nil, err
	}
	if len(composition) > 0 {
		return sql.NullInt64{}, sql.NullInt64{},
			entity.WithMarkerSizeAreas(composition, markerPieceAreasFromPb(pb.GetLayout())), nil
	}
	if pb.GetSizeId() > 0 && pb.GetSets() >= 1 {
		legacy := []entity.MarkerCompositionEntry{{SizeId: int(pb.GetSizeId()), Quantity: int(pb.GetSets())}}
		return sql.NullInt64{Int64: int64(pb.GetSizeId()), Valid: true},
			sql.NullInt64{Int64: int64(pb.GetSets()), Valid: true},
			// A legacy blob has no size on any piece, so every piece is size-agnostic and the one
			// entry's area is the whole per-garment area. Recorded even though a homogeneous раскладка
			// needs no area to state its norm (L / q): the number is a true measurement of what was
			// laid out, and Ф2.4's continuation across the размерный ряд is done against exactly such
			// per-garment areas.
			entity.WithMarkerSizeAreas(legacy, markerPieceAreasFromPb(pb.GetLayout())),
			nil
	}
	return sql.NullInt64{}, sql.NullInt64{}, nil, fmt.Errorf(
		"the раскладка needs a состав: send layout.composition (or size_id + sets if the bundle predates it)")
}

// markerPieceAreasFromPb reduces a layout's pieces to what the area distribution needs. It is the
// dto side of the seam MarkerLayoutFacts already draws: the protobuf stays here, the arithmetic
// stays in entity where a test can reach it without a wire message.
//
// A NON-FINITE OR ABSENT AREA IS CARRIED THROUGH AS A REFUSAL, not as zero: decimal.NewFromFloat
// panics on NaN/Inf, and «area 0» would quietly shrink the denominator of every OTHER size — the
// error that inflates a norm without looking wrong. A negative sentinel makes
// entity.MarkerSizeAreasPerGarment withhold the whole distribution, which is the honest outcome for
// geometry nothing can be derived from.
func markerPieceAreasFromPb(l *pb_common.TechCardMarkerLayout) []entity.MarkerPieceArea {
	pieces := l.GetPieces()
	if len(pieces) == 0 {
		return nil
	}
	out := make([]entity.MarkerPieceArea, 0, len(pieces))
	for _, p := range pieces {
		area := p.GetAreaCm2()
		if math.IsNaN(area) || math.IsInf(area, 0) {
			out = append(out, entity.MarkerPieceArea{
				SizeId: int(p.GetSizeId()), Quantity: int(p.GetQuantity()),
				AreaCm2: decimal.NewFromInt(-1),
			})
			continue
		}
		out = append(out, entity.MarkerPieceArea{
			SizeId:   int(p.GetSizeId()),
			Quantity: int(p.GetQuantity()),
			AreaCm2:  decimal.NewFromFloat(area),
		})
	}
	return out
}

// markerCompositionFromPb validates and normalises a состав off the wire. Called from BOTH the
// insert conversion and the layout distillation — it is a pure function of the same input, so the
// two cannot disagree, and neither has to trust the other's ordering.
func markerCompositionFromPb(entries []*pb_common.TechCardMarkerCompositionEntry) ([]entity.MarkerCompositionEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]entity.MarkerCompositionEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, entity.MarkerCompositionEntry{SizeId: int(e.GetSizeId()), Quantity: int(e.GetQuantity())})
	}
	if err := entity.ValidateMarkerComposition(out); err != nil {
		return nil, fmt.Errorf("layout.composition: %w", err)
	}
	entity.SortMarkerComposition(out)
	return out, nil
}

// MarkerLayoutFactsFromBlob distils the geometry ALREADY ON FILE out of a stored layout blob, for the
// one decision that needs it: whether a save is introducing an upside-down placement or merely
// carrying forward one that was already there (Ф1.6's exemption).
//
// It is deliberately NOT MarkerLayoutFactsFromPb. That one polices an incoming payload — it refuses
// an uncuttable angle and canonicalises what it accepts — and neither belongs here: a stored blob is
// history, it may predate every validation this server has, and REFUSING to read it would turn «this
// row is old» into «this row cannot be saved». So this reads tolerantly and judges nothing; an angle
// outside the four is simply not a half-turn, which is the only question being asked.
//
// The error is reserved for a blob that does not parse at all. The store never calls this — it holds
// the bytes and hands them here, because the JSON boundary of 0257/0268 is the reason the geometry
// can stay opaque to the storage layer at all.
func MarkerLayoutFactsFromBlob(blob string) (entity.MarkerLayoutFacts, error) {
	var l pb_common.TechCardMarkerLayout
	// DiscardUnknown, exactly like GetTechCardMarker: a blob written by a NEWER server must still be
	// readable here, or a rollback would make every marker saved meanwhile unexemptible.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(blob), &l); err != nil {
		return entity.MarkerLayoutFacts{}, fmt.Errorf("stored marker layout does not parse: %w", err)
	}
	out := entity.MarkerLayoutFacts{SchemaVersion: int(l.GetSchemaVersion())}
	for _, p := range l.GetPlacements() {
		if normaliseRotation(p.GetRotDeg()) == 180 {
			out.HalfTurnCount++
		}
		if p.GetFlipped() {
			out.FlipCount++
		}
	}
	return out, nil
}

// markerPlacementRotations is the closed set of rotations a placement may carry, and it is enforced
// NOWHERE ELSE. The proto's "0 | 90 | 180 | 270" beside rot_deg is a comment; the column is a JSON
// blob with no CHECK behind it, and the engine, the editor and the DXF export each just trust the
// number. So this save path is the only gate: an angle outside the set renders one way in the
// editor and another in the plotter file, and the раскладка stops being the thing that was measured.
var markerPlacementRotations = map[int32]bool{0: true, 90: true, 180: true, 270: true}

// normaliseRotation folds a rotation into [0, 360). -180 and 540 are the SAME half-turn as 180, and
// a policy that compared the raw number would miss both — which is the cheapest possible way to put
// a piece upside down on ворс with the server's blessing.
func normaliseRotation(deg int32) int32 { return ((deg % 360) + 360) % 360 }

// MarkerLayoutFactsFromPb distils the few things the SAVE PATH must know about a layout out of the
// blob, so nothing downstream has to open it again: the blob is stored opaque (0257) and the store
// never parses it, so whatever the store DECIDES on has to leave the transport layer as a fact.
//
// It also CANONICALISES rot_deg on the message it is handed, on purpose: the blob is marshalled from
// this same message moments later, so a placement the server counted as a half-turn must not be
// stored as -180 and read back by a consumer whose check is `rot === 180`. The facts and the bytes
// have to describe the same placement.
//
// Call it AFTER schema_version has been normalised — a blob that arrives with 0 is a v1 blob, and
// the version is what decides whether the rotation policy applies at all (Ф1.6).
func MarkerLayoutFactsFromPb(l *pb_common.TechCardMarkerLayout) (entity.MarkerLayoutFacts, error) {
	out := entity.MarkerLayoutFacts{SchemaVersion: int(l.GetSchemaVersion())}
	// СОСТАВ (Ф2). Validated here so a malformed one is refused before anything is stored, and
	// CANONICALISED in place for the same reason rot_deg is: these bytes are the blob moments later,
	// and the состав the server judged must be the состав that gets written down. Sorting also makes
	// the stored blob a function of the состав as a SET, so two clients that build the same раскладка
	// in a different form order produce the same bytes — which the Ф0.5 regression probe asserts.
	composition, err := markerCompositionFromPb(l.GetComposition())
	if err != nil {
		return entity.MarkerLayoutFacts{}, err
	}
	out.HasComposition = len(composition) > 0
	slices.SortFunc(l.GetComposition(), func(a, b *pb_common.TechCardMarkerCompositionEntry) int {
		return int(a.GetSizeId()) - int(b.GetSizeId())
	})
	// …AND THE DERIVED HALF OF THE ENTRY IS STRIPPED, for the same reason and in the same breath.
	// TechCardMarkerCompositionEntry is one message on two surfaces: on a SUMMARY it carries the
	// per-size норма and the area it was derived from (Ф2.4), and inside a LAYOUT BLOB it must carry
	// neither. A client that round-tripped a summary's состав back into a save — the obvious thing to
	// do, and the admin is an SPA that holds both — would otherwise freeze a derived figure into
	// immutable history, where it outlives the used_length it came from and is indistinguishable from
	// a measurement to every later reader. Zeroing here rather than refusing is deliberate: the blob
	// is canonicalised on this path anyway (rot_deg, ordering), and refusing would turn a harmless
	// client convenience into a failed save.
	for _, c := range l.GetComposition() {
		c.ConsumptionPerUnitCm = nil
		c.AreaPerGarmentCm2 = nil
	}
	inComposition := make(map[int32]bool, len(composition))
	for _, c := range composition {
		inComposition[int32(c.SizeId)] = true
	}
	withPieces := make(map[int32]bool, len(composition))
	for i, p := range l.GetPieces() {
		sizeID := p.GetSizeId()
		if sizeID == 0 {
			continue
		}
		if sizeID < 0 {
			return entity.MarkerLayoutFacts{}, fmt.Errorf("layout.pieces[%d].size_id is %d", i, sizeID)
		}
		out.HasPieceSize = true
		withPieces[sizeID] = true
		if !inComposition[sizeID] {
			// The instance formula multiplies a sized piece by composition[size].quantity. A piece
			// pointing at a size the состав does not cut would resolve to a MISSING key, i.e. to zero
			// instances — geometry that is stored, counted against the caps, drawn in the editor and
			// cut never. Refusing is the only reading that cannot be silent.
			return entity.MarkerLayoutFacts{}, fmt.Errorf(
				"layout.pieces[%d].size_id is %d, which the состав does not cut", i, sizeID)
		}
	}
	// …AND THE OTHER DIRECTION. A состав line whose size carries no graded piece is the same lie told
	// backwards: total_units counts that size's garments — into the row and into
	// tech_card_marker_size, which Ф4.5 and Ф6.2 are designed to JOIN as truth — while the geometry
	// lays none of them, so the spread is charged to more garments than it cuts and every norm off it
	// is short. Checked only when SOME piece is graded: a blob where NO piece carries a size is the
	// legitimate «nothing in this DXF grades» case, and there every piece is cut once per garment of
	// the whole состав.
	if out.HasPieceSize {
		for _, c := range composition {
			if !withPieces[int32(c.SizeId)] {
				return entity.MarkerLayoutFacts{}, fmt.Errorf(
					"layout.composition cuts size %d but no piece is laid out for it", c.SizeId)
			}
		}
	}
	for i, p := range l.GetPlacements() {
		rot := normaliseRotation(p.GetRotDeg())
		if !markerPlacementRotations[rot] {
			return entity.MarkerLayoutFacts{}, fmt.Errorf(
				"layout.placements[%d].rot_deg is %d; only 0, 90, 180 and 270 can be cut", i, p.GetRotDeg())
		}
		p.RotDeg = rot
		// 180° and a mirror are the same physical mistake on directional cloth — the piece ends up
		// the wrong way up — so both are collected, and the refusal names whichever fired. COUNTED,
		// not flagged: the exemption compares how many, because «this row already had one» is not a
		// licence to add thirty-nine more.
		if rot == 180 {
			out.HalfTurnCount++
		}
		if p.GetFlipped() {
			out.FlipCount++
		}
	}
	return out, nil
}

// TechCardOutputVariantsToPb emits an auxiliary card's colour variants for display, each with its
// colour name, bucket name/unit and on-hand balance already resolved. on_hand stays nil (not zero)
// when the bucket has no stock row — "no balance recorded" is a different fact from "none left".
func TechCardOutputVariantsToPb(vs []entity.TechCardOutputVariant) []*pb_common.TechCardOutputVariant {
	if len(vs) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardOutputVariant, 0, len(vs))
	for i := range vs {
		out = append(out, &pb_common.TechCardOutputVariant{
			Id:           int32(vs[i].Id),
			TechCardId:   int32(vs[i].TechCardId),
			ColorCode:    vs[i].ColorCode,
			ColorName:    vs[i].ColorName,
			MaterialId:   int32(vs[i].MaterialId),
			MaterialName: vs[i].MaterialName,
			OnHand:       pbDecimalFromNull(vs[i].OnHand),
			Unit:         vs[i].Unit,
			Active:       vs[i].Active,
		})
	}
	return out
}

// ConvertPbTechCardOutputVariantToEntity parses the writable half of a colour-variant payload. The
// resolved fields (color_name, material_name, on_hand, unit) are read-only projections and are
// ignored on the way in — accepting them would let a caller contradict the warehouse.
//
// id 0 asks for a create; material_id 0 asks the store to auto-create the bucket on a create and
// means "keep the current bucket" on an update. Range checks only — colour existence, purpose,
// uniqueness and unit consistency are the store's, where they can be held against the write.
func ConvertPbTechCardOutputVariantToEntity(pb *pb_common.TechCardOutputVariant) (entity.TechCardOutputVariantInsert, error) {
	var out entity.TechCardOutputVariantInsert
	if pb == nil {
		return out, fmt.Errorf("variant is required")
	}
	if pb.Id < 0 {
		return out, fmt.Errorf("id must not be negative")
	}
	if pb.MaterialId < 0 {
		return out, fmt.Errorf("material_id must not be negative")
	}
	code := strings.ToUpper(strings.TrimSpace(pb.ColorCode))
	if code == "" {
		return out, fmt.Errorf("color_code is required")
	}
	return entity.TechCardOutputVariantInsert{
		Id:         int(pb.Id),
		ColorCode:  code,
		MaterialId: int(pb.MaterialId),
		Active:     pb.Active,
	}, nil
}

// techCardRevisionsToPb emits the server-stamped auto-journal (Q1) for display: who/what/when.
func techCardRevisionsToPb(revs []entity.TechCardRevision) []*pb_common.TechCardRevision {
	out := make([]*pb_common.TechCardRevision, 0, len(revs))
	for _, r := range revs {
		out = append(out, &pb_common.TechCardRevision{
			Author:     pbStringFromNull(r.Author),
			Section:    pbStringFromNull(r.Section),
			Action:     pbStringFromNull(r.Action),
			ChangeNote: pbStringFromNull(r.ChangeNote),
			CreatedAt:  pbTimestampFromNullTime(r.CreatedAt),
		})
	}
	return out
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
			SizeId:     int32(p.SizeId),
			LineKey:    p.LineKey,
			BomLineKey: pbOptStringFromNull(p.BomLineKey),
			// ALWAYS present on read (like name / bom_line_key), so a new client round-trips presence
			// and its saves state the binding explicitly instead of falling into carry-forward.
			FabricPurpose: pbPtr(pbBomPurpose(p.FabricPurpose)),
			Url:           p.URL,
			Filename:      pbStringFromNull(p.Filename),
			Name:          pbOptStringFromNull(p.Name),
			SizeBytes:     p.SizeBytes.Int64,
			Version:       int32(p.Version),
			UploadedAt:    pbTimestampFromNullTime(p.UploadedAt),
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
		SkuSeason:     skuSeasonToPb(tc.SeasonCode, tc.SeasonYear),
		CreatedAt:     timestamppb.New(tc.CreatedAt),
		UpdatedAt:     timestamppb.New(tc.UpdatedAt),
		LockVersion:   int32(tc.LockVersion),
		PreviewUrl:    tc.PreviewURL,
		Purpose:       techCardPurposeToPb(tc.Purpose),
		AuxSubtype:    techCardAuxSubtypeToPb(tc.AuxSubtype),
		// Category (leaf tag + the derived taxonomy path) so a list can group and label without an
		// N+1 GetTechCard, and the same columns ListTechCardsRequest.category_ids filters on.
		CategoryId:    tc.CategoryId.Int32,
		TopCategoryId: tc.TopCategoryId.Int32,
		SubCategoryId: tc.SubCategoryId.Int32,
		TypeId:        tc.TypeId.Int32,
		ColorwayCount: int32(tc.ColorwayCount),
		// Auxiliary output: zero/empty for a sellable card. on_hand is left unset (not zero) when the
		// material has no stock row — "no balance recorded" is not the same as "none left".
		OutputMaterialId:     int32(tc.OutputMaterialId.Int64),
		OutputMaterialName:   tc.OutputMaterialName,
		OutputMaterialOnHand: pbDecimalFromNull(tc.OutputMaterialOnHand),
		// Colour variants of that output, ACTIVE only (0252): the "3 colours · 820 on hand" a list row
		// shows for a varianted aux card. 0/unset means legacy single-output mode, where the
		// output_material_* trio above is the whole answer.
		OutputVariantCount:   int32(tc.OutputVariantCount),
		OutputVariantsOnHand: pbDecimalFromNull(tc.OutputVariantsOnHand),
		// Saved раскладки (0257): count only — a "latest consumption" without its size and BOM
		// slot would mislead, so the number stays on the card itself.
		MarkerCount: int32(tc.MarkerCount),
	}
}

// ConvertStylePipelineToPb converts the development-board columns to pb (gap-01), reusing the
// light-card mapper for each column's preview cards.
func ConvertStylePipelineToPb(cols []entity.StylePipelineColumn) *pb_admin.GetStylePipelineResponse {
	out := &pb_admin.GetStylePipelineResponse{Columns: make([]*pb_admin.StylePipelineColumn, 0, len(cols))}
	for i := range cols {
		cards := make([]*pb_common.TechCardListItem, 0, len(cols[i].Cards))
		for j := range cols[i].Cards {
			cards = append(cards, ConvertEntityTechCardToListItemPb(&cols[i].Cards[j]))
		}
		out.Columns = append(out.Columns, &pb_admin.StylePipelineColumn{
			Stage: pbTechCardStage(cols[i].Stage),
			Count: int32(cols[i].Count),
			Cards: cards,
		})
	}
	return out
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

// parseTechCardColorways was removed in the R1 merge: colourways are no longer a style write child
// (product_ids / colorways left TechCardInsert). Colourway creation and its dev/lab-dip fields are
// handled by the Colorway RPCs (CreateColorway) via ColorwayDevelopmentInsert; the recipe parser
// (parseTechCardColorwayUsages) is reused there.

// parseTechCardColorwayUsages parses one colourway's material recipe. A usage's
// bom_item_index (when set) must point at a submitted BOM line; placement is normalised
// (trim+lower) so the construction resolver can match operation.placement to it (plan §3).
func parseTechCardColorwayUsages(pbs []*pb_common.TechCardColorwayUsage, bomItemCount int, sizeIds []int, pieceCount int) ([]entity.TechCardColorwayUsage, error) {
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
		var pieceIndex sql.NullInt32
		if u.PieceIndex != nil {
			idx := *u.PieceIndex
			if idx < 0 || int(idx) >= pieceCount {
				return nil, fmt.Errorf("usage piece_index %d out of range (have %d pieces)", idx, pieceCount)
			}
			pieceIndex = sql.NullInt32{Int32: idx, Valid: true}
		}
		materialID, materialIDSet := parseUsageMaterialID(u.MaterialId)
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
			MaterialId:       materialID,
			MaterialIdSet:    materialIDSet,
			Placement:        normalizedPlacementNull(u.Placement),
			Color:            nullStringFromPb(u.Color),
			Pantone:          nullStringFromPb(u.Pantone),
			Consumption:      consumption,
			Quantity:         quantity,
			PieceIndex:       pieceIndex,
			SizeConsumptions: sizeConsumptions,
		})
	}
	return out, nil
}

// wasteDecompositionMaxPct bounds both waste components. See parseUsageProvenance for WHY it is
// not 100, and 0263 for the matching column widening.
const wasteDecompositionMaxPct = 1000

// parseUsageProvenance parses the consumption provenance triple (Ф9.4). Presence is the
// stale-client protocol (mirrors material_id): an ABSENT consumption_source keeps
// Valid=false so the store preserves the stored triple across the full-replace; a present
// value is normalised ("" → manual) and validated. The waste pcts are accepted only with
// source=marker — display decomposition of a measured раскладка, meaningless on manual rows.
func parseUsageProvenance(u *pb_common.TechCardColorwayUsage, i int) (sql.NullString, decimal.NullDecimal, decimal.NullDecimal, error) {
	var src sql.NullString
	var selvedge, cut decimal.NullDecimal
	if u.ConsumptionSource == nil {
		return src, selvedge, cut, nil
	}
	v := strings.TrimSpace(u.GetConsumptionSource())
	if v == "" {
		v = entity.ConsumptionSourceManual
	}
	if !entity.ValidConsumptionSources[v] {
		return src, selvedge, cut, entity.NewFieldViolation(
			fmt.Sprintf("usages[%d].consumption_source", i), "invalid", v, "manual or marker")
	}
	src = sql.NullString{String: v, Valid: true}
	var err error
	if selvedge, err = nullDecimalFromPb(u.WasteSelvedgePct); err != nil {
		return src, selvedge, cut, fmt.Errorf("usages[%d].waste_selvedge_pct: %w", i, err)
	}
	if cut, err = nullDecimalFromPb(u.WasteCutPct); err != nil {
		return src, selvedge, cut, fmt.Errorf("usages[%d].waste_cut_pct: %w", i, err)
	}
	if v != entity.ConsumptionSourceMarker {
		if selvedge.Valid || cut.Valid {
			return src, selvedge, cut, entity.NewFieldViolation(
				fmt.Sprintf("usages[%d].waste_selvedge_pct", i), "provenance_mismatch", "",
				"waste decomposition is meaningful only with consumption_source=marker")
		}
		return src, decimal.NullDecimal{}, decimal.NullDecimal{}, nil
	}
	// Ceiling is 1000%, not 100% (0263): both percentages are quoted OF THE PIECE AREA, and the
	// inter-piece component is 1/efficiency − 1, so it crosses 100% for any раскладка laying
	// below 50% efficiency — real for awkward small sets on a wide roll, where the layout does
	// waste more cloth than it turns into pieces. Past 1000% the input is a mis-entered width.
	maxPct := decimal.NewFromInt(wasteDecompositionMaxPct)
	// Two explicit checks, selvedge first — deterministic field attribution when both are bad.
	if selvedge.Valid && (selvedge.Decimal.IsNegative() || selvedge.Decimal.GreaterThan(maxPct)) {
		return src, selvedge, cut, entity.NewFieldViolation(
			fmt.Sprintf("usages[%d].waste_selvedge_pct", i), "out_of_range", selvedge.Decimal.String(),
			fmt.Sprintf("0..%d", wasteDecompositionMaxPct))
	}
	if cut.Valid && (cut.Decimal.IsNegative() || cut.Decimal.GreaterThan(maxPct)) {
		return src, selvedge, cut, entity.NewFieldViolation(
			fmt.Sprintf("usages[%d].waste_cut_pct", i), "out_of_range", cut.Decimal.String(),
			fmt.Sprintf("0..%d", wasteDecompositionMaxPct))
	}
	// Engine-computed floats: round to the column scale rather than rejecting float dust.
	if selvedge.Valid {
		selvedge.Decimal = selvedge.Decimal.Round(2)
	}
	if cut.Valid {
		cut.Decimal = cut.Decimal.Round(2)
	}
	return src, selvedge, cut, nil
}

// ParseRecipeUsages parses the usages of an UpdateColorwayRecipe request. Unlike the style-save
// parser it references each style BOM line by its stable line_key (resolved to a real bom_item_id in
// the store, S2/S3), so there is no positional range check here. size_id membership in the style's
// range is checked by the store inside the write transaction (the request carries no size range to
// check against, and the FK is on the global size dictionary, not on tech_card_size).
func ParseRecipeUsages(pbs []*pb_common.TechCardColorwayUsage) ([]entity.TechCardColorwayUsage, error) {
	out := make([]entity.TechCardColorwayUsage, 0, len(pbs))
	for i, u := range pbs {
		if len(u.Placement) > maxVarchar255 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].placement", i), "too long", "", fmt.Sprintf("max %d characters", maxVarchar255))
		}
		if len(u.Color) > maxVarchar255 || len(u.Pantone) > maxVarchar64 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].color", i), "color/pantone too long", "", "")
		}
		consumption, err := nullDecimalFromPb(u.Consumption)
		if err != nil {
			return nil, fmt.Errorf("usages[%d].consumption: %w", i, err)
		}
		if err := validateDecimalScale(consumption, "usage consumption", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		quantity, err := nullDecimalFromPb(u.Quantity)
		if err != nil {
			return nil, fmt.Errorf("usages[%d].quantity: %w", i, err)
		}
		if err := validateDecimalScale(quantity, "usage quantity", bomQtyMaxFrac, bomQtyLimit); err != nil {
			return nil, err
		}
		scs := make([]entity.TechCardBomSizeConsumption, 0, len(u.SizeConsumptions))
		for _, sc := range u.SizeConsumptions {
			c, err := nullDecimalFromPb(sc.Consumption)
			if err != nil {
				return nil, fmt.Errorf("usages[%d].size_consumptions: %w", i, err)
			}
			if !c.Valid || c.Decimal.IsNegative() {
				return nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].size_consumptions", i), "consumption must be a non-negative number", "", "")
			}
			scs = append(scs, entity.TechCardBomSizeConsumption{SizeId: int(sc.SizeId), Consumption: c.Decimal})
		}
		var bomItemIndex, pieceIndex sql.NullInt32
		if u.BomItemIndex != nil {
			bomItemIndex = sql.NullInt32{Int32: *u.BomItemIndex, Valid: true}
		}
		if u.PieceIndex != nil {
			pieceIndex = sql.NullInt32{Int32: *u.PieceIndex, Valid: true}
		}
		materialID, materialIDSet := parseUsageMaterialID(u.MaterialId)
		consumptionSource, wasteSelvedge, wasteCut, err := parseUsageProvenance(u, i)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardColorwayUsage{
			BomLineKey:        strings.TrimSpace(u.BomLineKey),
			PieceLineKey:      strings.TrimSpace(u.PieceLineKey),
			ConsumptionSource: consumptionSource,
			WasteSelvedgePct:  wasteSelvedge,
			WasteCutPct:       wasteCut,
			BomItemIndex:      bomItemIndex,
			PieceIndex:        pieceIndex,
			MaterialId:        materialID,
			MaterialIdSet:     materialIDSet,
			Placement:         normalizedPlacementNull(u.Placement),
			Color:             nullStringFromPb(u.Color),
			Pantone:           nullStringFromPb(u.Pantone),
			Consumption:       consumption,
			Quantity:          quantity,
			SizeConsumptions:  scs,
		})
	}
	return out, nil
}

// parseUsageMaterialID keeps proto3 optional presence separate from the nullable pin value. An
// omitted field belongs to an older client and must be preserved by the store; an explicit zero (or
// negative value) is an authoritative clear and therefore has presence without a valid SQL value.
func parseUsageMaterialID(id *int64) (sql.NullInt64, bool) {
	if id == nil {
		return sql.NullInt64{}, false
	}
	if *id <= 0 {
		return sql.NullInt64{}, true
	}
	return sql.NullInt64{Int64: *id, Valid: true}, true
}

// parseTechCardPieces parses the structural cut-pieces (NF-05). Each piece's per-colourway fabric
// mapping addresses its colourway by explicit colorway_id = product.id (R1/§14.3; the store validates
// membership against product.style_id) and the BOM positionally (bom_item_index / fusing_bom_item_index);
// callout_number, when set, must be a submitted callout. BOM/callout refs are range-checked here.
func parseTechCardPieces(pbs []*pb_common.TechCardPiece, bomItemCount int, calloutNumbers map[int]bool) ([]entity.TechCardPiece, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	out := make([]entity.TechCardPiece, 0, len(pbs))
	// Piece names are how a human addresses a part everywhere else on the card -- the operation
	// picker, the recipe norm, the cut list, the factory sheet. Two pieces sharing a name makes every
	// one of those references ambiguous to the person reading it (the FKs stay correct, the sheet does
	// not), so the name is unique per card, compared case-insensitively on the trimmed value.
	seenName := make(map[string]bool, len(pbs))
	for i, p := range pbs {
		if p.Name == "" {
			return nil, entity.NewFieldViolation(fmt.Sprintf("pieces[%d].name", i), "piece name is required", "", "")
		}
		if len(p.Name) > maxVarchar255 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("pieces[%d].name", i),
				fmt.Sprintf("piece name must be at most %d characters", maxVarchar255), "", "")
		}
		key := strings.ToLower(strings.TrimSpace(p.Name))
		if seenName[key] {
			return nil, entity.NewFieldViolation(fmt.Sprintf("pieces[%d].name", i),
				"another cut piece on this card already has this name", p.Name,
				"rename one of them so operations and norms point at an unambiguous part")
		}
		seenName[key] = true
		if len(p.Note) > maxVarchar255 {
			return nil, fmt.Errorf("piece note must be at most %d characters", maxVarchar255)
		}
		// proto3 zero means "unset" → default to 1; but a negative value is a client bug and must not be
		// silently coerced (the user would see 1, not what they sent), matching the other numeric guards
		// in this file that reject negatives (nf05-02).
		if p.PiecesPerGarment < 0 {
			return nil, fmt.Errorf("piece %q pieces_per_garment must be non-negative", p.Name)
		}
		perGarment := int(p.PiecesPerGarment)
		if perGarment == 0 {
			perGarment = 1
		}
		grainline := strings.ToLower(strings.TrimSpace(p.Grainline))
		if grainline == "" {
			grainline = "lengthwise"
		}
		if !entity.ValidTechCardGrainlines[grainline] {
			return nil, fmt.Errorf("piece %q grainline must be one of lengthwise|crosswise|bias|any", p.Name)
		}
		// КАК КРОИТСЯ (0275) — ПРИСУТСТВИЕ, а не значение, by the same argument as направление ткани on
		// the BOM line: the field is optional, and a tab holding an older bundle does not send it at all.
		// A bare proto3 enum would arrive as UNKNOWN and wipe the marking on every piece of the card —
		// and that marking, unlike направление, cannot be reconstructed without a human holding the
		// patterns. An EXPLICIT UNKNOWN still clears the column: returning a piece to «не размечено» is a
		// deliberate act.
		cutSymmetryOmitted := p.CutSymmetry == nil
		cutSymmetry := sql.NullString{}
		if !cutSymmetryOmitted && p.GetCutSymmetry() != pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN {
			cs, ok := techCardPieceCutSymmetryPbToEntity[p.GetCutSymmetry()]
			if !ok {
				return nil, entity.NewFieldViolation(fmt.Sprintf("pieces[%d].cut_symmetry", i),
					"unknown cut symmetry", p.GetCutSymmetry().String(),
					"pick one of: identical (одинаковые копии), mirrored (зеркальные пары), fold (крой по сгибу)")
			}
			cutSymmetry = sql.NullString{String: string(cs), Valid: true}
		}
		// Refuse the unresolvable pair here rather than at MySQL: the DB CHECK is two-column, so its
		// 3819 names pieces_per_garment on a save that only touched the dropdown.
		if err := entity.ValidatePieceCutSymmetry(
			fmt.Sprintf("pieces[%d].cut_symmetry", i), cutSymmetry, perGarment); err != nil {
			return nil, err
		}
		var calloutNumber sql.NullInt32
		// A ZERO is «no callout», not callout number zero. Callouts number from one (the client mints
		// max+1), so 0 can never resolve — and the pointer alone does not protect: a client that sends
		// the field unconditionally hands over 0 for a piece nobody ever pinned, which arrives as a
		// VALID number, matches nothing, and the store marks the piece detached. That is a card telling
		// its operator «the callout you pinned this to was deleted» about a pin that never existed —
		// 16 of 18 pieces on beta carried that badge, with no control anywhere to clear it.
		//
		// A callout_number that no longer matches a callout on the card is still NOT rejected here (S8
		// orphan-control): the store marks such a piece detached — it may carry recipe history and
		// must survive its source callout's removal — rather than failing the whole save. The store
		// also enforces that only a TECHNICAL-sketch callout confers piece semantics (S7).
		if p.CalloutNumber != nil && *p.CalloutNumber > 0 {
			calloutNumber = sql.NullInt32{Int32: *p.CalloutNumber, Valid: true}
		}

		materials := make([]entity.TechCardPieceMaterial, 0, len(p.Materials))
		seenColorway := make(map[int64]bool, len(p.Materials))
		for _, m := range p.Materials {
			if m.ColorwayId <= 0 {
				return nil, fmt.Errorf("piece %q material colorway_id is required", p.Name)
			}
			if seenColorway[m.ColorwayId] {
				return nil, fmt.Errorf("piece %q maps colorway_id %d more than once", p.Name, m.ColorwayId)
			}
			seenColorway[m.ColorwayId] = true
			bomIdx, err := pieceBomRef(m.BomItemIndex, bomItemCount, "bom_item_index", p.Name)
			if err != nil {
				return nil, err
			}
			fusingIdx, err := pieceBomRef(m.FusingBomItemIndex, bomItemCount, "fusing_bom_item_index", p.Name)
			if err != nil {
				return nil, err
			}
			if len(m.Note) > maxVarchar255 {
				return nil, fmt.Errorf("piece %q material note must be at most %d characters", p.Name, maxVarchar255)
			}
			materials = append(materials, entity.TechCardPieceMaterial{
				ColorwayID:         int(m.ColorwayId),
				BomLineKey:         strings.TrimSpace(m.BomLineKey),       // stable ref (WS3 follow-up); store prefers it over the index
				FusingBomLineKey:   strings.TrimSpace(m.FusingBomLineKey), //
				BomItemIndex:       bomIdx,
				FusingBomItemIndex: fusingIdx,
				Note:               nullStringFromPb(m.Note),
			})
		}
		lineKey := strings.TrimSpace(p.LineKey)
		if lineKey == "" {
			mintedLineKey, err := mintTechCardLineKey()
			if err != nil {
				return nil, fmt.Errorf("pieces[%d].line_key: %w", i, err)
			}
			lineKey = mintedLineKey
		}

		out = append(out, entity.TechCardPiece{
			Name:               p.Name,
			LineKey:            lineKey,
			PiecesPerGarment:   perGarment,
			Mirrored:           p.Mirrored,
			CutSymmetry:        cutSymmetry,
			CutSymmetryOmitted: cutSymmetryOmitted,
			Grainline:          grainline,
			Fused:              p.Fused,
			CalloutNumber:      calloutNumber,
			Note:               nullStringFromPb(p.Note),
			Materials:          materials,
		})
	}
	return out, nil
}

// pieceBomRef range-checks an optional positional BOM reference on a piece material.
func pieceBomRef(v *int32, bomItemCount int, field, pieceName string) (sql.NullInt32, error) {
	if v == nil {
		return sql.NullInt32{}, nil
	}
	idx := *v
	if idx < 0 || int(idx) >= bomItemCount {
		return sql.NullInt32{}, fmt.Errorf("piece %q %s %d out of range (have %d bom_items)", pieceName, field, idx, bomItemCount)
	}
	return sql.NullInt32{Int32: idx, Valid: true}, nil
}

func parseTechCardBomItems(pbs []*pb_common.TechCardBomItem) ([]entity.TechCardBomItem, error) {
	out := make([]entity.TechCardBomItem, 0, len(pbs))
	for i, b := range pbs {
		section, ok := techCardBomSectionPbToEntity[b.Section]
		if !ok {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].section", i),
				"section is required and must be valid", "", "pick a BOM section for this line")
		}
		// A LINKED line takes its name from the material it links (resolved on read, see
		// enrichMaterials), so the client need not send one and an empty name is not an error. Only a
		// FREE-TEXT line -- one with no material_id -- must name itself, because nothing else can.
		if b.Name == "" && b.MaterialId == 0 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].name", i),
				"name is required for a line that is not linked to a material", "",
				"type a name for this line, or link it to a material in the catalog")
		}
		// Field-tagged so the admin client's applyServerFieldErrors can pin the rejection to the exact
		// BOM row and column instead of showing a form-level string.
		for _, c := range []struct {
			field string
			val   string
			max   int
		}{
			{"name", b.Name, maxVarchar255},
			{"supplier", b.Supplier, maxVarchar255},
			{"supplier_ref", b.SupplierRef, maxVarchar255},
			{"color", b.Color, maxVarchar255},
			{"composition", b.Composition, maxVarchar255},
			{"spec", b.Spec, maxVarchar255},
			{"unit", b.Unit, maxVarchar32},
		} {
			if len(c.val) > c.max {
				return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].%s", i, c.field),
					fmt.Sprintf("must be at most %d characters", c.max), "", "shorten this value")
			}
		}
		if b.Currency != "" && !IsExpenseCurrency(b.Currency) {
			return nil, fmt.Errorf("bom currency must be a supported currency or USDT")
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
		// НАПРАВЛЕНИЕ ТКАНИ — присутствие, а не значение, по тем же основаниям, что и назначение
		// ниже: поле optional, и клиент со старым бандлом его не шлёт вовсе. Голый proto3-энум
		// пришёл бы как UNKNOWN и стёр бы направление у всех строк карточки — а с Ф1 это не косметика:
		// стёртое направление снимает с сохранения КАЖДУЮ раскладку карточки, пока его не проставят
		// заново. Явно присланный UNKNOWN по-прежнему очищает колонку: это осознанное действие.
		directionOmitted := b.FabricDirection == nil
		direction := sql.NullString{}
		if !directionOmitted && b.GetFabricDirection() != pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_UNKNOWN {
			d, ok := techCardFabricDirectionPbToEntity[b.GetFabricDirection()]
			if !ok {
				return nil, fmt.Errorf("unknown bom fabric_direction: %v", b.GetFabricDirection())
			}
			direction = sql.NullString{String: string(d), Valid: true}
		}

		// НАЗНАЧЕНИЕ (0265). UNSET stays NULL — "not sorted yet" is a real answer and the only honest
		// one for every line that predates the field. The section restriction is enforced downstream,
		// in the store, against the one roll-goods list the marker/pattern binding already uses.
		//
		// ПРИСУТСТВИЕ, а не значение. Поле optional: клиент, который про него не знает, поле НЕ
		// шлёт, и это означает «не трогай», а не «очисти». Голое proto3-поле пришло бы как UNSET и
		// стёрло бы назначение у всех строк карточки — бесследно, потому что этих полей нет в
		// дайджесте подписи, а NULL неотличим от «ещё не разложили».
		purposeOmitted := b.Purpose == nil
		purpose := sql.NullString{}
		if !purposeOmitted && b.GetPurpose() != pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET {
			p, ok := techCardBomPurposePbToEntity[b.GetPurpose()]
			if !ok {
				return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].purpose", i),
					"unknown purpose", "", "pick a purpose from the list")
			}
			purpose = sql.NullString{String: string(p), Valid: true}
		}
		// The note is the escape hatch for OTHER and nothing else. Accepting it on MAIN would let a
		// free-text role in through the back door and dissolve the closed list the field exists to
		// keep — the same reason chk_bom_item_purpose_note guards the column.
		purposeNote := nullStringFromPb(strings.TrimSpace(b.GetPurposeNote()))
		if purposeNote.Valid && purpose.String != string(entity.BomPurposeOther) {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].purpose_note", i),
				"a note is only meaningful on the 'other' purpose", "",
				"clear the note, or set the purpose to 'другое'")
		}
		if len(purposeNote.String) > maxVarchar255 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].purpose_note", i),
				fmt.Sprintf("must be at most %d characters", maxVarchar255), "", "shorten this value")
		}

		// ЧТО ЭТО ЗА ПОЗИЦИЯ (0278) — the mirror of назначение, parsed the same way: UNSET stays NULL
		// ("not classified yet" is the honest answer for every line that predates the field), an
		// unrecognised value is REFUSED rather than degraded, and the kind↔section pairing is left to
		// the store, which owns the one derived list of eligible families.
		//
		// ПРИСУТСТВИЕ ОДНО НА ДВОИХ, и это не упрощение. Колонки связаны в схеме
		// (chk_bom_item_kind_note: примечание легально только при kind='other'), поэтому «перепиши
		// одну половину, вторую оставь как лежала» — это строка, которую MySQL обязан отвергнуть
		// сырым 3819 на сохранении карточки. Значит присланная ЛЮБАЯ из двух половин означает «пиши
		// обе», и только отсутствие ОБЕИХ означает «не трогай» — ровно то состояние, в котором
		// приходит вкладка со старым бандлом, и ровно та защита, ради которой поля optional.
		kindOmitted := b.Kind == nil && b.KindNote == nil
		kind := sql.NullString{}
		if !kindOmitted && b.GetKind() != pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET {
			k, ok := techCardBomKindPbToEntity[b.GetKind()]
			if !ok {
				return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].kind", i),
					"unknown kind", "", "pick a kind from the list")
			}
			kind = sql.NullString{String: string(k), Valid: true}
		}
		// The note is the escape hatch for OTHER and nothing else — accepting it on ZIPPER would let
		// free text in through the back door and dissolve the closed list the field exists to keep,
		// the same containment chk_bom_item_kind_note gives the column.
		kindNote := nullStringFromPb(strings.TrimSpace(b.GetKindNote()))
		if kindNote.Valid && kind.String != string(entity.BomKindOther) {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].kind_note", i),
				"a note is only meaningful on the 'other' kind", "",
				"clear the note, or set the kind to 'другое'")
		}
		if len(kindNote.String) > maxVarchar255 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].kind_note", i),
				fmt.Sprintf("must be at most %d characters", maxVarchar255), "", "shorten this value")
		}

		materialID := sql.NullInt64{}
		if b.MaterialId != 0 {
			materialID = sql.NullInt64{Int64: b.MaterialId, Valid: true}
		}
		lineKey := strings.TrimSpace(b.LineKey)
		if lineKey == "" {
			mintedLineKey, err := mintTechCardLineKey()
			if err != nil {
				return nil, fmt.Errorf("bom_items[%d].line_key: %w", i, err)
			}
			lineKey = mintedLineKey
		}

		out = append(out, entity.TechCardBomItem{
			// A keyless line cannot be named by a submitted key reference; legacy referrers use their
			// unchanged positional index. id is read-only.
			LineKey:                lineKey,
			MaterialId:             materialID,
			Section:                section,
			Purpose:                purpose,
			PurposeOmitted:         purposeOmitted,
			PurposeNote:            purposeNote,
			Kind:                   kind,
			KindOmitted:            kindOmitted,
			KindNote:               kindNote,
			KindNoteOmitted:        kindOmitted,
			IsSample:               b.GetIsSample(),
			IsSampleOmitted:        b.IsSample == nil,
			Name:                   b.Name,
			Supplier:               nullStringFromPb(b.Supplier),
			SupplierRef:            nullStringFromPb(b.SupplierRef),
			Color:                  nullStringFromPb(b.Color),
			Composition:            nullStringFromPb(b.Composition),
			Spec:                   nullStringFromPb(b.Spec),
			Unit:                   nullStringFromPb(b.Unit),
			UnitPrice:              unitPrice,
			Currency:               nullStringFromPb(b.Currency),
			Comment:                nullStringFromPb(b.Comment),
			FabricWidth:            fabricWidth,
			FabricWeightGsm:        fabricGsm,
			FabricDirection:        direction,
			FabricDirectionOmitted: directionOmitted,
			WastagePercent:         wastage,
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

// techCardColorwayRefsToPb emits a style's colourways as derived, output-only AdminColorwayRef
// (R1/§3.3): a style's colourways are its products, not writable through the style. Merchandising
// detail (media, tags, translations) is read via the Colorway RPCs; the development block (the
// colour's own code/label/pantone/hex/swatch — write-only until this fix, persisted by
// UpdateColorway and returned by nothing), the lab-dip state and history, the COGS and the retail
// prices ARE here, because the constructor judges a colour and its margin
// from the style view and fanning GetColorwayByID out per colourway to get them was an N+1. The
// recipe (usages) IS included (H1 fix, WS3/S2-S3): the constructor view of a style shows each
// colourway's material recipe alongside its identity — the recipe used to be write-only
// (UpdateColorwayRecipe persisted usages that no read path surfaced, A3.4). bomItems/orderQtyBySize
// resolve each usage's line_total/size_run_total against the style's BOM (caller strips money for an
// account without costing:read, same as the rest of the tech-card read).
func techCardColorwayRefsToPb(cws []entity.TechCardColorway, bomItems []entity.TechCardBomItem, pieces []entity.TechCardPiece, orderQtyBySize map[int]int, fx CostingFx) []*pb_common.AdminColorwayRef {
	if len(cws) == 0 {
		return nil
	}
	out := make([]*pb_common.AdminColorwayRef, 0, len(cws))
	for i := range cws {
		c := &cws[i]
		ref := &pb_common.AdminColorwayRef{
			ColorwayId:         int32(c.Id),
			BaseSku:            c.BaseSku.String,
			ColorCode:          c.ColorCode,
			Status:             pb_common.ColorwayLifecycleStatus(c.Status),
			Usages:             ConvertRecipeUsagesToPb(c.Usages, bomItems, pieces, orderQtyBySize),
			LabDipStatus:       pbLabDipStatus(c.LabDipStatus),
			LabDipRound:        c.LabDipRound.Int32,
			LabDipDecidedBy:    c.LabDipDecidedBy.String,
			LabDipRejectReason: c.LabDipRejectReason.String,
			LockVersion:        int32(c.LockVersion),
			// The rest of the development block, flattened the same way the lab-dip scalars above are.
			// Written by the Colorway RPCs, output-only here — and unread anywhere until now: the colour's
			// own identity (code/label), the pantone the dyehouse matches, the screen hex and the approved
			// swatch were persisted and never returned by any RPC.
			DevCode:       c.Code.String,
			DevName:       c.Name,
			DevComment:    c.Comment.String,
			Pantone:       c.Pantone.String,
			PantoneSystem: c.PantoneSystem.String,
			DevHex:        c.Hex.String,
			SwatchMediaId: c.SwatchMediaId.Int32,
		}
		if c.LabDipSubmittedAt.Valid {
			ref.LabDipSubmittedAt = timestamppb.New(c.LabDipSubmittedAt.Time)
		}
		if c.LabDipDecidedAt.Valid {
			ref.LabDipDecidedAt = timestamppb.New(c.LabDipDecidedAt.Time)
		}
		ref.LabDipRounds = ColorwayLabDipRoundsToPb(c.LabDipRounds)
		if c.CostPrice.Valid {
			ref.CostPrice = &pb_decimal.Decimal{Value: c.CostPrice.Decimal.StringFixed(2)}
		}
		ref.CostPriceSource = c.CostPriceSource.String
		if c.CostPriceUpdatedAt.Valid {
			ref.CostPriceUpdatedAt = timestamppb.New(c.CostPriceUpdatedAt.Time)
		}
		ref.Prices = convertEntityPricesToPb(c.Prices)
		ref.NetPrices = netColorwayPricesToPb(c.Prices, fx)
		out = append(out, ref)
	}
	return out
}

// netColorwayPricesToPb removes VAT from each catalogue price at the read's VAT rate. Returns nil when
// there is no rate to apply — an export destination has nothing to net, and echoing the gross list
// back under the name `net_prices` would reintroduce the very confusion this field exists to end.
func netColorwayPricesToPb(prices []entity.ColorwayPrice, fx CostingFx) []*pb_common.ColorwayPrice {
	if len(prices) == 0 {
		return nil
	}
	out := make([]*pb_common.ColorwayPrice, 0, len(prices))
	for _, p := range prices {
		net, ok := fx.netOfVat(p.Price)
		if !ok {
			return nil
		}
		out = append(out, &pb_common.ColorwayPrice{
			Currency: p.Currency,
			Price:    &pb_decimal.Decimal{Value: net.StringFixed(2)},
		})
	}
	return out
}

// compositionEntriesToPb emits a style's structured fibre composition (S17/M1 fix) — the typed
// replacement for overloading the free-text composition string with an encoded array of the same
// data. Shared by the tech-card read (ConvertEntityTechCardToPb) and the storefront/colourway read
// (dto/storefront.go storefrontDisplay): both project the SAME style_composition rows, just through
// different store queries (composition_read.go for the style read, query.go's
// styleCompositionEntriesSelect for the colourway/storefront read).
func compositionEntriesToPb(entries []entity.CompositionEntry) []*pb_common.CompositionEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*pb_common.CompositionEntry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		out = append(out, &pb_common.CompositionEntry{
			FiberCode: e.FiberCode,
			Name:      e.Name,
			Percent:   pbDecimalFromDecimal(e.Percent),
			Source:    e.Source,
		})
	}
	return out
}

func dictionaryColorToPb(code string) *pb_common.Color {
	color, ok := cache.GetColorByCode(code)
	if !ok {
		// The DB foreign key makes this impossible for persisted rows. Keeping the canonical code
		// in the response is more diagnosable than silently returning an empty object if cache state
		// is stale during startup or in a unit test.
		return &pb_common.Color{Code: code}
	}
	return &pb_common.Color{
		Id:   int32(color.ID),
		Code: color.Code,
		Name: color.Name,
		Hex:  color.Hex.String,
	}
}

func optionalStringFromNull(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

// ConvertRecipeUsagesToPb emits a colourway's usages, each with its computed per-garment
// line_total and whole-run size_run_total (resolved against the referenced BOM article). The
// counterpart read-side of ParseRecipeUsages. Exported: used both by the tech-card read
// (techCardColorwayRefsToPb, for the constructor view) and directly by GetColorwayByID (H1 fix —
// recipe is colourway-owned, 01-DOMAIN-MODEL §2.3, so GetColorwayByID is the minimum surface that
// must return it).
func ConvertRecipeUsagesToPb(usages []entity.TechCardColorwayUsage, bomItems []entity.TechCardBomItem, pieces []entity.TechCardPiece, orderQtyBySize map[int]int) []*pb_common.TechCardColorwayUsage {
	// piece_id → line_key: the write-path accepts piece_line_key and resolves it to the FK, but
	// the read previously emitted ONLY the resolved id — the editor keys piece binding by
	// line_key, so every piece-bound usage read back as unbound.
	pieceKeyByID := make(map[int64]string, len(pieces))
	for i := range pieces {
		if pieces[i].Id > 0 && pieces[i].LineKey != "" {
			pieceKeyByID[int64(pieces[i].Id)] = pieces[i].LineKey
		}
	}
	// bom_item_id → line_key, for exactly the same reason one line up. The write-path takes
	// bom_line_key as the preferred durable ref but the read emitted only the resolved id, so a
	// consumer that keys a usage by bom_line_key — with no id fallback — saw every usage as
	// unbound. The production cut list is one: it therefore reported "no fabric required" for every
	// style, under a caption promising the primary colourway's recipe.
	bomKeyByID := make(map[int64]string, len(bomItems))
	for i := range bomItems {
		if bomItems[i].Id > 0 && bomItems[i].LineKey != "" {
			bomKeyByID[int64(bomItems[i].Id)] = bomItems[i].LineKey
		}
	}
	out := make([]*pb_common.TechCardColorwayUsage, 0, len(usages))
	for i := range usages {
		u := &usages[i]
		// resolveUsageBom prefers the durable bom_item_id FK, falling back to the legacy positional
		// index (style_cost_estimate.go) — NOT bomItemAtIndex alone: a usage created via bom_line_key
		// (S2/S3) may round-trip with bom_item_index unset, and bomItemAtIndex would then wrongly show
		// no line_total/size_run_total even though the FK resolved fine.
		bom := resolveUsageBom(bomItems, u)
		var bomItemIndex *int32
		if u.BomItemIndex.Valid {
			v := u.BomItemIndex.Int32
			bomItemIndex = &v
		}
		var pieceIndex *int32
		if u.PieceIndex.Valid {
			v := u.PieceIndex.Int32
			pieceIndex = &v
		}
		var materialID *int64
		if u.MaterialId.Valid {
			v := u.MaterialId.Int64
			materialID = &v
		}
		sizeCons := make([]*pb_common.TechCardBomSizeConsumption, 0, len(u.SizeConsumptions))
		for _, sc := range u.SizeConsumptions {
			sizeCons = append(sizeCons, &pb_common.TechCardBomSizeConsumption{
				SizeId:      int32(sc.SizeId),
				Consumption: pbDecimalFromDecimal(sc.Consumption),
			})
		}
		out = append(out, &pb_common.TechCardColorwayUsage{
			BomItemIndex:      bomItemIndex,
			BomItemId:         u.BomItemId.Int64, // OUTPUT: resolved FK (S2/S3); 0 = unset
			MaterialId:        materialID,
			Placement:         pbStringFromNull(u.Placement),
			Color:             pbStringFromNull(u.Color),
			Pantone:           pbStringFromNull(u.Pantone),
			Consumption:       pbDecimalFromNull(u.Consumption),
			Quantity:          pbDecimalFromNull(u.Quantity),
			SizeConsumptions:  sizeCons,
			PieceIndex:        pieceIndex,
			PieceId:           u.PieceId.Int64, // OUTPUT: resolved FK to the cut-piece (WS4); 0 = unset
			PieceLineKey:      pieceKeyByID[u.PieceId.Int64],
			BomLineKey:        bomKeyByID[u.BomItemId.Int64],
			LineTotal:         pbMoneyFromNull(u.LineTotal(bom)),
			SizeRunTotal:      pbMoneyFromNull(u.SizeRunTotal(bom, orderQtyBySize)),
			ConsumptionSource: pbOptStringFromNull(u.ConsumptionSource),
			WasteSelvedgePct:  pbDecimalFromNull(u.WasteSelvedgePct),
			WasteCutPct:       pbDecimalFromNull(u.WasteCutPct),
		})
	}
	return out
}

// techCardPiecesToPb emits the structural cut-pieces (+ per-colourway fabric mapping) for display.
func techCardPiecesToPb(pieces []entity.TechCardPiece) []*pb_common.TechCardPiece {
	if len(pieces) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardPiece, 0, len(pieces))
	for i := range pieces {
		p := &pieces[i]
		var calloutNumber *int32
		if p.CalloutNumber.Valid {
			v := p.CalloutNumber.Int32
			calloutNumber = &v
		}
		materials := make([]*pb_common.TechCardPieceColorwayMaterial, 0, len(p.Materials))
		for j := range p.Materials {
			m := &p.Materials[j]
			var bomIdx *int32
			if m.BomItemIndex.Valid {
				v := m.BomItemIndex.Int32
				bomIdx = &v
			}
			var fusingIdx *int32
			if m.FusingBomItemIndex.Valid {
				v := m.FusingBomItemIndex.Int32
				fusingIdx = &v
			}
			materials = append(materials, &pb_common.TechCardPieceColorwayMaterial{
				ColorwayId:         int64(m.ColorwayID),
				BomItemIndex:       bomIdx,
				FusingBomItemIndex: fusingIdx,
				BomItemId:          m.BomItemId.Int64,       // OUTPUT: resolved FK (S2/S3); 0 = unset
				FusingBomItemId:    m.FusingBomItemId.Int64, // OUTPUT: resolved FK; 0 = unset
				// The durable refs the wire actually speaks. The message has carried these fields
				// since WS3 but the emit never set them, so a client that reads a piece's fabric
				// mapping by line_key saw it as unmapped — and the mapping is a full replace on
				// every card save, which turned "unmapped on read" into "cleared on write".
				BomLineKey:       m.BomLineKey,
				FusingBomLineKey: m.FusingBomLineKey,
				Note:             pbStringFromNull(m.Note),
			})
		}
		out = append(out, &pb_common.TechCardPiece{
			Name:             p.Name,
			LineKey:          p.LineKey,
			PiecesPerGarment: int32(p.PiecesPerGarment),
			Mirrored:         p.Mirrored,
			// ALWAYS present on read, even when the column is NULL: the optionality exists so a client
			// that cannot speak the field does not erase it, not so the server can go quiet. Returning
			// nil would make "cutSymmetry" VANISH from the JSON of every unmarked piece — which is most
			// of them today — and a client round-tripping what it read would then send nothing back and
			// look, to the store, exactly like the stale tab this design is protecting against.
			CutSymmetry:   pbPtr(PieceCutSymmetryToPb(p.CutSymmetry)),
			Grainline:     p.Grainline,
			Fused:         p.Fused,
			CalloutNumber: calloutNumber,
			Detached:      p.Detached,
			Note:          pbStringFromNull(p.Note),
			Materials:     materials,
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
			Id:         int64(b.Id),
			LineKey:    b.LineKey,
			MaterialId: b.MaterialId.Int64,
			Section:    pbBomSection(b.Section),
			// Читатель всегда отдаёт присутствие — «не задано» это UNSET, а не отсутствие поля:
			// отсутствие на чтении заставило бы клиента гадать, старый ли это сервер.
			Purpose:     pbPtr(pbBomPurpose(b.Purpose)),
			PurposeNote: pbPtr(pbStringFromNull(b.PurposeNote)),
			Kind:        pbPtr(pbBomKind(b.Kind)),
			KindNote:    pbPtr(pbStringFromNull(b.KindNote)),
			IsSample:    pbPtr(b.IsSample),
			Name:        b.Name,
			Supplier:    pbStringFromNull(b.Supplier),
			SupplierRef: pbStringFromNull(b.SupplierRef),
			Color:       pbStringFromNull(b.Color),
			Composition: pbStringFromNull(b.Composition),
			Spec:        pbStringFromNull(b.Spec),
			Unit:        pbStringFromNull(b.Unit),
			// Ф5а.3 — read-only projection of `unit` onto the closed vocabulary. Never read back on
			// write: the stored value stays the free text, because `unit` sits inside the SIGNED
			// MATERIALS digest and respelling it would stale sign-offs on cards that BUY exactly what
			// they bought before.
			UnitCode:        pbMaterialUnit(b.Unit.String),
			UnitPrice:       pbDecimalFromNull(b.UnitPrice),
			Currency:        pbStringFromNull(b.Currency),
			Comment:         pbStringFromNull(b.Comment),
			FabricWidth:     pbDecimalFromNull(b.FabricWidth),
			FabricWeightGsm: pbDecimalFromNull(b.FabricWeightGsm),
			FabricDirection: pbPtr(pbFabricDirection(b.FabricDirection)),
			WastagePercent:  pbDecimalFromNull(b.WastagePercent),
			// Stored price provenance (Phase 3) — read-only; '' / nil on pre-provenance rows.
			PriceSource:     b.PriceSource.String,
			PriceSnapshotAt: pbTimestampFromNullTime(b.PriceSnapshotAt),
			// Width enrichment (0259) — read-only, filled by the single-card read only.
			EffectiveFabricWidthCm: pbDecimalFromNull(b.EffectiveFabricWidthCm),
			SelvedgeCm:             pbDecimalFromNull(b.SelvedgeCm),
		})
	}
	return out
}

// pbFabricDirection maps a stored направление to the wire enum; an unset column reads as UNKNOWN,
// which is what it has always meant.
//
// The pointer is added at the CALL SITE (pbPtr), always — the same shape purpose uses since it became
// optional. Presence is honoured on the way IN and always emitted on the way OUT, deliberately: the
// gateway marshals with EmitUnpopulated, but protojson never emits an unset proto3-optional field, so
// returning nil here would make "fabricDirection" VANISH from the JSON of every line that has none,
// where every deployed client currently reads "TECH_CARD_FABRIC_DIRECTION_UNKNOWN". Optionality is a
// statement about what a WRITE means, not a licence to change the shape of a READ.
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

// managedPatternHosts is the set of hosts a stored pattern url may point at, configured
// once at boot from the bucket config (SetManagedPatternHosts). It is deliberately
// FAIL-CLOSED: with no hosts configured every pattern url is rejected, because the
// alternative — a path-shape-only check — accepts https://evil.example/tech-card-patterns/x
// and the admin renders stored pattern urls in an <object>.
var managedPatternHosts = map[string]struct{}{}

// SetManagedPatternHosts installs the bucket's own hosts (CDN subdomain + virtual-hosted
// origin). Called once during boot; tests configure their own fixtures.
func SetManagedPatternHosts(hosts ...string) {
	next := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			next[h] = struct{}{}
		}
	}
	managedPatternHosts = next
}

// managedPatternObjectKey mirrors storeutil.PatternObjectKey (dto cannot import storeutil
// — dependency imports dto). Keep the recognition rule in sync: https url on one of OUR
// hosts whose path contains the dedicated "tech-card-patterns" segment before the object
// name.
func managedPatternObjectKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", false
	}
	if _, ok := managedPatternHosts[strings.ToLower(u.Host)]; !ok {
		return "", false
	}
	key := strings.Trim(u.Path, "/")
	segments := strings.Split(key, "/")
	found := false
	for i, segment := range segments {
		// Checked over the WHOLE path, not up to the folder: a dot segment after it
		// (…/tech-card-patterns/../media/x.jpg) would otherwise pass on the earlier match.
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
		if segment == "tech-card-patterns" && i < len(segments)-1 {
			found = true
		}
	}
	if !found {
		return "", false
	}
	return key, true
}

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

// pbBomPurpose maps a stored НАЗНАЧЕНИЕ back to the wire. An unset column is UNSET, and so is a
// value the current build does not recognise — a purpose read from a newer schema must degrade to
// "not sorted yet" rather than be silently reported as some other purpose.
// pbPtr wraps a value for a proto3 `optional` field. Присутствие на чтении всегда явное: «не
// задано» это UNSET, а не отсутствие поля.
func pbPtr[T any](v T) *T { return &v }

func pbBomPurpose(p sql.NullString) pb_common.TechCardBomPurpose {
	if !p.Valid {
		return pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET
	}
	if v, ok := techCardBomPurposeEntityToPb[entity.TechCardBomPurpose(p.String)]; ok {
		return v
	}
	return pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET
}

// pbBomKind maps a stored ЧТО ЭТО ЗА ПОЗИЦИЯ back to the wire. An unset column is UNSET, and so is a
// value the current build does not recognise — a kind written by a newer schema must degrade to "not
// classified yet" rather than be silently reported as some other kind. The WRITE path refuses the
// same input instead of degrading it: a read must not lose a row, a write must not lose a decision.
func pbBomKind(k sql.NullString) pb_common.TechCardBomKind {
	if !k.Valid {
		return pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
	}
	if v, ok := techCardBomKindEntityToPb[entity.TechCardBomKind(k.String)]; ok {
		return v
	}
	return pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
}

// fabricPurposeFromPb maps the OPTIONAL назначение that binds a выкройка to cloth (0267), keeping
// proto presence intact: nil → Valid=false, «поля не было — сохрани что лежит», which is what a
// client predating the field sends; UNSET → present-empty, an explicit unbind. Unknown values are
// REFUSED rather than degraded to UNSET the way the read path degrades them: a read must not lose a
// row, but a write that silently unbound a sheet because the client spoke a newer vocabulary would
// be a data loss the operator never asked for and could not see.
func fabricPurposeFromPb(p *pb_common.TechCardBomPurpose, field string) (sql.NullString, error) {
	if p == nil {
		return sql.NullString{}, nil
	}
	if *p == pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET {
		return sql.NullString{Valid: true}, nil
	}
	v, ok := techCardBomPurposePbToEntity[*p]
	if !ok {
		return sql.NullString{}, fmt.Errorf("%s: unknown назначение %q", field, p.String())
	}
	return sql.NullString{String: string(v), Valid: true}, nil
}

// aliasFabricPurposeFromPb is the same mapping for an alias row, which needs no presence of its own:
// the alias SET carries presence as a whole and each row is written whole, so UNSET on a row that is
// being written means «line-scoped», never «leave the stored value alone».
func aliasFabricPurposeFromPb(p pb_common.TechCardBomPurpose, field string) (string, error) {
	if p == pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET {
		return "", nil
	}
	v, ok := techCardBomPurposePbToEntity[p]
	if !ok {
		return "", fmt.Errorf("%s: unknown назначение %q", field, p.String())
	}
	return string(v), nil
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

// validateDecimalFits is the FIELD-TAGGED sibling of validateDecimalScale: same three column rules
// (sign, fraction digits, magnitude) but it names the offending input path, so the admin grid can pin
// the rejection to the exact cell/row instead of showing a form-level banner. `signed` allows a
// negative value — a grade step legitimately grades downwards, a measurement or a quantity does not.
//
// The point is that MySQL does NOT reject an over-precise value: DECIMAL(10,2) silently rounds 10.005
// to 10.01 and hands it back on the next read, so a chart the author typed and a chart the factory
// gets differ with nothing anywhere saying so. Rejecting is the only way the two stay equal.
func validateDecimalFits(field string, d decimal.Decimal, maxFrac int, limit int64, signed bool) error {
	if !signed && d.IsNegative() {
		return entity.NewFieldViolation(field, "must_not_be_negative", d.String(), "enter a value of 0 or more")
	}
	if d.Exponent() < int32(-maxFrac) {
		return entity.NewFieldViolation(field, "too_many_decimal_places", d.String(),
			fmt.Sprintf("round to at most %d decimal places — the column stores no more, so the extra digits would be lost silently", maxFrac))
	}
	if d.Abs().GreaterThanOrEqual(decimal.NewFromInt(limit)) {
		return entity.NewFieldViolation(field, "out_of_range", d.String(),
			fmt.Sprintf("enter a value smaller than %d", limit))
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
		Id:            int32(m.Id),
		TechCardId:    int32(m.TechCardId),
		ReleaseNumber: int32(m.ReleaseNumber),
		ReleasedBy:    pbStringFromNull(m.ReleasedBy),
		UnitCost:      pbDecimalFromNull(m.UnitCost),
		Currency:      pbStringFromNull(m.Currency),
		CreatedAt:     timestamppb.New(m.CreatedAt),
	}
}

// techCardPurposeToPb maps the stored purpose string to the R6 numeric enum (UNKNOWN when unset).
func techCardPurposeToPb(p entity.TechCardPurpose) pb_common.TechCardPurpose {
	switch p {
	case entity.TechCardPurposeSellable:
		return pb_common.TechCardPurpose_TECH_CARD_PURPOSE_SELLABLE
	case entity.TechCardPurposeAuxiliary:
		return pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY
	default:
		return pb_common.TechCardPurpose_TECH_CARD_PURPOSE_UNKNOWN
	}
}

// styleNumberSourceFromPb maps the provenance enum to the stored string; UNKNOWN defaults to
// `generated` (the server-proposed default). An explicit MANUAL survives so the handler enforces
// the strict override contract.
func styleNumberSourceFromPb(s pb_common.StyleNumberSource) entity.StyleNumberSource {
	if s == pb_common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL {
		return entity.StyleNumberSourceManual
	}
	return entity.StyleNumberSourceGenerated
}

// styleNumberSourceToPb maps the stored provenance string back to the enum (default GENERATED).
func styleNumberSourceToPb(s entity.StyleNumberSource) pb_common.StyleNumberSource {
	if s == entity.StyleNumberSourceManual {
		return pb_common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL
	}
	return pb_common.StyleNumberSource_STYLE_NUMBER_SOURCE_GENERATED
}

var techCardRoleToPbMap = map[entity.TechCardRole]pb_common.TechCardRole{
	entity.RoleDesigner:     pb_common.TechCardRole_TECH_CARD_ROLE_DESIGNER,
	entity.RoleConstructor:  pb_common.TechCardRole_TECH_CARD_ROLE_CONSTRUCTOR,
	entity.RoleTechnologist: pb_common.TechCardRole_TECH_CARD_ROLE_TECHNOLOGIST,
	entity.RolePatternMaker: pb_common.TechCardRole_TECH_CARD_ROLE_PATTERN_MAKER,
	entity.RoleGrader:       pb_common.TechCardRole_TECH_CARD_ROLE_GRADER,
	entity.RoleApprover:     pb_common.TechCardRole_TECH_CARD_ROLE_APPROVER,
	entity.RoleOther:        pb_common.TechCardRole_TECH_CARD_ROLE_OTHER,
}

// TechCardRoleToPb maps a stored role to its enum (UNKNOWN when unset/unrecognised).
func TechCardRoleToPb(r entity.TechCardRole) pb_common.TechCardRole {
	return techCardRoleToPbMap[r]
}

// TechCardRoleFromPb maps the role enum to the stored string ("" for UNKNOWN, which the caller
// rejects via entity.ValidTechCardRoles).
func TechCardRoleFromPb(r pb_common.TechCardRole) entity.TechCardRole {
	for ent, pb := range techCardRoleToPbMap {
		if pb == r {
			return ent
		}
	}
	return ""
}

// TechCardRoleAssignmentToPb maps one role assignment to the wire (resolved username included).
func TechCardRoleAssignmentToPb(a entity.TechCardRoleAssignment) *pb_common.TechCardRoleAssignment {
	return &pb_common.TechCardRoleAssignment{
		Id:            int32(a.Id),
		TechCardId:    int32(a.TechCardId),
		Role:          TechCardRoleToPb(a.Role),
		AdminId:       int32(a.AdminId),
		AdminUsername: a.AdminUsername,
		AssignedBy:    a.AssignedBy,
		AssignedAt:    timestamppb.New(a.AssignedAt),
	}
}

func techCardRoleAssignmentsToPb(as []entity.TechCardRoleAssignment) []*pb_common.TechCardRoleAssignment {
	out := make([]*pb_common.TechCardRoleAssignment, 0, len(as))
	for _, a := range as {
		out = append(out, TechCardRoleAssignmentToPb(a))
	}
	return out
}

// techCardPurposeFromPb maps the R6 numeric enum to the stored purpose string ("" for UNKNOWN, which
// the caller rejects via ValidTechCardPurposes).
func techCardPurposeFromPb(p pb_common.TechCardPurpose) entity.TechCardPurpose {
	switch p {
	case pb_common.TechCardPurpose_TECH_CARD_PURPOSE_SELLABLE:
		return entity.TechCardPurposeSellable
	case pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY:
		return entity.TechCardPurposeAuxiliary
	default:
		return ""
	}
}

// auxSubtypePbByEntity is the single source for the aux_subtype enum<->string mapping, so the two
// direction helpers below can never drift from each other.
var auxSubtypePbByEntity = map[entity.TechCardAuxSubtype]pb_common.TechCardAuxSubtype{
	entity.AuxSubtypeBrandLabel:  pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_BRAND_LABEL,
	entity.AuxSubtypeCareLabel:   pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_CARE_LABEL,
	entity.AuxSubtypeSizeLabel:   pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_SIZE_LABEL,
	entity.AuxSubtypeHangtag:     pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_HANGTAG,
	entity.AuxSubtypeSticker:     pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_STICKER,
	entity.AuxSubtypeDustBag:     pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_DUST_BAG,
	entity.AuxSubtypeGarmentCase: pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_GARMENT_CASE,
	entity.AuxSubtypeToteBag:     pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_TOTE_BAG,
	entity.AuxSubtypeBox:         pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_BOX,
	entity.AuxSubtypeInsert:      pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_INSERT,
	entity.AuxSubtypeHanger:      pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_HANGER,
	entity.AuxSubtypeOther:       pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_OTHER,
}

// techCardAuxSubtypeToPb maps the nullable stored aux_subtype to the proto enum (UNKNOWN when NULL/unset).
func techCardAuxSubtypeToPb(s sql.NullString) pb_common.TechCardAuxSubtype {
	if !s.Valid {
		return pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_UNKNOWN
	}
	if v, ok := auxSubtypePbByEntity[entity.TechCardAuxSubtype(s.String)]; ok {
		return v
	}
	return pb_common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_UNKNOWN
}

// techCardAuxSubtypeFromPb maps the proto enum to the stored string ("" for UNKNOWN, which the caller
// rejects via ValidTechCardAuxSubtypes).
func techCardAuxSubtypeFromPb(p pb_common.TechCardAuxSubtype) entity.TechCardAuxSubtype {
	for ent, pb := range auxSubtypePbByEntity {
		if pb == p {
			return ent
		}
	}
	return ""
}

// CareEntriesToPb projects resolved care symbols onto the wire. Nil in, nil out — an empty list is
// the client's cue that the stored care value did not resolve (pre-ISO free text) and that it should
// render the raw care_instructions string instead.
func CareEntriesToPb(entries []entity.CareEntry) []*pb_common.CareEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*pb_common.CareEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &pb_common.CareEntry{
			Code:        e.Code,
			Category:    e.Category,
			SubCategory: e.SubCategory,
			Name:        e.Name,
			ShortProse:  e.ShortProse,
		})
	}
	return out
}
