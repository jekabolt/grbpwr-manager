package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// fakeBoardAI — a scripted router: one answer (or error) per purpose call, in order.
type fakeBoardAI struct {
	enabled map[string]bool
	answers map[string][]string
	errs    map[string]error
	calls   []string
}

func (f *fakeBoardAI) Enabled(purpose string) bool { return f.enabled[purpose] }

func (f *fakeBoardAI) Chat(_ context.Context, purpose string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	f.calls = append(f.calls, purpose)
	if err := f.errs[purpose]; err != nil {
		return nil, err
	}
	q := f.answers[purpose]
	if len(q) == 0 {
		return nil, errors.New("no scripted answer")
	}
	f.answers[purpose] = q[1:]
	return &aiprov.ChatResult{Text: q[0], Model: purpose + "/model"}, nil
}

func newFakeBoardAI() *fakeBoardAI {
	return &fakeBoardAI{
		enabled: map[string]bool{entity.AIPurposeBoardLabel: true, entity.AIPurposeBoardRead: true},
		answers: map[string][]string{}, errs: map[string]error{},
	}
}

var targetPic = designBoardPicture{MediaID: 124, Purpose: entity.TechCardMediaRoleTarget}

func TestBoardLabelLadderCheapSureTakesTheView(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"purpose":"target","view":"back","confidence":0.92}`}
	got, err := designBoardLabelLadder(context.Background(), ai, targetPic, "https://cdn.test/124.jpg", nil)
	require.NoError(t, err)
	require.Equal(t, entity.DesignViewBack, got.Role)
	require.Equal(t, entity.DesignLabelStateOk, got.State)
	require.Equal(t, entity.DesignLabelSourceModelCheap, got.Source)
	require.Equal(t, "", got.ProposedPurpose, "a purpose the form states is not proposed")
	require.Equal(t, []string{entity.AIPurposeBoardLabel}, ai.calls, "a sure cheap answer never pays the strong model")
}

func TestBoardLabelLadderSideFlankIsComputed(t *testing.T) {
	for raw, want := range map[string]string{
		`{"view":"side","faces":"left","confidence":0.9}`:   entity.DesignViewSideL, // front → picture's left = wearer's left flank
		`{"view":"side","faces":"right","confidence":0.9}`:  entity.DesignViewSideR,
		`{"view":"side","confidence":0.9}`:                  entity.DesignViewSide, // no direction → `side`, no question (101 Q2)
		`{"view":"side_r","confidence":0.9}`:                entity.DesignViewSide, // the model's own flank word is not trusted
		`{"view":"side_r","faces":"left","confidence":0.9}`: entity.DesignViewSideL,
	} {
		ai := newFakeBoardAI()
		ai.answers[entity.AIPurposeBoardLabel] = []string{raw}
		got, err := designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
		require.NoError(t, err)
		require.Equal(t, want, got.Role, raw)
	}
}

func TestBoardLabelLadderUnclearEscalatesToStrong(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"view":"unclear","confidence":0.3}`}
	ai.answers[entity.AIPurposeBoardRead] = []string{"I think side_l.\n```json\n{\"view\":\"side\",\"faces\":\"left\",\"confidence\":0.5}\n```\nWait — final:\n{\"view\":\"side\",\"faces\":\"right\",\"confidence\":0.8,\"why\":\"front points right\"}"}
	got, err := designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
	require.NoError(t, err)
	require.Equal(t, entity.DesignViewSideR, got.Role)
	require.Equal(t, entity.DesignLabelSourceModelStrong, got.Source)
	require.Equal(t, "front points right", got.ModelCaption)
	require.Equal(t, "chat.board_read/model", got.LabelModel)
	require.Equal(t, []string{entity.AIPurposeBoardLabel, entity.AIPurposeBoardRead}, ai.calls)
}

func TestBoardLabelLadderBothUnsureAsksThePerson(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`{"view":"front","confidence":0.5}`}
	ai.answers[entity.AIPurposeBoardRead] = []string{`{"view":"unclear","confidence":0.2,"why":"three-quarter angle, collar and back both visible"}`}
	got, err := designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
	require.NoError(t, err)
	require.Equal(t, "", got.Role, "an unsure label carries no role — it never travels")
	require.Equal(t, entity.DesignLabelStateUnsure, got.State)
	require.Equal(t, "three-quarter angle, collar and back both visible", got.ModelCaption)
}

func TestBoardLabelLadderErrorsLeaveItPending(t *testing.T) {
	ai := newFakeBoardAI()
	ai.errs[entity.AIPurposeBoardLabel] = errors.New("503")
	_, err := designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
	require.Error(t, err, "an error is retried lazily, never written as a label")

	ai = newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{`I think it is the front.`}
	_, err = designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
	require.Error(t, err, "garbage is not a label")
}

