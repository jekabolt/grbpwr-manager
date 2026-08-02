package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readinessStageOrder is the lifecycle indexed by entity.TechCardStageOrdinal, so "the next stage"
// is a +1 on the ordinal the regression guard already uses — one order of stages in the codebase,
// not two that can drift.
var readinessStageOrder = []entity.TechCardStage{
	entity.TechCardStageIdea, entity.TechCardStageProto, entity.TechCardStageFit,
	entity.TechCardStageSMS, entity.TechCardStagePP, entity.TechCardStageProd,
}

// GetTechCardReadiness reports what is still missing before a style can advance a stage or be
// released. ADVISORY: it computes and returns, it never blocks a write — UpdateTechCard keeps stage
// and approval_state free-standing, and the only stage rule the server enforces is the backward one.
func (s *Server) GetTechCardReadiness(ctx context.Context, req *pb_admin.GetTechCardReadinessRequest) (*pb_admin.GetTechCardReadinessResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	facts, card, err := s.repo.TechCards().GetTechCardReadinessSnapshot(ctx, tcID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card readiness", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get tech card readiness")
	}
	if card == nil {
		slog.Default().ErrorContext(ctx, "readiness snapshot returned a nil tech card", slog.Int("tech_card_id", tcID))
		return nil, status.Error(codes.Internal, "can't get tech card readiness")
	}
	staleSignoffs := staleApprovedSignoffSections(card)

	resp := &pb_admin.GetTechCardReadinessResponse{
		CurrentStage: dto.ConvertEntityTechCardStageToPb(facts.Stage),
		NextStage:    pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN,
	}
	if next, ok := nextTechCardStage(facts.Stage); ok {
		resp.NextStage = dto.ConvertEntityTechCardStageToPb(next)
		resp.NextStageRequirements = nextStageRequirements(next, facts)
	}
	// An empty checklist (prod, or an unrecognised stage) reads as ready, per the field's contract
	// "every next_stage_requirements entry is met"; a client gates on next_stage != UNKNOWN.
	resp.NextStageReady = allReadinessMet(resp.NextStageRequirements)
	resp.ReleaseRequirements = releaseRequirements(facts, staleSignoffs)
	resp.ReleaseReady = allReadinessMet(resp.ReleaseRequirements)
	return resp, nil
}

// nextTechCardStage returns the stage one lifecycle step ahead of s. Not ok at prod (the last stage)
// or for a stage the codebase does not know.
func nextTechCardStage(s entity.TechCardStage) (entity.TechCardStage, bool) {
	ord, ok := entity.TechCardStageOrdinal(s)
	if !ok || ord+1 >= len(readinessStageOrder) {
		return "", false
	}
	return readinessStageOrder[ord+1], true
}

// nextStageRequirements is the studio's entry condition for each stage, in display order. Keys are
// stable machine names a client switches on; labels are the sentence it shows. `first_sample` is
// reused for the proto-sample condition on purpose — both rows ask for the style's first sewn
// prototype, they only differ in how specific the demand is.
func nextStageRequirements(target entity.TechCardStage, f entity.TechCardReadinessFacts) []*pb_admin.TechCardReadinessRequirement {
	switch target {
	case entity.TechCardStageProto:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("style_number", "the style has a style number", f.HasStyleNumber, "no style number set"),
			readinessReq("bom_fabric", "the BOM has at least one fabric line", f.BomFabricLines > 0, "no fabric line in the BOM"),
			readinessReq("first_sample", "at least one sample exists", f.Samples > 0, "no sample recorded"),
		}
	case entity.TechCardStageFit:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("fitting_recorded", "a fitting has been recorded", f.Fittings > 0, "no fitting recorded"),
			readinessReq("first_sample", "a proto sample exists", f.ProtoSamples > 0, "no proto sample recorded"),
		}
	case entity.TechCardStageSMS:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("fit_approved", "a fitting was approved", f.FittingsApproved > 0, "no fitting has an approved verdict"),
			readinessReq("fittings_resolved", "every fitting change request is resolved",
				f.OpenChangeRequests == 0, openChangeRequestDetail(f.OpenChangeRequests)),
		}
	case entity.TechCardStagePP:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("sms_sample", "a salesman sample exists", f.SmsSamples > 0, "no sms sample recorded"),
			readinessReq("colorway_linked", "at least one live colourway", f.LiveColorways > 0, "no live colourway"),
			readinessReq("bom_linked", "every BOM slot has an article (a default, or a pin in every live colourway)",
				f.BomLines > 0 && f.BomLinkedLines == f.BomLines, bomLinkedDetail(f)),
		}
	case entity.TechCardStageProd:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("pp_sample", "a pre-production sample exists", f.PpSamples > 0, "no pp sample recorded"),
			readinessReq("run_planned", "a production run is planned", f.ProductionRuns > 0, "no production run planned"),
			readinessReq("patterns", "every size in the range has a pattern",
				f.Sizes > 0 && f.PatternSizes == f.Sizes, patternsDetail(f)),
		}
	}
	return nil
}

