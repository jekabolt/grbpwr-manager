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
	"google.golang.org/protobuf/proto"
)

// ═══ THE PER-RUN ENGINE AT THE DOOR (params.image, PLAYGROUND phase 2) ═══
//
// One table (designgen.EngineTable) decides what the door accepts, what it reserves and what the
// band advertises; the worker resolves the frozen words against the same table. Everything here
// runs BEFORE StartRun, so every refusal is free.

// SetDesignEngines wires the engine table (app.go, beside SetDesignKindGate). A function, not a
// slice, for the same reason as the kind gate: the answer belongs to the image client.
func (s *Server) SetDesignEngines(f func() []designgen.Engine) { s.designEngines = f }

// designEngineTable — the engines this server accepts; nil when none are wired, and then the door
// refuses every params.image (a run it cannot price is a run it does not take).
func (s *Server) designEngineTable() []designgen.Engine {
	if s.designEngines == nil {
		return nil
	}
	return s.designEngines()
}

// designImageOptionsKind — the kinds drawn by the OpenRouter image route, i.e. the only kinds whose
// request has a model, a quality and a ratio to choose.
func designImageOptionsKind(kind string) bool {
	switch kind {
	case entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindRecolor,
		entity.DesignRunKindPattern, entity.DesignRunKindFreeform:
		return true
	}
	return false
}

// designImageStated — whether params.image names anything. An empty block is the same request as
// no block (designgen reads it the same way: imageOptionsStated).
func designImageStated(o *pb_common.DesignImageOptions) bool {
	return o != nil && (strings.TrimSpace(o.GetModel()) != "" || strings.TrimSpace(o.GetQuality()) != "" ||
		strings.TrimSpace(o.GetAspectRatio()) != "" || strings.TrimSpace(o.GetBackground()) != "")
}

