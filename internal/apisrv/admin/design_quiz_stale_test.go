package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// 98-STALE — per-topic staleness (§1), the change lines (§3), into the next generation (§4).

var staleTestNames = map[int]string{1: "chest", 2: "back_length"}

func staleTestSizeName(id int) string {
	if id == 3 {
		return "M"
	}
	return ""
}

func staleTestCard() (*entity.TechCard, entity.StyleSizeChart) {
	card := &entity.TechCard{Id: 7}
	card.CategoryId = sql.NullInt32{Int32: 12, Valid: true}
	card.Fit = sql.NullString{String: "relaxed", Valid: true}
	card.TargetGender = sql.NullString{String: "male", Valid: true}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 3, Valid: true}
	card.Details = []entity.TechCardDetail{{Key: sql.NullString{String: "collar", Valid: true},
		Text: sql.NullString{String: "shirt collar", Valid: true}}}
	card.BomItems = []entity.TechCardBomItem{{Name: "main fabric", Composition: sql.NullString{String: "cotton twill", Valid: true}}}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 501, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleTarget},
		{MediaId: 502, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMood},
		{MediaId: 503, Category: entity.TechCardMediaCategoryMoodboard},
	}
	chart := entity.StyleSizeChart{Cells: []entity.StyleSizeChartCell{
		{SizeID: 3, MeasurementNameID: 1, Value: decimal.RequireFromString("54")},
		{SizeID: 3, MeasurementNameID: 2, Value: decimal.RequireFromString("70")},
		{SizeID: 4, MeasurementNameID: 1, Value: decimal.RequireFromString("56")},
	}}
	return card, chart
}

// One answer per topic, stamped against the card as it is now (a save).
func staleTestAnswers(card *entity.TechCard, chart entity.StyleSizeChart) []entity.TechCardQuizAnswer {
	qs := []entity.DesignQuizQuestion{
		{ID: "chest_room", Category: "fit", Part: "whole", DecisionKey: "chest_room", Question: "Room at the chest?"},
		{ID: "shell", Category: "materials", Part: "whole", DecisionKey: "shell_fabric", Question: "Main shell fabric?"},
		{ID: "collar_type", Category: "details", Part: "collar", DecisionKey: "collar_type", Question: "Which collar?"},
		{ID: "silhouette", Category: "design", Part: "whole", DecisionKey: "silhouette", Question: "Silhouette?"},
		{ID: "pic2", Category: "design", Part: "whole", DecisionKey: "pic_502_mood", MediaID: 502, Question: "What to take from picture 2?"},
		{ID: "pic3", Category: "design", Part: "whole", DecisionKey: "pic_503_mood", MediaID: 503, Question: "What to take from picture 3?"},
	}
	out := make([]entity.TechCardQuizAnswer, 0, len(qs))
	for _, q := range qs {
		out = append(out, entity.TechCardQuizAnswer{Question: q, Selected: []string{"a"}})
	}
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).stamp(out)
	return out
}

func staleIDs(answers []entity.TechCardQuizAnswer) []string {
	var out []string
	for _, a := range answers {
		if a.Stale {
			out = append(out, a.Question.ID)
		}
	}
	return out
}

func TestDesignQuizTopicOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    entity.DesignQuizQuestion
		want string
	}{
		{"fit category", entity.DesignQuizQuestion{Category: "fit", Part: "whole", DecisionKey: "chest_room"}, "fit"},
		{"materials category", entity.DesignQuizQuestion{Category: "materials", Part: "whole", DecisionKey: "shell_fabric"}, "materials"},
		{"details", entity.DesignQuizQuestion{Category: "details", Part: "collar", DecisionKey: "collar_type"}, "construction"},
		{"finish", entity.DesignQuizQuestion{Category: "finish", Part: "whole", DecisionKey: "wash_finish"}, "construction"},
		{"use", entity.DesignQuizQuestion{Category: "use", Part: "whole", DecisionKey: "season"}, "construction"},
		{"design", entity.DesignQuizQuestion{Category: "design", Part: "whole", DecisionKey: "silhouette"}, "design"},
		{"palette part", entity.DesignQuizQuestion{Category: "design", Part: "col_palette", DecisionKey: "colour_direction"}, "materials"},
		{"colourway key", entity.DesignQuizQuestion{Category: "design", Part: "whole", DecisionKey: "colourway_count"}, "materials"},
		{"per-colourway key", entity.DesignQuizQuestion{Category: "finish", Part: "whole", DecisionKey: "wash_per_colourway"}, "materials"},
		{"seam part", entity.DesignQuizQuestion{Category: "details", Part: "sm_flat_felled", DecisionKey: "main_seam"}, "construction"},
		{"hardware part on materials", entity.DesignQuizQuestion{Category: "materials", Part: "hw_button", DecisionKey: "trims_hardware"}, "construction"},
		{"label part", entity.DesignQuizQuestion{Category: "finish", Part: "lbl_care", DecisionKey: "labels"}, "construction"},
		{"length key on design", entity.DesignQuizQuestion{Category: "design", Part: "whole", DecisionKey: "length_proportion"}, "fit"},
		{"sleeve length on details", entity.DesignQuizQuestion{Category: "details", Part: "sleeve", DecisionKey: "sleeve_length"}, "fit"},
		{"rise key", entity.DesignQuizQuestion{Category: "details", Part: "waistband", DecisionKey: "rise"}, "fit"},
		{"rise part", entity.DesignQuizQuestion{Category: "design", Part: "rise", DecisionKey: ""}, "fit"},
		{"width on materials stays materials", entity.DesignQuizQuestion{Category: "materials", Part: "whole", DecisionKey: "tape_width"}, "materials"},
		{"not a token: release", entity.DesignQuizQuestion{Category: "details", Part: "whole", DecisionKey: "quick_release"}, "construction"},
		{"picture wins", entity.DesignQuizQuestion{Category: "fit", Part: "whole", DecisionKey: "pic_9_match", MediaID: 9}, "picture"},
	} {
		require.Equal(t, tc.want, designQuizTopicOf(tc.q), tc.name)
	}
}

func TestDesignQuizStaleFreshAfterSave(t *testing.T) {
	card, chart := staleTestCard()
	answers := staleTestAnswers(card, chart)
	for _, a := range answers {
		require.NotNil(t, a.Facts, a.Question.ID)
		require.NotEmpty(t, a.Fingerprint)
	}
	require.Equal(t, []entity.DesignQuizFact{
		{Label: "fit", Value: "relaxed"}, {Label: "gender", Value: "male"},
		{Label: "chest (M)", Value: "54"}, {Label: "back length (M)", Value: "70"},
	}, answers[0].Facts)
	require.Equal(t, []entity.DesignQuizFact{{Label: "main fabric", Value: "cotton twill"}}, answers[1].Facts)
	require.Equal(t, []entity.DesignQuizFact{{Label: "category", Value: "category 12"}, {Label: "detail: collar", Value: "shirt collar"}}, answers[2].Facts)
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Empty(t, staleIDs(answers))
}

func TestDesignQuizBomEditStalesOnlyMaterials(t *testing.T) {
	card, chart := staleTestCard()
	answers := staleTestAnswers(card, chart)
	card.BomItems[0].Composition.String = "wool flannel"
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{Name: "lining", Composition: sql.NullString{String: "viscose twill", Valid: true}})
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Equal(t, []string{"shell"}, staleIDs(answers))
	require.Equal(t, []string{"main fabric: cotton twill → wool flannel", "lining: — → viscose twill"}, answers[1].StaleChanges)
	for _, a := range answers {
		if !a.Stale {
			require.Empty(t, a.StaleChanges)
		}
	}
	// A re-ordered BOM is not a change.
	card2, chart2 := staleTestCard()
	card2.BomItems = append(card2.BomItems, entity.TechCardBomItem{Name: "lining", Composition: sql.NullString{String: "viscose", Valid: true}})
	ans2 := staleTestAnswers(card2, chart2)
	card2.BomItems[0], card2.BomItems[1] = card2.BomItems[1], card2.BomItems[0]
	designQuizStateOf(card2, chart2, staleTestNames, staleTestSizeName).mark(ans2)
	require.Empty(t, staleIDs(ans2))
}

func TestDesignQuizMeasurementEditStalesOnlyFit(t *testing.T) {
	card, chart := staleTestCard()
	answers := staleTestAnswers(card, chart)
	chart.Cells[0].Value = decimal.RequireFromString("58")
	chart.Cells[2].Value = decimal.RequireFromString("60") // another size: not the base sample
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Equal(t, []string{"chest_room"}, staleIDs(answers))
	require.Equal(t, []string{"chest (M): 54 → 58"}, answers[0].StaleChanges)

	// A detail edit stales construction only, a removed detail renders "→ —".
	card, chart = staleTestCard()
	answers = staleTestAnswers(card, chart)
	card.Details = nil
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Equal(t, []string{"collar_type"}, staleIDs(answers))
	require.Equal(t, []string{"detail: collar: shirt collar → —"}, answers[2].StaleChanges)
}

