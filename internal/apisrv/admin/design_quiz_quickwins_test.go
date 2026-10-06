package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 61-QUICKWINS W-B1..W-B6, backend lane.

func storedQuiz(id string, at time.Time, selected ...string) entity.TechCardQuizAnswer {
	return entity.TechCardQuizAnswer{
		Question: entity.DesignQuizQuestion{ID: id, Category: "details", Part: "collar", Question: id + "?", Options: []string{"a", "b"}},
		Selected: selected, AnsweredAt: at,
	}
}

// W-B1: a save never erases what it does not name; an empty row forgets; an unchanged answer keeps
// its time; a changed one is restamped in place; new rows append in request order.
func TestDesignQuizMergeIsNonDestructive(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := t0.Add(time.Hour)
	prev := []entity.TechCardQuizAnswer{storedQuiz("a", t0, "a"), storedQuiz("b", t0, "a"), storedQuiz("c", t0, "b")}

	// A client whose GET has not resolved sends ONLY the new row: the three stored rows stay.
	got := entity.MergeDesignQuizAnswers(prev, []entity.TechCardQuizAnswer{storedQuiz("d", time.Time{}, "a")}, nil, now)
	require.Equal(t, []string{"a", "b", "c", "d"}, quizIDs(got))
	require.Equal(t, now, got[3].AnsweredAt)

	// Update in place (b changed), keep time when unchanged (a), forget c, append e.
	got = entity.MergeDesignQuizAnswers(prev, []entity.TechCardQuizAnswer{
		storedQuiz("e", time.Time{}, "a"), storedQuiz("b", time.Time{}, "b"), storedQuiz("a", time.Time{}, "a"),
	}, []string{"c", "zzz"}, now)
	require.Equal(t, []string{"a", "b", "e"}, quizIDs(got))
	require.Equal(t, t0, got[0].AnsweredAt, "unchanged answer keeps its time")
	require.Equal(t, now, got[1].AnsweredAt, "changed answer restamped")
	require.Equal(t, []string{"b"}, got[1].Selected)

	// Two stale clients saving different ids both survive.
	afterA := entity.MergeDesignQuizAnswers(nil, []entity.TechCardQuizAnswer{storedQuiz("x", time.Time{}, "a")}, nil, now)
	afterB := entity.MergeDesignQuizAnswers(afterA, []entity.TechCardQuizAnswer{storedQuiz("y", time.Time{}, "a")}, nil, now)
	require.Equal(t, []string{"x", "y"}, quizIDs(afterB))

	// Skipped → answered replaces the skipped row.
	skipped := storedQuiz("s", t0)
	skipped.Skipped = true
	got = entity.MergeDesignQuizAnswers([]entity.TechCardQuizAnswer{skipped}, []entity.TechCardQuizAnswer{storedQuiz("s", time.Time{}, "b")}, nil, now)
	require.Len(t, got, 1)
	require.False(t, got[0].Skipped)
	require.Equal(t, now, got[0].AnsweredAt)
}

func quizIDs(in []entity.TechCardQuizAnswer) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, a.Question.ID)
	}
	return out
}

// W-B1: validation splits the request — an empty row (no selection, no own words, not skipped) is a
// forget; a skipped row and an own-words-only row are upserts; the empty row still passes validation.
func TestValidateDesignQuizAnswersSplitsForget(t *testing.T) {
	opts := []string{"soft", "stiff stand, 3 cm"}
	skip := quizAnswer("season", "single", opts)
	skip.Skipped = true
	words := quizAnswer("words", "single", opts)
	words.FreeText = "  only in the body  "
	up, forget, ve := validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{
		quizAnswer("collar_stand", "single", opts, "soft"),
		quizAnswer("gone", "single", opts),
		skip, words,
	})
	require.Nil(t, ve)
	require.Equal(t, []string{"gone"}, forget)
	require.Equal(t, []string{"collar_stand", "season", "words"}, quizIDs(up))
	require.Equal(t, "only in the body", up[2].FreeText)

	// An empty row is still validated: a forget with a malformed id is refused.
	_, _, ve = validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{quizAnswer("Bad-Id", "single", opts)})
	require.NotNil(t, ve)
}