// designRefuseImageOptions — THE VOCABULARY of params.image against the engine table, asked of the
// SPEAKER only (G-02, Fable m-5). The repo's own doctrine (designRefuseMalformedFreeform): a
// vocabulary legally narrows — the plan itself foresees deleting `transparent` on a 400 — and a
// frozen run must stay rerunnable. A silent rerun sends its frozen words verbatim (applyImageOptions)
// and is priced by designEstimateForRun, which falls back to the kind's table for a slug the table no
// longer lists. The REFERENCE CEILING is a different question — whether this call fits — and is
// asked of the effective params (designRefuseImageReferenceCeiling).
func (s *Server) designRefuseImageOptions(kind string, spoken *pb_common.DesignRunParams) error {
	img := spoken.GetImage()
	if !designImageStated(img) {
		return nil
	}
	if !designImageOptionsKind(kind) {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeImageOptionsForbidden,
			fmt.Sprintf("params.image picks the engine of an image run (flat | render | recolor | pattern "+
				"| freeform); a %s run has no such engine. Nothing was reserved and nothing was charged", kind),
			map[string]string{"kind": kind})
	}
	model := strings.TrimSpace(img.GetModel())
	engine, ok := designgen.FindEngine(s.designEngineTable(), model)
	if !ok {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeUnknownImageModel,
			fmt.Sprintf("params.image.model %q is not an engine this server offers (see the band's "+
				"image_models). Nothing was reserved and nothing was charged", model),
			map[string]string{"model": model})
	}
	if q := strings.TrimSpace(img.GetQuality()); q != "" {
		if _, ok := engine.Tier(q); !ok {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeQualityNotSupported,
				fmt.Sprintf("params.image.quality %q is not a tier of %s (%s). Nothing was reserved and "+
					"nothing was charged", q, engine.Label, strings.Join(designEngineTierWords(engine), " | ")),
				map[string]string{"model": engine.Slug, "quality": q})
		}
	}
	// ⚠ ONE CALL MAY NOT ASK FOR MORE PICTURES THAN THE ENGINE RETURNS (B-16: `n` is 1..1 on Gemini
	// and Seedream). Only outputs that are variants of ONE call count: a recolour's photographs and a
	// per_view sheet are one n = 1 call each (designgen imageCalls), so they are never refused here.
	// Every route builds n = 1 today, so this is the door's lock for the day a «variants» count
	// arrives; the image route refuses the same thing for free before paying (designgen images.go).
	if n := designImageVariantsPerCall(kind, spoken); engine.MaxN > 0 && n > engine.MaxN {
		return designRefusal(codes.InvalidArgument, "outputs_not_supported",
			fmt.Sprintf("this run asks %s for %d pictures in one call and it returns at most %d. Nothing "+
				"was reserved and nothing was charged", engine.Label, n, engine.MaxN),
			map[string]string{"model": engine.Slug, "outputs": strconv.Itoa(n), "max": strconv.Itoa(engine.MaxN)})
	}
	// ⚠ A WINDOWED RUN TAKES THE CROP'S SHAPE (G-02, Codex 6). The answer is scaled straight into
	// the frozen crop (designgen compositeWindow), so an explicit ratio would buy a picture of another
	// shape and squeeze it into the rectangle. REFUSED rather than silently ignored: the person chose
	// a format, and dropping it without a word is the «accepted, did nothing» the doors exist to
	// prevent; the worker also sends no ratio on a windowed run (the lock for runs frozen before this).
	// '' and `auto` ask for nothing and pass.
	if r := strings.TrimSpace(img.GetAspectRatio()); r != "" && r != "auto" {
		if _, windowed := designFreeformWindowMediaID(spoken); windowed {
			return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeAspectNotSupported,
				fmt.Sprintf("params.image.aspect_ratio %q: this run edits a crop around its marked area and "+
					"fits the answer back into it, so the crop decides the shape — leave the format empty. "+
					"Nothing was reserved and nothing was charged", r),
				map[string]string{"model": engine.Slug, "aspect_ratio": r, "why": "window"})
		}
	}
	if r := strings.TrimSpace(img.GetAspectRatio()); !engine.Accepts(r) {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeAspectNotSupported,
			fmt.Sprintf("params.image.aspect_ratio %q is not a ratio %s draws (%s). Nothing was reserved "+
				"and nothing was charged", r, engine.Label, strings.Join(engine.Ratios, " ")),
			map[string]string{"model": engine.Slug, "aspect_ratio": r})
	}
	if b := strings.TrimSpace(img.GetBackground()); !engine.Background(b) {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeBackgroundNotSupported,
			fmt.Sprintf("params.image.background %q is not offered by %s. Nothing was reserved and "+
				"nothing was charged", b, engine.Label),
			map[string]string{"model": engine.Slug, "background": b})
	}
	return nil
}

// designRefuseImageReferenceCeiling — THE REFERENCE CEILING OF THE ENGINE THIS RUN WILL CALL, on
// EFFECTIVE params: a rerun that does not fit one call does not fit it however it was spoken. The
// engine is the effective one (params.image.model, or the default row); a slug the table no longer
// lists has no ceiling to read here, and the provider's own (designRefuseFreeformOverflow) still
// holds. Counted per call, the way the worker builds it (designImageCallImages).
func (s *Server) designRefuseImageReferenceCeiling(kind string, params *pb_common.DesignRunParams) error {
	if !designImageOptionsKind(kind) {
		return nil
	}
	img := params.GetImage()
	engine, ok := designgen.FindEngine(s.designEngineTable(), img.GetModel())
	if !ok {
		return nil
	}
	// Flat / render / pattern are capped by the provider client itself.
	refs := 0
	switch kind {
	case entity.DesignRunKindFreeform, entity.DesignRunKindRecolor:
		refs = designImageCallImages(kind, params, nil, 0)
	}
	if engine.MaxRefs > 0 && refs > engine.MaxRefs {
		return designRefusal(codes.InvalidArgument, "too_many_pictures",
			fmt.Sprintf("this run would send %d images in one call and %s takes at most %d. Remove a "+
				"picture. Nothing was reserved and nothing was charged", refs, engine.Label, engine.MaxRefs),
			map[string]string{
				"images":  strconv.Itoa(refs),
				"ceiling": strconv.Itoa(engine.MaxRefs),
				"model":   engine.Slug,
			})
	}
	return nil
}

