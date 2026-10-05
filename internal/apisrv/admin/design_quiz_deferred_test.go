package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 64-DEFERRED E1 (decision_key) + E2 (server quiz session), backend lane.

func keyedQuiz(id, key string, selected ...string) entity.TechCardQuizAnswer {
	a := storedQuiz(id, time.Time{}, selected...)
	a.Question.DecisionKey = key
	return a
}

// E1: an ANSWERED saved key closes the decision whatever the id or wording; skipped and stale rows
// close nothing; a clarify_ question keeps its key; a key twice in one batch is a repeat; the
// model's key is normalised.
func TestDesignQuizDedupeByDecisionKey(t *testing.T) {
	saved := []entity.TechCardQuizAnswer{
		keyedQuiz("room_at_chest", "chest_room", "a"),
		{Question: entity.DesignQuizQuestion{ID: "lining", DecisionKey: "lining_insulation", Question: "Lining?"}, Skipped: true},
		{Question: entity.DesignQuizQuestion{ID: "hem", DecisionKey: "body_length", Question: "Hem?"}, Selected: []string{"a"}, Stale: true},
	}
	raw := `{"questions":[
	 {"id":"chest_ease","decision_key":"chest_room","category":"fit","kind":"single","question":"How loose across the chest?","options":["close","easy","loose"]},
	 {"id":"clarify_room_at_chest","decision_key":"chest_room","category":"fit","kind":"single","question":"The pictures look loose — which?","options":["close","loose"]},
	 {"id":"lining_type","decision_key":"lining_insulation","category":"materials","kind":"single","question":"What lining?","options":["none","mesh"]},
	 {"id":"hem_sit","decision_key":"body_length","category":"fit","kind":"single","question":"Where does it end?","options":["hip","thigh"]},
	 {"id":"collar_a","decision_key":" Collar Type ","category":"details","kind":"single","question":"Which collar?","options":["shirt","band"]},
	 {"id":"collar_b","decision_key":"collar_type","category":"details","kind":"single","question":"Collar style?","options":["shirt","band"]},
	 {"id":"no_key","category":"use","kind":"single","question":"Season?","options":["ss","aw"]}
	]}`
	qs, st, ok := parseDesignQuizCounted(raw, "jacket", saved)
	require.True(t, ok)
	require.Equal(t, []string{"clarify_room_at_chest", "lining_type", "hem_sit", "collar_a", "no_key"}, func() []string {
		var ids []string
		for _, q := range qs {
			ids = append(ids, q.ID)
		}
		return ids
	}())
	require.Equal(t, 2, st.repeated, "chest_ease (answered key) and collar_b (key repeated in the batch)")
	require.Equal(t, "collar_type", qs[3].DecisionKey, "normalised")
	require.Equal(t, "", qs[4].DecisionKey)
	require.Equal(t, []string{"chest_room"}, designQuizAnsweredKeys(saved))
	require.Contains(t, designQuizUserPrompt(&entity.TechCard{QuizAnswers: saved}, nil, nil, "jacket", ""),
		"Decision keys already answered (closed — never ask a question with one of these keys): chest_room\n")
}

// E1: saving a key held by a DIFFERENT stored id forgets the older row in the same merge; the same
// id keeps its place; "" never supersedes; two upserts of one key — the later wins.
func TestDesignQuizMergeSupersedesByDecisionKey(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	prev := []entity.TechCardQuizAnswer{
		keyedQuiz("chest_ease", "chest_room", "a"),
		keyedQuiz("collar", "", "a"),
		keyedQuiz("collar_style", "", "b"),
		keyedQuiz("hem", "body_length", "a"),
	}
	got := entity.MergeDesignQuizAnswers(prev, []entity.TechCardQuizAnswer{
		keyedQuiz("room_at_chest", "chest_room", "b"),
		keyedQuiz("hem", "body_length", "b"),
		keyedQuiz("new_collar", "", "a"),
	}, nil, now)
	require.Equal(t, []string{"collar", "collar_style", "hem", "room_at_chest", "new_collar"}, quizIDs(got))

	got = entity.MergeDesignQuizAnswers(prev, []entity.TechCardQuizAnswer{
		keyedQuiz("x1", "chest_room", "a"), keyedQuiz("x2", "chest_room", "b"),
	}, nil, now)
	require.Equal(t, []string{"collar", "collar_style", "hem", "x2"}, quizIDs(got))

	// A key carried by a forgotten id supersedes nothing.
	got = entity.MergeDesignQuizAnswers(prev, nil, []string{"hem"}, now)
	require.Equal(t, []string{"chest_ease", "collar", "collar_style"}, quizIDs(got))
}

