package entity

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A slice of the real vocabulary: two washing symbols (so they compete for one slot), one from each
// of two other categories, both Professional Care sub-categories (so they must NOT compete), and one
// archived entry. Sort orders are the real ones, deliberately out of declaration order so the tests
// prove ordering comes from the dictionary rather than from input or slice position.
func testCareIndex() CareIndex {
	sub := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	return BuildCareIndex([]CareSymbol{
		{Code: "DNB", Category: "Bleaching", Name: "Do Not Bleach", ShortProse: "do not bleach", SortOrder: 12},
		{Code: "MW30", Category: "Washing", Name: "Machine Wash Cold (30°C)", ShortProse: "machine wash 30°", SortOrder: 2,
			Translations: map[int]CareTranslation{
				2: {ShortProse: "lavage en machine 30°"},
				5: {Name: "洗濯機洗い（30℃）", ShortProse: "洗濯機洗い 30°"},
			}},
		{Code: "MW40", Category: "Washing", Name: "Machine Wash Warm (40°C)", ShortProse: "machine wash 40°", SortOrder: 3},
		{Code: "IL", Category: "Ironing", Name: "Iron at Low Temperature (110°C)", ShortProse: "iron low", SortOrder: 25},
		{Code: "DNDC", Category: "Professional Care", SubCategory: sub("Dry Cleaning"), Name: "Do Not Dry Clean", ShortProse: "do not dry clean", SortOrder: 35},
		{Code: "PWC", Category: "Professional Care", SubCategory: sub("Wet Cleaning"), Name: "Professional Wet Clean", ShortProse: "professional wet clean", SortOrder: 36},
		{Code: "RETIRED", Category: "Washing", Name: "Retired", ShortProse: "retired", SortOrder: 99,
			ArchivedAt: sql.NullTime{Time: time.Unix(0, 0), Valid: true}},
	})
}

func TestCareNormalize(t *testing.T) {
	ix := testCareIndex()

	t.Run("canonicalises order regardless of how it was clicked", func(t *testing.T) {
		got, err := ix.Normalize("IL,DNB,MW30")
		require.NoError(t, err)
		assert.Equal(t, "MW30,DNB,IL", got, "dictionary sort_order, not input order")
	})

	t.Run("empty is valid and means no care", func(t *testing.T) {
		for _, in := range []string{"", "   ", ",,", " , "} {
			got, err := ix.Normalize(in)
			require.NoErrorf(t, err, "input %q", in)
			assert.Equalf(t, "", got, "input %q", in)
		}
	})

	t.Run("tolerates whitespace and lower case", func(t *testing.T) {
		got, err := ix.Normalize(" mw30 , dnb ")
		require.NoError(t, err)
		assert.Equal(t, "MW30,DNB", got)
	})

	t.Run("rejects legacy free text", func(t *testing.T) {
		// This is the exact shape of the values already in the column.
		_, err := ix.Normalize("Machine wash cold at 30, do not tumble dry")
		require.Error(t, err)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "unknown_care_code", ve.Reason)
		assert.Equal(t, "care_instructions", ve.Field)
	})

	t.Run("rejects an unknown code", func(t *testing.T) {
		_, err := ix.Normalize("MW30,NOPE")
		require.Error(t, err)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "unknown_care_code", ve.Reason)
	})

	t.Run("rejects an archived code", func(t *testing.T) {
		_, err := ix.Normalize("RETIRED")
		require.Error(t, err)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "archived_care_code", ve.Reason)
	})

	t.Run("rejects the same code twice", func(t *testing.T) {
		_, err := ix.Normalize("MW30,MW30")
		require.Error(t, err)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "duplicate_care_code", ve.Reason)
	})

	t.Run("rejects two symbols competing for one category", func(t *testing.T) {
		_, err := ix.Normalize("MW30,MW40")
		require.Error(t, err)
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "conflicting_care_codes", ve.Reason)
	})

	t.Run("professional care holds one dry AND one wet", func(t *testing.T) {
		// The whole reason the slot key is category+sub_category rather than category.
		got, err := ix.Normalize("PWC,DNDC")
		require.NoError(t, err)
		assert.Equal(t, "DNDC,PWC", got)
	})

	t.Run("an unloaded dictionary rejects rather than silently wiping", func(t *testing.T) {
		_, err := CareIndex{}.Normalize("MW30")
		require.Error(t, err)
		// ...but still lets an empty value through, so a save that is not writing care is unaffected.
		got, err := CareIndex{}.Normalize("")
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})
}

