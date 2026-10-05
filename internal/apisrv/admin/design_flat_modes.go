package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// ═══ THE THREE FLAT MODES AT THE DOOR (tmp/plans/flat-consistency/80-BUILD-MODES.md §2.1) ═══
//
// params.flat = { mode, underdrawing_media_id, underdrawing_joins_rev, underdrawing_renderer_rev }.
// Every refusal below stands BEFORE StartRun reserves anything: it is free.
//
//   - flat_forbidden          — the block on any kind but flat;
//   - unknown_flat_mode       — a mode word that is none of "" | quick | drawing | drawing_photos;
//   - underdrawing_forbidden  — an underdrawing on a quick run (it would never be sent);
//   - mode_not_for_this_run   — a drawing mode on a detail-only or per_view run, or a rerun whose mode
//     or drawing differs from its parent's (the parent's snapshot is what a rerun sends);
//   - underdrawing_required   — a drawing mode without a media id;
//   - underdrawing_stale      — a NEW press whose drawing was rendered from another joins rev than the
//     card's current one, or the card has no usable list (FailedPrecondition, metadata joins_rev);
//   - underdrawing_malformed  — the stored picture is not 16:9 ±3 % (the sheet is four 384×864 columns).
//
// A rerun inherits the parent's block (designFlatRerunInherit) and skips the rev check; a fix sends the
// block of the plate's run and skips the rev check too (the drawing is the one that sheet was traced
// from); neither re-reads the picture's shape.

const (
	designFlatUnderdrawingRatio    = 16.0 / 9.0
	designFlatUnderdrawingRatioTol = 0.03
)

// designFlatModeOf — the normalised mode of the effective params ("" → quick); ok=false for an unknown
// word.
func designFlatModeOf(params *pb_common.DesignRunParams) (string, bool) {
	return designgen.NormalizeFlatMode(params.GetFlat().GetMode())
}

// designFlatParentBlock — the parent's frozen params.flat (nil when it had none or does not parse).
func designFlatParentBlock(parent *entity.DesignRun) *pb_common.DesignFlatParams {
	if parent == nil || len(parent.Params) == 0 {
		return nil
	}
	pp := &pb_common.DesignRunParams{}
	if designUnmarshalJSON(parent.Params, pp) != nil {
		return nil
	}
	return pp.GetFlat()
}

// designFlatRerunInherit — a flat RERUN draws in its parent's mode, from its parent's drawing: the
// rerun's snapshot is the parent's copy (designRunInputs), so a different mode would send the parent's
// pictures under another craft — the construction drawing captioned as a photo, or photos traced as a
// drawing. A client that omits the block (every client before the modes) inherits it; one that states
// the parent's own block is accepted; any other is refused.
func designFlatRerunInherit(kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun) error {
	if kind != entity.DesignRunKindFlat || parent == nil {
		return nil
	}
	pf := designFlatParentBlock(parent)
	pm, _ := designgen.NormalizeFlatMode(pf.GetMode())
	if params.GetFlat() == nil {
		if pf != nil {
			params.Flat = proto.Clone(pf).(*pb_common.DesignFlatParams)
		}
		return nil
	}
	cm, ok := designFlatModeOf(params)
	if ok && cm == pm && (!designgen.FlatIsDrawingMode(pm) || designFlatSameStructure(params.GetFlat(), pf)) {
		if pf != nil {
			params.Flat = proto.Clone(pf).(*pb_common.DesignFlatParams)
		}
		return nil
	}
	return designRefusal(codes.InvalidArgument, "mode_not_for_this_run",
		fmt.Sprintf("run %d was drawn in the %s mode and a rerun repeats it with the same pictures; start a new "+
			"run for another mode. Nothing was reserved and nothing was charged", parent.Id, pm),
		map[string]string{"parent_mode": pm, "mode": params.GetFlat().GetMode()})
}

// designFlatSameStructure — two blocks name the same structure picture(s).
func designFlatSameStructure(a, b *pb_common.DesignFlatParams) bool {
	sa, _ := designgen.NormalizeFlatStructureSource(a.GetStructureSource())
	sb, _ := designgen.NormalizeFlatStructureSource(b.GetStructureSource())
	if sa != sb || a.GetUnderdrawingMediaId() != b.GetUnderdrawingMediaId() || len(a.GetStructureRefs()) != len(b.GetStructureRefs()) {
		return false
	}
	for i, r := range a.GetStructureRefs() {
		o := b.GetStructureRefs()[i]
		if r.GetMediaId() != o.GetMediaId() || strings.TrimSpace(r.GetView()) != strings.TrimSpace(o.GetView()) {
			return false
		}
	}
	return true
}