func TestDesignQuizPictureRemovedOrRoleChanged(t *testing.T) {
	card, chart := staleTestCard()
	answers := staleTestAnswers(card, chart)
	// Picture 2 changes role, picture 3 is removed; a new picture added at the end changes nothing.
	card.Media[1].Role = entity.TechCardMediaRoleMaterial
	card.Media = append(card.Media[:2], entity.TechCardMediaItem{MediaId: 504, Category: entity.TechCardMediaCategoryMoodboard})
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Equal(t, []string{"pic2", "pic3"}, staleIDs(answers))
	require.Equal(t, []string{"picture 2 role: mood → material"}, answers[4].StaleChanges)
	require.Equal(t, []string{"picture 3: removed from the board"}, answers[5].StaleChanges)

	// Re-numbering alone (picture 1 removed: picture 2 becomes picture 1) is not a change for picture 2.
	card, chart = staleTestCard()
	answers = staleTestAnswers(card, chart)
	card.Media = card.Media[1:]
	designQuizStateOf(card, chart, staleTestNames, staleTestSizeName).mark(answers)
	require.Empty(t, staleIDs(answers))
}

func TestDesignQuizFactChangesFormatting(t *testing.T) {
	old := []entity.DesignQuizFact{{Label: "main fabric", Value: "cotton twill"}, {Label: "detail: hood", Value: "drawcord hood"}, {Label: "fit", Value: "relaxed"}}
	cur := []entity.DesignQuizFact{{Label: "fit", Value: "relaxed"}, {Label: "main fabric", Value: "wool flannel"}, {Label: "lining", Value: "viscose twill"}}
	require.Equal(t, []string{
		"main fabric: cotton twill → wool flannel",
		"detail: hood: drawcord hood → —",
		"lining: — → viscose twill",
	}, entity.DesignQuizFactChanges("materials", old, cur))
	require.Empty(t, entity.DesignQuizFactChanges("fit", old, old))

	lines := []string{"a", "b", "c", "d", "e", "f"}
	require.Equal(t, []string{"a", "b", "c", "d", "+2 more"}, entity.DesignQuizCapLines(lines))
	require.Equal(t, lines[:4], entity.DesignQuizCapLines(lines[:4]))

	var many []entity.DesignQuizFact
	for i := 0; i < 6; i++ {
		many = append(many, entity.DesignQuizFact{Label: "line " + string(rune('a'+i)), Value: "x"})
	}
	answers := []entity.TechCardQuizAnswer{{Topic: "materials", Facts: many}}
	entity.MarkDesignQuizStale(answers, "", func(string, int) []entity.DesignQuizFact { return []entity.DesignQuizFact{} })
	require.True(t, answers[0].Stale)
	require.Len(t, answers[0].StaleChanges, 5)
	require.Equal(t, "+2 more", answers[0].StaleChanges[4])
}

func TestDesignQuizLegacyRowFallsBackToFingerprint(t *testing.T) {
	card, chart := staleTestCard()
	st := designQuizStateOf(card, chart, staleTestNames, staleTestSizeName)
	legacy := []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "a", Category: "design"}, Selected: []string{"x"}, Fingerprint: st.fingerprint},
		{Question: entity.DesignQuizQuestion{ID: "b", Category: "design"}, Selected: []string{"x"}, Fingerprint: strings.Repeat("0", 64)},
		{Question: entity.DesignQuizQuestion{ID: "c", Category: "design"}, Selected: []string{"x"}}, // pre-0392: fresh
	}
	st.mark(legacy)
	require.Equal(t, []string{"b"}, staleIDs(legacy))
	require.Equal(t, []string{entity.DesignQuizLegacyStaleLine}, legacy[1].StaleChanges)
	require.Equal(t, "the card changed (answered before per-topic tracking)", entity.DesignQuizLegacyStaleLine)

	// Unknown current facts (unreadable chart): snapshot rows stay fresh.
	answers := staleTestAnswers(card, chart)
	card.BomItems = nil
	entity.MarkDesignQuizStale(answers, "", nil)
	require.Empty(t, staleIDs(answers))
}

