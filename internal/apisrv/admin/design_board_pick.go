package admin

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// designBoardIsTheSource — 101 §2.8 / Ф4: a run reads ONLY labels on pictures lying on the moodboard
// with the purpose `target` (a view) or `detail`. Until the REFERENCE rows are moved onto the board
// (Ф4), a person's label on a picture outside the board still travels, as before.
var designBoardIsTheSource = false

// designRunRefs — the design_reference rows a run (and the join list) may read:
//   - a settled label only (entity.DesignReferenceTravels: a role, state ok — pending / unsure / failed
//     rows and a person's «no view» never ride);
//   - a MODEL's label only while its picture lies on the board with the purpose the label belongs to —
//     a view on a `target`, `detail` on a `detail`: a label on a picture the person has since called
//     mood, or never confirmed as the garment, is the model's guess and stays home;
//   - with designBoardIsTheSource, a person's label the same way.
func designRunRefs(card *entity.TechCard, refs []entity.DesignReference) []entity.DesignReference {
	purposeOf := map[int]entity.TechCardMediaRole{}
	for _, p := range designBoardPictures(card) {
		purposeOf[p.MediaID] = p.Purpose
	}
	out := make([]entity.DesignReference, 0, len(refs))
	for _, r := range refs {
		if !entity.DesignReferenceTravels(r) {
			continue
		}
		if designBoardIsTheSource || entity.IsDesignLabelByModel(r.LabelSource) {
			purpose, onBoard := purposeOf[r.MediaId]
			if !onBoard || !designLabelFitsPurpose(r.Role, purpose) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// designLabelFitsPurpose — a view rides on a `target` picture, `detail` on a `detail` picture.
func designLabelFitsPurpose(role string, purpose entity.TechCardMediaRole) bool {
	if role == entity.DesignViewDetail {
		return purpose == entity.TechCardMediaRoleDetail
	}
	return purpose == entity.TechCardMediaRoleTarget
}

const (
	// designFlatPerView — photos of one view a flat views run takes: the newest two (owner 06.10, R17).
	designFlatPerView = 2
	// designFlatPerDetail — photos of one detail a detail run takes: the newest four (101 Q4).
	designFlatPerDetail = 4
)

// designFlatViewRank — the prompt order of the views a views run reads; anything else is not a view.
var designFlatViewRank = map[string]int{
	entity.DesignViewFront: 0, entity.DesignViewBack: 1, entity.DesignViewSideL: 2, entity.DesignViewSideR: 3,
	entity.DesignViewSide: 4, entity.DesignViewThreeQuarterL: 5, entity.DesignViewThreeQuarterR: 6,
}

// designFlatPickFromBoard — 101 §2.8: the photos a flat run takes, after the roled-photo and the
// detail-run filters (so its input is already the run's candidates):
//   - a VIEWS run takes view photos only (a detail photo is a detail run's), grouped by view in the
//     order front, back, side L, side R, side, then the legacy three-quarters; in each view the newest
//     designFlatPerView (newest = the larger media id = the later upload);
//   - a DETAIL run takes, per asked detail, its newest designFlatPerDetail photos (in the order the
//     person gave them), the details in the asked order.
//
// Other kinds keep their refs. Frozen into the snapshot, so «what the model gets» (the preview) and the
// run's history both show exactly this.
func designFlatPickFromBoard(src designInputSources, refs []*pb_common.DesignInputRef) []*pb_common.DesignInputRef {
	// A FIX keeps what it had (Codex Ф2 #3): it corrects named plates and may carry the very detail
	// photo of the plate it fixes, which the views branch below would drop.
	if src.Kind != entity.DesignRunKindFlat || designFlatIsFix(src.Params) {
		return refs
	}
	type item struct {
		ref   *pb_common.DesignInputRef
		group int
		at    int
	}
	var items []item
	if designFlatIsDetailRun(src.Kind, src.Params) {
		order := map[int32]int{}
		for i, id := range src.Params.GetDetailSlotIds() {
			if _, dup := order[id]; !dup {
				order[id] = i
			}
		}
		slotOf := map[int32]int32{}
		for _, r := range src.Refs {
			if r.Role == entity.DesignViewDetail && r.DetailSlotId.Valid {
				slotOf[int32(r.MediaId)] = r.DetailSlotId.Int32
			}
		}
		for i, r := range refs {
			g, ok := order[slotOf[r.GetMediaId()]]
			if !ok {
				continue
			}
			items = append(items, item{r, g, i})
		}
	} else {
		for i, r := range refs {
			g, ok := designFlatViewRank[strings.TrimSpace(r.GetRole())]
			if !ok {
				continue
			}
			items = append(items, item{r, g, i})
		}
	}
	per := designFlatPerView
	if designFlatIsDetailRun(src.Kind, src.Params) {
		per = designFlatPerDetail
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].group != items[j].group {
			return items[i].group < items[j].group
		}
		if items[i].ref.GetMediaId() != items[j].ref.GetMediaId() {
			return items[i].ref.GetMediaId() > items[j].ref.GetMediaId()
		}
		return items[i].at < items[j].at
	})
	kept := make([]item, 0, len(items))
	taken := map[int]int{}
	for _, it := range items {
		if taken[it.group] >= per {
			continue
		}
		taken[it.group]++
		kept = append(kept, it)
	}
	if designFlatIsDetailRun(src.Kind, src.Params) {
		// A detail's photos keep the order the person gave them (ordinal); only WHICH four is by age.
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].group != kept[j].group {
				return kept[i].group < kept[j].group
			}
			return kept[i].at < kept[j].at
		})
	}
	out := make([]*pb_common.DesignInputRef, 0, len(kept))
	for _, it := range kept {
		out = append(out, it.ref)
	}
	return out
}