// designFlatTechnicalMedia — the card's technical media ids (its own flats).
func designFlatTechnicalMedia(card *entity.TechCard) map[int32]bool {
	out := map[int32]bool{}
	if card == nil {
		return out
	}
	for _, m := range card.Media {
		if m.Category == entity.TechCardMediaCategoryTechnical && m.MediaId > 0 {
			out[int32(m.MediaId)] = true
		}
	}
	return out
}

// designRefuseFlatParams — the rules of params.flat on the EFFECTIVE params (after the rerun
// inheritance and the detail canonicalisation). band is the door's own read: the stale check compares
// against the same join list the snapshot freezes.
func designRefuseFlatParams(kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun, band *entity.DesignBand, card *entity.TechCard) error {
	f := params.GetFlat()
	if kind != entity.DesignRunKindFlat {
		if f != nil {
			return designRefusal(codes.InvalidArgument, "flat_forbidden",
				fmt.Sprintf("params.flat is the flat route's mode and only a flat run reads it; this is a %s run. "+
					"Nothing was reserved and nothing was charged", kind),
				map[string]string{"kind": kind})
		}
		return nil
	}
	if f == nil {
		return nil
	}
	mode, ok := designgen.NormalizeFlatMode(f.GetMode())
	if !ok {
		return designRefusal(codes.InvalidArgument, "unknown_flat_mode",
			fmt.Sprintf("params.flat.mode %q is not quick | drawing | drawing_photos", f.GetMode()),
			map[string]string{"mode": f.GetMode()})
	}
	if !designgen.FlatIsDrawingMode(mode) {
		if f.GetUnderdrawingMediaId() != 0 || len(f.GetStructureRefs()) > 0 || strings.TrimSpace(f.GetStructureSource()) != "" {
			return designRefusal(codes.InvalidArgument, "underdrawing_forbidden",
				"a quick flat sends the card's photos, never a construction drawing; drop params.flat.underdrawing_media_id "+
					"or pick a drawing mode. Nothing was reserved and nothing was charged",
				map[string]string{"mode": mode})
		}
		return nil
	}
	if !designgen.FlatIsGarmentSheet(params.GetViews(), params.GetLayout()) {
		return designRefusal(codes.InvalidArgument, "mode_not_for_this_run",
			"a drawing mode draws the garment on one sheet; a detail sketch or a per-view run runs quick. "+
				"Nothing was reserved and nothing was charged",
			map[string]string{"mode": mode, "layout": params.GetLayout()})
	}
	source, ok := designgen.NormalizeFlatStructureSource(f.GetStructureSource())
	if !ok {
		return designRefusal(codes.InvalidArgument, "unknown_structure_source",
			fmt.Sprintf("params.flat.structure_source %q is not rendered | hand_flat", f.GetStructureSource()),
			map[string]string{"structure_source": f.GetStructureSource()})
	}
	if source == designgen.FlatStructureHandFlat {
		return designRefuseFlatHandFlat(f, parent, card)
	}
	if len(f.GetStructureRefs()) > 0 {
		return designRefusal(codes.InvalidArgument, "structure_malformed",
			"params.flat.structure_refs names the card's own flats and only a hand_flat source reads it. "+
				"Nothing was reserved and nothing was charged", map[string]string{"structure_source": source})
	}
	if f.GetUnderdrawingMediaId() <= 0 {
		return designRefusal(codes.InvalidArgument, "underdrawing_required",
			"a drawing mode traces the construction drawing: params.flat.underdrawing_media_id is required. "+
				"Nothing was reserved and nothing was charged",
			map[string]string{"mode": mode})
	}
	// A rerun repeats its parent's frozen drawing; a fix traces the drawing its plate was traced from.
	if parent != nil || designFlatIsFix(params) {
		return nil
	}
	cur := 0
	usable := false
	if band != nil && band.Joins != nil {
		cur = band.Joins.Rev
		doc := band.Joins.Doc
		usable = designgen.JoinsUsable(&doc)
	}
	if !usable || int(f.GetUnderdrawingJoinsRev()) != cur {
		return designRefusal(codes.FailedPrecondition, "underdrawing_stale",
			fmt.Sprintf("the construction drawing was rendered from join list rev %d and the card's list is rev %d%s — "+
				"redraw it and try again. Nothing was reserved and nothing was charged",
				f.GetUnderdrawingJoinsRev(), cur, map[bool]string{true: "", false: " with nothing usable in it"}[usable]),
			map[string]string{"joins_rev": strconv.Itoa(cur)})
	}
	return nil
}

