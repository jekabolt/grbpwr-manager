//go:build quizlive

package admin

// LIVE A/B HARNESS for the moodboard quiz (61-QUICKWINS W-B7, 60-REVIEW-fable §4). Never compiled by
// CI or a plain `go test`: the build tag keeps it out. It touches no database and no store — the
// fixture cards are literals, the prompts are OUR builders (designQuizSystemPrompt +
// designQuizUserPrompt), the answer goes through OUR parser (parseDesignQuizCounted).
//
// Run (from the repo root):
//
//	OPENROUTER_API_KEY=… \
//	QUIZ_MODELS=anthropic/claude-opus-5.5:medium,anthropic/claude-opus-5.5:low,anthropic/claude-sonnet-5.5:medium \
//	QUIZ_RUNS=1 \
//	go test -tags quizlive ./internal/apisrv/admin/ -run TestDesignQuizLive -v -count=1 -timeout 60m
//
// Env: OPENROUTER_API_KEY (required, else SKIP; never printed), QUIZ_MODELS (slug[:effort],…; default
// opus-5.5 medium + sonnet-5.5 medium), QUIZ_RUNS (per model × fixture, default 1), QUIZ_OUT (output
// folder, default <plans>/moodboard-quiz/live), QUIZ_FIXTURES (image URL file, default
// $QUIZ_OUT/fixtures.json), QUIZ_ONLY (comma list of fixture names).
// Output: $QUIZ_OUT/<fixture>/<model>_<effort>_<n>.json + $QUIZ_OUT/summary.md.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

const quizLiveEndpoint = "https://openrouter.ai/api/v1/chat/completions"

type quizLiveFixture struct {
	name   string
	family string
	card   *entity.TechCard
	pom    string
	// check — fixture-specific violations (empty = pass).
	check func(qs []entity.DesignQuizQuestion, st designQuizParseStats) []string
}

