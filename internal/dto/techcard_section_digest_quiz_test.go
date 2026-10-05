package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// 62-DEEP-FIXES D2: the moodboard quiz enters the DESIGN digest as a TAIL opened only by a card
// with answers. A card without answers hashes byte-identically to the pre-quiz projection — the
// same four-element tuple, the same golden digest — so the deploy flips no sign-off.
func TestDesignDigestWithoutQuizAnswersIsByteIdentical(t *testing.T) {
	tc := &entity.TechCardInsert{}
	tc.Concept = sql.NullString{String: "a field jacket", Valid: true}
	tc.Details = []entity.TechCardDetail{{Key: sql.NullString{String: "closure", Valid: true},
		Text: sql.NullString{String: "zip", Valid: true}}}

	proj := designProjection(tc).([]any)
	require.Len(t, proj, 4, "no tail without answers")
	pre := digestOf([]any{tc.Concept.String, proj[1], proj[2], proj[3]})
	got := TechCardSectionDigests(tc)[entity.SignoffDesign]
	require.Equal(t, pre, got)
	// Golden: computed by the PRE-QUIZ digest code (3814f07, via go -overlay) on this same card.
	require.Equal(t, "c65cba0f685acb93b89864b6fd6679bbc4640faabfeba1ba0a4539efcb9288f5", got)

	// The store's token for a card with no answers is "" — the same projection.
	tc.DesignQuizDigest = entity.DesignQuizAnswersDigest(nil)
	require.Equal(t, got, TechCardSectionDigests(tc)[entity.SignoffDesign])

	// With answers the DESIGN digest moves, and only DESIGN.
	before := TechCardSectionDigests(tc)
	tc.DesignQuizDigest = entity.DesignQuizAnswersDigest([]entity.TechCardQuizAnswer{{
		Question: entity.DesignQuizQuestion{ID: "hem"}, Selected: []string{"mid-thigh"}}})
	after := TechCardSectionDigests(tc)
	require.NotEqual(t, before[entity.SignoffDesign], after[entity.SignoffDesign])
	for sec, d := range before {
		if sec != entity.SignoffDesign {
			require.Equal(t, d, after[sec], sec)
		}
	}
}
