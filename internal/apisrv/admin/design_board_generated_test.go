package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// M16 (owner 07.10): «в инпут на флеты не должны подсовываться фабрик рендеры». A design run's output
// never feeds a flat — except a cutout (M17: remove bg is the garment photo itself).

// EVERY KIND BUT CUTOUT IS GENERATED. A new run kind lands on the generated side by default: the
// exception is spelled out, not the rule.
func TestOnlyACutoutOutputMayFeedAFlat(t *testing.T) {
	for _, k := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
		entity.DesignRunKindDraftIdea, entity.DesignRunKindRecolor, entity.DesignRunKindPattern,
		entity.DesignRunKindFreeform, entity.DesignRunKindExtend, entity.DesignRunKindInpaint,
		entity.DesignRunKindVideo, "a_kind_born_tomorrow",
	} {
		require.False(t, designOutputFeedsAFlat(k), k)
	}
	require.True(t, designOutputFeedsAFlat(entity.DesignRunKindCutout))
}

// THE LINEAGE OF A CUTOUT (Codex M16 #3): a cutout goes when it was cut from a garment photo, and is a
// render when it was cut from one — directly, through a crop of the render (the crop carries the
// render's run), or through a cutout of a cutout.
func TestACutoutIsAsGeneratedAsWhatItWasCutFrom(t *testing.T) {
	cut := func(src ...int) entity.DesignMediaProducer {
		return entity.DesignMediaProducer{RunKind: entity.DesignRunKindCutout, Sources: src}
	}
	run := func(kind string) entity.DesignMediaProducer { return entity.DesignMediaProducer{RunKind: kind} }
	store := map[int][]entity.DesignMediaProducer{
		1:  {run(entity.DesignRunKindRender)},             // a render
		2:  {cut(100)},                                    // a cutout of photo 100 (no run)
		3:  {cut(1)},                                      // a cutout of the render
		4:  {cut(3)},                                      // a cutout of that cutout
		5:  {cut(6)},                                      // a cutout of a crop of a 3D still
		6:  {run(entity.DesignRunKindThreed)},             // the crop: it carries the 3D run
		7:  {cut()},                                       // a cutout whose source is not recorded
		8:  {cut(100), run(entity.DesignRunKindFreeform)}, // two producers, one generated
		9:  {cut(10)},                                     // a cycle: judged by what is known
		10: {cut(9)},
	}
	calls := 0
	lookup := func(_ context.Context, ids []int) (map[int][]entity.DesignMediaProducer, error) {
		calls++
		out := map[int][]entity.DesignMediaProducer{}
		for _, id := range ids {
			if p, ok := store[id]; ok {
				out[id] = p
			}
		}
		return out, nil
	}
	got, err := designResolveGenerated(context.Background(), []int{1, 2, 3, 4, 5, 7, 8, 9, 100, 3}, lookup)
	require.NoError(t, err)
	require.Equal(t, map[int]string{
		1: entity.DesignRunKindRender,
		3: entity.DesignRunKindRender,
		4: entity.DesignRunKindRender,
		5: entity.DesignRunKindThreed,
		8: entity.DesignRunKindFreeform,
	}, got)
	require.LessOrEqual(t, calls, designLineageReads, "one read per level")

	none, err := designResolveGenerated(context.Background(), []int{2, 100}, lookup)
	require.NoError(t, err)
	require.Nil(t, none)

	// A LONG LINEAGE (Codex M16 r2): a chain of cutouts longer than any fixed depth still ends at its
	// render; a chain ending at a photo still goes; a chain longer than the read cap is held.
	chain := func(base, n int, end []entity.DesignMediaProducer) {
		for i := 0; i < n; i++ {
			store[base+i] = []entity.DesignMediaProducer{cut(base + i + 1)}
		}
		if end != nil {
			store[base+n] = end
		}
	}
	chain(1000, 8, []entity.DesignMediaProducer{run(entity.DesignRunKindRender)}) // 1000 … 1007 → render 1008
	chain(2000, 8, nil)                                                           // 2000 … 2007 → photo 2008
	chain(3000, designLineageReads+2, nil)                                        // beyond the cap
	got, err = designResolveGenerated(context.Background(), []int{1000, 2000, 3000}, lookup)
	require.NoError(t, err)
	require.Equal(t, map[int]string{1000: entity.DesignRunKindRender, 3000: entity.DesignRunKindCutout}, got)
}