// releaseRequirements is what a card needs before approval_state may go RELEASED — the spec the
// factory is handed. Independent of the stage checklist: a sampling-complete style can still be
// un-releasable (no costing currency, a colourway whose lab dip nobody signed).
func releaseRequirements(f entity.TechCardReadinessFacts, staleSignoffs []entity.TechCardSignoffSection) []*pb_admin.TechCardReadinessRequirement {
	return []*pb_admin.TechCardReadinessRequirement{
		readinessReq("style_number", "the style has a style number", f.HasStyleNumber, "no style number set"),
		readinessReq("size_range", "the size range is not empty", f.Sizes > 0, "no sizes in the range"),
		readinessReq("bom_fabric", "the BOM has at least one fabric line", f.BomFabricLines > 0, "no fabric line in the BOM"),
		readinessReq("costing", "the costing is filled in with a currency",
			f.HasCosting && f.HasCostingCurrency, costingDetail(f)),
		readinessReq("colorway_linked", "at least one live colourway", f.LiveColorways > 0, "no live colourway"),
		// Vacuously met with no colourways: the colorway_linked row above already carries that
		// failure, and a checklist that reds the same fact twice reads as two separate problems.
		readinessReq("lab_dip", "every live colourway has an approved lab dip",
			f.LabDipPendingColorways == 0, labDipDetail(f)),
		readinessReq("signoffs", "every recorded sign-off is approved",
			f.Signoffs > 0 && f.SignoffsApproved == f.Signoffs && len(staleSignoffs) == 0,
			signoffsDetail(f, staleSignoffs)),
	}
}

// readinessReq builds one checklist row. A met row carries no detail — the detail field is the
// explanation of a failure, so the caller can pass it unconditionally.
func readinessReq(key, label string, met bool, detail string) *pb_admin.TechCardReadinessRequirement {
	if met {
		detail = ""
	}
	return &pb_admin.TechCardReadinessRequirement{Key: key, Label: label, Met: met, Detail: detail}
}

func allReadinessMet(rows []*pb_admin.TechCardReadinessRequirement) bool {
	for _, r := range rows {
		if !r.Met {
			return false
		}
	}
	return true
}

func openChangeRequestDetail(n int) string {
	if n == 1 {
		return "1 fitting change request is still open"
	}
	return fmt.Sprintf("%d fitting change requests are still open", n)
}

// bomLinkedDetail distinguishes "nothing to link" from "partly linked": an empty BOM is not
// vacuously compliant here, since no other row on the pp checklist notices it.
func bomLinkedDetail(f entity.TechCardReadinessFacts) string {
	if f.BomLines == 0 {
		return "the BOM is empty"
	}
	return fmt.Sprintf("%d of %d BOM slots have no article (no default and not pinned by every live colourway)", f.BomLines-f.BomLinkedLines, f.BomLines)
}

// patternsDetail likewise: an empty grade cannot be "fully covered" by patterns, and the prod
// checklist has no size-range row of its own.
func patternsDetail(f entity.TechCardReadinessFacts) string {
	if f.Sizes == 0 {
		return "the size range is empty"
	}
	return fmt.Sprintf("%d of %d sizes have no pattern", f.Sizes-f.PatternSizes, f.Sizes)
}

func costingDetail(f entity.TechCardReadinessFacts) string {
	if !f.HasCosting {
		return "no costing recorded"
	}
	return "the costing has no currency"
}

func labDipDetail(f entity.TechCardReadinessFacts) string {
	return fmt.Sprintf("%d of %d colourways have no approved lab dip", f.LabDipPendingColorways, f.LiveColorways)
}

func signoffsDetail(f entity.TechCardReadinessFacts, stale []entity.TechCardSignoffSection) string {
	if f.Signoffs == 0 {
		return "no sign-off recorded"
	}
	if len(stale) == 1 {
		return fmt.Sprintf("%s approval is stale", stale[0])
	}
	if len(stale) > 1 {
		sections := make([]string, 0, len(stale))
		for _, section := range stale {
			sections = append(sections, string(section))
		}
		return fmt.Sprintf("approvals are stale: %s", strings.Join(sections, ", "))
	}
	return fmt.Sprintf("%d of %d sign-offs are not approved", f.Signoffs-f.SignoffsApproved, f.Signoffs)
}

// staleApprovedSignoffSections compares server-owned signed digests with the current enriched card
// using the exact projection used when an approval is stamped. Empty legacy digests are unverifiable
// and therefore stale: release readiness must never treat an approval of unknown content as current.
func staleApprovedSignoffSections(card *entity.TechCard) []entity.TechCardSignoffSection {
	if card == nil {
		return nil
	}
	current := dto.TechCardSectionDigests(&card.TechCardInsert)
	stale := make(map[entity.TechCardSignoffSection]bool)
	for _, signoff := range card.Signoffs {
		if signoff.State != entity.SignoffStateApproved {
			continue
		}
		if !signoff.SignedDigest.Valid || signoff.SignedDigest.String == "" ||
			signoff.SignedDigest.String != current[signoff.Section] {
			stale[signoff.Section] = true
		}
	}
	order := []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	}
	out := make([]entity.TechCardSignoffSection, 0, len(stale))
	for _, section := range order {
		if stale[section] {
			out = append(out, section)
		}
	}
	return out
}