// W-B1 at the door: the handler hands the store the upserts, the forget ids and the cap, and maps the
// store's "too many" to InvalidArgument and a missing card to NotFound.
func TestSaveDesignQuizAnswersHandlerUpsertsAndForgets(t *testing.T) {
	opts := []string{"soft", "stiff stand, 3 cm"}
	req := &pb_admin.SaveDesignQuizAnswersRequest{TechCardId: 7, Answers: []*pb_admin.DesignQuizAnswer{
		quizAnswer("collar_stand", "single", opts, "soft"), quizAnswer("gone", "single", opts),
	}}
	run := func(storeErr error) (*pb_admin.SaveDesignQuizAnswersResponse, error) {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)
		card := &entity.TechCard{Id: 7}
		cards.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
		cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{}, nil)
		fp := designQuizCardFingerprint(card, entity.StyleSizeChart{})
		cards.EXPECT().SaveDesignQuizAnswers(mock.Anything, 7,
			mock.MatchedBy(func(up []entity.TechCardQuizAnswer) bool {
				// 62 D1: the upsert carries the card's current fingerprint (a save is fresh).
				return len(up) == 1 && up[0].Question.ID == "collar_stand" && up[0].Fingerprint == fp
			}), []string{"gone"}, designQuizMaxAnswers, mock.Anything).
			Return([]entity.TechCardQuizAnswer{storedQuiz("kept", time.Now()), storedQuiz("collar_stand", time.Now(), "soft")}, storeErr)
		return (&Server{repo: repo}).SaveDesignQuizAnswers(context.Background(), req)
	}
	res, err := run(nil)
	require.NoError(t, err)
	require.Len(t, res.GetAnswers(), 2, "the stored list comes back, rows the request did not name included")

	_, err = run(entity.ErrDesignQuizTooManyAnswers)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = run(sql.ErrNoRows)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// W-B4: skipped is "for now" — not in the dedupe, listed as deferred; an answered row stays closed.
func TestDesignQuizSkipIsDeferred(t *testing.T) {
	saved := []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "lining", Category: "materials", Part: "whole", Question: "Lining?"}, Skipped: true},
		{Question: entity.DesignQuizQuestion{ID: "hem", Category: "fit", Part: "hem", Question: "Where does the hem sit?"}, Selected: []string{"mid-thigh"}},
	}
	raw := `{"questions":[` + quizQ("lining", "materials", "lining", "Lining?", "none", "half") + "," +
		quizQ("hem", "fit", "hem", "Hem?", "a", "b") + "," + quizQ("hem_2", "fit", "hem", "Where does the hem sit?", "a", "b") + `]}`
	qs, st, ok := parseDesignQuizCounted(raw, "jacket", saved)
	require.True(t, ok)
	require.Equal(t, []string{"lining"}, func() []string {
		var ids []string
		for _, q := range qs {
			ids = append(ids, q.ID)
		}
		return ids
	}())
	require.Equal(t, 2, st.repeated)

	card := &entity.TechCard{QuizAnswers: saved}
	p := designQuizUserPrompt(card, nil, nil, "jacket", "")
	require.Contains(t, p, "[lining · materials · whole] Lining? → deferred by the designer — may ask again if still open")
	require.Contains(t, p, "[hem · fit · hem] Where does the hem sit? → mid-thigh")
	require.NotContains(t, p, "do not ask again)")
}

// W-B5: an honest empty list is ok; a non-empty list where nothing survives validation is unusable;
// a list of nothing but repeats of saved answers is an honest "nothing new".
func TestDesignQuizParseStatsUnusable(t *testing.T) {
	_, st, ok := parseDesignQuizCounted(`{"questions":[]}`, "tee", nil)
	require.True(t, ok)
	require.False(t, st.unusable(), "empty list = nothing left to ask")

	bad := `{"questions":[` + quizQ("a", "colour", "whole", "Colour?", "red", "blue") + "," +
		quizQ("b", "design", "whole", "One option?", "only") + `]}`
	qs, st, ok := parseDesignQuizCounted(bad, "tee", nil)
	require.True(t, ok)
	require.Empty(t, qs)
	require.Equal(t, designQuizParseStats{raw: 2, invalid: 2}, st)
	require.True(t, st.unusable())

	saved := []entity.TechCardQuizAnswer{{Question: entity.DesignQuizQuestion{ID: "a", Question: "A?"}, Selected: []string{"x"}}}
	_, st, _ = parseDesignQuizCounted(`{"questions":[`+quizQ("a", "design", "whole", "A again?", "x", "y")+`]}`, "tee", saved)
	require.False(t, st.unusable(), "only repeats = honest empty")

	mixed := `{"questions":[` + quizQ("a", "colour", "whole", "Colour?", "red", "blue") + "," +
		quizQ("ok", "design", "whole", "Volume?", "close", "loose") + `]}`
	qs, st, _ = parseDesignQuizCounted(mixed, "tee", nil)
	require.Len(t, qs, 1)
	require.False(t, st.unusable())
	require.Equal(t, 1, st.invalid)

	var items []string
	for i := 0; i < 33; i++ {
		items = append(items, quizQ(fmt.Sprintf("q_%d", i), "design", "whole", fmt.Sprintf("Question %d?", i), "a", "b"))
	}
	_, st, _ = parseDesignQuizCounted(`{"questions":[`+strings.Join(items, ",")+`]}`, "tee", nil)
	require.Equal(t, designQuizParseStats{raw: 33, kept: 30, capped: 3}, st)
}

