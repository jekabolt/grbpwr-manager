package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Moodboard quiz answers (0389). One row per answered question; the client door always sends the
// FULL list and the store replaces it in one transaction (single writer, no merge, no CAS).

type designQuizAnswerRow struct {
	TechCardID         int       `db:"tech_card_id"`
	QuestionID         string    `db:"question_id"`
	Category           string    `db:"category"`
	Part               string    `db:"part"`
	Family             string    `db:"family"`
	View               string    `db:"part_view"`
	Kind               string    `db:"kind"`
	Question           string    `db:"question"`
	OptionsJSON        string    `db:"options_json"`
	ContradictsJSON    string    `db:"contradicts_json"`
	VisualEvidence     string    `db:"visual_evidence"`
	ClarifyQuestion    string    `db:"clarify_question"`
	ClarifyOptionsJSON string    `db:"clarify_options_json"`
	SelectedJSON       string    `db:"selected_json"`
	FreeText           string    `db:"free_text"`
	Skipped            bool      `db:"skipped"`
	AnsweredAt         time.Time `db:"answered_at"`
}

const designQuizSelect = `
	SELECT tech_card_id, question_id, category, part, family, part_view, kind, question,
	       options_json, contradicts_json, visual_evidence, clarify_question, clarify_options_json,
	       selected_json, free_text, skipped, answered_at
	FROM tech_card_design_quiz_answer`

func (r designQuizAnswerRow) entity() entity.TechCardQuizAnswer {
	var opts, clar, sel []string
	var contra []bool
	_ = json.Unmarshal([]byte(r.OptionsJSON), &opts)
	_ = json.Unmarshal([]byte(r.ContradictsJSON), &contra)
	_ = json.Unmarshal([]byte(r.ClarifyOptionsJSON), &clar)
	_ = json.Unmarshal([]byte(r.SelectedJSON), &sel)
	return entity.TechCardQuizAnswer{
		Question: entity.DesignQuizQuestion{
			ID: r.QuestionID, Category: r.Category, Part: r.Part, Family: r.Family, View: r.View,
			Kind: r.Kind, Question: r.Question, Options: opts, Contradicts: contra,
			VisualEvidence: r.VisualEvidence, ClarifyQuestion: r.ClarifyQuestion, ClarifyOptions: clar,
		},
		Selected: sel, FreeText: r.FreeText, Skipped: r.Skipped, AnsweredAt: r.AnsweredAt,
	}
}

// designQuizAnswersByTechCardIds loads the answers of a batch of cards, in display order.
func designQuizAnswersByTechCardIds(ctx context.Context, db dependency.DB, ids []int) (map[int][]entity.TechCardQuizAnswer, error) {
	out := make(map[int][]entity.TechCardQuizAnswer, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[designQuizAnswerRow](ctx, db,
		designQuizSelect+` WHERE tech_card_id IN (:ids) ORDER BY tech_card_id, display_order, id`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load design quiz answers: %w", err)
	}
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.entity())
	}
	return out, nil
}

// enrichDesignQuiz attaches the quiz answers to each card (one query per batch).
func (s *Store) enrichDesignQuiz(ctx context.Context, cards []entity.TechCard) error {
	ids := make([]int, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.Id)
	}
	byCard, err := designQuizAnswersByTechCardIds(ctx, s.DB, ids)
	if err != nil {
		return err
	}
	for i := range cards {
		cards[i].QuizAnswers = byCard[cards[i].Id]
	}
	return nil
}

// ListDesignQuizAnswers returns the card's stored quiz answers in display order. A card with none —
// or no such card — answers an empty list; the handler checks the card when it matters.
func (s *Store) ListDesignQuizAnswers(ctx context.Context, techCardID int) ([]entity.TechCardQuizAnswer, error) {
	byCard, err := designQuizAnswersByTechCardIds(ctx, s.DB, []int{techCardID})
	if err != nil {
		return nil, err
	}
	return byCard[techCardID], nil
}

func designQuizJSON[T any](v []T) string {
	if v == nil {
		v = []T{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ReplaceDesignQuizAnswers replaces the card's whole answer list with answers (already validated by
// the caller), in the given order, and returns the stored list. One transaction: the card row is
// locked first (sql.ErrNoRows when it does not exist), so two saves of one card serialise.
//
// answered_at is the server's: an answer that comes back unchanged (same question id, selection,
// free text and skip) keeps the time it was first given; a new or changed one is stamped now.
func (s *Store) ReplaceDesignQuizAnswers(ctx context.Context, techCardID int, answers []entity.TechCardQuizAnswer) ([]entity.TechCardQuizAnswer, error) {
	var stored []entity.TechCardQuizAnswer
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		var id int
		if err := db.QueryRowxContext(ctx, `SELECT id FROM tech_card WHERE id = ? FOR UPDATE`, techCardID).Scan(&id); err != nil {
			return err
		}
		prev, err := designQuizAnswersByTechCardIds(ctx, db, []int{techCardID})
		if err != nil {
			return err
		}
		prevAt := make(map[string]entity.TechCardQuizAnswer, len(prev[techCardID]))
		for _, a := range prev[techCardID] {
			prevAt[a.Question.ID] = a
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_design_quiz_answer WHERE tech_card_id = :id`,
			map[string]any{"id": techCardID}); err != nil {
			return fmt.Errorf("can't clear design quiz answers: %w", err)
		}
		now := time.Now().UTC()
		cols := []string{"tech_card_id", "question_id", "category", "part", "family", "part_view", "kind",
			"question", "options_json", "contradicts_json", "visual_evidence", "clarify_question",
			"clarify_options_json", "selected_json", "free_text", "skipped", "display_order", "answered_at"}
		rows := make([][]any, 0, len(answers))
		for i, a := range answers {
			at := now
			if p, ok := prevAt[a.Question.ID]; ok && p.Skipped == a.Skipped && p.FreeText == a.FreeText &&
				slices.Equal(p.Selected, a.Selected) {
				at = p.AnsweredAt
			}
			q := a.Question
			rows = append(rows, []any{techCardID, q.ID, q.Category, q.Part, q.Family, q.View, q.Kind,
				q.Question, designQuizJSON(q.Options), designQuizJSON(q.Contradicts), q.VisualEvidence,
				q.ClarifyQuestion, designQuizJSON(q.ClarifyOptions), designQuizJSON(a.Selected), a.FreeText,
				a.Skipped, i, at})
		}
		if err := storeutil.BulkInsertRows(ctx, db, "tech_card_design_quiz_answer", cols, rows); err != nil {
			return fmt.Errorf("can't store design quiz answers: %w", err)
		}
		out, err := designQuizAnswersByTechCardIds(ctx, db, []int{techCardID})
		if err != nil {
			return err
		}
		stored = out[techCardID]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}
