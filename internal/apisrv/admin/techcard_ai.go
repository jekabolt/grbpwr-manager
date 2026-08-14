package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			MachineProfiles:      aiMachineProfileSummaries(c.EquipmentDefaults),
			PressProfiles:        aiPressProfileSummaries(c.EquipmentDefaults),
		}
	}
	// The card's own allowance standard, in millimetres — the draft should not invent a per-step
	// allowance that contradicts it, and stating it is cheaper than correcting it afterwards.
	tcx.RequiredSeamAllowanceMm = decimalOrEmpty(card.RequiredSeamAllowanceMm)

	return tcx
}

// aiMachineProfileSummaries / aiPressProfileSummaries render the card's equipment park as one line
// per profile — «this style is sewn on these machines, set up like this».
//
// THE PROFILE KEY IS NOT IN THE LINE, and that is the contract with the model rather than an
// omission: it does not create profiles and cannot link a step to one. It names the machine or the
// equipment TYPE; the technologist attaches the profile afterwards. A key in the context would only
// teach it to emit a field that does not exist in the answer shape.
//
// A nil park (an older card, or a read that did not hydrate them) yields nil, and the prompt then
// says nothing about equipment — the same silence it keeps about an unset default.
func aiMachineProfileSummaries(d *entity.TechCardEquipmentDefaults) []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.Machines))
	for i := range d.Machines {
		m := &d.Machines[i]
		var parts []string
		if m.ThreadCount.Valid {
			parts = append(parts, fmt.Sprintf("%d threads", m.ThreadCount.Int32))
		}
		if needle := aiNeedleSummary(m.NeedleType, m.NeedleSizeNm); needle != "" {
			parts = append(parts, needle)
		}
		if m.ThreadTension.Valid {
			tension := "tension " + m.ThreadTension.String
			if m.ThreadTensionNote.Valid && m.ThreadTensionNote.String != "" {
				tension += " (" + m.ThreadTensionNote.String + ")"
			}
			parts = append(parts, tension)
		}
		if v := decimalOrEmpty(m.StitchesPerCm); v != "" {
			parts = append(parts, v+" st/cm")
		}
		if v := decimalOrEmpty(m.StitchWidthMm); v != "" {
			parts = append(parts, "stitch width "+v+" mm")
		}
		if s := aiAttachmentSummary(m.AttachmentKind); s != "" {
			parts = append(parts, s)
		}
		if m.Note.Valid && m.Note.String != "" {
			parts = append(parts, m.Note.String)
		}
		out = append(out, aiProfileLine(m.MachineType, m.Label, parts))
	}
	return out
}

func aiPressProfileSummaries(d *entity.TechCardEquipmentDefaults) []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.Presses))
	for i := range d.Presses {
		p := &d.Presses[i]
		head := p.PressEquipment
		// WHICH process the profile is for; NULL = universal, and then the head says nothing extra.
		if p.PressOperationType.Valid {
			head += " for " + p.PressOperationType.String
		}
		var parts []string
		if p.PressTemperatureC.Valid {
			parts = append(parts, fmt.Sprintf("%d °C", p.PressTemperatureC.Int32))
		}
		if p.PressDwellSec.Valid {
			parts = append(parts, fmt.Sprintf("%d s", p.PressDwellSec.Int32))
		}
		if v := decimalOrEmpty(p.PressPressureNCm2); v != "" {
			parts = append(parts, v+" N/cm²")
		}
		// Three states, three renderings: absent says nothing, false says «без пара» out loud.
		if p.PressSteam.Valid {
			if p.PressSteam.Bool {
				parts = append(parts, "with steam")
			} else {
				parts = append(parts, "no steam")
			}
		}
		if p.PressCloth.Valid {
			if p.PressCloth.String == "none" {
				parts = append(parts, "no press cloth")
			} else {
				parts = append(parts, "press cloth: "+p.PressCloth.String)
			}
		}
		if p.Note.Valid && p.Note.String != "" {
			parts = append(parts, p.Note.String)
		}
		out = append(out, aiProfileLine(head, p.Label, parts))
	}
	return out
}

// aiNeedleSummary states the point and the size together when both are set, because that is how a
// needle is quoted on a floor («SES Nm 90»), and either alone otherwise.
func aiNeedleSummary(needleType sql.NullString, sizeNm sql.NullInt32) string {
	switch {
	case needleType.Valid && sizeNm.Valid:
		return fmt.Sprintf("%s needle Nm %d", needleType.String, sizeNm.Int32)
	case needleType.Valid:
		return needleType.String + " needle"
	case sizeNm.Valid:
		return fmt.Sprintf("needle Nm %d", sizeNm.Int32)
	}
	return ""
}

// aiAttachmentSummary spells 'none' out as a decision. It is not the absence of an answer here — a
// profile that says «runs bare» is telling a step it has nothing to inherit.
func aiAttachmentSummary(kind sql.NullString) string {
	if !kind.Valid {
		return ""
	}
	if kind.String == "none" {
		return "no attachment"
	}
	return "attachment: " + kind.String
}

