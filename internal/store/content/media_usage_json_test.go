package content

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No database in this file: the JSON-holder query is checked for the sqlx colon trap and its
// shape, and the Go half (decode, filter to the requested ids, merge) is fed rows by hand.

const mediaJSONHolderBranches = 5 // hero, archive, campaign body, campaign variant body, colour plan

func TestMediaJSONHolderQueryShape(t *testing.T) {
	q := mediaJSONHolderQuery
	t.Log("\n" + q)

	assert.Equal(t, mediaJSONHolderBranches, strings.Count(q, ":ids"), "every holder binds the id list once")
	assert.Equal(t, mediaJSONHolderBranches, strings.Count(q, ":"),
		"no ':' may appear outside the ':ids' bind — sqlx would read it as an empty bind name")
	assert.Equal(t, mediaJSONHolderBranches-1, strings.Count(q, "UNION ALL"))
	assert.Equal(t, mediaJSONHolderBranches, strings.Count(q, "JSON_OVERLAPS("),
		"every branch must prefilter on the requested ids, never ship a whole table to Go")
	assert.NotContains(t, q, "--", "SQL comments do not belong in a named query")
	assert.NotContains(t, q, "#", "'#' opens a MySQL comment")
	assert.NotContains(t, q, "?", "a literal '?' would steal one of sqlx.In's placeholders")

	// Each holder's prefilter names every key its walker reads, scalar and array form.
	for doc, keys := range map[string][]string{
		"h.data": entity.HeroMediaKeys, "a.body": entity.ArchiveBodyMediaKeys,
		"c.body": entity.EmailBodyMediaKeys, "v.body": entity.EmailBodyMediaKeys,
		"cp.maps": entity.ColourPlanMediaKeys,
	} {
		assert.Contains(t, q, citesAnyRequested(doc, keys))
		for _, k := range keys {
			assert.Contains(t, q, "'$**."+k+"'")
			assert.Contains(t, q, "'$**."+k+"[*]'")
		}
	}

	expanded, args, err := storeutil.MakeQuery(q, map[string]any{"ids": []int{7, 8, 9}})
	require.NoError(t, err, "named-parameter expansion must succeed")
	assert.Len(t, args, 3*mediaJSONHolderBranches)
	assert.Equal(t, 3*mediaJSONHolderBranches, strings.Count(expanded, "?"))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestMediaJSONRefs(t *testing.T) {
	hero := entity.HeroFullInsert{
		Entities: []entity.HeroEntityInsert{
			{Type: entity.HeroTypeSlideshow, Slideshow: entity.HeroSlideshowInsert{
				Slides: []entity.HeroSingleInsert{{Media: entity.HeroMedia{PortraitId: 10, LandscapeId: 11}}},
			}},
			// A payload left behind under another type is not on the page.
			{Type: entity.HeroTypeMarquee, Single: entity.HeroSingleInsert{Media: entity.HeroMedia{PortraitId: 12}}},
		},
		NavFeatured: entity.NavFeaturedInsert{Women: entity.NavFeaturedEntityInsert{MediaId: 10}},
	}
	archive := []entity.ArchiveItemInsert{
		{Type: entity.ArchiveItemTypeText},
		{Type: entity.ArchiveItemTypeMediaLine, MediaLine: &entity.ArchiveMediaLineInsert{MediaIds: []int{20, 10}}},
	}
	email := []entity.EmailBlock{{Header: &entity.EmailHeaderBlock{LogoMediaID: 30}}}
	plan := []entity.DesignColourMap{{View: "front", MediaId: 40, BaseMediaId: 10}}

	rows := []mediaJSONHolderRow{
		{Holder: jsonHolderHero, Label: "home page", Doc: mustJSON(t, hero)},
		{Holder: jsonHolderArchive, EntityId: 3, Label: "SS26 drop", Doc: mustJSON(t, archive)},
		{Holder: jsonHolderEmail, EntityId: 4, Label: "Winter sale", Doc: mustJSON(t, email)},
		{Holder: jsonHolderEmail, EntityId: 4, Label: "Winter sale", Variant: "B", Doc: mustJSON(t, email)},
		{Holder: jsonHolderColourPlan, EntityId: 5, Label: "Field jacket", Doc: mustJSON(t, plan)},
		{Holder: jsonHolderArchive, EntityId: 6, Label: "broken", Doc: []byte(`{"not":"a list"}`)},
		{Holder: jsonHolderEmail, EntityId: 7, Label: "no body variant"},
	}

	refs := mediaJSONRefs(context.Background(), rows, []int{10, 12, 20, 30, 40})
	assert.Equal(t, []entity.MediaUsageRef{
		{MediaId: 10, Kind: "hero", EntityId: 0, Label: "home page", Slot: "slideshow, block 1"},
		{MediaId: 10, Kind: "hero", EntityId: 0, Label: "home page", Slot: "nav women"},
		{MediaId: 20, Kind: "archive", EntityId: 3, Label: "SS26 drop", Slot: "media line, block 2"},
		{MediaId: 10, Kind: "archive", EntityId: 3, Label: "SS26 drop", Slot: "media line, block 2"},
		{MediaId: 30, Kind: "email_campaign", EntityId: 4, Label: "Winter sale", Slot: "header logo, block 1"},
		{MediaId: 30, Kind: "email_campaign", EntityId: 4, Label: "Winter sale", Slot: "variant B, header logo, block 1"},
		{MediaId: 40, Kind: "tech_card", EntityId: 5, Label: "Field jacket", Slot: "colour map, front"},
	}, refs, "only requested ids, only what is shown (12 sits under a stale payload; 11 was not asked; 10 as a base is provenance)")

	assert.Empty(t, mediaJSONRefs(context.Background(), rows, []int{999}))

	_, _, err := mediaJSONHolderRow{Holder: "nope", Doc: []byte(`[]`)}.mediaSlots()
	assert.Error(t, err, "an unknown holder is a programming error, not a silent miss")
}

func TestMergeMediaUsage(t *testing.T) {
	registry := []entity.MediaUsageRef{
		{MediaId: 1, Kind: "product", EntityId: 9, Slot: "thumbnail"},
		{MediaId: 1, Kind: "archive", EntityId: 3, Slot: "thumbnail"},
		{MediaId: 2, Kind: "product", EntityId: 1, Slot: "gallery"},
	}
	jsonRefs := []entity.MediaUsageRef{
		{MediaId: 1, Kind: "archive", EntityId: 3, Slot: "media line, block 2"},
		{MediaId: 1, Kind: "hero", Slot: "nav men"},
		{MediaId: 5, Kind: "email_campaign", EntityId: 4, Slot: "image, block 1"},
	}
	got := mergeMediaUsage(registry, jsonRefs)

	assert.Equal(t, []entity.MediaUsageRef{
		{MediaId: 1, Kind: "archive", EntityId: 3, Slot: "thumbnail"},
		{MediaId: 1, Kind: "archive", EntityId: 3, Slot: "media line, block 2"},
		{MediaId: 1, Kind: "hero", Slot: "nav men"},
		{MediaId: 1, Kind: "product", EntityId: 9, Slot: "thumbnail"},
	}, got[1], "an archive's thumbnail and its timeline stay together")
	assert.Equal(t, registry[2:], got[2], "an id with no JSON refs keeps the registry's order untouched")
	assert.Len(t, got[5], 1, "an id only a JSON holder shows is in use")
	assert.Len(t, mergeMediaUsage(registry, nil), 2)
}