func quizLiveNull(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func quizLiveBoard(card *entity.TechCard, n int) []int {
	ids := make([]int, 0, n)
	for i := 1; i <= n; i++ {
		card.Media = append(card.Media, entity.TechCardMediaItem{MediaId: i, Category: entity.TechCardMediaCategoryMoodboard})
		ids = append(ids, i)
	}
	return ids
}

func quizLiveFixtures() []quizLiveFixture {
	jacket := &entity.TechCard{}
	jacket.Name = "field jacket"
	jacket.Fit = quizLiveNull("oversized")
	jacket.TargetGender = quizLiveNull("male")
	jacket.Concept = quizLiveNull("Military field jacket reworked as an everyday outer layer.\nWashed cotton, four front pockets, roomy through the body.")

	trousers := &entity.TechCard{}
	trousers.Name = "wide trousers"
	trousers.Fit = quizLiveNull("relaxed")
	trousers.Concept = quizLiveNull("Wide straight trousers in a dry wool, worn high with a tucked shirt.")
	trousers.Details = []entity.TechCardDetail{
		{Key: quizLiveNull("waistband"), Text: quizLiveNull("elastic back, flat front, 4 cm wide")},
		{Key: quizLiveNull("hem"), Text: quizLiveNull("raw edge, no turn-up")},
	}
	trousers.MeasurementUnit = entity.TechCardUnitCm
	trousers.BaseSampleSizeId = sql.NullInt32{Int32: 2, Valid: true}
	cell := func(m int, v string) entity.StyleSizeChartCell {
		return entity.StyleSizeChartCell{SizeID: 2, MeasurementNameID: m, Value: decimal.RequireFromString(v)}
	}
	trousersPOM := designQuizBaseMeasurements(trousers, entity.StyleSizeChart{Cells: []entity.StyleSizeChartCell{
		cell(1, "82"), cell(2, "108"), cell(3, "31"), cell(4, "78"), cell(5, "29"),
	}}, map[int]string{1: "waist", 2: "hip", 3: "front rise", 4: "inseam", 5: "leg opening"},
		func(int) string { return "M" })

	shirt := &entity.TechCard{}
	shirt.Name = "work shirt"
	shirt.Fit = quizLiveNull("regular")
	shirt.Concept = quizLiveNull("Denim work shirt, washed, worn open over a tee or buttoned.")
	ans := func(id, cat, part, q string, opts []string, sel ...string) entity.TechCardQuizAnswer {
		return entity.TechCardQuizAnswer{Question: entity.DesignQuizQuestion{ID: id, Category: cat, Part: part, Question: q, Options: opts}, Selected: sel}
	}
	skipped := ans("lining", "materials", "whole", "Is the body lined?", []string{"unlined", "half lined"})
	skipped.Skipped = true
	shirt.QuizAnswers = []entity.TechCardQuizAnswer{
		ans("fit_basis", "fit", "whole", "What governs the base fit?", []string{"our existing block", "develop a new block"}, "our existing block"),
		ans("chest_room", "fit", "whole", "How much room at the chest?", []string{"easy, natural movement", "relaxed, visibly loose"}, "easy, natural movement"),
		ans("collar_type", "details", "collar", "Collar type?", []string{"point collar", "button-down"}, "point collar"),
		ans("main_fabric", "materials", "whole", "Main shell fabric?", []string{"denim 8 oz", "denim 12 oz"}, "denim 8 oz"),
		ans("chest_pockets", "details", "pocket", "Chest pockets?", []string{"none", "one", "two"}, "none"),
		skipped,
	}

	// 70-SEAMS D4: seam and colourway fixtures.
	denim := &entity.TechCard{}
	denim.Name = "denim trucker"
	denim.Fit = quizLiveNull("relaxed")
	denim.Concept = quizLiveNull("Unlined 12 oz denim trucker jacket, contrast gold topstitching, two chest pockets, metal shank buttons.")
	// denimDifferentWashes — the board shows ≥2 pictures in different washes (then a colourway
	// question is required). The current Commons pictures are one mid-blue wash.
	const denimDifferentWashes = false

	tee := &entity.TechCard{}
	tee.Name = "heavy tee"
	tee.Fit = quizLiveNull("boxy")
	tee.Concept = quizLiveNull("Heavy 240 gsm cotton jersey tee, boxy, dropped shoulder, ribbed neck.")

	shell := &entity.TechCard{}
	shell.Name = "rain shell"
	shell.Fit = quizLiveNull("regular")
	shell.Concept = quizLiveNull("3-layer waterproof shell, fully seam-sealed, hood, two-way front zip.")

	coat := &entity.TechCard{}
	coat.Name = "melton overcoat"
	coat.Fit = quizLiveNull("regular")
	coat.Concept = quizLiveNull("Fully lined wool melton overcoat, single-breasted.")
	lined := ans("lining", "materials", "lining", "Is the body lined?", []string{"unlined", "half lined", "fully lined"}, "fully lined")
	lined.Question.DecisionKey = "lining_insulation"
	coat.QuizAnswers = []entity.TechCardQuizAnswer{lined}

	return []quizLiveFixture{
		{name: "denim_jacket", family: "jacket", card: denim, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			main := quizLiveByKey(qs, "main_seam")
			if main == nil {
				out = append(out, "no main_seam question")
			} else if kinds := quizLiveSeamKinds(main.Options); len(kinds) < 2 {
				out = append(out, fmt.Sprintf("main_seam options resolve to %d sm_ kinds (want ≥2): %s", len(kinds), strings.Join(main.Options, " / ")))
			}
			for _, o := range quizLiveAllOptions(qs) {
				if designQuizSeamOf(o) == "sm_french" {
					out = append(out, "French seam offered on denim: "+o)
				}
			}
			if denimDifferentWashes && quizLiveByKey(qs, "colourway_count") == nil && quizLiveByKey(qs, "colourway_colours") == nil {
				out = append(out, "no colourway question with washes on the board")
			}
			return out
		}},
		{name: "knit_tee", family: "tee", card: tee, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			knit := map[string]bool{"": true, "sm_plain_overlock": true, "sm_safety": true, "sm_flatlock": true,
				"sm_hem_cover": true, "sm_hem_raw": true, "sm_hem_bound": true}
			for _, o := range quizLiveAllOptions(qs) {
				if k := designQuizSeamOf(o); !knit[k] {
					out = append(out, "woven construction ("+k+") on a jersey tee: "+o)
				}
			}
			if quizLiveByKey(qs, "hem_finish") == nil && quizLiveByKey(qs, "neck_finish") == nil {
				out = append(out, "no hem_finish or neck_finish question")
			}
			return out
		}},
		{name: "shell", family: "jacket", card: shell, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			sealed := false
			for _, o := range quizLiveAllOptions(qs) {
				if k := designQuizSeamOf(o); k == "sm_taped" || k == "sm_bonded" {
					sealed = true
				}
			}
			if !sealed {
				out = append(out, "no taped or bonded option on a waterproof shell")
			}
			for _, q := range qs {
				only := len(q.Options) > 0
				for _, o := range q.Options {
					if k := designQuizSeamOf(o); k != "sm_plain_overlock" && k != "sm_safety" {
						only = false
					}
				}
				if only {
					out = append(out, "overlock-only seam choice on a shell: "+q.Question)
				}
			}
			return out
		}},
		{name: "lined_coat", family: "coat", card: coat, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			if q := quizLiveByKey(qs, "extra_seams"); q != nil {
				out = append(out, "asked interior constructions on a fully lined coat: "+q.Question)
			}
			for _, o := range quizLiveAllOptions(qs) {
				switch designQuizSeamOf(o) {
				case "sm_hong_kong", "sm_bound", "sm_plain_overlock":
					out = append(out, "hidden interior finish offered on a lined coat: "+o)
				}
			}
			return out
		}},
		{name: "jacket", family: "jacket", card: jacket, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			if !quizLiveHas(qs, "fit_basis") {
				out = append(out, "no fit_basis")
			}
			return out
		}},
		{name: "trousers", family: "trousers", card: trousers, pom: trousersPOM, check: func(qs []entity.DesignQuizQuestion, _ designQuizParseStats) []string {
			var out []string
			for _, q := range qs {
				l := strings.ToLower(q.ID + " " + q.Question)
				if strings.Contains(l, "waistband") || q.Part == "waistband" {
					out = append(out, "asked waistband (Known): "+q.Question)
				}
				if (strings.Contains(l, "hem finish") || strings.Contains(l, "turn-up")) && q.Category != "fit" {
					out = append(out, "asked hem finish (Known): "+q.Question)
				}
				if strings.Contains(l, "inseam") || strings.Contains(l, "leg opening width") {
					out = append(out, "asked a supplied POM: "+q.Question)
				}
			}
			return out
		}},
		{name: "shirt", family: "shirt", card: shirt, check: func(qs []entity.DesignQuizQuestion, st designQuizParseStats) []string {
			var out []string
			if len(qs) == 0 || !strings.HasPrefix(qs[0].ID, "clarify_") {
				out = append(out, "first question is not clarify_")
			}
			if st.repeated > 0 {
				out = append(out, fmt.Sprintf("%d repeats of answered points dropped by the parser", st.repeated))
			}
			return out
		}},
	}
}