// E1 at the door: the key survives validation onto the upsert; a malformed key is refused.
func TestDesignQuizValidateDecisionKey(t *testing.T) {
	a := quizAnswer("chest_ease", "single", []string{"close", "loose"}, "close")
	a.Question.DecisionKey = "chest_room"
	out, _, ve := validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{a})
	require.Nil(t, ve)
	require.Equal(t, "chest_room", out[0].Question.DecisionKey)
	require.Equal(t, "chest_room", designQuizQuestionToPb(out[0].Question).GetDecisionKey())

	a.Question.DecisionKey = "Chest Room!"
	_, _, ve = validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{a})
	require.NotNil(t, ve)
}

// E2: pending = the open session's questions minus every saved id (answered or skipped), order kept.
func TestDesignQuizPendingIsSessionMinusSaved(t *testing.T) {
	qs := []entity.DesignQuizQuestion{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	saved := []entity.TechCardQuizAnswer{keyedQuiz("b", "", "a"), {Question: entity.DesignQuizQuestion{ID: "d"}, Skipped: true}, keyedQuiz("z", "", "a")}
	var ids []string
	for _, q := range entity.DesignQuizPending(qs, saved) {
		ids = append(ids, q.ID)
	}
	require.Equal(t, []string{"a", "c"}, ids)
	require.Empty(t, entity.DesignQuizPending(nil, saved))

	// Supersession: the session asked chest_ease (chest_room); the designer answered room_at_chest
	// with the same key, which forgot chest_ease — chest_ease must not come back as pending.
	qs = []entity.DesignQuizQuestion{{ID: "chest_ease", DecisionKey: "chest_room"}, {ID: "collar", DecisionKey: "collar_type"}, {ID: "free"}}
	saved = []entity.TechCardQuizAnswer{keyedQuiz("room_at_chest", "chest_room", "a"), keyedQuiz("unkeyed", "", "a")}
	ids = nil
	for _, q := range entity.DesignQuizPending(qs, saved) {
		ids = append(ids, q.ID)
	}
	require.Equal(t, []string{"collar", "free"}, ids)
}

// E2 at the door: GetDesignQuizAnswers returns the pending tail and its family; no open session →
// an empty pending list; a failed session read degrades to empty, never a refusal.
func TestGetDesignQuizAnswersReturnsPending(t *testing.T) {
	run := func(sess *entity.DesignQuizSession, sessErr error) *pb_admin.GetDesignQuizAnswersResponse {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)
		card := &entity.TechCard{Id: 7, QuizAnswers: []entity.TechCardQuizAnswer{keyedQuiz("b", "", "a")}}
		cards.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
		cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{}, nil)
		cards.EXPECT().GetOpenDesignQuizSession(mock.Anything, 7).Return(sess, sessErr)
		res, err := (&Server{repo: repo}).GetDesignQuizAnswers(context.Background(), &pb_admin.GetDesignQuizAnswersRequest{TechCardId: 7})
		require.NoError(t, err)
		require.Len(t, res.GetAnswers(), 1)
		return res
	}
	res := run(&entity.DesignQuizSession{Family: "jacket", Questions: []entity.DesignQuizQuestion{
		{ID: "a", DecisionKey: "chest_room"}, {ID: "b"}, {ID: "c"},
	}}, nil)
	require.Len(t, res.GetPending(), 2)
	require.Equal(t, "a", res.GetPending()[0].GetId())
	require.Equal(t, "chest_room", res.GetPending()[0].GetDecisionKey())
	require.Equal(t, "c", res.GetPending()[1].GetId())
	require.Equal(t, "jacket", res.GetPendingFamily())

	require.Empty(t, run(nil, nil).GetPending())
	require.Empty(t, run(nil, errors.New("db down")).GetPending())
}

// E2: close_session closes the open session after a successful save (discard sends no answers);
// without it nothing is closed (the strict mock fails on an unexpected call); a failed close is
// Internal.
func TestSaveDesignQuizAnswersCloseSession(t *testing.T) {
	run := func(closeSession bool, closeErr error) (*pb_admin.SaveDesignQuizAnswersResponse, error) {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)
		cards.EXPECT().GetTechCardById(mock.Anything, 7).Return(&entity.TechCard{Id: 7}, nil)
		cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{}, nil)
		cards.EXPECT().SaveDesignQuizAnswers(mock.Anything, 7, mock.Anything, mock.Anything, designQuizMaxAnswers, mock.Anything).
			Return([]entity.TechCardQuizAnswer{storedQuiz("kept", time.Now(), "a")}, nil)
		if closeSession {
			cards.EXPECT().CloseDesignQuizSession(mock.Anything, 7).Return(closeErr)
		}
		return (&Server{repo: repo}).SaveDesignQuizAnswers(context.Background(),
			&pb_admin.SaveDesignQuizAnswersRequest{TechCardId: 7, CloseSession: closeSession})
	}
	res, err := run(true, nil)
	require.NoError(t, err)
	require.Len(t, res.GetAnswers(), 1, "discard still returns the stored list")
	_, err = run(false, nil)
	require.NoError(t, err)
	_, err = run(true, errors.New("db down"))
	require.Equal(t, codes.Internal, status.Code(err))
}