// designRefuseFlatHandFlat — a hand_flat structure: 1..4 of the card's own technical flats, one per
// silhouette view, no rendered drawing beside them. No joins-rev guard: the designer's flat is not
// rendered from the list. A rerun's refs passed this door with its parent and are not re-read
// against today's card (a flat removed from the card since stays the parent's picture).
func designRefuseFlatHandFlat(f *pb_common.DesignFlatParams, parent *entity.DesignRun, card *entity.TechCard) error {
	refs := f.GetStructureRefs()
	if len(refs) == 0 {
		return designRefusal(codes.InvalidArgument, "structure_required",
			"a hand_flat drawing traces the card's own technical flats: params.flat.structure_refs is required. "+
				"Nothing was reserved and nothing was charged", nil)
	}
	if len(refs) > designgen.FlatMaxStructureRefs || f.GetUnderdrawingMediaId() != 0 {
		return designRefusal(codes.InvalidArgument, "structure_malformed",
			fmt.Sprintf("a hand_flat drawing names 1..%d of the card's flats and no rendered drawing. "+
				"Nothing was reserved and nothing was charged", designgen.FlatMaxStructureRefs), nil)
	}
	views, media := map[string]bool{}, map[int32]bool{}
	for i, r := range refs {
		v := strings.TrimSpace(r.GetView())
		if !entity.IsDesignSilhouetteView(v) || views[v] || r.GetMediaId() <= 0 || media[r.GetMediaId()] {
			return designRefusal(codes.InvalidArgument, "structure_malformed",
				fmt.Sprintf("params.flat.structure_refs.%d: each flat names its own media and its own view "+
					"(front | back | side_l | side_r | three_quarter_l | three_quarter_r). Nothing was reserved and nothing was charged", i),
				map[string]string{"index": strconv.Itoa(i), "view": v})
		}
		views[v], media[r.GetMediaId()] = true, true
	}
	if parent != nil {
		return nil
	}
	tech := designFlatTechnicalMedia(card)
	for i, r := range refs {
		if !tech[r.GetMediaId()] {
			return designRefusal(codes.InvalidArgument, "structure_not_on_card",
				fmt.Sprintf("media %d is not a technical flat of this card; a hand_flat drawing traces the card's own "+
					"flats. Nothing was reserved and nothing was charged", r.GetMediaId()),
				map[string]string{"index": strconv.Itoa(i), "media_id": strconv.Itoa(int(r.GetMediaId()))})
		}
	}
	return nil
}

// designFlatIsRendered — a drawing-mode run whose structure is the client-rendered sheet.
func designFlatIsRendered(params *pb_common.DesignRunParams) bool {
	mode, _ := designFlatModeOf(params)
	src, _ := designgen.NormalizeFlatStructureSource(params.GetFlat().GetStructureSource())
	return designgen.FlatIsDrawingMode(mode) && src == designgen.FlatStructureRendered
}

// designRefuseUnderdrawingShape — the stored picture is a 16:9 sheet (±3 %). Read off the media row's
// stored size; a row with none (legacy 0×0) is unknown and passes. New presses and fixes only: a rerun's
// drawing passed this door with its parent.
func (s *Server) designRefuseUnderdrawingShape(ctx context.Context, kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun) error {
	if kind != entity.DesignRunKindFlat || parent != nil || !designFlatIsRendered(params) {
		return nil
	}
	id := int(params.GetFlat().GetUnderdrawingMediaId())
	if id <= 0 {
		return nil
	}
	byID, err := s.repo.Media().GetMediaByIds(ctx, []int{id})
	if err != nil {
		return designError(ctx, "failed to read the construction drawing", err, nil)
	}
	m, ok := byID[id]
	if !ok {
		return designRefusal(codes.InvalidArgument, "underdrawing_malformed",
			fmt.Sprintf("the construction drawing (media %d) does not exist; redraw it and try again. "+
				"Nothing was reserved and nothing was charged", id),
			map[string]string{"media_id": strconv.Itoa(id)})
	}
	return designUnderdrawingShapeRefusal(id, m.FullSizeWidth, m.FullSizeHeight)
}

