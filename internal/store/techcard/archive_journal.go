package techcard

import (
	"context"
	"fmt"
)

// archiveJournalSection is the journal section an archive export is filed under. `section` is a free
// VARCHAR with no CHECK (0162 deferred that one and never came back), so a new word costs nothing
// and says exactly what happened.
const archiveJournalSection = "archive"

// archiveJournalAction is what goes in the `action` column, and it is 'other' ON PURPOSE.
//
// 0162 put a CHECK on that column with a CLOSED list — created, updated, approved, released,
// reverted, role_assigned, other — so writing 'exported' would not add a journal line, it would
// make the INSERT fail. And because this write is best-effort (the archive already exists by then,
// see journalArchiveExport), the failure would be invisible: the export would report success and
// the audit trail would be silently short.
//
// Widening the CHECK is a migration, and an ADD CONSTRAINT on this table copies it whole. This
// feature does not need one: 'other' is the escape hatch the enum was given for exactly this, and
// the SECTION plus the summary say the rest. When somebody does widen it, the only line to change
// is this one.
const archiveJournalAction = "other"

// AppendTechCardArchiveExportedEvent records one archive export in the card's auto-journal — who
// took the whole card out of the building, and when.
//
// It exists because nothing else remembers. The object expires in days, the presigned link in
// minutes, and the card itself is not touched by an export at all: without this row, «was this
// style ever sent to a factory» has no answer anywhere in the database.
//
// Same table, same writer and the same append-only discipline as the creation and release events
// (appendTechCardRevision); this is a thin exported door onto it, because the journal writer is
// package-private and the handler that needs it lives in apisrv.
func (s *Store) AppendTechCardArchiveExportedEvent(ctx context.Context, techCardID int, author, summary string) error {
	if techCardID <= 0 {
		return fmt.Errorf("can't journal an archive export: tech card id is required")
	}
	return appendTechCardRevision(ctx, s.DB, techCardID, author, archiveJournalSection, archiveJournalAction, summary)
}