func TestCareResolve(t *testing.T) {
	ix := testCareIndex()

	t.Run("resolves in canonical order with display data", func(t *testing.T) {
		got := ix.Resolve("IL,MW30", 0)
		require.Len(t, got, 2)
		assert.Equal(t, "MW30", got[0].Code)
		assert.Equal(t, "Washing", got[0].Category)
		assert.Equal(t, "machine wash 30°", got[0].ShortProse)
		assert.Equal(t, "IL", got[1].Code)
	})

	t.Run("carries the professional sub-category", func(t *testing.T) {
		got := ix.Resolve("PWC", 0)
		require.Len(t, got, 1)
		assert.Equal(t, "Wet Cleaning", got[0].SubCategory)
	})

	t.Run("legacy free text resolves to nothing rather than failing", func(t *testing.T) {
		// A read must never blow up on data written before the vocabulary existed; nil is the
		// client's cue to render the raw string instead.
		assert.Nil(t, ix.Resolve("Machine wash cold at 30, do not tumble dry", 0))
	})

	t.Run("skips unknown codes but keeps the known ones", func(t *testing.T) {
		got := ix.Resolve("MW30,NOPE,IL", 0)
		require.Len(t, got, 2)
		assert.Equal(t, "MW30", got[0].Code)
		assert.Equal(t, "IL", got[1].Code)
	})

	t.Run("renders archived symbols that are already referenced", func(t *testing.T) {
		// Archiving removes a symbol from the picker; it must not blank out styles using it.
		got := ix.Resolve("RETIRED", 0)
		require.Len(t, got, 1)
		assert.Equal(t, "RETIRED", got[0].Code)
	})

	t.Run("translates prose, falling back to English per field", func(t *testing.T) {
		fr := ix.Resolve("MW30", 2)
		require.Len(t, fr, 1)
		assert.Equal(t, "lavage en machine 30°", fr[0].ShortProse)
		assert.Equal(t, "Machine Wash Cold (30°C)", fr[0].Name, "fr has no name override")

		ja := ix.Resolve("MW30", 5)
		require.Len(t, ja, 1)
		assert.Equal(t, "洗濯機洗い 30°", ja[0].ShortProse)
		assert.Equal(t, "洗濯機洗い（30℃）", ja[0].Name, "ja overrides the name too")
	})

	t.Run("an untranslated language keeps English", func(t *testing.T) {
		got := ix.Resolve("MW30", 7)
		require.Len(t, got, 1)
		assert.Equal(t, "machine wash 30°", got[0].ShortProse)
	})

	t.Run("empty and unloaded both resolve to nil", func(t *testing.T) {
		assert.Nil(t, ix.Resolve("", 0))
		assert.Nil(t, CareIndex{}.Resolve("MW30", 0))
	})
}

func TestCareProse(t *testing.T) {
	ix := testCareIndex()
	assert.Equal(t, "machine wash 30°, do not bleach, iron low", CareProse(ix.Resolve("IL,DNB,MW30", 0)))
	assert.Equal(t, "lavage en machine 30°", CareProse(ix.Resolve("MW30", 2)))
	assert.Equal(t, "", CareProse(nil))
}

// A value that Normalize accepts must resolve back to the same codes in the same order — the two
// directions have to agree or the stored string and the rendered tag drift apart.
func TestCareNormalizeResolveRoundTrip(t *testing.T) {
	ix := testCareIndex()
	for _, in := range []string{"IL,DNB,MW30", "mw30", "PWC,DNDC", "DNB,IL,MW40,PWC"} {
		canonical, err := ix.Normalize(in)
		require.NoErrorf(t, err, "input %q", in)
		entries := ix.Resolve(canonical, 0)
		codes := make([]string, len(entries))
		for i, e := range entries {
			codes[i] = e.Code
		}
		assert.Equalf(t, SplitCareCodes(canonical), codes, "input %q", in)
	}
}