// aiProfileLine assembles «type ("label"): setting, setting». The label is a name for a human
// («оверлок у окна») and never the identity, so it is parenthetical — the type is what the model
// has to answer with.
func aiProfileLine(head string, label sql.NullString, parts []string) string {
	if label.Valid && strings.TrimSpace(label.String) != "" {
		head += ` ("` + strings.TrimSpace(label.String) + `")`
	}
	if len(parts) == 0 {
		return head
	}
	return head + ": " + strings.Join(parts, ", ")
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
	opType, machineType := aiOperationType(o.OperationType, o.MachineType)
	op := &pb_common.TechCardOperation{
		Note:           o.Note,
		OperationType:  opType,
		Zone:           aiEnum(o.Zone, aiZoneTokens),
		SeamClass:      aiEnum(o.SeamClass, aiSeamClassTokens),
		AttachmentKind: aiEnum(o.AttachmentKind, aiAttachmentTokens),
	}
	// EACH BLOCK BELONGS TO ITS OWN STEP TYPE and the save path refuses it anywhere else, so a
	// press temperature drafted onto a handwork step would not be a stray field — it would be a card
	// that cannot be saved until someone finds it. The same argument as the topstitch width below.
	switch opType {
	case pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE:
		// machine_type may still be UNKNOWN here (the model named no machine). That is a draft with a
		// blank to fill, not a reason to discard the step.
		op.MachineType = machineType
		op.ThreadCount = aiRangedInt32(o.ThreadCount.String(), entity.MinThreadCount, entity.MaxThreadCount)
		op.NeedleType = aiEnum(o.NeedleType, aiNeedleTypeTokens)
		op.NeedleSizeNm = aiRangedInt32(o.NeedleSizeNm.String(), entity.MinNeedleSizeNm, entity.MaxNeedleSizeNm)
		op.ThreadTension = aiEnum(o.ThreadTension, aiThreadTensionTokens)
		op.StitchWidthMm = aiRangedDecimal(o.StitchWidthMm.String(), 1, entity.MinStitchWidthMm, entity.MaxStitchWidthMm)
	case pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN,
		pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING:
		op.PressEquipment = aiEnum(o.PressEquipment, aiPressEquipmentTokens)
		op.PressTemperatureC = aiRangedInt32(o.PressTemperatureC.String(), entity.MinPressTemperatureC, entity.MaxPressTemperatureC)
		op.PressDwellSec = aiRangedInt32(o.PressDwellSec.String(), entity.MinPressDwellSec, entity.MaxPressDwellSec)
		op.PressPressureNCm2 = aiRangedDecimal(o.PressPressureNCm2.String(), 1, entity.MinPressPressureNCm2, entity.MaxPressPressureNCm2)
		// nil = the model said nothing; false = «без пара», which is an instruction and not a default.
		op.PressSteam = o.PressSteam.Ptr()
		op.PressCloth = aiEnum(o.PressCloth, aiPressClothTokens)
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
	if mode := aiEnum(o.TopstitchMode, aiTopstitchTokens); mode != pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN {
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

// EVERY dictionary here is BUILT from the entity token slices rather than typed out: a hand-written
// copy is exactly where the vocabulary silently loses a value that was added elsewhere, and here the
// loss would be invisible — the model's correct answer would simply become UNKNOWN. The equipment
// vocabularies made that concrete: twenty-five machines and six presses arrived in one phase, and a
// list transcribed by hand would have been stale before it was read.
//
// The nine LEGACY operation types are deliberately absent from aiOpTypeTokens. They are not a type a
// draft may hold any more; they are canonicalised into (MACHINE, machine_type) by aiOperationType.
var aiOpTypeTokens = aiTokenMap[pb_common.TechCardOperationType](entity.OperationTypeTokens, "TECH_CARD_OPERATION_TYPE_", pb_common.TechCardOperationType_value)
var aiZoneTokens = aiTokenMap[pb_common.TechCardGarmentZone](entity.GarmentZoneTokens, "TECH_CARD_GARMENT_ZONE_", pb_common.TechCardGarmentZone_value)
var aiSeamClassTokens = aiTokenMap[pb_common.TechCardSeamClass](entity.SeamClassTokens, "TECH_CARD_SEAM_CLASS_", pb_common.TechCardSeamClass_value)
var aiAttachmentTokens = aiTokenMap[pb_common.TechCardAttachmentKind](entity.AttachmentKindTokens, "TECH_CARD_ATTACHMENT_KIND_", pb_common.TechCardAttachmentKind_value)
var aiTopstitchTokens = aiTokenMap[pb_common.TechCardTopstitchMode](entity.TopstitchModeTokens, "TECH_CARD_TOPSTITCH_MODE_", pb_common.TechCardTopstitchMode_value)
var aiMachineTypeTokens = aiTokenMap[pb_common.TechCardMachineType](entity.MachineTypeTokens, "TECH_CARD_MACHINE_TYPE_", pb_common.TechCardMachineType_value)
var aiPressEquipmentTokens = aiTokenMap[pb_common.TechCardPressEquipment](entity.PressEquipmentTokens, "TECH_CARD_PRESS_EQUIPMENT_", pb_common.TechCardPressEquipment_value)
var aiNeedleTypeTokens = aiTokenMap[pb_common.TechCardNeedleType](entity.NeedleTypeTokens, "TECH_CARD_NEEDLE_TYPE_", pb_common.TechCardNeedleType_value)
var aiThreadTensionTokens = aiTokenMap[pb_common.TechCardThreadTension](entity.ThreadTensionTokens, "TECH_CARD_THREAD_TENSION_", pb_common.TechCardThreadTension_value)
var aiPressClothTokens = aiTokenMap[pb_common.TechCardPressCloth](entity.PressClothTokens, "TECH_CARD_PRESS_CLOTH_", pb_common.TechCardPressCloth_value)

// aiTokenMap projects a vocabulary onto its proto enum by name. A token with no matching member is
// dropped rather than fatal — the same slice is fed to dto.enumTokenMap, which panics at init on
// exactly that mismatch, so the loud check already exists one package over.
func aiTokenMap[E ~int32](tokens []string, prefix string, values map[string]int32) map[string]E {
	m := make(map[string]E, len(tokens))
	for _, tok := range tokens {
		if v, ok := values[prefix+strings.ToUpper(tok)]; ok {
			m[tok] = E(v)
		}
	}
	return m
}

// aiEnum resolves one drafted token, answering UNKNOWN (the zero member of every one of these enums)
// to anything it does not recognise. UNKNOWN IS NOT A REFUSAL HERE: this is a draft a technologist
// reviews, and dropping the step because one word was guessed badly would throw away the fields that
// were right.
func aiEnum[E ~int32](token string, m map[string]E) E {
	var unknown E
	if v, ok := m[normalizeToken(token)]; ok {
		return v
	}
	return unknown
}

// aiOperationType splits the model's answer into the two axes a step has: the verb and the machine.
//
// A model asked for «machine» will still answer «overlock» sometimes — that word WAS the operation
// type until this phase, it is in every sewing text, and the models were trained on those. So the
// nine legacy words are accepted and canonicalised into (MACHINE, <that machine>) instead of landing
// as UNKNOWN and handing the technologist a blank type on an otherwise complete step.
//
// An explicit machine_type WINS over the machine implied by a legacy word: it is the answer on the
// axis the field actually asks about, and the save path refuses a payload that carries both and
// disagrees — so preferring one is the only way this draft is saveable as shown.
func aiOperationType(typeToken, machineToken string) (pb_common.TechCardOperationType, pb_common.TechCardMachineType) {
	machine := aiMachineType(machineToken)
	tok := normalizeToken(typeToken)
	if _, legacy := entity.LegacyOperationMachineType[entity.TechCardOperationType(tok)]; legacy {
		if machine == pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN {
			machine = aiMachineType(tok)
		}
		return pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE, machine
	}
	return aiEnum(tok, aiOpTypeTokens), machine
}

// aiMachineType resolves the machine, accepting the legacy OPERATION word for it as a spelling of
// the machine itself. Two of the nine renamed on the way across (`double_needle` →
// `lockstitch_double_needle`, `blindhem` → `blindstitch`), and those are precisely the two a model
// is most likely to write in machine_type from habit.
func aiMachineType(token string) pb_common.TechCardMachineType {
	tok := normalizeToken(token)
	if v, ok := aiMachineTypeTokens[tok]; ok {
		return v
	}
	if machine, ok := entity.LegacyOperationMachineType[entity.TechCardOperationType(tok)]; ok {
		return aiMachineTypeTokens[machine]
	}
	return pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN
}

// aiRangedInt32 reads a drafted integer setting and answers 0 («not set» on the wire) to anything
// outside the band the save enforces. A hallucinated «1800 °C» is not a fact worth carrying: it
// would arrive at the editor as a field the technologist cannot save until they clear it, which is
// strictly worse than the blank they would have filled in anyway.
func aiRangedInt32(literal string, min, max int) int32 {
	n := parsePositiveInt(literal)
	if n == 0 || int(n) < min || int(n) > max {
		return 0
	}
	return n
}

// aiRangedDecimal is the decimal twin, with one addition: it ROUNDS to the column's scale. The save
// refuses an over-precise number outright (the column would round it silently and hand a different
// one back), so «3.75 N/cm²» rounded here is a draft that saves, and left alone is one that does not.
func aiRangedDecimal(literal string, maxFrac int32, min, max int64) *pb_decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(literal))
	if err != nil {
		return nil
	}
	d = d.Round(maxFrac)
	if d.LessThan(decimal.NewFromInt(min)) || d.GreaterThan(decimal.NewFromInt(max)) {
		return nil
	}
	return &pb_decimal.Decimal{Value: d.String()}
}

// normalizeToken lowercases and collapses spaces/hyphens to underscores so "Double Needle",
// "double-needle" and "double_needle" all match.
func normalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
