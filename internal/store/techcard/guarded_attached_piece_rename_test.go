package techcard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func attachedRenameFixture(t *testing.T) (*entity.GuardedZeroOperationPieceRename, *entity.TechCard, []entity.RollGoodsLine) {
	t.Helper()
	card, row, lines := completeSourceFixture()
	card.Pieces[0].Id = 45
	card.Pieces[1].Id = 46
	source, err := buildSavedAssemblyCompleteSourceV1(card, row, lines, completeSourceClock)
	require.NoError(t, err)
	return &entity.GuardedZeroOperationPieceRename{
		PieceLineKey: completeSourceMainPiece, ExpectedName: "Front", NextName: "Front panel", ScopeKey: row.ScopeKey,
		PatternLineKey: row.PatternLineKey, DxfSHA256: row.SHA256, SavedRowRevision: row.SavedRowRevision,
		Attached: &entity.GuardedAttachedPieceRenameAuthority{
			ExpectedServerAssemblyRevision: completeSourceClock, ExpectedCompleteSourceSHA256: source.SourceSHA256,
			ExpectedCurrentSidecarSHA256: strings.Repeat("a", 64),
		},
	}, card, lines
}

func TestGuardedAttachedPieceRenamePinsExactNameAndNativeRow(t *testing.T) {
	intent, card, lines := attachedRenameFixture(t)
	piece, err := entity.GuardedAttachedPieceRenamePiece(card, intent)
	require.NoError(t, err)
	require.Equal(t, 45, piece.Id)
	anchor, err := attachedPieceRenameAnchor(card, lines, intent)
	require.NoError(t, err)
	source, err := buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, intent.Attached.ExpectedCompleteSourceSHA256)
	require.NoError(t, err)
	require.True(t, attachedPieceRenameSourceOwnsPiece(source, anchor, piece.LineKey))
	require.False(t, attachedPieceRenameSourceOwnsPiece(source, anchor, card.Pieces[1].LineKey))

	renamed := *card
	renamed.Pieces = slices.Clone(card.Pieces)
	renamed.Pieces[0].Name = intent.NextName
	unchangedGeometry, err := buildSavedAssemblyCompleteSourceV1(&renamed, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.Equal(t, source.CanonicalJSON, unchangedGeometry.CanonicalJSON)
	renamed.LockVersion++
	newAnchor := anchor
	newAnchor.CardLockVersion++
	authority, err := buildSavedAssemblyCompleteSourceV1(&renamed, newAnchor, lines, "asm:v1:0123456789abcdef0123456789abcdef:20")
	require.NoError(t, err)
	require.NotEqual(t, source.SourceSHA256, authority.SourceSHA256)
	for i := range source.Rows {
		updated := authority.Rows[i]
		updated.CardLockVersion--
		require.Equal(t, source.Rows[i], updated)
	}
	require.Equal(t, source.OwnedSeams, authority.OwnedSeams)
	require.Equal(t, source.Aliases, authority.Aliases)

	stale := *intent
	stale.SavedRowRevision--
	_, err = attachedPieceRenameAnchor(card, lines, &stale)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	card.Pieces[0].CalloutNumber.Valid = true
	_, err = entity.GuardedAttachedPieceRenamePiece(card, intent)
	require.ErrorIs(t, err, entity.ErrTechCardAttachedPieceRenameUnsupported)
	card.Pieces[0].CalloutNumber.Valid = false
	card.Pieces[1].Name = " front PANEL "
	_, err = entity.GuardedAttachedPieceRenamePiece(card, intent)
	require.ErrorIs(t, err, entity.ErrTechCardAttachedPieceRenameUnsupported)
	card.Pieces[1].Name = " front "
	_, err = entity.GuardedAttachedPieceRenamePiece(card, intent)
	require.ErrorIs(t, err, entity.ErrTechCardAttachedPieceRenameUnsupported)
}

type attachedRenameSQL struct {
	fakeConn
	cardLock int64
	clock    int64
}

type attachedRenameConnector struct{ conn *attachedRenameSQL }

func (c attachedRenameConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c attachedRenameConnector) Driver() driver.Driver                        { return fakeDriver{} }

func (c *attachedRenameSQL) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, recordedStatement{query: q, args: args})
	switch {
	case strings.Contains(q, "FROM tech_card WHERE id = ? FOR UPDATE"):
		return &fakeRows{
			cols: []string{"lock_version", "approval_state", "approved_at", "released_at", "purpose", "stage", "measurement_unit", "create_request_id"},
			vals: [][]driver.Value{{c.cardLock, "draft", nil, nil, "garment", "production", "mm", nil}},
		}, nil
	case strings.Contains(q, "FROM tech_card_assembly_clock WHERE"):
		return &fakeRows{
			cols: []string{"lineage_id", "revision"},
			vals: [][]driver.Value{{"0123456789abcdef0123456789abcdef", c.clock}},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected SQL during attached rename: %s", q)
	}
}

func TestGuardedAttachedPieceRenameChecksClockAfterCardLockBeforeAnyWrite(t *testing.T) {
	intent, card, _ := attachedRenameFixture(t)
	conn := &attachedRenameSQL{cardLock: int64(card.LockVersion), clock: 20}
	db := sqlx.NewDb(sql.OpenDB(attachedRenameConnector{conn}), "mysql")
	defer db.Close()
	rep := guardedRenameRepo{db: db, cards: guardedRenameCardReader{card: card}}
	_, err := (&Store{}).UpdateTechCardTx(context.Background(), rep, card.Id,
		&entity.TechCardInsert{GuardedPieceRename: intent}, card.LockVersion)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	require.Len(t, conn.queries, 2)
	require.Contains(t, conn.queries[0].query, "FOR UPDATE")
	require.Contains(t, conn.queries[1].query, "tech_card_assembly_clock")
	require.Empty(t, conn.execs)
}

func TestGuardedAttachedPieceRenameWritesOnlyNameAndAudit(t *testing.T) {
	intent, card, _ := attachedRenameFixture(t)
	conn := &attachedRenameSQL{}
	db := sqlx.NewDb(sql.OpenDB(attachedRenameConnector{conn}), "mysql")
	defer db.Close()
	require.NoError(t, writeAttachedPieceRename(context.Background(), db, card.Id, card.LockVersion, card.Pieces[0].Id, "operator", intent))
	require.Len(t, conn.execs, 4)
	require.Contains(t, conn.execs[0].query, "UPDATE tech_card_piece SET name")
	require.Contains(t, conn.execs[0].query, "CAST(name AS BINARY)")
	require.Contains(t, conn.execs[1].query, "UPDATE tech_card SET lock_version = lock_version + 1, updated_by = ?")
	require.Contains(t, conn.execs[2].query, "INSERT INTO tech_card_assembly_clock")
	require.Contains(t, conn.execs[3].query, "INSERT INTO tech_card_revision")
}
