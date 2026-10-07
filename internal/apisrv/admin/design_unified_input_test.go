package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ═══ M15 · ONE INPUT (109-UNIFIED-INPUT) — the server proposes, the person decides ═══════════════

// countingSlots — a slot provider that says how often it was read.
func countingSlots(slots ...designBoardDetailSlot) (designBoardSlotsFn, *int) {
	n := 0
	return func() ([]designBoardDetailSlot, error) {
		n++
		return slots, nil
	}, &n
}

// 109 §2.1: a picture dropped with NO purpose that the cheap model is sure is a detail is read as a
// detail in the same task — one strong call, the slot and the proposal in one answer.
func TestUnmarkedDetailIsReadAsADetailInTheSameTask(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"purpose":"detail","view":"unclear","confidence":0.86}`}
	ai.answers[entity.AIPurposeBoardRead] = []string{`{"slot":"new","name":"cuff vent","caption":"a vented cuff","confidence":0.8}`}
	slots, reads := countingSlots(designBoardDetailSlot{ID: 17, Name: "collar"})
	got, err := designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 815}, "u", slots)
	require.NoError(t, err)
	require.Equal(t, []string{entity.AIPurposeBoardLabel, entity.AIPurposeBoardRead}, ai.calls, "one cheap call, one detail read")
	require.Equal(t, 1, *reads, "the slots are read once, when the detail read needs them")
	require.Equal(t, "detail", got.ProposedPurpose, "the proposal rides with the slot")
	require.Equal(t, entity.DesignViewDetail, got.Role)
	require.Equal(t, "cuff vent", got.NewDetailName)
	require.Equal(t, entity.DesignLabelStateOk, got.State)
	require.Equal(t, entity.DesignLabelSourceModelStrong, got.Source)
}

// Not sure it is a detail: no strong call, no slot read — settled with the proposal, as before.
func TestUnsureDetailWaitsForThePurpose(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"purpose":"detail","view":"unclear","confidence":0.5}`}
	slots, reads := countingSlots()
	got, err := designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 816}, "u", slots)
	require.NoError(t, err)
	require.Equal(t, []string{entity.AIPurposeBoardLabel}, ai.calls)
	require.Zero(t, *reads)
	require.Equal(t, "detail", got.ProposedPurpose)
	require.Equal(t, "", got.Role)
	require.Equal(t, entity.DesignLabelStateOk, got.State)

	// A target never reads the slots.
	ai = newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"purpose":"target","view":"front","confidence":0.95}`}
	slots, reads = countingSlots()
	_, err = designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 817}, "u", slots)
	require.NoError(t, err)
	require.Zero(t, *reads, "the slots are read lazily")

	// A slot read that fails leaves the row pending (retried), it never settles a detail without slots.
	ai = newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"purpose":"detail","view":"unclear","confidence":0.9}`}
	_, err = designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 818}, "u",
		func() ([]designBoardDetailSlot, error) { return nil, errors.New("db down") })
	require.Error(t, err)
}

// 109 §3: two new photos of one new part in one sync → one slot. The second read sees the slot the
// first minted (the provider is read per picture) and joins it.
func TestTwoPhotosOfOneNewPartJoinOneSlot(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{
		`{"purpose":"detail","view":"unclear","confidence":0.9}`,
		`{"purpose":"detail","view":"unclear","confidence":0.9}`,
	}
	ai.answers[entity.AIPurposeBoardRead] = []string{
		`{"slot":"new","name":"cuff vent","confidence":0.8}`,
		`{"slot":140,"name":"cuff vent","confidence":0.8}`,
	}
	var minted []designBoardDetailSlot
	slots := func() ([]designBoardDetailSlot, error) { return minted, nil }
	first, err := designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 1}, "u", slots)
	require.NoError(t, err)
	require.Equal(t, 0, first.DetailSlotId)
	require.Equal(t, "cuff vent", first.NewDetailName)
	minted = append(minted, designBoardDetailSlot{ID: 140, Name: first.NewDetailName}) // the store minted it
	second, err := designBoardLabelLadder(context.Background(), ai, designBoardPicture{MediaID: 2}, "u", slots)
	require.NoError(t, err)
	require.Equal(t, 140, second.DetailSlotId, "the second photo joins the slot the first minted")
}

// 109 §3: a name outside the shared vocabulary is dropped — the photo waits for a person.
func TestDetailNamesStayInTheVocabulary(t *testing.T) {
	for name, ok := range map[string]bool{
		"cuff vent": true, "back vent": true, "shoulder straps": true, "left cuff": true, "patch pocket": true,
		"layered neckline": true, "collar": true, "racerback thing": false, "pretty bit": false, "": false,
	} {
		require.Equal(t, ok, designDetailNameOK(name), name)
	}
	det := designBoardPicture{MediaID: 460, Purpose: entity.TechCardMediaRoleDetail}
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardRead] = []string{`{"slot":"new","name":"pretty bit","confidence":0.9}`}
	got, err := designBoardLabelLadder(context.Background(), ai, det, "u", nil)
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateUnsure, got.State)
	require.Equal(t, "", got.Role, "no slot is minted for a free word")

	ai = newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardRead] = []string{`{"slot":17,"name":"pretty bit","confidence":0.9}`}
	got, err = designBoardLabelLadder(context.Background(), ai, det, "u", designSlotsOf([]designBoardDetailSlot{{ID: 17, Name: "collar"}}))
	require.NoError(t, err)
	require.Equal(t, 17, got.DetailSlotId, "a known slot is joined by its id")
	require.Equal(t, "", got.NewDetailName, "… with no out-of-vocabulary fallback name")
}

func TestDetailReadTellsTwoPartsOfOneKindApart(t *testing.T) {
	require.Contains(t, designBoardReadDetailSystemPrompt,
		`A different part of the same kind (the other cuff, a second pocket) is "new".`)
}

// 109 §4: a held picture stays home with the reason `held`; a render is still a render first (M16).
func TestPreviewHeldSaysHeld(t *testing.T) {
	withBoardSource(t)
	refs := boardRefs()
	for i := range refs {
		switch refs[i].MediaId {
		case 403, 460:
			refs[i].LabelState = entity.DesignLabelStateHeld
		}
	}
	src := designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: refs},
		&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	snap, err := designAssembleInputs(src)
	require.NoError(t, err)
	require.Equal(t, []int32{402, 401, 410}, boardSnapIDs(snap), "the held front leaves; the older one comes back")
	got := map[int32]string{}
	for _, h := range designPreviewHeld(src, snap, refs) {
		got[h.GetMediaId()] = h.GetReason()
	}
	require.Equal(t, designHeldHeld, got[403])
	require.Equal(t, designHeldHeld, got[460])
	_, sent := got[401]
	require.False(t, sent, "401 rides now")

	// A detail run does not take a held photo of its detail.
	src = designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: refs},
		&pb_common.DesignRunParams{Views: []string{"detail"}, Layout: designLayoutOne, DetailSlotIds: []int32{17}})
	snap, err = designAssembleInputs(src)
	require.NoError(t, err)
	require.Empty(t, boardSnapIDs(snap))

	// M16 first: a held render says render.
	src = designRunSources(entity.DesignRunKindFlat, boardCard(), &entity.DesignBand{References: refs},
		&pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne})
	src.Generated = map[int]string{403: entity.DesignRunKindRender}
	snap, err = designAssembleInputs(src)
	require.NoError(t, err)
	for _, h := range designPreviewHeld(src, snap, refs) {
		if h.GetMediaId() == 403 {
			require.Equal(t, designHeldRender, h.GetReason())
		}
	}
}
