package admin

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// 62-DEEP-FIXES — staleness (D1), lifecycle (D2), quiz-only apply (D3).

func deepQuizCard() (*entity.TechCard, entity.StyleSizeChart) {
	card := &entity.TechCard{Id: 7}
	card.CategoryId = sql.NullInt32{Int32: 12, Valid: true}
	card.Fit = sql.NullString{String: "relaxed", Valid: true}
	card.TargetGender = sql.NullString{String: "male", Valid: true}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 3, Valid: true}
	card.Concept = sql.NullString{String: "a field jacket", Valid: true}
	card.Details = []entity.TechCardDetail{{Key: sql.NullString{String: "closure", Valid: true},
		Text: sql.NullString{String: "exposed metal zip", Valid: true}}}
	card.BomItems = []entity.TechCardBomItem{{Name: "cotton twill", Composition: sql.NullString{String: "100% CO", Valid: true}}}
	chart := entity.StyleSizeChart{Cells: []entity.StyleSizeChartCell{
		{SizeID: 3, MeasurementNameID: 1, Value: decimal.RequireFromString("112")},
		{SizeID: 4, MeasurementNameID: 1, Value: decimal.RequireFromString("116")},
	}}
	return card, chart
}

// D1: the fingerprint moves with the structured facts only — not with the concept, the board or
// another size's grading.
func TestDesignQuizFingerprintCoversStructuredInputsOnly(t *testing.T) {
	card, chart := deepQuizCard()
	base := designQuizCardFingerprint(card, chart)
	require.Len(t, base, 64)

	same := func(mut func(c *entity.TechCard, ch *entity.StyleSizeChart)) string {
		c, ch := deepQuizCard()
		mut(c, &ch)
		return designQuizCardFingerprint(c, ch)
	}
	require.Equal(t, base, same(func(c *entity.TechCard, _ *entity.StyleSizeChart) {
		c.Concept = sql.NullString{String: "rewritten by apply to description", Valid: true}
		c.Media = append(c.Media, entity.TechCardMediaItem{MediaId: 99})
	}), "concept and board pictures are not inputs")
	require.Equal(t, base, same(func(_ *entity.TechCard, ch *entity.StyleSizeChart) {
		ch.Cells[1].Value = decimal.RequireFromString("120")
	}), "another size's value is not the base sample")

	for name, mut := range map[string]func(c *entity.TechCard, ch *entity.StyleSizeChart){
		"category": func(c *entity.TechCard, _ *entity.StyleSizeChart) { c.CategoryId.Int32 = 13 },
		"fit":      func(c *entity.TechCard, _ *entity.StyleSizeChart) { c.Fit.String = "slim" },
		"gender":   func(c *entity.TechCard, _ *entity.StyleSizeChart) { c.TargetGender.String = "female" },
		"detail": func(c *entity.TechCard, _ *entity.StyleSizeChart) {
			c.Details[0].Text.String = "concealed button placket"
		},
		"bom": func(c *entity.TechCard, _ *entity.StyleSizeChart) { c.BomItems[0].Composition.String = "98% CO 2% EA" },
		"measurement": func(_ *entity.TechCard, ch *entity.StyleSizeChart) {
			ch.Cells[0].Value = decimal.RequireFromString("114")
		},
	} {
		require.NotEqual(t, base, same(mut), name)
	}

	// The staleness rule: "" on either side is fresh (pre-0392 rows; an unreadable chart).
	require.False(t, entity.DesignQuizIsStale("", base))
	require.False(t, entity.DesignQuizIsStale(base, ""))
	require.False(t, entity.DesignQuizIsStale(base, base))
	require.True(t, entity.DesignQuizIsStale(base, same(func(c *entity.TechCard, _ *entity.StyleSizeChart) { c.Fit.String = "slim" })))
}

// D1 downstream: fresh answers carry the precedence line; stale ones go under their own unconfirmed
// header, in the drafts and in the image block; the quiz prompt marks them to re-confirm.
func TestDesignQuizStaleAnswersAreUnconfirmedDownstream(t *testing.T) {
	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "hem", Category: "fit", Part: "hem", Question: "Where does the hem sit?"}, Selected: []string{"mid-thigh"}},
		{Question: entity.DesignQuizQuestion{ID: "closure", Category: "details", Part: "whole", Question: "What closes the front?"},
			Selected: []string{"exposed metal zip"}, Stale: true},
	}}
	block := designQuizDecisionsBlock(card)
	fresh := strings.Index(block, designQuizDecisionsHeader)
	stale := strings.Index(block, designQuizStaleHeader)
	require.True(t, fresh >= 0 && stale > fresh, block)
	require.Contains(t, designQuizDecisionsHeader, "current card fields (details, BOM, measurements) outrank these answers")
	require.Less(t, strings.Index(block, "mid-thigh"), stale)
	require.Greater(t, strings.Index(block, "exposed metal zip"), stale)

	img := designQuizImageBlock(card, "")
	require.True(t, strings.HasPrefix(img, "decided with the designer (current card fields outrank"), img)
	require.Contains(t, img, "earlier quiz answers — the card changed since; unconfirmed, current card facts win:\n- details — What closes the front? → exposed metal zip")

	line := designQuizAnsweredLine(card.QuizAnswers[1])
	require.Contains(t, line, "TO RE-CONFIRM")
	require.NotContains(t, designQuizAnsweredLine(card.QuizAnswers[0]), "RE-CONFIRM")
	require.Contains(t, designQuizUserPrompt(card, nil, nil, "jacket", ""), "you may ask ONE re-confirmation question")

	// Only stale → no fresh header at all.
	onlyStale := &entity.TechCard{QuizAnswers: card.QuizAnswers[1:]}
	require.NotContains(t, designQuizDecisionsBlock(onlyStale), designQuizDecisionsHeader)
}

