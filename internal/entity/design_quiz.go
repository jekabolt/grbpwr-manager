package entity

import "time"

// Moodboard quiz (0389): the questions the model asked about one garment and the designer's answers.
// Vocabularies are closed in Go (no ENUM, no CHECK — the design band rule).
const (
	DesignQuizCategoryDesign    = "design"
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

// IsDesignQuizCategory reports whether v is one of the five quiz categories.
func IsDesignQuizCategory(v string) bool {
	switch v {
	case DesignQuizCategoryDesign, DesignQuizCategoryDetails, DesignQuizCategoryMaterials,
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
