package techcard

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestCalloutSyncApply locks the S6/S7/S8 piece↔callout rules: a piece linked to a callout anchored on
// a TECHNICAL sketch takes that callout's part as its canonical name (S8, name lives once); a piece
// linked to a moodboard/unanchored callout, or to a callout that no longer exists, has NO piece
// semantics (S7) and is marked detached but kept (orphan-control, S8). No DB needed.
func TestCalloutSyncApply(t *testing.T) {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	ni := func(i int32) sql.NullInt32 { return sql.NullInt32{Int32: i, Valid: true} }

	tc := &entity.TechCardInsert{
		Media: []entity.TechCardMediaItem{
			{MediaId: 10, Category: entity.TechCardMediaCategoryTechnical},
			{MediaId: 20, Category: entity.TechCardMediaCategoryMoodboard},
		},
		Callouts: []entity.TechCardCallout{
			{Number: 1, Part: ns("Collar"), MediaId: ni(10)}, // technical sketch
			{Number: 2, Part: ns("Vibe"), MediaId: ni(20)},   // moodboard
			{Number: 3, Part: ns("Floating"), MediaId: sql.NullInt32{}}, // unanchored
		},
	}
	cs := buildCalloutSync(tc)

	cases := []struct {
		name         string
		piece        entity.TechCardPiece
		wantName     string
		wantDetached bool
	}{
		{"technical callout syncs name", entity.TechCardPiece{Name: "old", CalloutNumber: ni(1)}, "Collar", false},
		{"moodboard callout has no piece semantics", entity.TechCardPiece{Name: "keep", CalloutNumber: ni(2)}, "keep", true},
		{"unanchored callout has no piece semantics", entity.TechCardPiece{Name: "keep", CalloutNumber: ni(3)}, "keep", true},
		{"missing callout detaches", entity.TechCardPiece{Name: "keep", CalloutNumber: ni(99)}, "keep", true},
		{"no callout keeps free name", entity.TechCardPiece{Name: "free"}, "free", false},
	}
	for _, c := range cases {
		p := c.piece
		cs.apply(&p)
		if p.Name != c.wantName {
			t.Errorf("%s: name = %q, want %q", c.name, p.Name, c.wantName)
		}
		if p.Detached != c.wantDetached {
			t.Errorf("%s: detached = %v, want %v", c.name, p.Detached, c.wantDetached)
		}
	}
}
