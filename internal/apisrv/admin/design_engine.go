package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
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

// designRefuseImageOptions — params.image against the engine table, on EFFECTIVE params (a rerun
// inherits the engine of the run it repeats and is priced by it).
func (s *Server) designRefuseImageOptions(kind string, params *pb_common.DesignRunParams) error {
	img := params.GetImage()
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
	// THE REFERENCE CEILING OF THE CHOSEN ENGINE. Counted per call, the way the worker builds it:
	// a playground call carries every picture plus its crops and outlines, a recolour call one
	// photograph plus the cloths. Flat / render / pattern are capped by the provider client itself.
	refs := 0
	switch kind {
	case entity.DesignRunKindFreeform:
		refs = designFreeformCallImages(params)
	case entity.DesignRunKindRecolor:
		refs = designRecolorCallImages(params)
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

func designEngineTierWords(e designgen.Engine) []string {
	out := make([]string, 0, len(e.Tiers))
	for _, t := range e.Tiers {
		out = append(out, t.UI)
	}
	return out
}

// designFreeformCallImages — how many images the ONE playground call carries: each picture, a crop
// per marked area and an outlined copy per marked picture (the designRefuseFreeformOverflow
// arithmetic, which reads this same function).
func designFreeformCallImages(params *pb_common.DesignRunParams) int {
	pictures, regions, marked := designFreeformImageCounts(params)
	return pictures + regions + marked
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