// designFreezeImageModel — A STATED ENGINE IS FROZEN WITH ITS SLUG (G-02, Codex 5). A params.image
// that states anything (quality, ratio, background) but leaves `model` empty used to be stored as
// is, and the worker filled the model from the deployment's CURRENT default — so the day
// OPENROUTER_MODEL_IMAGE moves, the same frozen row (and every rerun of it) executes on another
// engine with no provenance change. The door resolves the default row HERE, once, and the row
// stores the slug it was priced by. A wholly absent / empty block stays the legacy «deployment
// default» run. Called after every refusal, before the params are marshalled.
func (s *Server) designFreezeImageModel(kind string, params *pb_common.DesignRunParams) {
	img := params.GetImage()
	if !designImageOptionsKind(kind) || !designImageStated(img) || strings.TrimSpace(img.GetModel()) != "" {
		return
	}
	if e, ok := designgen.FindEngine(s.designEngineTable(), ""); ok {
		img.Model = e.Slug
	}
}

// designImageVariantsPerCall — how many pictures ONE provider call of this run asks for: the
// requested outputs when they are variants of one call, 1 when each output is its own call (a
// recolour's photographs, a per_view sheet's views).
//
// ⚠ IT READS THE SPOKEN PARAMS, BEFORE THE DOOR WRITES `one` INTO AN EMPTY LAYOUT (design_run.go),
// so an unstated layout is counted as what it becomes — the composite sheet, one picture — never as
// len(views): designRequestedOutputs reads "" as per-view and would refuse a two-view render on an
// n = 1 engine that the worker draws as ONE n = 1 call (designgen imageCalls: unspecified = one).
func designImageVariantsPerCall(kind string, params *pb_common.DesignRunParams) int {
	switch {
	case kind == entity.DesignRunKindRecolor, params.GetLayout() == designLayoutPerView:
		return 1
	case params.GetLayout() == "":
		cp := proto.Clone(params).(*pb_common.DesignRunParams)
		cp.Layout = designLayoutOne
		return designRequestedOutputs(kind, cp)
	}
	return designRequestedOutputs(kind, params)
}

func designEngineTierWords(e designgen.Engine) []string {
	out := make([]string, 0, len(e.Tiers))
	for _, t := range e.Tiers {
		out = append(out, t.UI)
	}
	return out
}

// designFreeformCallImages — how many images the ONE playground call carries: each picture, a crop
// per marked area and an outlined copy per marked picture.
//
// ⚠ A WINDOWED RUN CARRIES ONLY ITS PICTURES (G-02, Codex 8). When the worker takes a generation
// window (designgen.FreeformWindowMediaID — its own decision, asked here, not copied), the marked
// picture is REPLACED by the window crop and its outlined copy and area crop are never made
// (deriveFreeformWindow + deriveFreeform's skip): retouch sends one image, add_hardware the crop plus
// its hardware pictures. Counting the phantom three overstated the reserve ($0.35 for a $0.33 call).
func designFreeformCallImages(params *pb_common.DesignRunParams) int {
	pictures, regions, marked := designFreeformImageCounts(params)
	if _, windowed := designFreeformWindowMediaID(params); windowed {
		return pictures
	}
	return pictures + regions + marked
}

// designFreeformWindowMediaID — whether the worker will cut a generation window for these params,
// and from which picture: designgen's own predicate over the params as they will be frozen.
func designFreeformWindowMediaID(params *pb_common.DesignRunParams) (int, bool) {
	if len(params.GetFreeform().GetItems()) == 0 {
		return 0, false
	}
	raw, err := designMarshalJSON(params)
	if err != nil {
		return 0, false
	}
	return designgen.FreeformWindowMediaID(entity.RawJSON(raw))
}

