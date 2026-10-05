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

// ═══ THE THREE FLAT MODES AT THE DOOR (tmp/plans/flat-consistency/81-FINAL-MODES.md) ═══
//
// params.flat = { mode: "" | photos | hand_flat | straps, structure_refs: [{media_id, role}] }.
// Every refusal below stands BEFORE StartRun reserves anything: it is free.
//
//   - flat_forbidden        — the block on any kind but flat;
//   - unknown_flat_mode     — a mode word that is none of the three;
//   - structure_forbidden   — structure_refs on a mode that does not read them;
//   - structure_required    — hand_flat without refs;
//   - structure_malformed   — a role that is not front_flat | back_flat, a role or a media twice;
//   - structure_not_on_card — a ref that is not a TECHNICAL media of this card;
//   - structure_gone        — a ref whose media row no longer exists (reruns included);
//   - joins_unconfirmed     — straps on a card whose join list is missing, unusable or not confirmed at
//     its current rev (FailedPrecondition, metadata joins_rev);
//   - mode_not_for_this_run — hand_flat / straps on a detail-only or per_view run, or a rerun that
//     changes its parent's mode, flats or (hand_flat) views.
//
// A rerun inherits the parent's block (designFlatRerunInherit) and its snapshot (joins included), so
// neither the confirmation nor today's card media are asked again; a fix sends the block of the plate's
// run and skips the confirmation the same way.

// designFlatModeOf — the normalised mode of the effective params ("" = photos); ok=false for an unknown
// word.
func designFlatModeOf(params *pb_common.DesignRunParams) (string, bool) {
	return designgen.NormalizeFlatMode(params.GetFlat().GetMode())
}

// designFlatParentParams — the parent's frozen params (nil when they do not parse).
func designFlatParentParams(parent *entity.DesignRun) *pb_common.DesignRunParams {
	if parent == nil || len(parent.Params) == 0 {
		return nil
	}
	pp := &pb_common.DesignRunParams{}
	if designUnmarshalJSON(parent.Params, pp) != nil {
		return nil
	}
	return pp
}

// designFlatRerunInherit — a flat RERUN draws in its parent's mode from its parent's pictures: the
// rerun's snapshot is the parent's copy (designRunInputs), so another mode would send those pictures
// under another craft. A client that omits the block (every client before the modes) inherits it; one
// that states the parent's own block is accepted; any other is refused. A hand_flat rerun also keeps the
// parent's views in order (the designer's flats and the derived views are named by position).
func designFlatRerunInherit(kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun) error {
	if kind != entity.DesignRunKindFlat || parent == nil {
		return nil
	}
	pp := designFlatParentParams(parent)
	pf := pp.GetFlat()
	pm, _ := designgen.NormalizeFlatMode(pf.GetMode())
	refuse := func(why string) error {
		return designRefusal(codes.InvalidArgument, "mode_not_for_this_run",
			fmt.Sprintf("run %d was drawn in the %q mode and a rerun repeats it with the same pictures%s; start a "+
				"new run for another mode. Nothing was reserved and nothing was charged", parent.Id, pm, why),
			map[string]string{"parent_mode": pm, "mode": params.GetFlat().GetMode()})
	}
	if pm == designgen.FlatModeHandFlat && !designFlatSameViews(pp, params) {
		return refuse(" and views")
	}
	if params.GetFlat() == nil {
		if pf != nil {
			params.Flat = proto.Clone(pf).(*pb_common.DesignFlatParams)
		}
		return nil
	}
	cm, ok := designFlatModeOf(params)
	if !ok || cm != pm || !designFlatSameStructure(params.GetFlat(), pf) {
		return refuse("")
	}
	if pf != nil {
		params.Flat = proto.Clone(pf).(*pb_common.DesignFlatParams)
	}
	return nil
}

func designFlatSameViews(a, b *pb_common.DesignRunParams) bool {
	if a.GetLayout() != b.GetLayout() || len(a.GetViews()) != len(b.GetViews()) {
		return false
	}
	for i, v := range a.GetViews() {
		if v != b.GetViews()[i] {
			return false
		}
	}
	return true
}