func TestBoardLabelLadderAIOffFails(t *testing.T) {
	ai := newFakeBoardAI()
	ai.enabled = map[string]bool{}
	got, err := designBoardLabelLadder(context.Background(), ai, targetPic, "u", nil)
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateFailed, got.State)
	require.Empty(t, ai.calls)
}

func TestBoardLabelLadderProposesAPurposeForAnUnmarkedPicture(t *testing.T) {
	ai := newFakeBoardAI()
	ai.answers[entity.AIPurposeBoardLabel] = []string{
		`{"purpose":"mood","view":"unclear","confidence":0.9}`,
		`{"purpose":"target","view":"front","confidence":0.95,"lr_sure":true}`,
	}
	none := designBoardPicture{MediaID: 9}
	got, err := designBoardLabelLadder(context.Background(), ai, none, "u", nil)
	require.NoError(t, err)
	require.Equal(t, "mood", got.ProposedPurpose)
	require.Equal(t, "", got.Role)
	require.Equal(t, entity.DesignLabelStateOk, got.State, "a settled «no view»")

	got, err = designBoardLabelLadder(context.Background(), ai, none, "u", nil)
	require.NoError(t, err)
	require.Equal(t, "target", got.ProposedPurpose)
	require.Equal(t, entity.DesignViewFront, got.Role, "the view rides with the proposal; it travels only once the form says target")
}

func ref(media int, role, source, state string) entity.DesignReference {
	return entity.DesignReference{MediaId: media, Role: role, LabelSource: source, LabelState: state,
		LabelledAt: sql.NullTime{Time: time.Now(), Valid: true}}
}

func TestBoardLabelPlan(t *testing.T) {
	board := []designBoardPicture{
		{MediaID: 1, Purpose: entity.TechCardMediaRoleTarget},    // new target → label
		{MediaID: 2, Purpose: entity.TechCardMediaRoleNone},      // new unmarked → label (proposal)
		{MediaID: 3, Purpose: entity.TechCardMediaRoleMood},      // new mood → nothing
		{MediaID: 4, Purpose: entity.TechCardMediaRoleTarget},    // person's row → never
		{MediaID: 5, Purpose: entity.TechCardMediaRoleMood},      // model view, now mood → drop
		{MediaID: 6, Purpose: entity.TechCardMediaRoleTarget},    // model proposed mood, person says target → relabel
		{MediaID: 7, Purpose: entity.TechCardMediaRoleTarget},    // settled model view → nothing
		{MediaID: 8, Purpose: entity.TechCardMediaRoleTarget},    // lost task → label (re-arm)
		{MediaID: 10, Purpose: entity.TechCardMediaRoleDetail},   // detail, Ф1 → nothing
		{MediaID: 11, Purpose: entity.TechCardMediaRoleMaterial}, // model's own «no view» proposal → kept
	}
	stale := ref(8, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStatePending)
	stale.LabelledAt = sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}
	refs := []entity.DesignReference{
		ref(4, "front", "", ""),
		ref(5, "back", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),
		ref(6, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),
		ref(7, "side_l", entity.DesignLabelSourceModelStrong, entity.DesignLabelStateOk),
		stale,
		ref(11, "", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),
		ref(12, "front", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk), // left the board → drop
		ref(13, "front", entity.DesignLabelSourceHuman, entity.DesignLabelStateOk),      // a person's, off board → kept
	}
	tasks, drop := designBoardLabelPlan(board, refs, time.Now().Add(-entity.DesignBoardLabelStaleAfter), false)
	got := map[int]bool{}
	for _, tk := range tasks {
		got[tk.MediaID] = tk.Relabel
	}
	require.Equal(t, map[int]bool{1: false, 2: false, 6: true, 8: false}, got)
	require.Equal(t, []int{5, 12}, drop)
}

func TestDesignRunRefsTravelRule(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 1, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard, Role: entity.TechCardMediaRoleTarget},
		{MediaId: 2, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard, Role: entity.TechCardMediaRoleNone},
		{MediaId: 3, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard, Role: entity.TechCardMediaRoleTarget},
		{MediaId: 6, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaReference},
	}
	refs := []entity.DesignReference{
		ref(1, "front", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk), // rides
		ref(2, "back", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),  // model view on an unmarked picture: home
		ref(3, "", entity.DesignLabelSourceModelStrong, entity.DesignLabelStateUnsure), // unsure: home
		ref(5, "side_l", "", ""), // a person's label on a picture off the card: not the flat's (Ф4)
		ref(6, "back", entity.DesignLabelSourceHuman, entity.DesignLabelStateOk), // a person's legacy input row: rides
		ref(7, "", entity.DesignLabelSourceHuman, entity.DesignLabelStateOk),     // a person's «no view»: home
	}
	ids := func(rs []entity.DesignReference) []int {
		var out []int
		for _, r := range rs {
			out = append(out, r.MediaId)
		}
		return out
	}
	// Render, 3D, the join list: a person's label rides wherever its picture is.
	require.Equal(t, []int{1, 5, 6}, ids(designRunRefs(card, refs)))
	require.Equal(t, []int{1, 5, 6}, ids(designRunRefsFor(entity.DesignRunKindRender, card, refs)))
	// Ф4, the flat: only labels on board pictures of a fitting purpose — and a legacy input row (6, a
	// released card's) keeps the old rule.
	require.Equal(t, []int{1, 6}, ids(designRunRefsFor(entity.DesignRunKindFlat, card, refs)))
	designBoardIsTheSource = false
	t.Cleanup(func() { designBoardIsTheSource = designBoardIsTheSourceDefault })
	require.Equal(t, []int{1, 5, 6}, ids(designRunRefsFor(entity.DesignRunKindFlat, card, refs)), "the old rule")
}