func quizLiveHas(qs []entity.DesignQuizQuestion, id string) bool {
	for _, q := range qs {
		if q.ID == id {
			return true
		}
	}
	return false
}

func quizLiveByKey(qs []entity.DesignQuizQuestion, key string) *entity.DesignQuizQuestion {
	for i := range qs {
		if qs[i].DecisionKey == key {
			return &qs[i]
		}
	}
	return nil
}

func quizLiveAllOptions(qs []entity.DesignQuizQuestion) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Options...)
	}
	return out
}

// quizLiveSeamKinds — the distinct sm_ keys the options resolve to (the client's chip icons).
func quizLiveSeamKinds(opts []string) map[string]bool {
	out := map[string]bool{}
	for _, o := range opts {
		if k := designQuizSeamOf(o); k != "" {
			out[k] = true
		}
	}
	return out
}

// quizLiveSeamKeys — the decision keys that count as a seam question in the summary.
var quizLiveSeamKeys = map[string]bool{"main_seam": true, "extra_seams": true, "hem_finish": true, "neck_finish": true}

// quizLiveEdgeKeys — the decision keys that count as an edge question in the summary (90-EDGES).
var quizLiveEdgeKeys = map[string]bool{"edge_finish_main": true, "edge_exceptions": true, "neck_finish": true,
	"armhole_finish": true, "sleeve_finish": true, "front_edge_finish": true, "waistband_finish": true, "leg_finish": true,
	"pocket_edge_finish": true, "vent_finish": true, "hem_finish": true, "hood_edge_finish": true}