// designFlatSameStructure — two blocks name the same flats in the same roles and order.
func designFlatSameStructure(a, b *pb_common.DesignFlatParams) bool {
	if len(a.GetStructureRefs()) != len(b.GetStructureRefs()) {
		return false
	}
	for i, r := range a.GetStructureRefs() {
		o := b.GetStructureRefs()[i]
		if r.GetMediaId() != o.GetMediaId() || strings.TrimSpace(r.GetRole()) != strings.TrimSpace(o.GetRole()) {
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
// inheritance and the detail canonicalisation). band and card are the door's own reads: the
// confirmation is checked on the same join list the snapshot freezes.
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
			fmt.Sprintf("params.flat.mode %q is not photos | hand_flat | straps", f.GetMode()),
			map[string]string{"mode": f.GetMode()})
	}
	if mode != designgen.FlatModeHandFlat && len(f.GetStructureRefs()) > 0 {
		return designRefusal(codes.InvalidArgument, "structure_forbidden",
			"params.flat.structure_refs names the card's own flats and only a hand_flat run reads it. "+
				"Nothing was reserved and nothing was charged", map[string]string{"mode": mode})
	}
	if mode == designgen.FlatModePhotos {
		return nil
	}
	if !designgen.FlatIsGarmentSheet(params.GetViews(), params.GetLayout()) {
		return designRefusal(codes.InvalidArgument, "mode_not_for_this_run",
			fmt.Sprintf("the %s mode draws the garment on one sheet; a detail sketch or a per-view run uses the photos route. "+
				"Nothing was reserved and nothing was charged", mode),
			map[string]string{"mode": mode, "layout": params.GetLayout()})
	}
	if mode == designgen.FlatModeHandFlat {
		return designRefuseFlatHandFlat(f, parent, card)
	}
	// straps: a rerun repeats its parent's frozen list. A fix freezes the card's CURRENT list, so it
	// needs the confirmation like a new press.
	if parent != nil {
		return nil
	}
	cur, usable, confirmed := 0, false, false
	if band != nil && band.Joins != nil {
		cur = band.Joins.Rev
		doc := band.Joins.Doc
		usable = designgen.JoinsUsable(&doc)
		confirmed = doc.Confirmed
	}
	if !usable || !confirmed {
		return designRefusal(codes.FailedPrecondition, "joins_unconfirmed",
			fmt.Sprintf("the straps mode draws from the join list a designer confirmed; the card's list (rev %d) is %s. "+
				"Check it and confirm it, then generate. Nothing was reserved and nothing was charged",
				cur, map[bool]string{true: "not confirmed", false: "missing or empty"}[usable]),
			map[string]string{"joins_rev": strconv.Itoa(cur)})
	}
	return nil
}

// designRefuseFlatHandFlat — 1..2 of the card's own technical flats, front_flat / back_flat, each once.
// A rerun's refs passed this door with its parent and are not re-read against today's card.
func designRefuseFlatHandFlat(f *pb_common.DesignFlatParams, parent *entity.DesignRun, card *entity.TechCard) error {
	refs := f.GetStructureRefs()
	if len(refs) == 0 {
		return designRefusal(codes.InvalidArgument, "structure_required",
			"the hand_flat mode redraws the card's own technical flats: params.flat.structure_refs is required. "+
				"Nothing was reserved and nothing was charged", nil)
	}
	if len(refs) > designgen.FlatMaxStructureRefs {
		return designRefusal(codes.InvalidArgument, "structure_malformed",
			fmt.Sprintf("the hand_flat mode names at most %d flats (front_flat, back_flat). Nothing was reserved and "+
				"nothing was charged", designgen.FlatMaxStructureRefs), nil)
	}
	roles, media := map[string]bool{}, map[int32]bool{}
	for i, r := range refs {
		role := strings.TrimSpace(r.GetRole())
		if designgen.FlatStructureView(role) == "" || roles[role] || r.GetMediaId() <= 0 || media[r.GetMediaId()] {
			return designRefusal(codes.InvalidArgument, "structure_malformed",
				fmt.Sprintf("params.flat.structure_refs.%d: each flat names its own media and its own role "+
					"(front_flat | back_flat). Nothing was reserved and nothing was charged", i),
				map[string]string{"index": strconv.Itoa(i), "role": role})
		}
		roles[role], media[r.GetMediaId()] = true, true
	}
	if parent != nil {
		return nil
	}
	tech := designFlatTechnicalMedia(card)
	for i, r := range refs {
		if !tech[r.GetMediaId()] {
			return designRefusal(codes.InvalidArgument, "structure_not_on_card",
				fmt.Sprintf("media %d is not a technical flat of this card; the hand_flat mode redraws the card's own "+
					"flats. Nothing was reserved and nothing was charged", r.GetMediaId()),
				map[string]string{"index": strconv.Itoa(i), "media_id": strconv.Itoa(int(r.GetMediaId()))})
		}
	}
	return nil
}