// D1 parse: a stale answered row may come back ONCE (the re-confirmation); a fresh one never.
func TestDesignQuizStaleAnswerMayBeReconfirmedOnce(t *testing.T) {
	saved := []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "closure", Question: "What closes the front?"}, Selected: []string{"zip"}, Stale: true},
		{Question: entity.DesignQuizQuestion{ID: "pocket", Question: "Chest pockets?"}, Selected: []string{"two"}, Stale: true},
		{Question: entity.DesignQuizQuestion{ID: "hem", Question: "Where does the hem sit?"}, Selected: []string{"mid-thigh"}},
	}
	raw := `{"questions":[` + quizQ("closure", "details", "whole", "What closes the front?", "zip", "buttons") + "," +
		quizQ("pocket", "details", "whole", "Chest pockets?", "one", "two") + "," +
		quizQ("hem", "fit", "hem", "Where does the hem sit?", "hip", "mid-thigh") + `]}`
	qs, st, ok := parseDesignQuizCounted(raw, "jacket", saved)
	require.True(t, ok)
	require.Len(t, qs, 1)
	require.Equal(t, "closure", qs[0].ID)
	require.Equal(t, 2, st.repeated)
}

// D2 archive: design_quiz.json round-trips through the live-save validation; a list that does not
// validate is refused whole.
func TestDesignQuizArchiveRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	in := []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "hem", Category: "fit", Part: "hem", View: "front", Kind: "single",
			Question: "Where does the hem sit?", Options: []string{"hip", "mid-thigh"}},
			Selected: []string{"mid-thigh"}, AnsweredAt: at, Fingerprint: strings.Repeat("a", 64)},
		{Question: entity.DesignQuizQuestion{ID: "lining", Category: "materials", Part: "whole", View: "front", Kind: "single",
			Question: "Lining?", Options: []string{"none", "half"}}, Skipped: true, AnsweredAt: at},
	}
	rows := designQuizToArchive(in)
	require.Len(t, rows, 2)
	out, err := designQuizFromArchive(rows)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "mid-thigh", out[0].Selected[0])
	require.Equal(t, at, out[0].AnsweredAt)
	require.Empty(t, out[0].Fingerprint, "an imported answer is fresh: the fingerprint does not travel")
	require.True(t, out[1].Skipped)

	bad := append([]techcardarchive.DesignQuizAnswer(nil), rows...)
	bad[0].Selected = []string{"not an option"}
	_, err = designQuizFromArchive(bad)
	require.Error(t, err)
	require.Nil(t, designQuizToArchive(nil))
}

// D2 digest: the quiz token is "" for a card without answers (so the DESIGN digest is byte-identical,
// proven in dto), ignores answered_at and the fingerprint (re-confirming is not an edit), and moves
// with the answer itself.
func TestDesignQuizAnswersDigest(t *testing.T) {
	require.Empty(t, entity.DesignQuizAnswersDigest(nil))
	a := storedQuiz("hem", time.Now(), "a")
	d := entity.DesignQuizAnswersDigest([]entity.TechCardQuizAnswer{a})
	require.Len(t, d, 64)
	b := a
	b.AnsweredAt, b.Fingerprint, b.Stale = b.AnsweredAt.Add(time.Hour), "ff", true
	require.Equal(t, d, entity.DesignQuizAnswersDigest([]entity.TechCardQuizAnswer{b}))
	b.Selected = []string{"b"}
	require.NotEqual(t, d, entity.DesignQuizAnswersDigest([]entity.TechCardQuizAnswer{b}))
}

// D3: a card with quiz answers and NO board picture is drafted text-only instead of refused.
func TestDraftDescriptionFromQuizWithoutPictures(t *testing.T) {
	card := &entity.TechCard{}
	card.Name = "quiz only"
	card.QuizAnswers = []entity.TechCardQuizAnswer{{Question: entity.DesignQuizQuestion{ID: "hem", Category: "fit",
		Part: "hem", Question: "Where does the hem sit?"}, Selected: []string{"mid-thigh"}}}
	rig := newDraftRigWithCard(t, http.StatusOK, "A jacket to mid-thigh.", card, nil, nil)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)
	require.Nil(t, resp.GetConstruction())
	require.Equal(t, "A jacket to mid-thigh.", rig.completedText)
	require.Empty(t, rig.stub.imageURLs(t), "text only")
	require.Contains(t, rig.stub.body, "Where does the hem sit? → mid-thigh")

	// Skipped answers only are not decisions: still refused.
	require.False(t, designQuizHasDecisions(&entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "x"}, Skipped: true}}}))
}
