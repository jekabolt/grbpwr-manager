package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Moodboard quiz answers (0389). One row per answered question. A save MERGES by question id (W-B1):
// rows the request does not name stay; a row sent empty (no selection, no own words, not skipped)
// deletes its question id. No CAS: two writers of the same id — the later one wins.

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
	CardFingerprint    string    `db:"card_fingerprint"`
	AnsweredAt         time.Time `db:"answered_at"`
}

const designQuizSelect = `
	SELECT tech_card_id, question_id, category, part, family, part_view, kind, question,
	       options_json, contradicts_json, visual_evidence, clarify_question, clarify_options_json,
	       selected_json, free_text, skipped, card_fingerprint, answered_at
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
		Fingerprint: r.CardFingerprint,
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

// enrichDesignQuiz attaches the quiz answers to each card (one query per batch) and the DESIGN
// digest's quiz token (62-DEEP-FIXES D2; the write path computes the same token from the same rows).
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
		cards[i].DesignQuizDigest = entity.DesignQuizAnswersDigest(cards[i].QuizAnswers)
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

// designQuizInsertCols — the columns insertDesignQuizRows writes, in its row order.
var designQuizInsertCols = []string{"tech_card_id", "question_id", "category", "part", "family", "part_view", "kind",
	"question", "options_json", "contradicts_json", "visual_evidence", "clarify_question",
	"clarify_options_json", "selected_json", "free_text", "skipped", "card_fingerprint", "display_order", "answered_at"}

// insertDesignQuizRows writes list as the card's answers in that order (display_order = index). The
// caller owns the transaction and has cleared the card's rows (or the card is new). Shared by the
// save, the archive import (62-DEEP-FIXES D2) — one row shape, one writer.
func insertDesignQuizRows(ctx context.Context, db dependency.DB, techCardID int, list []entity.TechCardQuizAnswer) error {
	if len(list) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(list))
	for i, a := range list {
		q := a.Question
		at := a.AnsweredAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		rows = append(rows, []any{techCardID, q.ID, q.Category, q.Part, q.Family, q.View, q.Kind,
			q.Question, designQuizJSON(q.Options), designQuizJSON(q.Contradicts), q.VisualEvidence,
			q.ClarifyQuestion, designQuizJSON(q.ClarifyOptions), designQuizJSON(a.Selected), a.FreeText,
			a.Skipped, a.Fingerprint, i, at})
	}
	if err := storeutil.BulkInsertRows(ctx, db, "tech_card_design_quiz_answer", designQuizInsertCols, rows); err != nil {
		return fmt.Errorf("can't store design quiz answers: %w", err)
	}
	return nil
}

// designQuizRevisionSection / Action — the card journal line a quiz save appends (62-DEEP-FIXES D2).
// `section` is free VARCHAR; `action` is CHECKed by 0162 to a closed list, `updated` is on it.
const (
	designQuizRevisionSection = "design"
	designQuizRevisionAction  = "updated"
)

// SaveDesignQuizAnswers is the NON-DESTRUCTIVE save (61-QUICKWINS W-B1): upserts (already validated
// by the caller, Fingerprint already stamped with the card's current one — 62 D1) are written over
// the stored rows of the same question id or appended; the ids in forget are deleted; every other
// stored row STAYS. Returns the stored list. One transaction: the card row is locked first
// (sql.ErrNoRows when it does not exist), so two saves of one card serialise and each merges over
// what the other stored. A merge leaving more than maxStored rows is refused with
// entity.ErrDesignQuizTooManyAnswers and changes nothing.
//
// The merge itself is entity.MergeDesignQuizAnswers (pure, unit-tested); the store writes its result
// back as the card's list (DELETE by card + INSERT in merge order, inside the same lock), which keeps
// display_order dense and answered_at as the merge decided. A save that changed the stored answers
// appends a `design · updated` line to the card journal (62 D2), author = actor.
func (s *Store) SaveDesignQuizAnswers(ctx context.Context, techCardID int, upserts []entity.TechCardQuizAnswer, forget []string, maxStored int, actor string) ([]entity.TechCardQuizAnswer, error) {
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
		merged := entity.MergeDesignQuizAnswers(prev[techCardID], upserts, forget, time.Now().UTC())
		if len(merged) > maxStored {
			return entity.ErrDesignQuizTooManyAnswers
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_design_quiz_answer WHERE tech_card_id = :id`,
			map[string]any{"id": techCardID}); err != nil {
			return fmt.Errorf("can't clear design quiz answers: %w", err)
		}
		if err := insertDesignQuizRows(ctx, db, techCardID, merged); err != nil {
			return err
		}
		out, err := designQuizAnswersByTechCardIds(ctx, db, []int{techCardID})
		if err != nil {
			return err
		}
		stored = out[techCardID]
		if entity.DesignQuizAnswersDigest(prev[techCardID]) != entity.DesignQuizAnswersDigest(stored) {
			if err := appendTechCardRevision(ctx, db, techCardID, actor, designQuizRevisionSection,
				designQuizRevisionAction, fmt.Sprintf("quiz answers saved (%d on the card)", len(stored))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}