// designRefuseFlatStructureGone — every flat a hand_flat run redraws still exists, reruns included:
// the worker refuses a trace without its flat (free), but only after the run was booked.
func (s *Server) designRefuseFlatStructureGone(ctx context.Context, kind string, params *pb_common.DesignRunParams) error {
	mode, _ := designFlatModeOf(params)
	if kind != entity.DesignRunKindFlat || mode != designgen.FlatModeHandFlat {
		return nil
	}
	var ids []int
	for _, r := range params.GetFlat().GetStructureRefs() {
		if r.GetMediaId() > 0 {
			ids = append(ids, int(r.GetMediaId()))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byID, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		return designError(ctx, "failed to read the card's flats", err, nil)
	}
	for _, id := range ids {
		if m, ok := byID[id]; !ok || strings.TrimSpace(m.FullSizeMediaURL) == "" {
			return designRefusal(codes.FailedPrecondition, "structure_gone",
				fmt.Sprintf("the flat (media %d) no longer exists; start a new run. Nothing was reserved and nothing was charged", id),
				map[string]string{"media_id": strconv.Itoa(id)})
		}
	}
	return nil
}

// designRefuseFlatReferenceCeiling — a flat sends all its pictures in ONE call; more than the engine
// takes would be refused by the worker after the run was booked. Counted on the frozen params and
// snapshot by the worker's own list (designgen.FlatCallPictures).
func (s *Server) designRefuseFlatReferenceCeiling(kind string, params *pb_common.DesignRunParams, paramsJSON, inputsJSON []byte) error {
	if kind != entity.DesignRunKindFlat {
		return nil
	}
	e, ok := designgen.FindEngine(s.designEngineTable(), params.GetImage().GetModel())
	if !ok || e.MaxRefs <= 0 {
		return nil
	}
	if n := designgen.FlatCallPictures(kind, paramsJSON, inputsJSON); n > e.MaxRefs {
		return designRefusal(codes.InvalidArgument, "too_many_pictures",
			fmt.Sprintf("this run would send %d images in one call and %s takes at most %d. Remove a "+
				"reference photo. Nothing was reserved and nothing was charged", n, e.Label, e.MaxRefs),
			map[string]string{"images": strconv.Itoa(n), "ceiling": strconv.Itoa(e.MaxRefs), "model": e.Slug})
	}
	return nil
}

// designFlatStructureRefs — a hand_flat run's snapshot refs: the designer's flats first, with their
// roles, then the photos gathered for fit. nil, false for any other run (the caller keeps today's refs).
func designFlatStructureRefs(src designInputSources, photos []*pb_common.DesignInputRef) ([]*pb_common.DesignInputRef, bool) {
	if src.Kind != entity.DesignRunKindFlat {
		return nil, false
	}
	if mode, _ := designFlatModeOf(src.Params); mode != designgen.FlatModeHandFlat {
		return nil, false
	}
	var out []*pb_common.DesignInputRef
	structural := map[int32]bool{}
	for _, r := range src.Params.GetFlat().GetStructureRefs() {
		out = append(out, &pb_common.DesignInputRef{MediaId: r.GetMediaId(), Role: strings.TrimSpace(r.GetRole())})
		structural[r.GetMediaId()] = true
	}
	for _, r := range photos {
		if structural[r.GetMediaId()] {
			continue
		}
		out = append(out, r)
	}
	return out, true
}

// designFlatMoodRoles — a flat's role-less reference whose card picture is a MOOD picture travels with
// the snapshot role `mood` (captioned «a DIFFERENT garment; style mood only»).
func designFlatMoodRoles(src designInputSources, refs []*pb_common.DesignInputRef) {
	if src.Kind != entity.DesignRunKindFlat || src.Card == nil {
		return
	}
	roles := designBoardRoles(src.Card)
	for _, r := range refs {
		if strings.TrimSpace(r.GetRole()) == "" && roles[int(r.GetMediaId())] == entity.TechCardMediaRoleMood {
			r.Role = entity.DesignRefRoleMood
		}
	}
}

// designFlatModeKeepsSlots — a hand_flat press sends the designer's flats and photos, never old bench
// flats; only a fix carries the plates it corrects.
func designFlatModeKeepsSlots(src designInputSources) bool {
	if src.Kind != entity.DesignRunKindFlat {
		return true
	}
	mode, _ := designFlatModeOf(src.Params)
	return mode != designgen.FlatModeHandFlat || designFlatIsFix(src.Params)
}