// designRefuseWindowSourceTooSmall — A WINDOW CANNOT BE CUT FROM A PICTURE UNDER
// designgen.WindowMinSourcePx ON EITHER SIDE, and the stored full-size dimensions already say so
// (G-02, Codex 7): refused here, BEFORE the reservation, with the worker's own word
// (source_too_small). One media read, only for a windowed run. A row with no stored dimensions
// (legacy, 0×0) is UNKNOWN, not small: it passes, and the worker — which decodes the picture — stays
// the second lock (a free, terminal refusal before StartAttempt). The same read refuses a windowed
// freeform frame over designgen.CompositeMaxSourcePixels (source_too_large).
func (s *Server) designRefuseWindowSourceTooSmall(ctx context.Context, kind string, params *pb_common.DesignRunParams) error {
	var id int
	switch kind {
	case entity.DesignRunKindFreeform:
		wid, ok := designFreeformWindowMediaID(params)
		if !ok {
			return nil
		}
		id = wid
	case entity.DesignRunKindInpaint:
		// PHASE 3: the mask retouch cuts a padded crop out of its picture — the same minimum.
		id = int(params.GetInpaint().GetSourceMediaId())
		if id <= 0 {
			return nil
		}
	default:
		return nil
	}
	byID, err := s.repo.Media().GetMediaByIds(ctx, []int{id})
	if err != nil {
		return designError(ctx, "failed to read the picture a generation window is cut from", err, nil)
	}
	m, ok := byID[id]
	if !ok {
		return nil
	}
	// ⚠ A GENERATION WINDOW IS PASTED BACK INTO ITS WHOLE FRAME AFTER THE PAYMENT, inside a 0.5 GB
	// process (G-03 r2 follow-up): the frame is held to the composite's working pixel cap here, before
	// the reservation — from its stored size, or, for a legacy row with none, from its header. (The
	// inpaint source is capped by designRefuseUnusableMask, which reads its mask's header anyway.)
	window := kind == entity.DesignRunKindFreeform
	if m.FullSizeWidth <= 0 || m.FullSizeHeight <= 0 {
		if window {
			if w, h, ok := s.designStoredPictureHeader(ctx, m.FullSizeMediaURL); ok && designOverCompositeCap(w, h) {
				return designRefuseSourceTooLarge(id, w, h, "a generation window")
			}
		}
		return nil
	}
	if window && designOverCompositeCap(m.FullSizeWidth, m.FullSizeHeight) {
		return designRefuseSourceTooLarge(id, m.FullSizeWidth, m.FullSizeHeight, "a generation window")
	}
	if min := designgen.WindowMinSourcePx; m.FullSizeWidth < min || m.FullSizeHeight < min {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeSourceTooSmall,
			fmt.Sprintf("picture %d is %d×%d px, and the area is cut out of it at least %d px on each side "+
				"— use a larger picture. Nothing was reserved and nothing was charged",
				id, m.FullSizeWidth, m.FullSizeHeight, min),
			map[string]string{
				"media_id": strconv.Itoa(id),
				"width":    strconv.Itoa(m.FullSizeWidth),
				"height":   strconv.Itoa(m.FullSizeHeight),
				"minimum":  strconv.Itoa(min),
			})
	}
	return nil
}

func designFreeformImageCounts(params *pb_common.DesignRunParams) (pictures, regions, marked int) {
	for _, it := range params.GetFreeform().GetItems() {
		if it.GetMediaId() <= 0 {
			continue
		}
		pictures++
		if n := len(it.GetRegions()); n > 0 {
			regions += n
			marked++
		}
	}
	return pictures, regions, marked
}

// designRecolorCallImages — how many images ONE recolour call carries: its photograph and every
// stated cloth picture (the scalar echo and the list, counted once each).
func designRecolorCallImages(params *pb_common.DesignRunParams) int {
	cloths := map[int]struct{}{}
	if id := int(params.GetColour().GetFabricMediaId()); id > 0 {
		cloths[id] = struct{}{}
	}
	for _, id := range designClothMediaIDs(params.GetColour()) {
		if id > 0 {
			cloths[id] = struct{}{}
		}
	}
	return 1 + len(cloths)
}