// W-B3: the base size's POM values reach the prompt with size and unit; an empty chart adds nothing.
func TestDesignQuizBaseMeasurementsBlock(t *testing.T) {
	names := map[int]string{1: "chest", 2: "back_length", 3: "sleeve length"}
	sizes := func(id int) string { return map[int]string{10: "S", 11: "M"}[id] }
	cell := func(size, m int, v string) entity.StyleSizeChartCell {
		return entity.StyleSizeChartCell{SizeID: size, MeasurementNameID: m, Value: decimal.RequireFromString(v)}
	}
	chart := entity.StyleSizeChart{Cells: []entity.StyleSizeChartCell{
		cell(10, 1, "108"), cell(11, 1, "112"), cell(11, 2, "68.5"), cell(11, 3, "64"), cell(11, 99, "1"),
	}}
	card := &entity.TechCard{}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 11, Valid: true}
	card.MeasurementUnit = entity.TechCardUnitCm
	line := designQuizBaseMeasurements(card, chart, names, sizes)
	require.Equal(t, "Base sample measurements (M, cm): chest 112, back length 68.5, sleeve length 64", line)

	p := designQuizUserPrompt(card, nil, nil, "jacket", line)
	require.Contains(t, p, "Base sample measurements (M, cm): chest 112")
	require.Less(t, strings.Index(p, "Base sample measurements"), strings.Index(p, "</card_data>"), "inside the card data")

	// Grade base when the card names none; nothing when the base is ambiguous or the chart empty.
	chart.GradeBaseSizeID = 10
	require.Equal(t, "Base sample measurements (S, cm): chest 108", designQuizBaseMeasurements(&entity.TechCard{}, chart, names, sizes))
	chart.GradeBaseSizeID = 0
	require.Empty(t, designQuizBaseMeasurements(&entity.TechCard{}, chart, names, sizes))
	require.Empty(t, designQuizBaseMeasurements(card, entity.StyleSizeChart{}, names, sizes))
	require.NotContains(t, designQuizUserPrompt(card, nil, nil, "jacket", ""), "Base sample measurements")

	// Bounded.
	many := entity.StyleSizeChart{}
	bigNames := map[int]string{}
	for i := 1; i <= 60; i++ {
		bigNames[i] = fmt.Sprintf("point of measure number %d", i)
		many.Cells = append(many.Cells, cell(11, i, "10"))
	}
	long := designQuizBaseMeasurements(card, many, bigNames, sizes)
	require.LessOrEqual(t, len(long), designQuizMaxMeasurementBytes+32)
	require.Contains(t, long, "more)")
}

