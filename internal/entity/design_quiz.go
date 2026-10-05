package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Moodboard quiz (0389): the questions the model asked about one garment and the designer's answers.
// Vocabularies are closed in Go (no ENUM, no CHECK — the design band rule).
const (
	DesignQuizCategoryDesign    = "design"
	DesignQuizCategoryFit       = "fit"
	DesignQuizCategoryDetails   = "details"
	DesignQuizCategoryMaterials = "materials"
	DesignQuizCategoryUse       = "use"
	DesignQuizCategoryFinish    = "finish"

	DesignQuizKindSingle = "single"
	DesignQuizKindMulti  = "multi"

	DesignQuizViewFront = "front"
	DesignQuizViewBack  = "back"
	DesignQuizViewSideL = "side_l"

	DesignQuizPartWhole = "whole"
)

// IsDesignQuizCategory reports whether v is one of the six quiz categories.
func IsDesignQuizCategory(v string) bool {
	switch v {
	case DesignQuizCategoryDesign, DesignQuizCategoryFit, DesignQuizCategoryDetails, DesignQuizCategoryMaterials,
		DesignQuizCategoryUse, DesignQuizCategoryFinish:
		return true
	}
	return false
}

// IsDesignQuizKind reports whether v is single or multi.
func IsDesignQuizKind(v string) bool { return v == DesignQuizKindSingle || v == DesignQuizKindMulti }

// IsDesignQuizView reports whether v is a pictogram view the quiz draws.
func IsDesignQuizView(v string) bool {
	return v == DesignQuizViewFront || v == DesignQuizViewBack || v == DesignQuizViewSideL
}

// DesignQuizQuestion is one question as asked (and stored with its answer, so a re-run and an edit
// need nothing else). Contradicts is parallel to Options (empty = all false).
type DesignQuizQuestion struct {
	ID              string
	Category        string
	Part            string
	Family          string
	View            string
	Kind            string
	Question        string
	Options         []string
	Contradicts     []bool
	VisualEvidence  string
	ClarifyQuestion string
	ClarifyOptions  []string
	// DecisionKey — snake_case key of the DECISION (not the wording), "" = none (0394, 64-DEFERRED E1).
	DecisionKey string
}

// TechCardQuizAnswer is one stored answer: the question plus what the designer chose.
type TechCardQuizAnswer struct {
	Question   DesignQuizQuestion
	Selected   []string
	FreeText   string
	Skipped    bool
	AnsweredAt time.Time
	// Fingerprint — the card's structured-input fingerprint when this answer was last saved (0392,
	// 62-DEEP-FIXES D1). "" = saved before 0392 or imported: counts as fresh.
	Fingerprint string
	// Stale — derived on read, never stored: Fingerprint != "" and differs from the card's current
	// fingerprint (the card's structured facts changed since the answer was given).
	Stale bool
}

// DesignQuizIsStale — the one staleness rule: an answer saved under a fingerprint that is no longer
// the card's. An empty fingerprint on either side is fresh (pre-0392 rows; a current fingerprint
// that could not be computed).
func DesignQuizIsStale(answerFingerprint, current string) bool {
	return answerFingerprint != "" && current != "" && answerFingerprint != current
}

// MarkDesignQuizStale sets Stale on every answer against the card's current fingerprint.
func MarkDesignQuizStale(answers []TechCardQuizAnswer, current string) {
	for i := range answers {
		answers[i].Stale = DesignQuizIsStale(answers[i].Fingerprint, current)
	}
}

