package techcard

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// mf builds a resolved media item with a thumbnail URL in the given category/kind.
func mf(url string, cat entity.TechCardMediaCategory, kind entity.TechCardMediaKind) entity.TechCardMediaFull {
	m := entity.TechCardMediaFull{Category: cat, Kind: kind}
	m.Media.ThumbnailMediaURL = url
	return m
}

// TestPickTechCardPreviewURL covers B-9 preview selection: IDEA prefers the moodboard image, other
// stages prefer the PREVIEW sketch, with a fallback chain, and no media → empty.
func TestPickTechCardPreviewURL(t *testing.T) {
	moodboard := mf("mood.jpg", entity.TechCardMediaCategoryMoodboard, entity.TechCardMediaMoodboard)
	preview := mf("prev.jpg", entity.TechCardMediaCategoryTechnical, entity.TechCardMediaPreview)
	front := mf("front.jpg", entity.TechCardMediaCategoryTechnical, entity.TechCardMediaFront)

	cases := []struct {
		name  string
		stage entity.TechCardStage
		media []entity.TechCardMediaFull
		want  string
	}{
		{"idea prefers moodboard", entity.TechCardStageIdea, []entity.TechCardMediaFull{front, moodboard, preview}, "mood.jpg"},
		{"idea no moodboard falls to preview", entity.TechCardStageIdea, []entity.TechCardMediaFull{front, preview}, "prev.jpg"},
		{"idea only technical front", entity.TechCardStageIdea, []entity.TechCardMediaFull{front}, "front.jpg"},
		{"proto prefers preview", entity.TechCardStageProto, []entity.TechCardMediaFull{moodboard, front, preview}, "prev.jpg"},
		{"proto no preview falls to technical", entity.TechCardStageProto, []entity.TechCardMediaFull{moodboard, front}, "front.jpg"},
		{"proto only moodboard", entity.TechCardStageProto, []entity.TechCardMediaFull{moodboard}, "mood.jpg"},
		{"no media", entity.TechCardStageProto, nil, ""},
	}
	for _, c := range cases {
		if got := pickTechCardPreviewURL(c.stage, c.media); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	// URL falls back from an empty thumbnail to the compressed URL.
	noThumb := entity.TechCardMediaFull{Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard}
	noThumb.Media.CompressedMediaURL = "comp.jpg"
	if got := pickTechCardPreviewURL(entity.TechCardStageIdea, []entity.TechCardMediaFull{noThumb}); got != "comp.jpg" {
		t.Errorf("compressed fallback: got %q want comp.jpg", got)
	}
}
