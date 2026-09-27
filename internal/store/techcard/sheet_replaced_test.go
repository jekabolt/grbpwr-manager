package techcard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE SAVE-SIDE HALF OF technical_sheet, WITHOUT A DATABASE (27.09, D-57).
//
// The rule (which items, which head, which words) is proven in entity
// (TestDesignSheetReplacedRefusal*). The live halves — the save refused through the real save path,
// the two orders against FlattenEditLayer, the race — are the DB probes in
// internal/store/design/replace_db_test.go (disposable container, CI=1). What lives here is what
// only the SHAPE of the code holds: the words of the read and where the guard stands in the save.

// THE READ ASKS THIS CARD, THESE FILES, AND ONLY REPLACED PICTURES.
//
// MUTATIONS IT CATCHES: dropping the card (a replaced picture of ANOTHER card would lock its file out
// of this sheet — the file is global, the sheet is not); dropping `replaced_by IS NOT NULL` (the read
// would lock every picture of these files, and the rule would lean on the entity alone); comparing
// something other than media_id; losing the columns the walk and the entity read (sqlx under Unsafe
// would leave replaced_by NULL and the guard would pass everything).
func TestSheetReplacedReadNamesTheCardTheFilesAndTheReplacement(t *testing.T) {
	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture "+
			"WHERE tech_card_id = :card AND media_id IN (:media) AND replaced_by IS NOT NULL ORDER BY id",
		strings.Join(strings.Fields(techCardSheetReplacedPictures), " "))
	query, args, err := techCardSheetReplacedQuery(41, []int{900, 901})
	require.NoError(t, err)
	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture "+
			"WHERE tech_card_id = ? AND media_id IN (?, ?) AND replaced_by IS NOT NULL ORDER BY id",
		strings.Join(strings.Fields(query), " "))
	require.Equal(t, []any{41, 900, 901}, args, "the card, then every file of the sheet")

	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture WHERE id = :id",
		strings.Join(strings.Fields(techCardSheetPictureLink), " "),
		"a chain link is read by id with replaced_by, or the head walk stops at the first link")
}

// THE GUARD RUNS IN THE SAVE'S OWN TRANSACTION, BEFORE THE SAVE WRITES ANYTHING.
//
// The order is the whole point and no value can prove it: a guard that reads on another handle, or
// after the children are rewritten, passes every single-session probe and reopens exactly the race
// it exists for (the flatten does not bump lock_version, and a deadlock victim is retried).
//
// MUTATIONS IT CATCHES: removing the call; moving it after the header UPDATE, the DELETE loop or the
// children insert; handing it a handle other than rep.DB(); handing it anything but the incoming
// media.
func TestSheetReplacedGuardRunsInTheSaveTransactionBeforeAnyWrite(t *testing.T) {
	body := funcBody(t, "techcard.go", "func (s *Store) UpdateTechCardTx")
	guard := strings.Index(body, "refuseReplacedPicturesOnTheSheet(ctx, rep.DB(), id, tc.Media)")
	require.GreaterOrEqual(t, guard, 0,
		"UpdateTechCardTx no longer refuses a replaced picture on the technical sheet in its own transaction")
	for _, write := range []string{"UPDATE tech_card SET", "DELETE FROM %s WHERE tech_card_id", "insertTechCardChildren("} {
		at := strings.Index(body, write)
		require.GreaterOrEqual(t, at, 0, "the save changed shape: %q not found", write)
		require.Less(t, guard, at, "the guard must run before %q", write)
	}
}
