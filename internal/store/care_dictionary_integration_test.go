package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The care vocabulary is seeded by migration 0217, so this asserts against the shipped data rather
// than fixtures: if a future migration renames a category or renumbers the print order, the failure
// lands here rather than on a storefront that has already rendered the wrong tag.
func TestCareDictionaryLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Straight through the payload the admin and storefront dictionaries are both built from.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err, "load dictionary")
	symbols := di.CareSymbols
	require.Len(t, symbols, 39, "the full ISO 3758 set the picker offers")

	byCode := make(map[string]entity.CareSymbol, len(symbols))
	for _, s := range symbols {
		byCode[s.Code] = s
	}

	t.Run("the codes the client already stores all exist", func(t *testing.T) {
		// The set the admin picker has been writing since before the vocabulary moved server-side.
		// A gap here means a live style resolves to nothing.
		for _, code := range []string{
			"MWN", "MW30", "MW40", "MW50", "MW60", "GW", "VGW", "HW", "DNW",
			"BA", "NCB", "DNB",
			"TDN", "TDL", "TDM", "TDH", "DNTD", "LD", "DF", "DD", "DIS", "LDS", "DFS", "DDS",
			"IL", "IM", "IH", "DNS", "DNI",
			"DCAS", "DCPS", "DCASE", "GDC", "VGDC", "DNDC",
			"PWC", "GPWC", "VGPWC", "DNWC",
		} {
			assert.Containsf(t, byCode, code, "%s is stored by the picker but missing from the dictionary", code)
		}
	})

	t.Run("returned in canonical print order", func(t *testing.T) {
		for i := 1; i < len(symbols); i++ {
			assert.Lessf(t, symbols[i-1].SortOrder, symbols[i].SortOrder,
				"%s before %s", symbols[i-1].Code, symbols[i].Code)
		}
		// wash -> bleach -> dry -> iron -> professional, which is the order a care tag reads in.
		assert.Equal(t, "Washing", symbols[0].Category)
		assert.Equal(t, "Professional Care", symbols[len(symbols)-1].Category)
	})

	t.Run("only professional care nests", func(t *testing.T) {
		for _, s := range symbols {
			if s.Category == "Professional Care" {
				assert.Truef(t, s.SubCategory.Valid, "%s must carry a sub-category", s.Code)
			} else {
				assert.Falsef(t, s.SubCategory.Valid, "%s must not nest", s.Code)
			}
		}
	})

	t.Run("every symbol carries wording in every active language", func(t *testing.T) {
		rows, err := testDB.QueryContext(ctx,
			`SELECT id FROM language WHERE is_active = TRUE AND code <> 'en'`)
		require.NoError(t, err)
		defer rows.Close()
		var languageIDs []int
		for rows.Next() {
			var id int
			require.NoError(t, rows.Scan(&id))
			languageIDs = append(languageIDs, id)
		}
		require.NoError(t, rows.Err())
		require.NotEmpty(t, languageIDs, "the storefront supports more than English")

		for _, s := range symbols {
			assert.NotEmptyf(t, s.ShortProse, "%s has no English wording", s.Code)
			for _, id := range languageIDs {
				tr, ok := s.Translations[id]
				assert.Truef(t, ok, "%s has no wording for language %d", s.Code, id)
				assert.NotEmptyf(t, tr.ShortProse, "%s language %d is blank", s.Code, id)
			}
		}
	})

	t.Run("resolves a real tag end to end", func(t *testing.T) {
		ix := entity.BuildCareIndex(symbols)

		// Stored out of order, as a picker writing map-iteration order would.
		canonical, err := ix.Normalize("IL,DNDC,MW30,DNTD,DNB")
		require.NoError(t, err)
		assert.Equal(t, "MW30,DNB,DNTD,IL,DNDC", canonical)

		assert.Equal(t, "machine wash 30°, do not bleach, do not tumble dry, iron low, do not dry clean",
			entity.CareProse(ix.Resolve(canonical, 0)))

		// ...and the same tag in French, from the same stored string.
		var frID int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT id FROM language WHERE code = 'fr'`).Scan(&frID))
		assert.Equal(t, "lavage en machine 30°, ne pas javelliser, ne pas sécher en tambour, repassage doux, ne pas nettoyer à sec",
			entity.CareProse(ix.Resolve(canonical, frID)))
	})

	t.Run("the shipped seed survives a round trip through validation", func(t *testing.T) {
		// Every seeded code must be one the write path will accept — a code the picker can offer but
		// UpdateStyle rejects would be a dead entry.
		ix := entity.BuildCareIndex(symbols)
		for _, s := range symbols {
			got, err := ix.Normalize(s.Code)
			assert.NoErrorf(t, err, "%s is offered but not writable", s.Code)
			assert.Equal(t, s.Code, got)
		}
	})

	t.Run("legacy free text still reads", func(t *testing.T) {
		// The shape already sitting in the column on beta.
		ix := entity.BuildCareIndex(symbols)
		assert.Nil(t, ix.Resolve("Machine wash cold at 30, do not tumble dry", 0))
	})

}
