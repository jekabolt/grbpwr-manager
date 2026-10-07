package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// ═══ THE FLAT MODES AT THE DOOR (tmp/plans/flat-consistency/81-FINAL-MODES.md) ═══
//
// params.flat = { mode: "" | photos | hand_flat, structure_refs: [{media_id, role}] }. The third mode,
// straps, is RETIRED (owner 07.10, 100-CONSTRUCTION-DEADEND): it was the photos route plus the
// designer-confirmed join list, and the join list no longer reaches the image model — a straps press
// drew exactly what a photos press draws, behind a confirmation gate. M7 refused a new press of it; M7b
// took the word out of the modes (designgen.NormalizeFlatMode: the worker reads it as no mode, i.e. the
// photos route — the words a straps run has been drawn with since the list left the prompt). A RERUN of
// a straps run still repeats its parent, block and all, exactly as under M7: refusing it now would also
// refuse the replay of a rerun booked before this deploy whose answer was lost (the client_request_id
// replay lives in StartRun, after these doors), telling the person «nothing was reserved» beside a run
// that exists (Codex, M7b). Every refusal below stands BEFORE StartRun reserves anything: it is free.
//
//   - flat_forbidden        — the block on any kind but flat;
//   - unknown_flat_mode     — a mode word that is neither photos nor hand_flat (nor the retired straps);
//   - mode_retired          — straps on a NEW press (a rerun of a straps run repeats its parent);
//   - structure_forbidden   — structure_refs on a mode that does not read them;
//   - structure_required    — hand_flat without refs;
//   - structure_malformed   — a role that is not front_flat | back_flat, a role or a media twice;
//   - structure_not_on_card — a ref that is not a TECHNICAL media of this card;
//   - structure_gone        — a ref whose media row no longer exists (reruns included);
//   - mode_not_for_this_run — hand_flat on a detail-only or per_view run, or a rerun that changes its
//     parent's mode, flats or (hand_flat) views.
//
// A rerun inherits the parent's block (designFlatRerunInherit) and its snapshot, so today's card media
// are not asked again. The join list's state gates nothing (the joins_unconfirmed refusal went with the
// straps mode).

// designFlatModeStrapsRetired — the retired mode's word, kept only to name it in its refusal.
const designFlatModeStrapsRetired = "straps"

// designFlatModeIsRetired — the word names the retired straps mode.
func designFlatModeIsRetired(mode string) bool {
	return strings.TrimSpace(mode) == designFlatModeStrapsRetired
}

