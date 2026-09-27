package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
)

// ═══ PLAYGROUND phase 3 — THE fal JSON ROUTES AT THE DOOR (kind=extend, kind=inpaint) ═══
//
// Everything here runs BEFORE StartRun (which reserves the day's money), so every refusal is free.
// The route object (designgen.FalRoute) is built in app.go from the SAME fal client the worker's
// providers get; the band (run_kinds, playground_workflows), the door (route_reserve_unbounded) and
// the reserve (designFalRouteEstimate) read it and nothing else.

// SetDesignFalRoutes wires the extend / inpaint route objects (app.go, beside SetDesignThreedRoute).
func (s *Server) SetDesignFalRoutes(routes map[string]designgen.FalRoute) {
	s.designFalRoutes = make(map[string]designgen.FalRoute, len(routes))
	for k, r := range routes {
		s.designFalRoutes[k] = r
	}
}

// designFalRouteKind — the kinds that ride a fal JSON route.
func designFalRouteKind(kind string) bool {
	return kind == entity.DesignRunKindExtend || kind == entity.DesignRunKindInpaint
}

// designFalRouteBounded — the kind's route is wired AND has a number to reserve. Fail closed: no
// route object → false.
func (s *Server) designFalRouteBounded(kind string) bool {
	r, ok := s.designFalRoutes[kind]
	return ok && r.Bounded
}

// designRefuseFalRouteUnbounded — A fal-ROUTE KIND WITHOUT A RESERVE NUMBER IS CLOSED IN WORDS
// (the threed_reserve_unbounded precedent, G-02 Codex 4): FAL_UNIT_USD_OUTPAINT|FILL set without its
// units ceiling means the collect would book `tariff × whatever fal reports`, and no reservation
// covers that. No route object at all (the worker is off, a test) → kind_not_available: the door
// does not know what the worker would book, so it does not take the money.
func (s *Server) designRefuseFalRouteUnbounded(kind string) error {
	if !designFalRouteKind(kind) {
		return nil
	}
	r, ok := s.designFalRoutes[kind]
	if !ok {
		return designRefusal(codes.FailedPrecondition, designReasonKindUnavailable,
			fmt.Sprintf("a %s run cannot be started: no fal route is wired for it on this server. Nothing "+
				"was reserved and nothing was charged", kind),
			map[string]string{"kind": kind})
	}
	if !r.Bounded {
		return designRefusal(codes.FailedPrecondition, entity.DesignErrorCodeRouteReserveUnbounded,
			fmt.Sprintf("a %s run cannot be reserved on this deployment: %s. Nothing was reserved and "+
				"nothing was charged", kind, r.Unbounded),
			map[string]string{"kind": kind, "flag": r.Flag})
	}
	return nil
}

// designFalRouteEstimate — the reserve of a fal-route run: max(the kind's table row, the route's own
// ceiling) × outputs, exactly as the 3D reserve (designThreedRunEstimate). Never below the table,
// never below what the collect can book. ok = false for every other kind.
func (s *Server) designFalRouteEstimate(kind string, outputs int) (decimal.NullDecimal, bool) {
	if !designFalRouteKind(kind) {
		return decimal.NullDecimal{}, false
	}
	if outputs < 1 {
		outputs = 1
	}
	per := designPriceEstimate[kind]
	if r, ok := s.designFalRoutes[kind]; ok && r.Bounded {
		per = decimal.Max(per, r.Ceiling)
	}
	return decimal.NullDecimal{Decimal: per.Mul(decimal.NewFromInt(int64(outputs))), Valid: true}, true
}

// designRunKinds — THE RUN KINDS StartDesignRun ACCEPTS ON THIS BINARY RIGHT NOW (band field 32): the
// money flag, then each kind's gate (the worker's own pre-flight), then — for the kinds whose reserve
// depends on the deployment's tariff (threed, extend, inpaint) — a bounded reserve. The same ladder
// as the door. Empty, never nil: present-and-empty = generation is off; absent = an older binary.
// draft_idea is never listed (it has its own verb).
func (s *Server) designRunKinds() []string {
	out := []string{}
	if s.designGenerationGate() != nil {
		return out
	}
	for _, k := range entity.DesignRunKinds() {
		if k == entity.DesignRunKindDraftIdea || s.designKindGateCheck(k) != nil {
			continue
		}
		switch {
		case k == entity.DesignRunKindThreed && !s.designThreedRouteReserveBounded():
			continue
		case designFalRouteKind(k) && !s.designFalRouteBounded(k):
			continue
		}
		out = append(out, k)
	}
	return out
}

