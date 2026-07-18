package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// aiOpsNotConfiguredMsg is the single, clear message returned when the OpenRouter
// integration is not configured (no OPENROUTER_API_KEY). Kept as one const so the
// pre-check and the client-level ErrNotConfigured path report identically.
const aiOpsNotConfiguredMsg = "AI operations generation is not configured (set OPENROUTER_API_KEY)"

// GenerateTechCardOperations drafts structured sewing operations for a tech card from a
// plain-language description via OpenRouter. It loads the card (pieces + BOM + type) purely as
// grounding context, asks the model for strictly-JSON operations, and returns them as an UNSAVED
// proposal in the exact common.TechCardOperation shape — the technologist reviews, edits and saves
// them through UpdateTechCard. This handler persists nothing.
//
// Degradation: when OPENROUTER_API_KEY is unset the client is disabled and this returns a clear
// FailedPrecondition; a transport/API failure returns Unavailable; malformed model output returns a
// clear parse error (Internal). None of these ever mutate the card.
func (s *Server) GenerateTechCardOperations(ctx context.Context, req *pb_admin.GenerateTechCardOperationsRequest) (*pb_admin.GenerateTechCardOperationsResponse, error) {
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		return nil, status.Error(codes.InvalidArgument, "description is required")
	}
	if !s.aiOps.Enabled() {
		return nil, status.Error(codes.FailedPrecondition, aiOpsNotConfiguredMsg)
	}

	card, err := s.repo.TechCards().GetTechCardById(ctx, int(req.TechCardId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "AI ops: can't load tech card",
			slog.Int("tech_card_id", int(req.TechCardId)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}

	result, err := s.aiOps.GenerateOperations(ctx, s.buildAIOperationContext(ctx, card), description)
	if err != nil {
		if errors.Is(err, openrouter.ErrNotConfigured) {
			return nil, status.Error(codes.FailedPrecondition, aiOpsNotConfiguredMsg)
		}
		slog.Default().ErrorContext(ctx, "AI ops: generation failed",
			slog.Int("tech_card_id", int(req.TechCardId)), slog.String("err", err.Error()))
		// A malformed-JSON parse failure is a model/content problem (Internal); everything else
		// here is an upstream transport/API failure the caller may retry (Unavailable).
		if strings.Contains(err.Error(), "not valid operations JSON") || strings.Contains(err.Error(), "no JSON object") {
			return nil, status.Errorf(codes.Internal, "AI returned an unparseable draft: %v", err)
		}
		return nil, status.Errorf(codes.Unavailable, "AI operations generation failed: %v", err)
	}

	ops := make([]*pb_common.TechCardOperation, 0, len(result.Operations))
	for i := range result.Operations {
		ops = append(ops, aiOperationToPb(result.Operations[i]))
	}
	slog.Default().InfoContext(ctx, "drafted AI tech-card operations",
		slog.Int("tech_card_id", int(req.TechCardId)), slog.Int("operations", len(ops)))
	return &pb_admin.GenerateTechCardOperationsResponse{
		Operations: ops,
		Model:      s.aiOps.Model(),
		Notes:      result.Notes,
	}, nil
}

// buildAIOperationContext projects a stored tech card into the grounding context fed to the model:
// the style header, its cut-pieces and its BOM. The garment-type name is resolved best-effort from
// the dictionary cache (a lookup failure just leaves it blank rather than failing the draft).
func (s *Server) buildAIOperationContext(ctx context.Context, card *entity.TechCard) openrouter.TechCardContext {
	tcx := openrouter.TechCardContext{
		TechCardID:  card.Id,
		StyleName:   card.Name,
		StyleNumber: card.StyleNumber.String,
		Category:    s.resolveCategoryName(ctx, card.CategoryId),
		Gender:      card.TargetGender.String,
		Brand:       card.Brand.String,
		Notes:       card.Notes.String,
		Concept:     card.Concept.String,
	}

	tcx.Pieces = make([]openrouter.PieceContext, 0, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		tcx.Pieces = append(tcx.Pieces, openrouter.PieceContext{
			Name:             p.Name,
			PiecesPerGarment: p.PiecesPerGarment,
			Mirrored:         p.Mirrored,
			Grainline:        p.Grainline,
			Fused:            p.Fused,
			Note:             p.Note.String,
		})
	}

	tcx.BOM = make([]openrouter.BOMItemContext, 0, len(card.BomItems))
	for i := range card.BomItems {
		m := &card.BomItems[i]
		tcx.BOM = append(tcx.BOM, openrouter.BOMItemContext{
			Section:     string(m.Section),
			Name:        m.Name,
			Composition: m.Composition.String,
			Color:       m.Color.String,
			Spec:        m.Spec.String,
			Supplier:    m.Supplier.String,
		})
	}

	if c := card.Construction; c != nil {
		tcx.Construction = &openrouter.ConstructionContext{
			MainStitchType:  c.MainStitchType.String,
			StitchDensity:   c.StitchDensity.String,
			OverlockThreads: c.OverlockThreads.String,
			SeamAllowances:  c.SeamAllowances.String,
		}
	}

	return tcx
}

// resolveCategoryName best-effort maps a category_id to its display name via the dictionary cache.
// Returns "" on an unset id or any lookup failure — the type is context, not a hard requirement.
func (s *Server) resolveCategoryName(ctx context.Context, categoryID sql.NullInt32) string {
	if !categoryID.Valid || categoryID.Int32 <= 0 {
		return ""
	}
	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return ""
	}
	for _, c := range di.Categories {
		if int32(c.ID) == categoryID.Int32 {
			return c.Name
		}
	}
	return ""
}