type quizLiveModel struct{ slug, effort string }

func quizLiveModels() []quizLiveModel {
	spec := os.Getenv("QUIZ_MODELS")
	if strings.TrimSpace(spec) == "" {
		spec = "anthropic/claude-opus-5.5:medium,anthropic/claude-sonnet-5.5:medium"
	}
	var out []quizLiveModel
	for _, m := range strings.Split(spec, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		slug, effort, _ := strings.Cut(m, ":")
		out = append(out, quizLiveModel{slug: slug, effort: effort})
	}
	return out
}

type quizLiveUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	Details          struct {
		Reasoning int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type quizLiveResult struct {
	Fixture      string                      `json:"fixture"`
	Model        string                      `json:"model"`
	Effort       string                      `json:"effort"`
	Run          int                         `json:"run"`
	Seconds      float64                     `json:"seconds"`
	FinishReason string                      `json:"finish_reason"`
	Usage        quizLiveUsage               `json:"usage"`
	HTTPStatus   int                         `json:"http_status"`
	Error        string                      `json:"error,omitempty"`
	Raw          string                      `json:"raw"`
	ParsedOK     bool                        `json:"parsed_ok"`
	Stats        map[string]int              `json:"stats"`
	Questions    []entity.DesignQuizQuestion `json:"questions"`
	Score        map[string]int              `json:"score"`
	Violations   []string                    `json:"violations"`
}

func TestDesignQuizLive(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY is not set — the live quiz A/B calls OpenRouter and spends money; set it to run")
	}
	out := os.Getenv("QUIZ_OUT")
	if out == "" {
		abs, err := filepath.Abs("../../../../tmp/plans/moodboard-quiz/live")
		if err != nil {
			t.Fatal(err)
		}
		out = abs
	}
	fixturesPath := os.Getenv("QUIZ_FIXTURES")
	if fixturesPath == "" {
		fixturesPath = filepath.Join(out, "fixtures.json")
	}
	rawFix, err := os.ReadFile(fixturesPath)
	if err != nil {
		t.Fatalf("read fixtures %s: %v", fixturesPath, err)
	}
	var urls map[string]json.RawMessage
	if err := json.Unmarshal(rawFix, &urls); err != nil {
		t.Fatalf("fixtures json: %v", err)
	}
	runs := 1
	if v, err := strconv.Atoi(os.Getenv("QUIZ_RUNS")); err == nil && v > 0 {
		runs = v
	}
	only := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("QUIZ_ONLY"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			only[n] = true
		}
	}

	var results []quizLiveResult
	for _, fx := range quizLiveFixtures() {
		if len(only) > 0 && !only[fx.name] {
			continue
		}
		var list []string
		if raw, ok := urls[fx.name]; ok {
			_ = json.Unmarshal(raw, &list)
		}
		images := make([]string, 0, len(list))
		for _, u := range list {
			d, err := quizLiveDataURL(u)
			if err != nil {
				t.Fatalf("%s: picture %s: %v", fx.name, u, err)
			}
			images = append(images, d)
		}
		attached := quizLiveBoard(fx.card, len(images))
		user := designQuizUserPrompt(fx.card, designMoodSnapshot(fx.card), attached, fx.family, fx.pom)
		if err := os.MkdirAll(filepath.Join(out, fx.name), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(out, fx.name, "prompt_user.txt"), []byte(user), 0o644)
		for _, m := range quizLiveModels() {
			for n := 1; n <= runs; n++ {
				r := quizLiveCall(key, m, user, images)
				r.Fixture, r.Run = fx.name, n
				qs, st, ok := parseDesignQuizCounted(r.Raw, fx.family, fx.card.QuizAnswers)
				r.ParsedOK, r.Questions = ok, qs
				r.Stats = map[string]int{"raw": st.raw, "kept": st.kept, "invalid": st.invalid,
					"repeated": st.repeated, "capped": st.capped, "parts_fixed": st.partsFixed}
				if ok && st.unusable() {
					r.Violations = append(r.Violations, "unusable: nothing survived validation")
				}
				r.Score = quizLiveScore(r.Raw, qs)
				if ok {
					r.Violations = append(r.Violations, fx.check(qs, st)...)
				} else if r.Error == "" {
					r.Violations = append(r.Violations, "not the promised JSON")
				}
				name := strings.NewReplacer("/", "_", ":", "_", ".", "-").Replace(m.slug) + "_" + quizLiveOr(m.effort, "default") + "_" + strconv.Itoa(n) + ".json"
				b, _ := json.MarshalIndent(r, "", "  ")
				_ = os.WriteFile(filepath.Join(out, fx.name, name), b, 0o644)
				t.Logf("%s · %s:%s #%d → %d questions, %.1fs, $%.4f, finish=%s, violations=%d",
					fx.name, m.slug, m.effort, n, len(qs), r.Seconds, r.Usage.Cost, r.FinishReason, len(r.Violations))
				results = append(results, r)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(out, "summary.md"), []byte(quizLiveSummary(results)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("summary: %s", filepath.Join(out, "summary.md"))
}

func quizLiveOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// quizLiveDataURL downloads a public picture and returns it as a data: URL (the provider never has to
// fetch a third-party host).
func quizLiveDataURL(u string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "grbpwr-quiz-live-harness/1.0")
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return "", err
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(b)
	}
	return "data:" + strings.Split(ct, ";")[0] + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// quizLiveCall — one OpenRouter call in the dialect prod uses (oaichat DialectOpenRouter): system
// text, user turn as parts (text then pictures), max_tokens, temperature 0.2, JSON mode,
// reasoning:{effort}, usage:{include:true}. The key goes only into the Authorization header.
func quizLiveCall(key string, m quizLiveModel, user string, images []string) quizLiveResult {
	r := quizLiveResult{Model: m.slug, Effort: m.effort}
	parts := []map[string]any{{"type": "text", "text": user}}
	for _, img := range images {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img}})
	}
	body := map[string]any{
		"model": m.slug,
		"messages": []any{
			map[string]any{"role": "system", "content": designQuizSystemPrompt},
			map[string]any{"role": "user", "content": parts},
		},
		"max_tokens":      designQuizMaxTokens,
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
		"usage":           map[string]bool{"include": true},
	}
	if m.effort != "" {
		body["reasoning"] = map[string]string{"effort": m.effort}
	}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, quizLiveEndpoint, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "grbpwr quiz live A/B")
	started := time.Now()
	res, err := http.DefaultClient.Do(req)
	r.Seconds = time.Since(started).Seconds()
	if err != nil {
		r.Error = "transport: " + strings.ReplaceAll(err.Error(), key, "<key>")
		return r
	}
	defer res.Body.Close()
	r.HTTPStatus = res.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		r.Error = "http " + strconv.Itoa(res.StatusCode) + ": " + aiBoundedText(strings.ReplaceAll(string(respBody), key, "<key>"), 600)
		return r
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage quizLiveUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		r.Error = "decode: " + err.Error()
		return r
	}
	if parsed.Error != nil {
		r.Error = "provider: " + aiBoundedText(parsed.Error.Message, 600)
	}
	r.Usage = parsed.Usage
	if len(parsed.Choices) > 0 {
		r.Raw = parsed.Choices[0].Message.Content
		r.FinishReason = parsed.Choices[0].FinishReason
	}
	return r
}

