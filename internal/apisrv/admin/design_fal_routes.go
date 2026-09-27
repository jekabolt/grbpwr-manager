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
	"google.golang.org/grpc/status"
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
	if r.Unsupported {
		// A FAL_MODEL_* slug this binary builds no body for (G-03, Fable m-1): not a money question —
		// the route cannot be sent at all, so the kind is not available, and the band does not list it.
		return designRefusal(codes.FailedPrecondition, designReasonKindUnavailable,
			fmt.Sprintf("a %s run cannot be started on this deployment: %s. Nothing was reserved and nothing "+
				"was charged", kind, r.Unbounded),
			map[string]string{"kind": kind, "flag": r.Flag})
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
	if !ok {
		// ⚠ «NO ROW» IS NOT «A LEGACY ROW WITH NO DIMENSIONS» (G-03, Codex 8). A legacy row exists and
		// the worker can still decode it; an id with no row at all can only reach the worker's
		// source_gone — after the reservation. Refused here, free.
		return designRefuseMissingSource(id, "params.extra_input_media_ids")
	}
	if m.FullSizeWidth <= 0 || m.FullSizeHeight <= 0 {
		return nil
	}
	if minSide := designgen.WindowMinSourcePx; m.FullSizeWidth < minSide || m.FullSizeHeight < minSide {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeSourceTooSmall,
			fmt.Sprintf("picture %d is %d×%d px, and an extend needs at least %d px on each side — use a larger "+
				"picture. Nothing was reserved and nothing was charged", id, m.FullSizeWidth, m.FullSizeHeight, minSide),
			map[string]string{
				"media_id": strconv.Itoa(id),
				"width":    strconv.Itoa(m.FullSizeWidth),
				"height":   strconv.Itoa(m.FullSizeHeight),
				"minimum":  strconv.Itoa(minSide),
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

// designRefuseMissingSource — the one picture an extend / inpaint works on names no media row
// (G-03, Codex 8). Free, before the reservation; the worker's source_gone stays for a deletion that
// races the pickup.
func designRefuseMissingSource(id int, field string) error {
	return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeNoSourcePicture,
		fmt.Sprintf("%s names picture %d, and there is no such picture — upload it or pick one of this card's. "+
			"Nothing was reserved and nothing was charged", field, id),
		map[string]string{"media_id": strconv.Itoa(id), "why": "the picture does not exist"})
}

// designRefuseFalRerunPictureSwap — A RERUN OF AN EXTEND / INPAINT REPEATS ITS PICTURE (G-03, Fable
// M-1; the G-02 M-2 defect, third instance — designRefuseFreeformRerunPictureSwap and
// designRefuseThreedRerunReferenceSwap are the other two). A spoken rerun replaces the parent's
// params whole, so without this a rerun of extend run 5 (picture 88) naming picture 91 went out under
// `rerun_of = 5`: the money right, the provenance a lie.
//
//   - extend: the source (params.extra_input_media_ids) may not change;
//   - inpaint: the SOURCE may not change; the mask may — it is the «area», which the freeform doctrine
//     lets a rerun re-mark (a retouch repeated with a corrected paint is the same picture, retouched).
//
// Asked of the SPEAKER only (a silent rerun inherits the parent whole); a spoken rerun that names no
// source is somebody else's refusal (one_source_picture / no_source_picture).
func designRefuseFalRerunPictureSwap(kind string, spoken *pb_common.DesignRunParams, parentID int, parentParams []byte) error {
	if !designFalRouteKind(kind) || spoken == nil || parentID <= 0 {
		return nil
	}
	inherited := &pb_common.DesignRunParams{}
	if len(parentParams) > 0 {
		if err := designUnmarshalJSON(parentParams, inherited); err != nil {
			return status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parentID)
		}
	}
	var was, now []int
	switch kind {
	case entity.DesignRunKindExtend:
		for _, id := range spoken.GetExtraInputMediaIds() {
			now = append(now, int(id))
		}
		for _, id := range inherited.GetExtraInputMediaIds() {
			was = append(was, int(id))
		}
	case entity.DesignRunKindInpaint:
		if id := spoken.GetInpaint().GetSourceMediaId(); id > 0 {
			now = []int{int(id)}
		}
		if id := inherited.GetInpaint().GetSourceMediaId(); id > 0 {
			was = []int{int(id)}
		}
	}
	if len(now) == 0 {
		return nil
	}
	parentSet := make(map[int]struct{}, len(was))
	for _, id := range was {
		parentSet[id] = struct{}{}
	}
	childSet := make(map[int]struct{}, len(now))
	for _, id := range now {
		childSet[id] = struct{}{}
	}
	added := designSortedMissing(childSet, parentSet)
	dropped := designSortedMissing(parentSet, childSet)
	if len(added) == 0 && len(dropped) == 0 {
		return nil
	}
	what := "extended"
	if kind == entity.DesignRunKindInpaint {
		what = "retouched (the mask may change on a rerun; the picture may not)"
	}
	return designRefusal(codes.InvalidArgument, "rerun_changes_pictures",
		fmt.Sprintf("a rerun repeats the run it points at: run %d %s picture %s, and this one names %s — start "+
			"a new run instead of a rerun. Nothing was reserved and nothing was charged",
			parentID, what, designJoinIDsOrNone(was), designJoinIDsOrNone(now)),
		map[string]string{
			"rerun_of": strconv.Itoa(parentID),
			"added":    designJoinIDs(added),
			"dropped":  designJoinIDs(dropped),
		})
}