// aiOperationToPb maps one drafted operation onto the persisted common.TechCardOperation shape.
// Numeric-ish fields are parsed leniently (a bad value is dropped, not fatal) so a rough draft still
// yields an editable operation for the technologist.
func aiOperationToPb(o openrouter.Operation) *pb_common.TechCardOperation {
	op := &pb_common.TechCardOperation{
		Node:           strings.TrimSpace(o.Node),
		Description:    o.Description,
		SeamType:       o.SeamType,
		Machine:        o.Machine,
		TopstitchWidth: o.TopstitchWidth,
		SeamAllowance:  o.SeamAllowance,
		Thread:         o.Thread,
		Needle:         o.Needle,
		Attachment:     o.Attachment,
		Note:           o.Note,
		Placement:      o.Placement,
		OperationType:  aiOperationType(o.OperationType),
		Zone:           aiConstructionZone(o.Zone),
	}
	if v := normalizeDecimal(o.StitchesPerCm.String()); v != "" {
		op.StitchesPerCm = &pb_decimal.Decimal{Value: v}
	}
	if v := normalizeDecimal(o.TimeNormMinutes.String()); v != "" {
		op.TimeNorm = &pb_decimal.Decimal{Value: v}
	}
	if n := parsePositiveInt(o.OperationNumber.String()); n > 0 {
		op.OperationNumber = n
	}
	if n := parsePositiveInt(o.CalloutNumber.String()); n > 0 {
		op.CalloutNumber = n
	}
	return op
}

// normalizeDecimal validates a numeric literal and returns its canonical string, or "" when empty
// or unparseable (so a junk value is simply omitted rather than persisted).
func normalizeDecimal(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return ""
	}
	return d.String()
}

// parsePositiveInt parses a non-negative int32 from a literal; 0 on empty/invalid/negative.
func parsePositiveInt(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

// aiOpTypeTokens maps the model's operation_type tokens onto the proto enum. Tokens mirror the
// TechCardOperationType enum suffixes (and the entity string values), normalized to lower_snake.
var aiOpTypeTokens = map[string]pb_common.TechCardOperationType{
	"lockstitch":    pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH,
	"double_needle": pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_DOUBLE_NEEDLE,
	"overlock":      pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OVERLOCK,
	"coverstitch":   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_COVERSTITCH,
	"chainstitch":   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_CHAINSTITCH,
	"blindhem":      pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BLINDHEM,
	"bartack":       pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BARTACK,
	"buttonhole":    pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTONHOLE,
	"button_attach": pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_BUTTON_ATTACH,
	"fusing":        pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING,
	"handwork":      pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK,
	"other":         pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_OTHER,
}

// aiZoneTokens maps the model's zone tokens onto the proto construction-zone enum.
var aiZoneTokens = map[string]pb_common.TechCardConstructionZone{
	"outer":       pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_OUTER,
	"lining":      pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_LINING,
	"interlining": pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_INTERLINING,
	"other":       pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_OTHER,
}

func aiOperationType(token string) pb_common.TechCardOperationType {
	if v, ok := aiOpTypeTokens[normalizeToken(token)]; ok {
		return v
	}
	return pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN
}

func aiConstructionZone(token string) pb_common.TechCardConstructionZone {
	if v, ok := aiZoneTokens[normalizeToken(token)]; ok {
		return v
	}
	return pb_common.TechCardConstructionZone_TECH_CARD_CONSTRUCTION_ZONE_UNKNOWN
}

// normalizeToken lowercases and collapses spaces/hyphens to underscores so "Double Needle",
// "double-needle" and "double_needle" all match.
func normalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