// designRunSources — the sources a run of this kind assembles its snapshot from. The ONE builder of
// StartDesignRun and PreviewDesignRunInputs.
func designRunSources(kind string, card *entity.TechCard, band *entity.DesignBand, params *pb_common.DesignRunParams) designInputSources {
	return designInputSources{
		Kind:   kind,
		Card:   card,
		Refs:   designKeptReferences(kind, designRunRefs(card, band.References), band.Joins),
		Bench:  band.Bench,
		Params: params,
	}
}

// Why a board picture stays home (DesignInputHeld.reason).
const (
	designHeldMood        = "mood"
	designHeldMaterial    = "material"
	designHeldUnmarked    = "unmarked"
	designHeldPending     = "pending"
	designHeldViewUnknown = "view_unknown"
	designHeldNotAView    = "not_a_view"
	designHeldOlder       = "older"
	designHeldOtherDetail = "other_detail"
	designHeldDetail      = "detail"
)

// designPreviewHeld — the board pictures a FLAT run of these sources does not send, and why, in board
// order (101 §2.8: «not sent · mood 3 · view unknown 1 → answer below · older than the 2 newest 2»).
// Computed against the snapshot the same call assembled, so «sent» is exactly the snapshot.
func designPreviewHeld(src designInputSources, snap *pb_common.DesignInputSnapshot, all []entity.DesignReference) []*pb_admin.DesignInputHeld {
	if src.Kind != entity.DesignRunKindFlat {
		return nil
	}
	sent := map[int]bool{}
	for _, r := range snap.GetRefs() {
		sent[int(r.GetMediaId())] = true
	}
	rowOf := map[int]entity.DesignReference{}
	for _, r := range all {
		rowOf[r.MediaId] = r
	}
	travels := map[int]bool{}
	for _, r := range src.Refs {
		travels[r.MediaId] = true
	}
	detailRun := designFlatIsDetailRun(src.Kind, src.Params)
	var out []*pb_admin.DesignInputHeld
	for _, p := range designBoardPictures(src.Card) {
		if sent[p.MediaID] {
			continue
		}
		r, has := rowOf[p.MediaID]
		h := &pb_admin.DesignInputHeld{MediaId: int32(p.MediaID), Role: r.Role, ModelCaption: r.ModelCaption.String}
		state := entity.DesignLabelStateOrOk(r.LabelState)
		switch {
		case p.Purpose == entity.TechCardMediaRoleMood:
			h.Reason = designHeldMood
		case p.Purpose == entity.TechCardMediaRoleMaterial:
			h.Reason = designHeldMaterial
		case p.Purpose == entity.TechCardMediaRoleNone:
			h.Reason = designHeldUnmarked
		case !has || state == entity.DesignLabelStatePending:
			h.Reason = designHeldPending
		case state == entity.DesignLabelStateUnsure || state == entity.DesignLabelStateFailed:
			h.Reason = designHeldViewUnknown
		case r.Role == "":
			h.Reason = designHeldNotAView
		case !travels[p.MediaID] && !designLabelFitsPurpose(r.Role, p.Purpose):
			// a label of the other kind (a view on a detail picture, a detail on a target): the model's
			// is re-read by the next sync; a person's waits for the person
			h.Reason = designHeldViewUnknown
		case r.Role == entity.DesignViewDetail && !detailRun:
			h.Reason = designHeldDetail
		case r.Role != entity.DesignViewDetail && detailRun:
			continue // a view photo on a detail run: the run is about the detail, nothing to say
		case detailRun && !designDetailAsked(src.Params, r):
			h.Reason = designHeldOtherDetail
		default:
			h.Reason = designHeldOlder
		}
		out = append(out, h)
	}
	return out
}

