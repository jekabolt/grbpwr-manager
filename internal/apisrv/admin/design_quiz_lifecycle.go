package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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

// designQuizCurrentFingerprint — the card's fingerprint now; ok=false when the size chart cannot be
// read (staleness then degrades to "everything fresh", never to a refusal).
func (s *Server) designQuizCurrentFingerprint(ctx context.Context, card *entity.TechCard) (string, bool) {
	if card == nil {
		return "", false
	}
	chart, err := s.repo.TechCards().GetStyleSizeChart(ctx, card.Id)
	if err != nil {
		slog.Default().WarnContext(ctx, "design quiz: cannot read the size chart for the fingerprint",
			slog.Int("tech_card_id", card.Id), slog.String("err", err.Error()))
		return "", false
	}
	return designQuizCardFingerprint(card, chart), true
}

// designQuizMarkStale sets Stale on the card's loaded answers (one chart read; none without answers).
func (s *Server) designQuizMarkStale(ctx context.Context, card *entity.TechCard) {
	if card == nil || len(card.QuizAnswers) == 0 {
		return
	}
	if fp, ok := s.designQuizCurrentFingerprint(ctx, card); ok {
		entity.MarkDesignQuizStale(card.QuizAnswers, fp)
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
			Selected: append([]string{}, a.Selected...), FreeText: a.FreeText, Skipped: a.Skipped,
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