// W-B6: only the family group's checklist travels, in the user turn; the system prompt keeps the
// common rules; the unknown family assumes nothing wearable.
func TestDesignQuizGroupChecklistInUserTurn(t *testing.T) {
	for _, gone := range []string{"Headwear (cap, hat)", "Footwear (shoe, boot, sandal)", "bras: the band and cup basis",
		"bottoms: waistband (width"} {
		require.NotContains(t, designQuizSystemPrompt, gone, "family checklists left the system prompt")
	}
	require.Contains(t, designQuizSystemPrompt, "Fit, every wearable garment:")
	require.Contains(t, designQuizSystemPrompt, "is in the user message, after the card data")

	jacket := designQuizUserPrompt(nil, nil, nil, "jacket", "")
	require.Contains(t, jacket, "Checklist (outerwear):\n · fit (tops and outerwear): chest or bust ease")
	for _, other := range []string{"waistband (width", "heel height", "band and cup", "gauge"} {
		require.NotContains(t, jacket, other)
	}
	require.Contains(t, designQuizUserPrompt(nil, nil, nil, "knit", ""), "knitwear: gauge")
	trousers := designQuizUserPrompt(nil, nil, nil, "trousers", "")
	require.Contains(t, trousers, "fit (bottoms): where the waist sits")
	require.NotContains(t, trousers, "chest or bust")
	require.Contains(t, designQuizUserPrompt(nil, nil, nil, "bag", ""), "bags, wallets, belts: dimensions")

	unknown := designQuizUserPrompt(nil, nil, nil, "", "")
	require.Contains(t, unknown, "do not assume the product is worn")
	require.Contains(t, unknown, `"product_type"`)
	require.NotContains(t, unknown, "Fit stays open")
	// 64-DEFERRED E1 added the decision_key list (~0.7 KB), 70-SEAMS §C the seam paragraph and the
	// seam/colourway examples (~2.7 KB), 70-SEAMS §B the COLOURWAYS paragraph (~1.2 KB), 90-EDGES
	// the EDGES paragraph, edge keys and example (~2.1 KB, 20015 bytes); the per-group checklists
	// stay out. 96-PICTURE-QUESTIONS the PICTURES paragraph and the picture field (~1.1 KB, 22035 bytes).
	// 99-SPOTS the spots field and its schema entry (~0.9 KB, 23111 bytes).
	require.Less(t, len(designQuizSystemPrompt), 23500, "the system prompt shrank")
	for fam := range designQuizParts {
		require.NotContains(t, designQuizGroupChecklist(fam), "unknown", fam)
	}
}

// W-B2: an image run that reads the garment note freezes the quiz decisions with it — question
// qualified, hw_/lbl_ humanised, skipped omitted, bounded, not repeated when WORDS already carry them.
func TestDesignQuizDecisionsReachImageRuns(t *testing.T) {
	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "hem", Category: "fit", Part: "hem", Question: "Where does the hem sit?"}, Selected: []string{"mid-thigh"}},
		{Question: entity.DesignQuizQuestion{ID: "btn", Category: "details", Part: "hw_button", Question: "How many front buttons?"}, Selected: []string{"6"}},
		{Question: entity.DesignQuizQuestion{ID: "brand", Category: "finish", Part: "lbl_brand", Question: "Brand label placement?"}, FreeText: "inside back neck"},
		{Question: entity.DesignQuizQuestion{ID: "lining", Category: "materials", Part: "lining", Question: "Lining?"}, Skipped: true},
	}}
	card.GarmentDescription = sql.NullString{String: "olive field jacket", Valid: true}
	params := &pb_common.DesignRunParams{Views: []string{entity.DesignViewFront}, Layout: designLayoutPerView}
	snap, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: card, Params: params})
	require.NoError(t, err)
	note := snap.GetGarmentNote()
	require.True(t, strings.HasPrefix(note, "olive field jacket\n\ndecided with the designer (current card fields outrank these when they conflict):\n"), note)
	for _, want := range []string{"- hem — Where does the hem sit? → mid-thigh", "- button — How many front buttons? → 6",
		`- brand label — Brand label placement? → own words: "inside back neck"`} {
		require.Contains(t, note, want)
	}
	require.NotContains(t, note, "Lining?")

	// No description: the block alone. Kinds that read no garment note get nothing.
	card.GarmentDescription = sql.NullString{}
	snap, err = designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: card, Params: params})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(snap.GetGarmentNote(), "decided with the designer ("))
	require.Empty(t, designQuizImageBlock(&entity.TechCard{}, ""))

	// WORDS that already carry a line verbatim (case-insensitive) do not get it twice.
	words := "Olive jacket. hem — where does the hem sit? → mid-thigh"
	block := designQuizImageBlock(card, words)
	require.NotContains(t, block, "Where does the hem sit")
	require.Contains(t, block, "front buttons")

	// Bounded.
	var many []entity.TechCardQuizAnswer
	for i := 0; i < 60; i++ {
		many = append(many, entity.TechCardQuizAnswer{Question: entity.DesignQuizQuestion{ID: fmt.Sprintf("q%d", i), Category: "details",
			Part: "collar", Question: strings.Repeat("long question words ", 6)}, Selected: []string{"an answer of some length"}})
	}
	big := designQuizImageBlock(&entity.TechCard{QuizAnswers: many}, "")
	require.LessOrEqual(t, len(big), designQuizImageMaxBytes)
	require.Contains(t, big, "more decisions, not listed")
}