// designFlatRetiredRefusal — mode_retired: a new press that names the retired straps mode.
func designFlatRetiredRefusal() error {
	return designRefusal(codes.InvalidArgument, "mode_retired",
		"the straps mode was retired: the construction list no longer reaches the image model, so a flat "+
			"draws from the reference photos — send no params.flat. Nothing was reserved and nothing was charged",
		map[string]string{"mode": designFlatModeStrapsRetired})
}

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
	// A straps parent (retired, M7b) is matched by its own word: its rerun keeps the block and is drawn
	// by the photos route, as every straps run is now.
	retired := designFlatModeIsRetired(pf.GetMode())
	if retired {
		pm = designFlatModeStrapsRetired
	}
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
	if designFlatModeIsRetired(params.GetFlat().GetMode()) {
		cm, ok = designFlatModeStrapsRetired, true
	}
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
// inheritance and the detail canonicalisation). card is the door's own read (hand_flat's flats).
func designRefuseFlatParams(kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun, card *entity.TechCard) error {
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
		// THE STRAPS MODE IS RETIRED (M7/M7b, owner 07.10): a new press is refused, free, whatever else
		// the block carries; no client sends it. On a rerun the word can only be its straps parent's own
		// block (designFlatRerunInherit refuses it on any other parent): the rerun repeats that run, and
		// the worker draws it by the photos route.
		if designFlatModeIsRetired(f.GetMode()) {
			if parent == nil {
				return designFlatRetiredRefusal()
			}
			if !designgen.FlatIsGarmentSheet(params.GetViews(), params.GetLayout()) {
				return designRefusal(codes.InvalidArgument, "mode_not_for_this_run",
					"the straps mode drew the garment on one sheet; a detail sketch or a per-view run uses the photos "+
						"route. Nothing was reserved and nothing was charged",
					map[string]string{"mode": designFlatModeStrapsRetired, "layout": params.GetLayout()})
			}
			return nil
		}
		return designRefusal(codes.InvalidArgument, "unknown_flat_mode",
			fmt.Sprintf("params.flat.mode %q is not photos | hand_flat", f.GetMode()),
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
	// hand_flat: the only mode left that is not the photos route.
	return designRefuseFlatHandFlat(f, parent, card)
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

// designFlatOnlyRoledPhotos — A FLAT GETS THE GARMENT'S OWN PHOTOS AND NOTHING ELSE (owner 06.10, wave 10:
// «в флет генерейшен не будем передавать картинки из мудборда — это не нужно, только засоряет промпт»).
// A reference on a MOOD picture of the board (whatever role it carries in the references block), a
// reference whose role is `mood`, and a reference without a role (an extra input named in the request
// included) are dropped from the snapshot: the model sees only photos that say which side of THIS
// garment they show. A hand_flat run's own flats are added after this (designFlatStructureRefs), and a
// detail run's accepted FRONT/BACK flats travel as bench slots, not refs. Other kinds keep their refs.
func designFlatOnlyRoledPhotos(src designInputSources, refs []*pb_common.DesignInputRef) []*pb_common.DesignInputRef {
	if src.Kind != entity.DesignRunKindFlat {
		return refs
	}
	var roles map[int]entity.TechCardMediaRole
	if src.Card != nil {
		roles = designBoardRoles(src.Card)
	}
	out := refs[:0]
	for _, r := range refs {
		role := strings.TrimSpace(r.GetRole())
		if role == "" || role == entity.DesignRefRoleMood || roles[int(r.GetMediaId())] == entity.TechCardMediaRoleMood {
			continue
		}
		out = append(out, r)
	}
	return out
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

// designFlatIsDetailRun — a FLAT asked for details only: every view is `detail` (one per named
// detail) and the details are named by slot id. The one predicate the detail-reference filter keys on.
func designFlatIsDetailRun(kind string, params *pb_common.DesignRunParams) bool {
	if kind != entity.DesignRunKindFlat || len(params.GetDetailSlotIds()) == 0 || len(params.GetViews()) == 0 {
		return false
	}
	for _, v := range params.GetViews() {
		if v != entity.DesignViewDetail {
			return false
		}
	}
	return true
}

// designFlatDetailOnlyItsRefs — A DETAIL RUN GETS ONLY THE PICTURES OF THAT DETAIL (owner 06.10, T74:
// «если мы генерим деталь то в промпт не обязательно сыпать все картинки а только те что относятся к
// детали»). A reference travels only when its role is `detail` AND its design_reference row ties it
// (detail_slot_id, 0360) to one of the requested slots. Front/back/side photos, other details'
// pictures, a `detail` reference tied to no slot, mood and extras are dropped. No match → no refs:
// the accepted FRONT/BACK bench flats still travel as slots and give the silhouette. Frozen into the
// snapshot, so the run's history shows exactly what was sent. Other runs keep their refs.
func designFlatDetailOnlyItsRefs(src designInputSources, refs []*pb_common.DesignInputRef) []*pb_common.DesignInputRef {
	if !designFlatIsDetailRun(src.Kind, src.Params) {
		return refs
	}
	asked := make(map[int32]struct{}, len(src.Params.GetDetailSlotIds()))
	for _, id := range src.Params.GetDetailSlotIds() {
		asked[id] = struct{}{}
	}
	tied := make(map[int32]struct{}, len(src.Refs))
	for _, r := range src.Refs {
		if r.Role != entity.DesignViewDetail || !r.DetailSlotId.Valid {
			continue
		}
		if _, ok := asked[r.DetailSlotId.Int32]; ok {
			tied[int32(r.MediaId)] = struct{}{}
		}
	}
	out := make([]*pb_common.DesignInputRef, 0, len(tied))
	for _, r := range refs {
		if strings.TrimSpace(r.GetRole()) != entity.DesignViewDetail {
			continue
		}
		if _, ok := tied[r.GetMediaId()]; !ok {
			continue
		}
		out = append(out, r)
	}
	return out
}

// designFlatInFlightRefusal — M8 (07.10): the store refused a second flat run of a card while one is
// pending or running (entity.DesignFlatRunInFlightError, checked inside StartRun's transaction before
// the reservation). FailedPrecondition `flat_run_in_flight` with the holding run; nil for any other error.
func designFlatInFlightRefusal(err error) error {
	var live *entity.DesignFlatRunInFlightError
	if !errors.As(err, &live) {
		return nil
	}
	return designRefusal(codes.FailedPrecondition, "flat_run_in_flight",
		fmt.Sprintf("flat run %d of this card is still drawing; wait for it to finish (or cancel it), then generate "+
			"again. Nothing was reserved and nothing was charged", live.RunID),
		map[string]string{"run_id": strconv.Itoa(live.RunID), "status": live.Status})
}