// designUnderdrawingShapeRefusal — the pure half of the shape check.
func designUnderdrawingShapeRefusal(id, w, h int) error {
	if w <= 0 || h <= 0 {
		return nil
	}
	r := float64(w) / float64(h)
	if d := r/designFlatUnderdrawingRatio - 1; d > designFlatUnderdrawingRatioTol || d < -designFlatUnderdrawingRatioTol {
		return designRefusal(codes.InvalidArgument, "underdrawing_malformed",
			fmt.Sprintf("the construction drawing (media %d) is %d×%d px; a drawing sheet is 16:9. Redraw it and "+
				"try again. Nothing was reserved and nothing was charged", id, w, h),
			map[string]string{"media_id": strconv.Itoa(id), "width": strconv.Itoa(w), "height": strconv.Itoa(h)})
	}
	return nil
}

// designFreezeFlatAspect — a drawing-mode flat is drawn 16:9 (the drawing's four 384×864 columns)
// when the run states no ratio and its engine offers it.
func (s *Server) designFreezeFlatAspect(kind string, params *pb_common.DesignRunParams) {
	if kind != entity.DesignRunKindFlat || !designFlatIsRendered(params) {
		return
	}
	if strings.TrimSpace(params.GetImage().GetAspectRatio()) != "" {
		return
	}
	e, ok := designgen.FindEngine(s.designEngineTable(), params.GetImage().GetModel())
	if !ok {
		return
	}
	for _, r := range e.Ratios {
		if r == "16:9" {
			if params.Image == nil {
				params.Image = &pb_common.DesignImageOptions{}
			}
			params.Image.AspectRatio = "16:9"
			return
		}
	}
}

// designFlatDrawingRefs — the refs a drawing-mode flat's snapshot records: the underdrawing first
// (role `underdrawing`), then — drawing_photos only — the card's kept photos and the named extras.
// nil, false for any other run (the caller keeps today's refs).
func designFlatDrawingRefs(src designInputSources, photos []*pb_common.DesignInputRef) ([]*pb_common.DesignInputRef, bool) {
	if src.Kind != entity.DesignRunKindFlat {
		return nil, false
	}
	mode, _ := designFlatModeOf(src.Params)
	if !designgen.FlatIsDrawingMode(mode) {
		return nil, false
	}
	var out []*pb_common.DesignInputRef
	structural := map[int32]bool{}
	if designFlatIsRendered(src.Params) {
		under := src.Params.GetFlat().GetUnderdrawingMediaId()
		out = append(out, &pb_common.DesignInputRef{MediaId: under, Role: entity.DesignRefRoleUnderdrawing})
		structural[under] = true
	} else {
		for _, r := range src.Params.GetFlat().GetStructureRefs() {
			out = append(out, &pb_common.DesignInputRef{MediaId: r.GetMediaId(), Role: entity.DesignRefRoleUnderdrawing,
				Note: "the designer's own technical flat — " + strings.TrimSpace(r.GetView())})
			structural[r.GetMediaId()] = true
		}
	}
	if mode == designgen.FlatModeDrawingPhotos {
		for _, r := range photos {
			if structural[r.GetMediaId()] {
				continue
			}
			out = append(out, r)
		}
	}
	return out, true
}

// designFlatModeKeepsSlots — whether a drawing-mode flat's snapshot keeps bench plates: only a fix
// carries the plates it corrects; a drawing press sends the drawing (and photos), never old flats.
func designFlatModeKeepsSlots(src designInputSources) bool {
	if src.Kind != entity.DesignRunKindFlat {
		return true
	}
	mode, _ := designFlatModeOf(src.Params)
	return !designgen.FlatIsDrawingMode(mode) || designFlatIsFix(src.Params)
}