// DesignQuizAnswersDigest — the token the DESIGN sign-off digest appends for the quiz (62-DEEP-FIXES
// D2): sha256 over (question id, selected, free text, skipped) of every answer, in display order.
// "" when the card has none, so a card without answers keeps a byte-identical DESIGN digest.
// answered_at and the fingerprint are deliberately out: re-confirming the same answer is not an edit.
func DesignQuizAnswersDigest(answers []TechCardQuizAnswer) string {
	if len(answers) == 0 {
		return ""
	}
	rows := make([][]any, 0, len(answers))
	for _, a := range answers {
		sel := a.Selected
		if sel == nil {
			sel = []string{}
		}
		rows = append(rows, []any{a.Question.ID, sel, a.FreeText, a.Skipped})
	}
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ErrDesignQuizTooManyAnswers — a save would leave more answers on the card than the cap allows.
var ErrDesignQuizTooManyAnswers = errors.New("too many quiz answers on the card")

// DesignQuizSession — the card's last generated question list (0394, 64-DEFERRED E2), so a quiz
// resumes on another tab or device. ClosedAt nil = the open one (one per card).
type DesignQuizSession struct {
	ID         int
	TechCardID int
	Family     string
	Questions  []DesignQuizQuestion
	CreatedBy  string
	CreatedAt  time.Time
	ClosedAt   *time.Time
}

// DesignQuizPending — the session's questions not yet saved on the card: every question whose id is
// among the saved rows (answered or skipped) is out, and so is every question whose non-empty
// decision key a saved row carries — a save that superseded (forgot) an older row by key must not
// bring that row's question back. The generated order is kept.
func DesignQuizPending(questions []DesignQuizQuestion, saved []TechCardQuizAnswer) []DesignQuizQuestion {
	done := make(map[string]bool, len(saved))
	keys := make(map[string]bool, len(saved))
	for _, a := range saved {
		done[a.Question.ID] = true
		if k := a.Question.DecisionKey; k != "" {
			keys[k] = true
		}
	}
	out := make([]DesignQuizQuestion, 0, len(questions))
	for _, q := range questions {
		if !done[q.ID] && (q.DecisionKey == "" || !keys[q.DecisionKey]) {
			out = append(out, q)
		}
	}
	return out
}

// MergeDesignQuizAnswers is the NON-DESTRUCTIVE save (61-QUICKWINS W-B1): the stored list prev with
// every row of upserts written over the stored row of the same question id (in place) or appended
// (in request order), and every id in forget removed. A stored row the request does not name STAYS —
// a client that saves before its list has loaded can no longer erase the card's history.
//
// SUPERSESSION (64-DEFERRED E1): an upsert carrying a decision key forgets every OTHER row (another
// question id) with the same key — the decision was re-asked in other words, the latest answer wins.
// Two upserts of one key in a request: the later in request order wins. "" never supersedes.
//
// answered_at is the server's: an answer that comes back unchanged (same selection, free text and
// skip) keeps the time it was first given; a new or changed one is stamped now.
func MergeDesignQuizAnswers(prev, upserts []TechCardQuizAnswer, forget []string, now time.Time) []TechCardQuizAnswer {
	gone := make(map[string]bool, len(forget))
	for _, id := range forget {
		gone[id] = true
	}
	stamp := func(p *TechCardQuizAnswer, a TechCardQuizAnswer) TechCardQuizAnswer {
		a.AnsweredAt = now
		if p != nil && p.Skipped == a.Skipped && p.FreeText == a.FreeText && slices.Equal(p.Selected, a.Selected) {
			a.AnsweredAt = p.AnsweredAt
		}
		return a
	}
	byID := make(map[string]TechCardQuizAnswer, len(upserts))
	for _, a := range upserts {
		byID[a.Question.ID] = a
	}
	out := make([]TechCardQuizAnswer, 0, len(prev)+len(upserts))
	placed := make(map[string]bool, len(prev))
	for _, p := range prev {
		id := p.Question.ID
		if gone[id] || placed[id] {
			continue
		}
		placed[id] = true
		if a, ok := byID[id]; ok {
			out = append(out, stamp(&p, a))
			continue
		}
		out = append(out, p)
	}
	for _, a := range upserts {
		id := a.Question.ID
		if gone[id] || placed[id] {
			continue
		}
		placed[id] = true
		out = append(out, stamp(nil, a))
	}
	owner := make(map[string]string, len(upserts))
	for _, a := range upserts {
		if k := a.Question.DecisionKey; k != "" && !gone[a.Question.ID] {
			owner[k] = a.Question.ID
		}
	}
	if len(owner) == 0 {
		return out
	}
	kept := out[:0]
	for _, a := range out {
		if id, ok := owner[a.Question.DecisionKey]; ok && a.Question.DecisionKey != "" && id != a.Question.ID {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}
