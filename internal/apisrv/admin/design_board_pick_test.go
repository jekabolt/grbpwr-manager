package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// boardCard — a moodboard with three front photos (newest 403), a back, an unsure target, a mood, a
// material, an unmarked picture with a model's front label, a detail photo and a pending target.
func boardCard() *entity.TechCard {
	card := &entity.TechCard{}
	card.Name = "board"
	add := func(id int, role entity.TechCardMediaRole) {
		card.Media = append(card.Media, entity.TechCardMediaItem{MediaId: id, Category: entity.TechCardMediaCategoryMoodboard,
			Kind: entity.TechCardMediaMoodboard, Role: role})
	}
	add(401, entity.TechCardMediaRoleTarget)
	add(402, entity.TechCardMediaRoleTarget)
	add(403, entity.TechCardMediaRoleTarget)
	add(410, entity.TechCardMediaRoleTarget)
	add(420, entity.TechCardMediaRoleTarget) // unsure
	add(430, entity.TechCardMediaRoleMood)
	add(440, entity.TechCardMediaRoleMaterial)
	add(450, entity.TechCardMediaRoleNone) // a model's front: home until the person says target
	add(460, entity.TechCardMediaRoleDetail)
	add(470, entity.TechCardMediaRoleTarget) // pending
	return card
}

func boardRefs() []entity.DesignReference {
	at := sql.NullTime{Time: time.Now(), Valid: true}
	r := func(media int, role, src, state string, ord int) entity.DesignReference {
		return entity.DesignReference{TechCardId: designRunCardID, MediaId: media, Role: role, LabelSource: src,
			LabelState: state, Ordinal: ord, LabelledAt: at}
	}
	detail := r(460, entity.DesignViewDetail, entity.DesignLabelSourceModelStrong, entity.DesignLabelStateOk, 9)
	detail.DetailSlotId = sql.NullInt32{Int32: 17, Valid: true}
	unsure := r(420, "", entity.DesignLabelSourceModelStrong, entity.DesignLabelStateUnsure, 5)
	unsure.ModelCaption = sql.NullString{String: "three-quarter, both sides visible", Valid: true}
	return []entity.DesignReference{
		r(401, entity.DesignViewFront, "", "", 1), // oldest front — the third, stays home
		r(402, entity.DesignViewFront, entity.DesignLabelSourceHuman, entity.DesignLabelStateOk, 2),
		r(403, entity.DesignViewFront, entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk, 3),
		r(410, entity.DesignViewBack, entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk, 4),
		unsure,
		r(430, entity.DesignViewBack, entity.DesignLabelSourceHuman, entity.DesignLabelStateOk, 6), // a person's back on a mood picture
		r(450, entity.DesignViewFront, entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk, 7),
		detail,
		r(470, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStatePending, 10),
	}
}

func boardSnapIDs(in *pb_common.DesignInputSnapshot) []int32 {
	var out []int32
	for _, r := range in.GetRefs() {
		out = append(out, r.GetMediaId())
	}
	return out
}

func TestFlatViewsRunTakesTheTwoNewestOfEachViewFromTheBoard(t *testing.T) {
	src := designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: boardRefs()},
		&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	// front: 403, 402 (newest two; 401 stays home); back: 410. Not: the unsure 420, the mood 430 (its
	// person's label is on a mood picture — dropped by the roled-photo filter), the model's front on the
	// unmarked 450, the detail 460 (a detail run's), the pending 470.
	require.Equal(t, []int32{403, 402, 410}, boardSnapIDs(snap))
}

func TestFlatDetailRunTakesTheNewestFourOfItsDetail(t *testing.T) {
	card := &entity.TechCard{}
	var refs []entity.DesignReference
	for i := 1; i <= 6; i++ {
		id := 500 + i
		card.Media = append(card.Media, entity.TechCardMediaItem{MediaId: id, Category: entity.TechCardMediaCategoryMoodboard,
			Kind: entity.TechCardMediaMoodboard, Role: entity.TechCardMediaRoleDetail})
		refs = append(refs, entity.DesignReference{MediaId: id, Role: entity.DesignViewDetail, Ordinal: i,
			DetailSlotId: sql.NullInt32{Int32: 17, Valid: true}, LabelSource: entity.DesignLabelSourceModelStrong})
	}
	src := designRunSources(entity.DesignRunKindFlat, card, &entity.DesignBand{References: refs},
		&pb_common.DesignRunParams{Views: []string{"detail"}, Layout: designLayoutOne, DetailSlotIds: []int32{17}})
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	require.Equal(t, []int32{503, 504, 505, 506}, boardSnapIDs(snap), "the newest four, in the person's order")
}

func TestPreviewHeldSaysWhyEachBoardPictureStaysHome(t *testing.T) {
	src := designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: boardRefs()},
		&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	got := map[int32]string{}
	for _, h := range designPreviewHeld(src, snap, boardRefs()) {
		got[h.GetMediaId()] = h.GetReason()
		if h.GetMediaId() == 420 {
			require.Equal(t, "three-quarter, both sides visible", h.GetModelCaption())
		}
	}
	require.Equal(t, map[int32]string{
		401: designHeldOlder, 420: designHeldViewUnknown, 430: designHeldMood, 440: designHeldMaterial,
		450: designHeldUnmarked, 460: designHeldDetail, 470: designHeldPending,
	}, got)
}

// THE INVARIANT (101 Ф2): «what the model gets» == the snapshot the next run freezes. Both go through
// the real handlers on the same card and band.
func TestPreviewEqualsTheSnapshotOfTheNextRun(t *testing.T) {
	band := &entity.DesignBand{References: boardRefs(), Bench: designBandWith(false).Bench}
	rig := newDesignRunRig(t, boardCard(), band)
	params := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}

	prev, err := rig.srv.PreviewDesignRunInputs(designRunCtx(), &pb_admin.PreviewDesignRunInputsRequest{
		TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat, Params: proto.Clone(params).(*pb_common.DesignRunParams),
	})
	require.NoError(t, err)

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = proto.Clone(params).(*pb_common.DesignRunParams)
	_, err = rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	sent := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, sent))

	require.Equal(t, []int32{403, 402, 410}, boardSnapIDs(prev.GetInputs()))
	require.Equal(t, boardSnapIDs(sent), boardSnapIDs(prev.GetInputs()), "the preview's refs are the run's refs, in order")
	require.Equal(t, len(sent.GetSlots()), len(prev.GetInputs().GetSlots()))
	for i := range sent.GetSlots() {
		require.Equal(t, sent.GetSlots()[i].GetMediaId(), prev.GetInputs().GetSlots()[i].GetMediaId())
	}
	require.Equal(t, sent.GetGarmentNote(), prev.GetInputs().GetGarmentNote())
	require.NotEmpty(t, prev.GetHeld())
}