func TestParseBoardLabelAnswerLenient(t *testing.T) {
	a, ok := entity.ParseDesignBoardLabelAnswer(`{"purpose":"Target","view":"Left Side","faces":"Left","confidence":"85"}`)
	require.True(t, ok)
	require.Equal(t, "target", a.Purpose)
	require.Equal(t, entity.DesignViewSide, a.View)
	require.Equal(t, entity.DesignViewSideL, a.ResolvedView())
	require.InDelta(t, 0.85, a.Confidence, 1e-9)

	a, ok = entity.ParseDesignBoardLabelAnswer(`{"view":"three-quarter","confidence":2}`)
	require.True(t, ok)
	require.Equal(t, "unclear", a.View)
	require.Equal(t, 0.02, a.Confidence)
}

func TestBoardDetailReadJoinsMintsOrAsks(t *testing.T) {
	slots := []designBoardDetailSlot{{ID: 17, Name: "left cuff", URL: "https://cdn.test/cuff.jpg"}, {ID: 18, Name: "back yoke"}}
	det := designBoardPicture{MediaID: 460, Purpose: entity.TechCardMediaRoleDetail}
	cases := []struct {
		answer      string
		slot        int
		name, state string
		caption     string
	}{
		{`{"slot":17,"name":"left cuff","caption":"A buttoned cuff.","confidence":0.9}`, 17, "left cuff", entity.DesignLabelStateOk, "A buttoned cuff."}, // the name rides as the fallback
		{`{"slot":"new","name":"Patch Pocket!","caption":"A patch pocket.","confidence":0.8}`, 0, "patch pocket", entity.DesignLabelStateOk, "A patch pocket."},
		{`{"slot":99,"name":"left cuff","confidence":0.8}`, 0, "left cuff", entity.DesignLabelStateOk, ""}, // a slot that is not ours → by name (the store joins 17)
		{`{"slot":"new","name":"collar","caption":"blurry","confidence":0.4}`, 0, "", entity.DesignLabelStateUnsure, "blurry"},
	}
	for _, c := range cases {
		ai := newFakeBoardAI()
		ai.answers[entity.AIPurposeBoardRead] = []string{c.answer}
		got, err := designBoardLabelLadder(context.Background(), ai, det, "https://cdn.test/460.jpg", slots)
		require.NoError(t, err)
		require.Equal(t, []string{entity.AIPurposeBoardRead}, ai.calls, "a detail goes straight to the strong read")
		require.Equal(t, c.state, got.State, c.answer)
		require.Equal(t, c.slot, got.DetailSlotId, c.answer)
		require.Equal(t, c.name, got.NewDetailName, c.answer)
		require.Equal(t, c.caption, got.ModelCaption, c.answer)
		if c.state == entity.DesignLabelStateOk {
			require.Equal(t, entity.DesignViewDetail, got.Role)
		} else {
			require.Equal(t, "", got.Role, "no slot is made when unsure")
		}
	}
	require.Contains(t, designBoardReadDetailUserPrompt(slots), "- 17: left cuff (picture 2)")
	require.True(t, strings.HasSuffix(designBoardReadDetailUserPrompt(slots), "- 18: back yoke"), "a slot without a photo names no picture")
}

func TestBoardLabelPlanDetails(t *testing.T) {
	board := []designBoardPicture{
		{MediaID: 1, Purpose: entity.TechCardMediaRoleDetail}, // new detail → read
		{MediaID: 2, Purpose: entity.TechCardMediaRoleDetail}, // model labelled it a view → relabel
		{MediaID: 3, Purpose: entity.TechCardMediaRoleTarget}, // model labelled it a detail → relabel
	}
	refs := []entity.DesignReference{
		ref(2, "front", entity.DesignLabelSourceModelCheap, entity.DesignLabelStateOk),
		ref(3, "detail", entity.DesignLabelSourceModelStrong, entity.DesignLabelStateOk),
	}
	tasks, drop := designBoardLabelPlan(board, refs, time.Now().Add(-time.Minute), true)
	got := map[int]bool{}
	for _, tk := range tasks {
		got[tk.MediaID] = tk.Relabel
	}
	require.Equal(t, map[int]bool{1: false, 2: true, 3: true}, got)
	require.Empty(t, drop)
}
