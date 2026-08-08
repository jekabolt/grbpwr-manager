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
			CutSymmetry:      p.CutSymmetry.String, // "" when unmarked; the prompt then says nothing
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
			DefaultSeamClass:     c.DefaultSeamClass.String,
			DefaultStitchesPerCm: decimalOrEmpty(c.DefaultStitchesPerCm),
			OverlockThreadCount:  c.OverlockThreadCount.Int32,
		}
	}
	// The card's own allowance standard, in millimetres — the draft should not invent a per-step
	// allowance that contradicts it, and stating it is cheaper than correcting it afterwards.
	tcx.RequiredSeamAllowanceMm = decimalOrEmpty(card.RequiredSeamAllowanceMm)

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
//
// Every dictionary value goes through the token maps below, so a model that invents a word produces
// UNKNOWN rather than a stored string nothing recognises. UNKNOWN is deliberately NOT fatal here:
// this is a DRAFT a technologist reviews and completes, and dropping the whole step because one
// field was guessed badly would throw away the nine fields that were right. The save path is where
// the two required fields are actually enforced.
func aiOperationToPb(o openrouter.Operation) *pb_common.TechCardOperation {
	op := &pb_common.TechCardOperation{
		Note:           o.Note,
		OperationType:  aiOperationType(o.OperationType),
		Zone:           aiGarmentZone(o.Zone),
		SeamClass:      aiSeamClass(o.SeamClass),
		AttachmentKind: aiAttachmentKind(o.AttachmentKind),
	}
	if v := normalizeDecimal(o.StitchesPerCm.String()); v != "" {
		op.StitchesPerCm = &pb_decimal.Decimal{Value: v}
	}
	if v := normalizeDecimal(o.SmvMinutes.String()); v != "" {
		op.Smv = &pb_decimal.Decimal{Value: v}
	}
	if v := normalizeDecimal(o.SeamAllowanceMm.String()); v != "" {
		op.SeamAllowanceMm = &pb_decimal.Decimal{Value: v}
	}
	if mode := aiTopstitchMode(o.TopstitchMode); mode != pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN {
		t := &pb_common.TechCardTopstitch{Mode: mode, Rows: parsePositiveInt(o.TopstitchRows.String())}
		// A width only travels with WIDTH — the same rule the save path enforces. Letting a drafted
		// «edge, 6 mm» through would hand the technologist a step that cannot be saved as shown.
		if mode == pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_WIDTH {
			if v := normalizeDecimal(o.TopstitchWidthMm.String()); v != "" {
				t.WidthMm = &pb_decimal.Decimal{Value: v}
			}
		}
		op.Topstitch = t
	}
	if n := parsePositiveInt(o.OperationNumber.String()); n > 0 {
		op.OperationNumber = n
	}
	if n := parsePositiveInt(o.CalloutNumber.String()); n > 0 {
		op.CalloutNumber = n
	}
	return op
}

// decimalOrEmpty renders a nullable decimal for the prompt; "" when unset, so the prompt simply does
// not mention a default nobody configured instead of asserting a zero.
func decimalOrEmpty(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
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

// The remaining dictionaries are BUILT from the entity token slices rather than typed out: a
// hand-written copy is exactly where the vocabulary silently loses a value that was added elsewhere,
// and here the loss would be invisible — the model's correct answer would simply become UNKNOWN.
var aiZoneTokens = aiTokenMap(entity.GarmentZoneTokens, "TECH_CARD_GARMENT_ZONE_", pb_common.TechCardGarmentZone_value)
var aiSeamClassTokens = aiTokenMap(entity.SeamClassTokens, "TECH_CARD_SEAM_CLASS_", pb_common.TechCardSeamClass_value)
var aiAttachmentTokens = aiTokenMap(entity.AttachmentKindTokens, "TECH_CARD_ATTACHMENT_KIND_", pb_common.TechCardAttachmentKind_value)
var aiTopstitchTokens = aiTokenMap(entity.TopstitchModeTokens, "TECH_CARD_TOPSTITCH_MODE_", pb_common.TechCardTopstitchMode_value)

func aiTokenMap(tokens []string, prefix string, values map[string]int32) map[string]int32 {
	m := make(map[string]int32, len(tokens))
	for _, tok := range tokens {
		if v, ok := values[prefix+strings.ToUpper(tok)]; ok {
			m[tok] = v
		}
	}
	return m
}

func aiOperationType(token string) pb_common.TechCardOperationType {
	if v, ok := aiOpTypeTokens[normalizeToken(token)]; ok {
		return v
	}
	return pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN
}

func aiGarmentZone(token string) pb_common.TechCardGarmentZone {
	if v, ok := aiZoneTokens[normalizeToken(token)]; ok {
		return pb_common.TechCardGarmentZone(v)
	}
	return pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_UNKNOWN
}

func aiSeamClass(token string) pb_common.TechCardSeamClass {
	if v, ok := aiSeamClassTokens[normalizeToken(token)]; ok {
		return pb_common.TechCardSeamClass(v)
	}
	return pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN
}

func aiAttachmentKind(token string) pb_common.TechCardAttachmentKind {
	if v, ok := aiAttachmentTokens[normalizeToken(token)]; ok {
		return pb_common.TechCardAttachmentKind(v)
	}
	return pb_common.TechCardAttachmentKind_TECH_CARD_ATTACHMENT_KIND_UNKNOWN
}

func aiTopstitchMode(token string) pb_common.TechCardTopstitchMode {
	if v, ok := aiTopstitchTokens[normalizeToken(token)]; ok {
		return pb_common.TechCardTopstitchMode(v)
	}
	return pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN
}

// normalizeToken lowercases and collapses spaces/hyphens to underscores so "Double Needle",
// "double-needle" and "double_needle" all match.
func normalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
