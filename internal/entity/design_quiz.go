package entity

import (
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
}

// TechCardQuizAnswer is one stored answer: the question plus what the designer chose.
type TechCardQuizAnswer struct {
	Question   DesignQuizQuestion
	Selected   []string
	FreeText   string
	Skipped    bool
	AnsweredAt time.Time
}

// ErrDesignQuizTooManyAnswers — a save would leave more answers on the card than the cap allows.
var ErrDesignQuizTooManyAnswers = errors.New("too many quiz answers on the card")

// MergeDesignQuizAnswers is the NON-DESTRUCTIVE save (61-QUICKWINS W-B1): the stored list prev with
// every row of upserts written over the stored row of the same question id (in place) or appended
// (in request order), and every id in forget removed. A stored row the request does not name STAYS —
// a client that saves before its list has loaded can no longer erase the card's history.
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
	return out
}