// A VIEWS RUN: the render is held BEFORE «the two newest of its view» is counted — it must not push a
// garment photo out. 403 (the newest front) is a render, 410 (the only back) a 3D still.
func TestAFlatViewsRunNeverTakesAGeneratedPicture(t *testing.T) {
	withBoardSource(t)
	src := designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: boardRefs()},
		&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	src.Generated = map[int]string{403: entity.DesignRunKindRender, 410: entity.DesignRunKindThreed}
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	require.Equal(t, []int32{402, 401}, boardSnapIDs(snap), "the two newest garment photos of the front; no back")

	got := map[int32]string{}
	for _, h := range designPreviewHeld(src, snap, boardRefs()) {
		got[h.GetMediaId()] = h.GetReason()
	}
	require.Equal(t, designHeldRender, got[403])
	require.Equal(t, designHeldRender, got[410])
	require.NotContains(t, got, int32(401), "401 is sent now")

	// Mutation control: without the rule the same board sends the render and the 3D still.
	src.Generated = nil
	snap, err = designAssembleInputs(src)
	require.NoError(t, err)
	require.Equal(t, []int32{403, 402, 410}, boardSnapIDs(snap))
}

// A DETAIL RUN: a generated detail picture is held too; the newest four of the rest go.
func TestAFlatDetailRunNeverTakesAGeneratedPicture(t *testing.T) {
	withBoardSource(t)
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
	src.Generated = map[int]string{506: entity.DesignRunKindFreeform}
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	require.Equal(t, []int32{502, 503, 504, 505}, boardSnapIDs(snap))
	held := designPreviewHeld(src, snap, refs)
	require.Len(t, held, 2)
	require.Equal(t, int32(501), held[0].GetMediaId())
	require.Equal(t, int32(506), held[1].GetMediaId())
	require.Equal(t, designHeldRender, held[1].GetReason())
}

// «FROM MY FLAT»: the card's own technical flats go as today, whatever made them.
func TestAFlatKeepsTheDesignersOwnFlatsEvenIfGenerated(t *testing.T) {
	src := designInputSources{
		Kind: entity.DesignRunKindFlat,
		Params: &pb_common.DesignRunParams{Flat: &pb_common.DesignFlatParams{Mode: "hand_flat",
			StructureRefs: []*pb_common.DesignFlatStructureRef{{MediaId: 77, Role: "front_flat"}}}},
		Generated: map[int]string{77: entity.DesignRunKindFlat, 78: entity.DesignRunKindRender},
	}
	refs := []*pb_common.DesignInputRef{{MediaId: 77, Role: "front_flat"}, {MediaId: 78, Role: "front"}, {MediaId: 79, Role: "back"}}
	require.Equal(t, []int32{77, 79}, boardSnapIDs(&pb_common.DesignInputSnapshot{Refs: designFlatNoGenerated(src, refs)}))
	// Other kinds keep their refs.
	src.Kind = entity.DesignRunKindRender
	require.Len(t, designFlatNoGenerated(src, refs), 3)
}

// THE INVARIANT THROUGH THE REAL HANDLERS: the store says 403 is a render, 410 a 3D still and 402 a
// cutout; the preview and the run send the same [402, 401], the preview holds 403 and 410 as «render».
func TestPreviewAndRunHoldTheSameGeneratedPictures(t *testing.T) {
	withBoardSource(t)
	band := &entity.DesignBand{References: boardRefs(), Bench: designBandWith(false).Bench}
	rig := newDesignRunRig(t, boardCard(), band)
	rig.design.ExpectedCalls = pgDropCalls(rig.design.ExpectedCalls, "MediaProducers")
	rig.design.EXPECT().MediaProducers(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, ids []int) (map[int][]entity.DesignMediaProducer, error) {
			all := map[int][]entity.DesignMediaProducer{
				403: {{RunKind: entity.DesignRunKindRender}},
				410: {{RunKind: entity.DesignRunKindThreed}},
				402: {{RunKind: entity.DesignRunKindCutout, Sources: []int{9402}}}, // cut from a photo
			}
			out := map[int][]entity.DesignMediaProducer{}
			for _, id := range ids {
				if p, ok := all[id]; ok {
					out[id] = p
				}
			}
			return out, nil
		})
	params := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}

	prev, err := rig.srv.PreviewDesignRunInputs(designRunCtx(), &pb_admin.PreviewDesignRunInputsRequest{
		TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat, Params: proto.Clone(params).(*pb_common.DesignRunParams),
	})
	require.NoError(t, err)
	require.Equal(t, []int32{402, 401}, boardSnapIDs(prev.GetInputs()), "the cutout 402 goes, the render does not")
	held := map[int32]string{}
	for _, h := range prev.GetHeld() {
		held[h.GetMediaId()] = h.GetReason()
	}
	require.Equal(t, designHeldRender, held[403])
	require.Equal(t, designHeldRender, held[410])

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = proto.Clone(params).(*pb_common.DesignRunParams)
	_, err = rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	sent := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, sent))
	require.Equal(t, boardSnapIDs(prev.GetInputs()), boardSnapIDs(sent), "the run sends what the preview shows")
}