// designRefuseMalformedRoutes — params.extend / params.inpaint on a kind that does not read them,
// asked of the SPEAKER only (the freeform_forbidden doctrine, design_freeform.go): a silent rerun
// cannot contradict itself, and a block the route does not read would be «accepted, did nothing».
func designRefuseMalformedRoutes(kind string, spoken *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindExtend && spoken.GetExtend() != nil {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeExtendForbidden,
			fmt.Sprintf("params.extend is the target of an extend run and only an extend run reads it; this "+
				"is a %s run. Nothing was reserved and nothing was charged", kind),
			map[string]string{"kind": kind})
	}
	if kind != entity.DesignRunKindInpaint && spoken.GetInpaint() != nil {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeInpaintForbidden,
			fmt.Sprintf("params.inpaint is the picture and mask of a mask retouch and only an inpaint run "+
				"reads it; this is a %s run. Nothing was reserved and nothing was charged", kind),
			map[string]string{"kind": kind})
	}
	return nil
}

// designRefuseUnworkableExtend — the shape of an extend, on EFFECTIVE params (a rerun that cannot
// work does not work however it was spoken): exactly one source, no words, one of the nine ratios.
func designRefuseUnworkableExtend(ask string, params *pb_common.DesignRunParams) error {
	if n := len(params.GetExtraInputMediaIds()); n != 1 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneSourcePicture,
			fmt.Sprintf("extending a picture works on exactly one picture, and this run names %d: put that one "+
				"picture in params.extra_input_media_ids. Nothing was reserved and nothing was charged", n),
			map[string]string{"named": strconv.Itoa(n)})
	}
	if strings.TrimSpace(ask) != "" || params.GetFreeform() != nil {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeExtendTakesNoWords,
			"extending a picture takes the picture and a target format, nothing else: the route has no prompt, "+
				"so your words would go nowhere. Clear `ask` (and params.freeform). Nothing was reserved and "+
				"nothing was charged", nil)
	}
	r := strings.TrimSpace(params.GetExtend().GetAspectRatio())
	if !entity.IsDesignExtendRatio(r) {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeExtendAspectUnknown,
			fmt.Sprintf("params.extend.aspect_ratio %q is not one of %s — an extend needs a target format "+
				"(never auto). Nothing was reserved and nothing was charged",
				r, strings.Join(entity.DesignExtendRatios(), " | ")),
			map[string]string{"aspect_ratio": r})
	}
	return nil
}

// designRefuseExtendTarget — THE SOURCE MUST BE BIG ENOUGH AND THE TARGET MUST ADD PIXELS, read off
// the stored full-size dimensions (one media read, only for an extend) with the WORKER'S OWN
// predicate (designgen.ExtendTargetAddsNothing). A row with no stored dimensions (legacy 0×0) is
// UNKNOWN, not small: it passes, and the worker — which decodes the picture before any money — is
// the second lock.
func (s *Server) designRefuseExtendTarget(ctx context.Context, kind string, params *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindExtend {
		return nil
	}
	ids := params.GetExtraInputMediaIds()
	if len(ids) != 1 {
		return nil
	}
	id := int(ids[0])
	byID, err := s.repo.Media().GetMediaByIds(ctx, []int{id})
	if err != nil {
		return designError(ctx, "failed to read the picture an extend grows", err, nil)
	}
	m, ok := byID[id]
	if !ok || m.FullSizeWidth <= 0 || m.FullSizeHeight <= 0 {
		return nil
	}
	if min := designgen.WindowMinSourcePx; m.FullSizeWidth < min || m.FullSizeHeight < min {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeSourceTooSmall,
			fmt.Sprintf("picture %d is %d×%d px, and an extend needs at least %d px on each side — use a larger "+
				"picture. Nothing was reserved and nothing was charged", id, m.FullSizeWidth, m.FullSizeHeight, min),
			map[string]string{
				"media_id": strconv.Itoa(id),
				"width":    strconv.Itoa(m.FullSizeWidth),
				"height":   strconv.Itoa(m.FullSizeHeight),
				"minimum":  strconv.Itoa(min),
			})
	}
	target := strings.TrimSpace(params.GetExtend().GetAspectRatio())
	if nothing, ok := designgen.ExtendTargetAddsNothing(m.FullSizeWidth, m.FullSizeHeight, target); ok && nothing {
		src := strconv.Itoa(m.FullSizeWidth) + "x" + strconv.Itoa(m.FullSizeHeight)
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeTargetAspectMustExtend,
			fmt.Sprintf("picture %d is %s, which already is %s — extending it to its own proportion adds "+
				"nothing. Pick another format. Nothing was reserved and nothing was charged", id, src, target),
			map[string]string{"source": src, "target": target})
	}
	return nil
}