func designDetailAsked(params *pb_common.DesignRunParams, r entity.DesignReference) bool {
	if !r.DetailSlotId.Valid {
		return false
	}
	for _, id := range params.GetDetailSlotIds() {
		if id == r.DetailSlotId.Int32 {
			return true
		}
	}
	return false
}

// PreviewDesignRunInputs — a DRY RUN of StartDesignRun's input assembly (101 §2.8): the same sources
// builder (designRunSources), the same snapshot function (designRunInputs → designAssembleInputs), no
// reservation, no money, no model. «what the model gets» draws this answer.
//
// It does not walk StartDesignRun's refusals (money, foreign slots, params shape): those answer «may
// this run start», the preview answers «what would it take». A params shape the snapshot itself
// refuses (too many references) comes back as the same InvalidArgument.
func (s *Server) PreviewDesignRunInputs(ctx context.Context, req *pb_admin.PreviewDesignRunInputsRequest) (*pb_admin.PreviewDesignRunInputsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	// FLAT ONLY (Codex Ф2 #4): a render freezes its artworks into the snapshot after this assembly
	// (designSpliceArtworks), so a render preview would be short of pictures the model receives. A flat's
	// only later splice is the frozen join list, which no flat prompt reads (construction is off).
	kind := strings.TrimSpace(req.GetKind())
	if kind != entity.DesignRunKindFlat {
		return nil, status.Errorf(codes.InvalidArgument, "the preview answers for a flat run; kind %q is not one", kind)
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		return nil, designError(ctx, "failed to read the tech card", err, nil)
	}
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band", err, nil)
	}
	params, err := designEffectiveParams(req.GetParams(), nil)
	if err != nil {
		return nil, err
	}
	if kind == entity.DesignRunKindFlat {
		designFlatDetailsOnly(params)
	}
	src := designRunSources(kind, card, band, params)
	snap, _, err := s.designRunInputs(ctx, src, nil)
	if err != nil {
		return nil, err
	}
	held := designPreviewHeld(src, snap, band.References)
	// The pictures resolved the way a history row resolves them — the modal draws thumbnails from them.
	run := &pb_common.DesignRun{Inputs: snap}
	s.joinDesignRunInputMedia(ctx, []*pb_common.DesignRun{run})
	return &pb_admin.PreviewDesignRunInputsResponse{Inputs: run.GetInputs(), Held: held}, nil
}
