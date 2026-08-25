package content

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jmoiron/sqlx/reflectx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Пробы на media.content_hash БЕЗ БАЗЫ. Живая проверка колонки — дело контейнерного прогона;
// здесь закрыты ровно те два способа сломать её, которые база и так не поймает раньше
// продакшена: несвязываемый именованный запрос и пустая строка вместо NULL.

// TestMediaContentHashQueriesBind is the colon trap, applied to both queries that now mention
// content_hash. makeQuery is exactly what the store calls at runtime, so a query that binds
// here binds there; a stray ':' anywhere in the text (a comment, a literal, a typo) fails the
// bind for the WHOLE statement, and the failure would only surface on the first upload.
func TestMediaContentHashQueriesBind(t *testing.T) {
	t.Run("AddMedia binds every placeholder", func(t *testing.T) {
		q, args, err := storeutil.MakeQuery(addMediaQuery, map[string]any{
			"fullSize": "u", "fullSizeWidth": 1, "fullSizeHeight": 2,
			"compressed": "u", "compressedWidth": 1, "compressedHeight": 2,
			"thumbnail": "u", "thumbnailWidth": 1, "thumbnailHeight": 2,
			"blurHash": sql.NullString{}, "contentHash": "abc",
		})
		require.NoError(t, err)
		assert.NotContains(t, q, ":", "no bind name may survive into the final SQL")
		assert.Len(t, args, 11, "eleven columns, eleven positional args")
		assert.Equal(t, "abc", args[10], "content_hash is the last bound column")
	})

	t.Run("FindMediaByContentHash binds the hash", func(t *testing.T) {
		q, args, err := storeutil.MakeQuery(findMediaByContentHashQuery, map[string]any{"hash": "abc"})
		require.NoError(t, err)
		assert.NotContains(t, q, ":")
		assert.Equal(t, []any{"abc"}, args)
	})

	// Neither query may carry SQL comments: '--' and '#' both survive into the scanner.
	for name, q := range map[string]string{
		"addMediaQuery":               addMediaQuery,
		"findMediaByContentHashQuery": findMediaByContentHashQuery,
	} {
		assert.NotContainsf(t, q, "--", "%s: SQL comments do not belong in a named query", name)
		assert.NotContainsf(t, q, "#", "%s: '#' opens a MySQL comment", name)
	}
}

// TestAddMediaWritesContentHashColumn keeps the column in the statement. Dropping it is a
// silent regression: inserts keep working, every new row simply gets NULL, and the archive
// import then re-uploads files it already has — with nothing anywhere reporting a fault.
func TestAddMediaWritesContentHashColumn(t *testing.T) {
	assert.Contains(t, addMediaQuery, "content_hash",
		"the column must be in the INSERT column list")
	assert.Contains(t, addMediaQuery, ":contentHash",
		"the column must actually be bound, not defaulted")
	assert.Equal(t, strings.Count(addMediaQuery, ","), 2*10,
		"column list and value list must stay the same length (10 separators each)")
}

// TestMediaFullScansContentHash proves the column can be READ BACK, which nothing else here
// can prove and no runtime error ever will.
//
// The store handle is sqlx's Unsafe() (see store.New), so an unmapped column is silently
// dropped instead of raising "missing destination name". That is what keeps an older binary
// working against the migrated schema — and it is also what would turn a typo in the `db`
// tag into a permanent, noiseless bug: every de-duplication lookup would read an empty hash,
// match nothing, and re-upload every file forever without one error line.
//
// The mapper below is the one sqlx builds internally (tag "db", strings.ToLower for untagged
// fields), so agreeing with it is the same thing as agreeing with StructScan.
func TestMediaFullScansContentHash(t *testing.T) {
	names := reflectx.NewMapperFunc("db", strings.ToLower).
		TypeMap(reflect.TypeOf(entity.MediaFull{})).Names

	fi, ok := names["content_hash"]
	require.True(t, ok, "entity.MediaFull must map the content_hash column, or reads silently drop it")
	require.Equal(t, "ContentHash", fi.Field.Name)

	// CONTROL: a column that does not exist must not map, otherwise the assertion above
	// would pass for any spelling and prove nothing.
	_, bogus := names["content_hash_typo"]
	require.False(t, bogus)
}

// TestNormalizedContentHashNeverBindsEmptyString is the empty-string vs NULL boundary.
//
// It matters because the empty string is a REAL value that compares equal to itself: one
// empty hash written by a broken upload would make every other empty-hash row look like the
// same file, and the import would happily reuse an unrelated image. NULL is the only
// spelling of "not computed" that compares equal to nothing — including to itself.
func TestNormalizedContentHashNeverBindsEmptyString(t *testing.T) {
	cases := []struct {
		name string
		in   sql.NullString
		want any
	}{
		{"not computed", sql.NullString{}, nil},
		{"valid but empty", sql.NullString{String: "", Valid: true}, nil},
		{"valid but blank", sql.NullString{String: "   ", Valid: true}, nil},
		{"uppercase hex is normalised down", sql.NullString{String: "ABCDEF", Valid: true}, "abcdef"},
		{"surrounding blanks are trimmed", sql.NullString{String: " abcdef\n", Valid: true}, "abcdef"},
		{"CONTROL: an ordinary hash passes through", sql.NullString{String: "abcdef", Valid: true}, "abcdef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, normalizedContentHash(c.in))
		})
	}
}
