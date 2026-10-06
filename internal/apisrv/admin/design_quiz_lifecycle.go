package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// ─── 62-DEEP-FIXES: staleness (D1) and lifecycle (D2) of the moodboard quiz answers ───

// designQuizBaseSizeOf — the size whose measurements the quiz reads: the card's base sample size,
// else the grade rule's base, else the only size the chart carries; 0 when none of these has a cell.
func designQuizBaseSizeOf(card *entity.TechCard, chart entity.StyleSizeChart) int {
	if card == nil || len(chart.Cells) == 0 {
		return 0
	}
	has := func(size int) bool {
		for _, c := range chart.Cells {
			if c.SizeID == size {
				return true
			}
		}
		return false
	}
	switch {
	case card.BaseSampleSizeId.Valid && has(int(card.BaseSampleSizeId.Int32)):
		return int(card.BaseSampleSizeId.Int32)
	case chart.GradeBaseSizeID > 0 && has(chart.GradeBaseSizeID):
		return chart.GradeBaseSizeID
	}
	only := chart.Cells[0].SizeID
	for _, c := range chart.Cells {
		if c.SizeID != only {
			return 0
		}
	}
	return only
}

// designQuizCardFingerprint — sha256 of the card's STRUCTURED inputs only (D1): category id, fit,
// target gender, detail rows (key + text), BOM line names + compositions, base-size measurement
// values. NOT the concept (apply-to-description writes it) and NOT the board pictures (adding one
// must not stale every answer). Values are trimmed so a whitespace-only edit is not a change.
func designQuizCardFingerprint(card *entity.TechCard, chart entity.StyleSizeChart) string {
	if card == nil {
		return ""
	}
	details := make([][2]string, 0, len(card.Details))
	for _, d := range card.Details {
		k, t := strings.TrimSpace(d.Key.String), designOneLine(d.Text.String)
		if k == "" && t == "" {
			continue
		}
		details = append(details, [2]string{k, t})
	}
	bom := make([][2]string, 0, len(card.BomItems))
	for _, b := range card.BomItems {
		n, c := designOneLine(b.Name), designOneLine(b.Composition.String)
		if n == "" && c == "" {
			continue
		}
		bom = append(bom, [2]string{n, c})
	}
	type cell struct {
		M int    `json:"m"`
		V string `json:"v"`
	}
	var cells []cell
	if base := designQuizBaseSizeOf(card, chart); base > 0 {
		for _, c := range chart.Cells {
			if c.SizeID == base {
				cells = append(cells, cell{M: c.MeasurementNameID, V: c.Value.String()})
			}
		}
		sort.Slice(cells, func(i, j int) bool { return cells[i].M < cells[j].M })
	}
	category := 0
	if card.CategoryId.Valid {
		category = int(card.CategoryId.Int32)
	}
	b, _ := json.Marshal([]any{
		category, strings.TrimSpace(card.Fit.String), strings.TrimSpace(card.TargetGender.String),
		details, bom, cells,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ─── 98-STALE §1: per-topic staleness ───

// designQuizFitTokens — decision-key / part words that make an answer depend on the fit facts
// (measurements) whatever its category: a length, a width, a rise, an ease.
var designQuizFitTokens = map[string]bool{"length": true, "width": true, "rise": true, "ease": true}

// designQuizTopicOf — THE mapping of a question to the one topic of card facts its answer depends on
// (98-STALE §1). Overrides first: a picture question → picture; the palette part or a colourway key
// → materials; a seam / hardware / label part → construction; a decision key or part about a
// length, width, rise or ease → fit (not on a materials question: a tape width is a material). Then
// the category: fit → fit, materials → materials, design → design, details / finish / use →
// construction.
func designQuizTopicOf(q entity.DesignQuizQuestion) string {
	key, part := strings.ToLower(q.DecisionKey), strings.ToLower(q.Part)
	tokens := func(s string) []string { return strings.Split(s, "_") }
	has := func(set map[string]bool, s string) bool {
		for _, t := range tokens(s) {
			if set[t] {
				return true
			}
		}
		return false
	}
	colourway := map[string]bool{"colourway": true, "colorway": true, "colourways": true, "colorways": true}
	switch {
	case q.MediaID != 0:
		return entity.DesignQuizTopicPicture
	case part == designQuizPaletteKey || key == designQuizPaletteKey || has(colourway, key):
		return entity.DesignQuizTopicMaterials
	case strings.HasPrefix(part, "sm_") || strings.HasPrefix(part, "hw_") || strings.HasPrefix(part, "lbl_"):
		return entity.DesignQuizTopicConstruction
	case q.Category != entity.DesignQuizCategoryMaterials && (has(designQuizFitTokens, key) || has(designQuizFitTokens, part)):
		return entity.DesignQuizTopicFit
	}
	switch q.Category {
	case entity.DesignQuizCategoryFit:
		return entity.DesignQuizTopicFit
	case entity.DesignQuizCategoryMaterials:
		return entity.DesignQuizTopicMaterials
	case entity.DesignQuizCategoryDesign:
		return entity.DesignQuizTopicDesign
	}
	return entity.DesignQuizTopicConstruction
}

// designQuizFactLabelRunes / designQuizFactValueRunes — a fact's bounds (the snapshot and the change
// lines stay short; both sides are bounded the same way, so the bound never reads as a change).
const (
	designQuizFactLabelRunes = 60
	designQuizFactValueRunes = 80
)

// designQuizFactSet — the card's facts per topic NOW, plus the board (picture → «picture N», role)
// the picture topic reads.
type designQuizFactSet struct {
	byTopic   map[string][]entity.DesignQuizFact
	pictureAt map[int]int
	roles     map[int]entity.TechCardMediaRole
}

// designQuizFactsOf — the card's current facts per topic (98-STALE §1): fit = fit label, target
// gender, base-size measurement values ("chest (M)" → "54"); materials = BOM lines (name →
// composition); construction = category + detail rows ("detail: collar" → text); design = category.
// names / sizeName resolve measurement and size ids (the dictionary caches in production).
func designQuizFactsOf(card *entity.TechCard, chart entity.StyleSizeChart, names map[int]string, sizeName func(int) string) designQuizFactSet {
	fs := designQuizFactSet{byTopic: map[string][]entity.DesignQuizFact{}}
	if card == nil {
		return fs
	}
	add := func(topic, label, value string) {
		label = aiBoundedText(designOneLine(label), designQuizFactLabelRunes)
		value = aiBoundedText(designOneLine(value), designQuizFactValueRunes)
		if label == "" || value == "" {
			return
		}
		list := fs.byTopic[topic]
		uniq, n := label, 1
		for {
			dup := false
			for _, f := range list {
				if f.Label == uniq {
					dup = true
					break
				}
			}
			if !dup {
				break
			}
			n++
			uniq = label + " (" + strconv.Itoa(n) + ")"
		}
		fs.byTopic[topic] = append(list, entity.DesignQuizFact{Label: uniq, Value: value})
	}

	// fit
	add(entity.DesignQuizTopicFit, "fit", card.Fit.String)
	add(entity.DesignQuizTopicFit, "gender", card.TargetGender.String)
	if base := designQuizBaseSizeOf(card, chart); base > 0 {
		size := "base size"
		if sizeName != nil {
			if n := sizeName(base); n != "" {
				size = n
			}
		}
		cells := make([]entity.StyleSizeChartCell, 0, len(chart.Cells))
		for _, c := range chart.Cells {
			if c.SizeID == base {
				cells = append(cells, c)
			}
		}
		sort.SliceStable(cells, func(i, j int) bool { return cells[i].MeasurementNameID < cells[j].MeasurementNameID })
		for _, c := range cells {
			name := strings.TrimSpace(strings.ReplaceAll(names[c.MeasurementNameID], "_", " "))
			if name == "" {
				name = "measurement " + strconv.Itoa(c.MeasurementNameID)
			}
			add(entity.DesignQuizTopicFit, name+" ("+size+")", c.Value.String())
		}
	}

	// materials
	for i, b := range card.BomItems {
		name, comp := designOneLine(b.Name), designOneLine(b.Composition.String)
		if name == "" && comp == "" {
			continue
		}
		if name == "" {
			name = "BOM line " + strconv.Itoa(i+1)
		}
		if comp == "" {
			comp = "no composition"
		}
		add(entity.DesignQuizTopicMaterials, name, comp)
	}

	// construction + design: the category, then the detail rows
	category := ""
	if top, sub, typ := designQuizCategoryPath(card); top+sub+typ != "" {
		var path []string
		for _, n := range []string{top, sub, typ} {
			if n = strings.TrimSpace(n); n != "" {
				path = append(path, n)
			}
		}
		category = strings.Join(path, " / ")
	} else if card.CategoryId.Valid && card.CategoryId.Int32 > 0 {
		category = "category " + strconv.Itoa(int(card.CategoryId.Int32))
	}
	add(entity.DesignQuizTopicConstruction, "category", category)
	add(entity.DesignQuizTopicDesign, "category", category)
	for i, d := range card.Details {
		k, t := strings.TrimSpace(d.Key.String), designOneLine(d.Text.String)
		if k == "" && t == "" {
			continue
		}
		label := "detail: " + k
		if k == "" {
			label = "detail " + strconv.Itoa(i+1)
		}
		if t == "" {
			t = "set"
		}
		add(entity.DesignQuizTopicConstruction, label, t)
	}

	// picture: «picture N» in board order, roles
	ids := designBoardMediaIDs(card)
	fs.pictureAt = make(map[int]int, len(ids))
	for i, id := range ids {
		fs.pictureAt[id] = i + 1
	}
	fs.roles = designBoardRoles(card)
	return fs
}

// of — the facts the topic has now (never nil: an empty list is a real snapshot). The picture topic
// is per picture: on the board → its number (metadata), "on the board", its role; else "removed".
func (fs designQuizFactSet) of(topic string, mediaID int) []entity.DesignQuizFact {
	if topic == entity.DesignQuizTopicPicture {
		n, ok := fs.pictureAt[mediaID]
		if !ok {
			return []entity.DesignQuizFact{{Label: entity.DesignQuizFactPictureBoard, Value: entity.DesignQuizPictureRemoved}}
		}
		role := string(fs.roles[mediaID])
		if role == "" {
			role = entity.DesignQuizRoleNone
		}
		return []entity.DesignQuizFact{
			{Label: entity.DesignQuizFactPictureNumber, Value: strconv.Itoa(n)},
			{Label: entity.DesignQuizFactPictureBoard, Value: entity.DesignQuizPictureOnBoard},
			{Label: entity.DesignQuizFactPictureRole, Value: role},
		}
	}
	out := fs.byTopic[topic]
	if out == nil {
		return []entity.DesignQuizFact{}
	}
	return append([]entity.DesignQuizFact(nil), out...)
}

// designQuizStaleState — what staleness reads now: the whole-card fingerprint (legacy rows) and the
// per-topic facts. ok=false when the size chart cannot be read (staleness then degrades to
// "everything fresh", never to a refusal).
type designQuizStaleState struct {
	fingerprint string
	facts       designQuizFactSet
}

func designQuizStateOf(card *entity.TechCard, chart entity.StyleSizeChart, names map[int]string, sizeName func(int) string) designQuizStaleState {
	return designQuizStaleState{
		fingerprint: designQuizCardFingerprint(card, chart),
		facts:       designQuizFactsOf(card, chart, names, sizeName),
	}
}

func (s *Server) designQuizCurrentState(ctx context.Context, card *entity.TechCard) (designQuizStaleState, bool) {
	if card == nil {
		return designQuizStaleState{}, false
	}
	chart, err := s.repo.TechCards().GetStyleSizeChart(ctx, card.Id)
	if err != nil {
		slog.Default().WarnContext(ctx, "design quiz: cannot read the size chart for staleness",
			slog.Int("tech_card_id", card.Id), slog.String("err", err.Error()))
		return designQuizStaleState{}, false
	}
	return designQuizStateOf(card, chart, designQuizMeasurementNames(), designQuizSizeName), true
}

// mark sets Stale + StaleChanges on answers against this state.
func (st designQuizStaleState) mark(answers []entity.TechCardQuizAnswer) {
	entity.MarkDesignQuizStale(answers, st.fingerprint, st.facts.of)
}

// stamp gives each upserted answer its topic, the topic's facts now and the card fingerprint: a
// saved (or kept) answer is fresh by definition.
func (st designQuizStaleState) stamp(answers []entity.TechCardQuizAnswer) {
	for i := range answers {
		topic := designQuizTopicOf(answers[i].Question)
		answers[i].Topic = topic
		answers[i].Facts = st.facts.of(topic, answers[i].Question.MediaID)
		answers[i].Fingerprint = st.fingerprint
	}
}

// designQuizMarkStale sets Stale on the card's loaded answers (one chart read; none without answers).
func (s *Server) designQuizMarkStale(ctx context.Context, card *entity.TechCard) {
	if card == nil || len(card.QuizAnswers) == 0 {
		return
	}
	if st, ok := s.designQuizCurrentState(ctx, card); ok {
		st.mark(card.QuizAnswers)
	}
}

// designQuizHasDecisions — the card carries at least one non-skipped answer with content (D3).
func designQuizHasDecisions(card *entity.TechCard) bool {
	if card == nil {
		return false
	}
	for _, a := range card.QuizAnswers {
		if !a.Skipped && designQuizAnswerText(a) != "" {
			return true
		}
	}
	return false
}

// ─── archive (D2) ───

// designQuizToArchive — the card's answers as design_quiz.json rows (display order kept).
func designQuizToArchive(in []entity.TechCardQuizAnswer) []techcardarchive.DesignQuizAnswer {
	if len(in) == 0 {
		return nil
	}
	out := make([]techcardarchive.DesignQuizAnswer, 0, len(in))
	for _, a := range in {
		q := a.Question
		row := techcardarchive.DesignQuizAnswer{
			ID: q.ID, Category: q.Category, Part: q.Part, Family: q.Family, View: q.View, Kind: q.Kind,
			Question: q.Question, Options: append([]string{}, q.Options...),
			Contradicts: append([]bool(nil), q.Contradicts...), VisualEvidence: q.VisualEvidence,
			ClarifyQuestion: q.ClarifyQuestion, ClarifyOptions: append([]string(nil), q.ClarifyOptions...),
			DecisionKey: q.DecisionKey,
			Selected:    append([]string{}, a.Selected...), FreeText: a.FreeText, Skipped: a.Skipped,
		}
		if !a.AnsweredAt.IsZero() {
			row.AnsweredAt = a.AnsweredAt.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

// designQuizFromArchive validates design_quiz.json rows by the rules of a live save
// (validateDesignQuizAnswers) and returns them in entity form, fingerprint "" (fresh). Rows sent
// empty are dropped (on a live save they would mean "forget"). A violation refuses the whole list.
func designQuizFromArchive(rows []techcardarchive.DesignQuizAnswer) ([]entity.TechCardQuizAnswer, error) {
	pb := make([]*pb_admin.DesignQuizAnswer, 0, len(rows))
	for _, r := range rows {
		pb = append(pb, &pb_admin.DesignQuizAnswer{
			Question: &pb_admin.DesignQuizQuestion{
				Id: r.ID, Category: r.Category, Part: r.Part, Family: r.Family, View: r.View, Kind: r.Kind,
				Question: r.Question, Options: r.Options, Contradicts: r.Contradicts,
				VisualEvidence: r.VisualEvidence, ClarifyQuestion: r.ClarifyQuestion, ClarifyOptions: r.ClarifyOptions,
				DecisionKey: r.DecisionKey,
			},
			Selected: r.Selected, FreeText: r.FreeText, Skipped: r.Skipped,
		})
	}
	out, _, ve := validateDesignQuizAnswers(pb)
	if ve != nil {
		return nil, fmt.Errorf("%s", ve.Error())
	}
	at := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		if t, err := time.Parse(time.RFC3339, r.AnsweredAt); err == nil {
			at[strings.TrimSpace(r.ID)] = t.UTC()
		}
	}
	for i := range out {
		out[i].AnsweredAt = at[out[i].Question.ID]
	}
	return out, nil
}

// resolveDesignQuiz reads design_quiz.json (1.2) into the import plan. Absent = nothing to do; a
// list that does not validate is dropped whole and reported, the card imports without it.
func (r *tcimpResolver) resolveDesignQuiz() error {
	var rows []techcardarchive.DesignQuizAnswer
	ok, err := r.readSidecar(techcardarchive.FileDesignQuiz, &rows)
	if err != nil || !ok || len(rows) == 0 {
		return err
	}
	answers, verr := designQuizFromArchive(rows)
	if verr != nil {
		r.hole(techcardarchive.EntityCard, "file="+techcardarchive.FileDesignQuiz, techcardarchive.StatusSkipped,
			techcardarchive.ReasonArchiveRowInvalid,
			fmt.Sprintf("the moodboard quiz answers do not validate (%s); the card was imported without them", verr.Error()))
		return nil
	}
	r.out.DesignQuizPlan = answers
	return nil
}