var (
	quizLiveNumberRe = regexp.MustCompile(`\d+\s*(cm|%)`)
	quizLiveBanned   = regexp.MustCompile(`(?i)\b(standard|classic|normal|as in the picture|not sure|depends)\b`)
)

// quizLiveScore — the automatic discipline counts (60-REVIEW-fable §4.4) over the kept questions and
// the raw list's option counts.
func quizLiveScore(raw string, qs []entity.DesignQuizQuestion) map[string]int {
	s := map[string]int{"questions": len(qs)}
	for _, q := range qs {
		s["cat_"+q.Category]++
		if strings.HasPrefix(q.ID, "clarify_") {
			s["clarify"]++
		}
		for _, c := range q.Contradicts {
			if c {
				s["contradicts"]++
				break
			}
		}
		if len(strings.Fields(q.Question)) > 15 {
			s["question_over_15_words"]++
		}
		if q.Category == entity.DesignQuizCategoryDetails && q.Part == entity.DesignQuizPartWhole {
			s["details_on_whole"]++
		}
		if quizLiveSeamKeys[q.DecisionKey] {
			s["seam_q"]++
		}
		if quizLiveEdgeKeys[q.DecisionKey] {
			s["edge_q"]++
		}
		for _, o := range q.Options {
			if designQuizSeamOf(o) != "" {
				s["sm_icons"]++
			}
			lo := strings.ToLower(strings.TrimSpace(o))
			if quizLiveBanned.MatchString(o) || lo == "regular" || lo == "other" {
				s["banned_option_words"]++
			}
			if len(strings.Fields(o)) > 8 {
				s["option_over_8_words"]++
			}
			if q.Category == entity.DesignQuizCategoryFit && quizLiveNumberRe.MatchString(o) {
				s["fit_invented_numbers"]++
			}
		}
	}
	if items, ok := designQuizExtract(raw); ok {
		for _, it := range items {
			if n := len(it.Options); n < designQuizMinOptions || n > designQuizMaxOptions {
				s["raw_options_out_of_2_6"]++
			}
		}
	}
	return s
}