// The DESIGN sign-off digest ignores topic, facts and stale state: re-confirming is not an edit.
func TestDesignQuizDigestIgnoresTopicFacts(t *testing.T) {
	card, chart := staleTestCard()
	answers := staleTestAnswers(card, chart)
	plain := make([]entity.TechCardQuizAnswer, len(answers))
	for i, a := range answers {
		plain[i] = entity.TechCardQuizAnswer{Question: a.Question, Selected: a.Selected, FreeText: a.FreeText, Skipped: a.Skipped}
	}
	answers[1].Stale, answers[1].StaleChanges = true, []string{"x"}
	require.Equal(t, entity.DesignQuizAnswersDigest(plain), entity.DesignQuizAnswersDigest(answers))
}

// §4: dedupe runs against FRESH answers only — a stale key stays askable once; a fresh key is closed;
// re-questions of stale answers are sorted to the front, stable.
func TestDesignQuizStaleKeysAskableAndFirst(t *testing.T) {
	saved := []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "shell", DecisionKey: "shell_fabric", Question: "Main shell fabric?"}, Selected: []string{"twill"}, Stale: true},
		{Question: entity.DesignQuizQuestion{ID: "collar", DecisionKey: "collar_type", Question: "Which collar?"}, Selected: []string{"shirt"}, Stale: true},
		{Question: entity.DesignQuizQuestion{ID: "chest", DecisionKey: "chest_room", Question: "Chest room?"}, Selected: []string{"easy"}},
	}
	raw := `{"questions":[
	 {"id":"season","decision_key":"season","category":"use","kind":"single","question":"Season?","options":["ss","aw"]},
	 {"id":"chest_again","decision_key":"chest_room","category":"fit","kind":"single","question":"Room across the chest?","options":["close","easy"]},
	 {"id":"clarify_shell","decision_key":"shell_fabric","category":"materials","kind":"single","question":"Still twill with wool on the BOM?","options":["twill","wool flannel"]},
	 {"id":"fabric_b","decision_key":"shell_fabric","category":"materials","kind":"single","question":"Shell fabric again?","options":["twill","wool"]},
	 {"id":"lining","decision_key":"lining_insulation","category":"materials","kind":"single","question":"Lining?","options":["none","half"]},
	 {"id":"collar_shape","decision_key":"collar_type","category":"details","kind":"single","question":"Collar shape now?","options":["shirt","band"]}
	]}`
	qs, st, ok := parseDesignQuizCounted(raw, "jacket", saved)
	require.True(t, ok)
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	require.Equal(t, []string{"clarify_shell", "collar_shape", "season", "lining"}, ids)
	require.Equal(t, 2, st.repeated, "chest_again (fresh key) and fabric_b (shell already re-asked)")
	require.Equal(t, "shell_fabric", qs[0].DecisionKey)
}

// §4: the user prompt renders a stale answer with its changes in the bracket; the system prompt
// carries the STALE rule verbatim; the wire carries stale_changes.
func TestDesignQuizPromptStaleRendering(t *testing.T) {
	card := &entity.TechCard{}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 502, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMaterial},
	}
	card.QuizAnswers = []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "shell", Category: "materials", Part: "whole", Question: "Main shell fabric?"},
			Selected: []string{"cotton twill"}, Stale: true,
			StaleChanges: []string{"main fabric: cotton twill → wool flannel", "lining: — → viscose twill"}},
		{Question: entity.DesignQuizQuestion{ID: "pic", Category: "design", Part: "whole", MediaID: 502, Question: "What to take?"},
			Selected: []string{"the colour"}, Stale: true, StaleChanges: []string{"picture 1 role: mood → material"}},
	}
	user := designQuizUserPrompt(card, nil, []int{502}, "jacket", "")
	require.Contains(t, user, "- [shell · materials · whole · STALE: main fabric: cotton twill → wool flannel; lining: — → viscose twill] Main shell fabric? → cotton twill\n")
	require.Contains(t, user, "- [pic · design · whole · about picture 1 (material reference) · STALE: picture 1 role: mood → material] What to take? → the colour\n")
	require.Contains(t, designQuizSystemPrompt, designQuizStaleRule)
	require.Equal(t, "A STALE answer was given before the card changed as shown. If the change contradicts or reopens it, ask ONE short clarifying question about it FIRST (id clarify_<that id>, same decision_key, same category/part/picture); if it still holds, ask nothing about it.", designQuizStaleRule)

	pb := designQuizAnswersToPb(card.QuizAnswers)
	require.Equal(t, []string{"main fabric: cotton twill → wool flannel", "lining: — → viscose twill"}, pb[0].GetStaleChanges())
	require.True(t, pb[0].GetStale())
}
