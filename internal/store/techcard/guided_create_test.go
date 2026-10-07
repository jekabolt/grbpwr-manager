package techcard

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// (3) guided and create_request_id (0407) are written only by AddTechCardWithOpts and
// ExitTechCardGuide. The shared header column list (insert/clone/import) and the update SET list
// must never name them: an autosave would clear guided, a clone would copy a replay key.
func TestGuidedColumnsStayOutOfSharedWrites(t *testing.T) {
	for name, sqlText := range map[string]string{
		"techCardHeaderColumns":   techCardHeaderColumns,
		"techCardHeaderValues":    techCardHeaderValues,
		"techCardUpdateHeaderSQL": techCardUpdateHeaderSQL,
	} {
		for _, col := range []string{"guided", "create_request_id"} {
			if strings.Contains(sqlText, col) {
				t.Errorf("%s names %q", name, col)
			}
		}
	}
	params := techCardHeaderParams(&entity.TechCardInsert{})
	for _, col := range []string{"guided", "create_request_id"} {
		if _, ok := params[col]; ok {
			t.Errorf("techCardHeaderParams carries %q", col)
		}
	}
}

// The list `setup` rule, through the one helper both list paths use (A6).
func TestApplyListMediaSetup(t *testing.T) {
	board := mf("mood.jpg", entity.TechCardMediaCategoryMoodboard, entity.TechCardMediaMoodboard)
	reference := mf("ref.jpg", entity.TechCardMediaCategoryMoodboard, entity.TechCardMediaReference)
	flat := mf("front.jpg", entity.TechCardMediaCategoryTechnical, entity.TechCardMediaFront)
	concept := sql.NullString{String: "a coat for rain", Valid: true}

	cases := []struct {
		name    string
		guided  bool
		concept sql.NullString
		media   []entity.TechCardMediaFull
		want    bool
	}{
		{"guided, empty", true, sql.NullString{}, nil, true},
		{"guided, only reference rows and technical", true, sql.NullString{}, []entity.TechCardMediaFull{reference, flat}, true},
		{"guided, blank concept", true, sql.NullString{String: "  ", Valid: true}, nil, true},
		{"guided, a board picture", true, sql.NullString{}, []entity.TechCardMediaFull{reference, board}, false},
		{"guided, a concept", true, concept, nil, false},
		{"not guided", false, sql.NullString{}, nil, false},
	}
	for _, c := range cases {
		card := entity.TechCard{Guided: c.guided}
		card.Stage = entity.TechCardStageIdea
		card.Concept = c.concept
		applyListMedia(&card, c.media)
		if card.Setup != c.want {
			t.Errorf("%s: setup = %v, want %v", c.name, card.Setup, c.want)
		}
	}
	card := entity.TechCard{}
	card.Stage = entity.TechCardStageIdea
	applyListMedia(&card, []entity.TechCardMediaFull{board})
	if card.PreviewURL != "mood.jpg" {
		t.Errorf("preview not set by the shared helper: %q", card.PreviewURL)
	}
}
