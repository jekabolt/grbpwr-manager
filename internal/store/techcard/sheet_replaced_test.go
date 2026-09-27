package techcard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE SAVE-SIDE HALF OF technical_sheet — STRUCTURAL ALARMS, NOT BEHAVIOURAL PROOF (27.09, D-57).
//
// Everything here reads source text or expands a query without a database. It fires when the words of
// a read or the place of a call change, and a change it flags may still be harmless — so treat a red
// test here as «re-check the argument», not as a demonstrated defect, and a green one as nothing more
// than «the text still says what it said». The behaviour is proven elsewhere: the rule (which
// occurrence, which head, which words, the transition) by the entity tests
// (TestDesignSheetReplacedRefusal*), the reads and both doors by the DB probes in
// internal/store/design/replace_db_test.go (disposable container, CI=1).

// THE READS ASK THIS CARD, THESE FILES — AND ONLY REPLACED PICTURES, ONLY THE TECHNICAL SHEET.
//
// Text mutants it flags: dropping the card from either read (a replaced picture of another card
// would lock its file out of this sheet; another card's rows would count as this sheet's);
// dropping `replaced_by IS NOT NULL`; comparing something other than media_id; losing the columns
// the entity reads (sqlx under Unsafe would leave replaced_by NULL and the guard would pass
// everything); counting the moodboard as the sheet; putting an ORDER BY back on the picture read.
func TestSheetReplacedReadsNameTheCardTheFilesAndTheSheet(t *testing.T) {
	norm := func(q string) string { return strings.Join(strings.Fields(q), " ") }

	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture "+
			"WHERE tech_card_id = :card AND media_id IN (:media) AND replaced_by IS NOT NULL",
		norm(techCardSheetReplacedPictures))
	query, args, err := techCardSheetReplacedQuery(41, []int{900, 901})
	require.NoError(t, err)
	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture "+
			"WHERE tech_card_id = ? AND media_id IN (?, ?) AND replaced_by IS NOT NULL",
		norm(query))
	require.Equal(t, []any{41, 900, 901}, args, "the card, then every file of the sheet")

	require.Equal(t,
		"SELECT media_id, COUNT(*) AS n FROM tech_card_media "+
			"WHERE tech_card_id = :card AND media_id IN (:media) AND category = :technical GROUP BY media_id",
		norm(techCardStoredSheetRows))
	query, args, err = techCardStoredSheetQuery(41, []int{900, 901})
	require.NoError(t, err)
	require.Equal(t,
		"SELECT media_id, COUNT(*) AS n FROM tech_card_media "+
			"WHERE tech_card_id = ? AND media_id IN (?, ?) AND category = ? GROUP BY media_id",
		norm(query))
	require.Equal(t, []any{41, 900, 901, "technical"}, args, "the card, the files, the sheet's word")

	require.Equal(t,
		"SELECT id, tech_card_id, media_id, replaced_by FROM design_picture WHERE id = :id",
		norm(techCardSheetPictureLink),
		"a chain link is read by id with replaced_by and its card, or the head walk stops or trusts a foreign link")
}

// THE GUARD RUNS ON THE SAVE'S HANDLE, BEFORE THE DELETE LOOP — AND READS NOTHING ELSEWHERE.
//
// Two facts carry the rule and neither is a value a test without a database can observe. The stored
// count must be read before the DELETE loop: read after the rewrite it is the save's own incoming
// sheet, every occurrence looks inherited, and the guard passes everything. And both reads must run
// on the save's transaction: a count taken on another handle could still show a row another save has
// meanwhile taken off, and the file would come back. Where between the version check and the DELETE
// loop the call stands is a choice (ahead of the header UPDATE a refused save writes nothing), so the
// alarm does not pin it.
//
// Text mutants it flags: removing the call; moving it past the DELETE loop or the children insert;
// handing it a handle other than rep.DB() or anything but the incoming media; reading either the
// replaced pictures or the stored count on a handle other than the guard's own db.
func TestSheetReplacedGuardReadsOnTheSaveHandleBeforeTheSheetIsRewritten(t *testing.T) {
	body := funcBody(t, "techcard.go", "func (s *Store) UpdateTechCardTx")
	guard := strings.Index(body, "refuseReplacedPicturesOnTheSheet(ctx, rep.DB(), id, tc.Media)")
	require.GreaterOrEqual(t, guard, 0,
		"UpdateTechCardTx no longer runs the sheet guard on its own transaction with the incoming media")
	for _, write := range []string{"DELETE FROM %s WHERE tech_card_id", "insertTechCardChildren("} {
		at := strings.Index(body, write)
		require.GreaterOrEqual(t, at, 0, "the save changed shape: %q not found", write)
		require.Less(t, guard, at, "the guard must read the stored sheet before %q", write)
	}

	reads := funcBody(t, "sheet_replaced.go", "func refuseReplacedPicturesOnTheSheet")
	require.Contains(t, reads, "db.SelectContext(ctx, &replaced, query, args...)", "the replaced pictures are read on the guard's handle")
	require.Contains(t, reads, "techCardStoredSheetCounts(ctx, db, card, ids)", "the stored sheet is counted on the guard's handle")
	require.Contains(t, reads, "storeutil.QueryNamedOne[entity.DesignPicture](ctx, db, techCardSheetPictureLink", "chain links are read on the guard's handle")
	counts := funcBody(t, "sheet_replaced.go", "func techCardStoredSheetCounts")
	require.Contains(t, counts, "db.SelectContext(ctx, &rows, query, args...)")
}