func quizLiveSummary(rs []quizLiveResult) string {
	var b strings.Builder
	b.WriteString("# Moodboard quiz — live A/B (" + time.Now().UTC().Format("2006-01-02 15:04 UTC") + ")\n\n")
	b.WriteString("Generated by internal/apisrv/admin/design_quiz_live_test.go (build tag quizlive). Prompts = production builders; parse = production parser.\n\n")
	b.WriteString("| fixture | model | effort | # | q | fit | clarify | banned | fit cm/% | seam q | edges asked | sm icons | parts fixed | invalid | repeated | finish | sec | prompt tok | compl tok | reasoning tok | USD | violations |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		viol := strings.Join(r.Violations, "; ")
		if r.Error != "" {
			viol = "ERROR " + r.Error
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %s | %.1f | %d | %d | %d | %.4f | %s |\n",
			r.Fixture, r.Model, quizLiveOr(r.Effort, "default"), r.Run, r.Score["questions"], r.Score["cat_fit"], r.Score["clarify"],
			r.Score["banned_option_words"], r.Score["fit_invented_numbers"], r.Score["seam_q"], r.Score["edge_q"], r.Score["sm_icons"], r.Stats["parts_fixed"], r.Stats["invalid"],
			r.Stats["repeated"], r.FinishReason, r.Seconds, r.Usage.PromptTokens, r.Usage.CompletionTokens,
			r.Usage.Details.Reasoning, r.Usage.Cost, strings.ReplaceAll(viol, "|", "/"))
	}
	b.WriteString("\n## Questions per run (blind read)\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "\n### %s · %s · %s · #%d\n", r.Fixture, r.Model, quizLiveOr(r.Effort, "default"), r.Run)
		if len(r.Questions) == 0 {
			b.WriteString("(none)\n")
		}
		for i, q := range r.Questions {
			fmt.Fprintf(&b, "%d. [%s · %s · %s] %s → %s\n", i+1, q.ID, q.Category, q.Part, q.Question, strings.Join(q.Options, " / "))
		}
	}
	return b.String()
}