// designImageCallImages — an UPPER BOUND on the images one call of this run carries, for the
// reserve. Freeform and recolour are exact (the arithmetic above); a flat / render / pattern call
// carries at most every media id of the run plus its colour maps, and never more than the engine
// takes — the provider client refuses anything above that before the call.
func designImageCallImages(kind string, params *pb_common.DesignRunParams, inputs *pb_common.DesignInputSnapshot, maxRefs int) int {
	n := 0
	switch kind {
	case entity.DesignRunKindFreeform:
		n = designFreeformCallImages(params)
	case entity.DesignRunKindRecolor:
		n = designRecolorCallImages(params)
	default:
		n = len(designRunInputMediaRefs(params, inputs)) + len(designColourMapMediaIDs(params.GetColour()))
	}
	if maxRefs > 0 && n > maxRefs {
		n = maxRefs
	}
	return n
}

// designEstimateForRun — THE RESERVE OF ONE RUN, from the engine table: ceiling(tier) × outputs +
// InputUSD × images per call × outputs (requested_outputs IS the number of paid calls on every
// image kind: one per view, one per photograph, one for the playground).
//
//   - a run that NAMES an engine is priced by that engine and tier, and only by it — a named `low`
//     is honestly cheaper than the kind's table, because `low` is what the worker sends;
//   - a run that names none runs on the default engine at the deployment's dial, which the reserve
//     cannot read, so it is priced at the default engine's TOP tier with its references — and never
//     below the kind's own table (designEstimateFor), so no run reserves less than before the table;
//   - a server with no engine table, and every non-image kind, keep designEstimateFor.
//
// ⚠ NEVER NULL FOR A RUN THE DOOR ACCEPTED: designRefuseImageOptions refused a stated engine the
// table does not list, so the stated lookup cannot miss — and if it ever did, the answer is the
// kind's own table price, not zero.
//
// Kind threed prices by its options (B-09), through designThreedRunEstimate → designThreedCeilingUSDFor.
func (s *Server) designEstimateForRun(kind string, outputs int, params *pb_common.DesignRunParams,
	inputs *pb_common.DesignInputSnapshot) decimal.NullDecimal {
	base := designEstimateFor(kind, outputs)
	// PHASE 3: extend / inpaint reserve max(table, the route's own ceiling) — designFalRouteEstimate.
	if e, ok := s.designFalRouteEstimate(kind, outputs); ok {
		return e
	}
	// B-32: a video clip reserves RUNBLOB_VIDEO_CEILING_USD (the live route), else the table's default.
	if e, ok := s.designVideoRunEstimate(kind, outputs); ok {
		return e
	}
	// 3D reserves by ITS OWN options (texture off / detailed = fal «ultra», $1.40), B-09. With the
	// default options this is exactly designThreedCeilingUSD() × outputs — the kind's table row.
	if e, ok := s.designThreedRunEstimate(kind, params, outputs); ok {
		return e
	}
	if !designImageOptionsKind(kind) {
		return base
	}
	img := params.GetImage()
	stated := designImageStated(img)
	model, tier := "", ""
	if stated {
		model, tier = img.GetModel(), strings.TrimSpace(img.GetQuality())
	}
	engine, ok := designgen.FindEngine(s.designEngineTable(), model)
	if !ok {
		return base
	}
	if outputs < 1 {
		outputs = 1
	}
	calls := decimal.NewFromInt(int64(outputs))
	images := decimal.NewFromInt(int64(designImageCallImages(kind, params, inputs, engine.MaxRefs)))
	total := engine.CeilingUSD(tier).Mul(calls).Add(engine.InputUSD.Mul(images).Mul(calls))
	if !stated && base.Valid && base.Decimal.GreaterThan(total) {
		return base
	}
	return decimal.NullDecimal{Decimal: total, Valid: true}
}