// A RERUN of a flat booked before the rule does not carry its render on.
func TestAFlatRerunDropsItsParentsGeneratedPictures(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.ExpectedCalls = pgDropCalls(rig.design.ExpectedCalls, "MediaProducers")
	rig.design.EXPECT().MediaProducers(mock.Anything, []int{700, 701}).
		Return(map[int][]entity.DesignMediaProducer{700: {{RunKind: entity.DesignRunKindRender}}}, nil).Once()
	parentInputs, err := designMarshalJSON(&pb_common.DesignInputSnapshot{
		Refs: []*pb_common.DesignInputRef{{MediaId: 700, Role: entity.DesignViewFront}, {MediaId: 701, Role: entity.DesignViewBack}},
	})
	require.NoError(t, err)
	parent := &entity.DesignRun{Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat, Inputs: entity.RawJSON(parentInputs)}
	snap, _, err := rig.srv.designRunInputs(context.Background(), designInputSources{
		Kind: entity.DesignRunKindFlat, Card: designMoodCard(),
		Params: &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne},
	}, parent)
	require.NoError(t, err)
	require.Equal(t, []int32{701}, boardSnapIDs(snap))

	// Every picture generated, no plate: refused before any money.
	rig.design.EXPECT().MediaProducers(mock.Anything, []int{700, 701}).
		Return(map[int][]entity.DesignMediaProducer{
			700: {{RunKind: entity.DesignRunKindRender}}, 701: {{RunKind: entity.DesignRunKindPattern}},
		}, nil).Once()
	_, _, err = rig.srv.designRunInputs(context.Background(), designInputSources{
		Kind: entity.DesignRunKindFlat, Card: designMoodCard(),
		Params: &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne},
	}, parent)
	require.ErrorContains(t, err, "generated")
}

// THE LABELLER spends nothing on an output and proposes nothing for it: a new one is settled `output`
// for free, a model's old view on one is re-settled `output`, a person's label is never touched, and
// an `output` row whose picture is no output any more is read again.
func TestTheLabellerSettlesOutputsWithoutAModel(t *testing.T) {
	at := sql.NullTime{Time: time.Now(), Valid: true}
	ref := func(media int, role, source, state string) entity.DesignReference {
		return entity.DesignReference{MediaId: media, Role: role, LabelSource: source, LabelState: state, LabelledAt: at}
	}
	pic := func(id int, p entity.TechCardMediaRole) designBoardPicture {
		return designBoardPicture{MediaID: id, Purpose: p}
	}
	board := []designBoardPicture{
		pic(1, entity.TechCardMediaRoleTarget), // output, no row
		pic(2, entity.TechCardMediaRoleNone),   // output, no row, unmarked: no purpose proposed
		pic(3, entity.TechCardMediaRoleTarget), // output, a model's «front» from before the rule
		pic(4, entity.TechCardMediaRoleTarget), // output, already settled
		pic(5, entity.TechCardMediaRoleTarget), // output, a person's «front»
		pic(6, entity.TechCardMediaRoleTarget), // `output` row, no longer an output
		pic(7, entity.TechCardMediaRoleMood),   // output on a mood picture with an `output` row: dropped
		pic(8, entity.TechCardMediaRoleDetail), // output detail photo, no row
		pic(9, entity.TechCardMediaRoleTarget), // a garment photo: read by the model as always
	}
	refs := []entity.DesignReference{
		ref(3, entity.DesignViewFront, entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),
		ref(4, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOutput),
		ref(5, entity.DesignViewFront, entity.DesignLabelSourceHuman, entity.DesignLabelStateOk),
		ref(6, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOutput),
		ref(7, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOutput),
	}
	generated := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 7: true, 8: true}
	tasks, drop := designBoardLabelPlan(board, refs, time.Now().Add(-time.Minute), true, generated)
	got := map[int]designBoardLabelTask{}
	for _, tk := range tasks {
		got[tk.MediaID] = tk
	}
	require.Len(t, got, 6)
	for _, id := range []int{1, 2, 8} {
		require.True(t, got[id].Output, id)
		require.False(t, got[id].Relabel, id)
	}
	require.True(t, got[3].Output && got[3].Relabel)
	require.NotContains(t, got, 4)
	require.NotContains(t, got, 5)
	require.True(t, got[6].Relabel && !got[6].Output, "read again by the model")
	require.False(t, got[9].Output)
	require.Equal(t, []int{7}, drop)

	// Mutation control: without the set every one of them is read by a model.
	tasks, _ = designBoardLabelPlan(board, refs[:1], time.Now().Add(-time.Minute), true, nil)
	for _, tk := range tasks {
		require.False(t, tk.Output)
	}
}

// An `output` row never travels and never asks the person: its role is empty and its state is not ok.
func TestAnOutputLabelNeverTravels(t *testing.T) {
	require.False(t, entity.DesignReferenceTravels(entity.DesignReference{Role: entity.DesignViewFront, LabelState: entity.DesignLabelStateOutput}))
}
