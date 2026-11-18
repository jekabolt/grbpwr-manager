package techcard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
)

type guardedRenameRepo struct {
	dependency.Repository
	db    dependency.DB
	cards dependency.TechCards
}

func (r guardedRenameRepo) DB() dependency.DB               { return r.db }
func (r guardedRenameRepo) TechCards() dependency.TechCards { return r.cards }

type guardedRenameCardReader struct {
	dependency.TechCards
	card *entity.TechCard
}

func (r guardedRenameCardReader) GetTechCardById(context.Context, int) (*entity.TechCard, error) {
	return r.card, nil
}

type guardedRenameSQL struct {
	fakeConn
	t            *testing.T
	cardLock     sync.Mutex
	firstAttempt chan struct{}
	attachment   bool
}

type guardedRenameSQLConnector struct {
	conn *guardedRenameSQL
}

func (c guardedRenameSQLConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c guardedRenameSQLConnector) Driver() driver.Driver                        { return fakeDriver{} }

func (c *guardedRenameSQL) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, recordedStatement{query: q, args: args})
	switch {
	case strings.Contains(q, "FROM tech_card WHERE id = ? FOR UPDATE"):
		close(c.firstAttempt)
		c.cardLock.Lock()
		return &fakeRows{
			cols: []string{"lock_version", "approval_state", "approved_at", "released_at", "purpose", "stage", "measurement_unit", "create_request_id"},
			vals: [][]driver.Value{{int64(14), "draft", nil, nil, "garment", "production", "mm", nil}},
		}, nil
	case strings.Contains(q, "tech_card_pattern_semantic_sidecar"):
		return &fakeRows{cols: []string{"attached"}, vals: [][]driver.Value{{c.attachment}}}, nil
	default:
		c.t.Errorf("unexpected SQL during protected update: %s", q)
		return nil, errors.New("unexpected query")
	}
}

func TestGuardedZeroOperationPieceRenameRefusesV1AttachmentWinningSameLock(t *testing.T) {
	ctx := context.Background()
	conn := &guardedRenameSQL{t: t, firstAttempt: make(chan struct{})}
	conn.cardLock.Lock()
	// Simulate the existing versionless V1 writer holding the shared card lock: guarded update
	// already started with lock 14, but the attachment commits first and the visible lock stays 14.
	go func() {
		<-conn.firstAttempt
		conn.attachment = true
		conn.cardLock.Unlock()
	}()
	db := sqlx.NewDb(sql.OpenDB(guardedRenameSQLConnector{conn}), "mysql")
	defer db.Close()
	intent, card, _ := guardedRenameFixture()
	rep := guardedRenameRepo{db: db, cards: guardedRenameCardReader{card: card}}
	snapshotCalled := false
	tc := &entity.TechCardInsert{
		GuardedPieceRename: intent,
		SnapshotReason:     "rename",
		BuildSnapshotFromLockedCard: func(*entity.TechCard) (string, error) {
			snapshotCalled = true
			return "{}", nil
		},
	}
	_, err := (&Store{}).UpdateTechCardTx(ctx, rep, 12, tc, 14)
	conn.cardLock.Unlock()
	if !errors.Is(err, entity.ErrTechCardPieceRenameProtected) {
		t.Fatalf("expected attached V1 refusal, got %v", err)
	}
	if len(conn.queries) != 2 || len(conn.execs) != 0 || snapshotCalled || !conn.attachment {
		t.Fatalf("refusal must preserve the new attachment, card and snapshot; queries=%d writes=%d snapshot=%v", len(conn.queries), len(conn.execs), snapshotCalled)
	}
}

func guardedRenameFixture() (*entity.GuardedZeroOperationPieceRename, *entity.TechCard, []entity.RollGoodsLine) {
	intent := &entity.GuardedZeroOperationPieceRename{
		PieceLineKey: "01ARZ3NDEKTSV4RRFFQ69G5FAV", ExpectedName: "Front", ScopeKey: "shell",
		PatternLineKey: "01ARZ3NDEKTSV4RRFFQ69G5FAW", SavedRowRevision: 6, DxfSHA256: strings.Repeat("a", 64),
	}
	line := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	card := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{{LineKey: intent.PieceLineKey, Name: "Front"}},
		Patterns: []entity.TechCardSizePattern{{
			LineKey: intent.PatternLineKey, SavedRowRevision: intent.SavedRowRevision,
			SHA256: sql.NullString{String: intent.DxfSHA256, Valid: true}, BomLineKey: sql.NullString{String: line, Valid: true},
		}},
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{{PieceLineKey: intent.PieceLineKey, FabricPurpose: "shell", BlockName: "FRONT_M"}},
	}}
	return intent, card, []entity.RollGoodsLine{{LineKey: line, Purpose: "shell", Section: entity.BomSectionFabric}}
}

func TestGuardedZeroOperationPieceRenameRequiresExactSavedBinding(t *testing.T) {
	for _, name := range []string{"current", "revision", "sha", "scope", "missing alias"} {
		t.Run(name, func(t *testing.T) {
			intent, card, lines := guardedRenameFixture()
			switch name {
			case "revision":
				card.Patterns[0].SavedRowRevision++
			case "sha":
				card.Patterns[0].SHA256.String = strings.Repeat("b", 64)
			case "scope":
				lines[0].Purpose = "lining"
			case "missing alias":
				card.PieceDxfAliases = nil
			}
			if got := matchesGuardedPieceRenamePattern(card, lines, intent); got != (name == "current") {
				t.Fatalf("binding accepted: %v", got)
			}
		})
	}
}

func TestGuardedZeroOperationPieceRenameResultRejectsChangesBeyondNameAndClocks(t *testing.T) {
	for _, name := range []string{"current", "wrong name", "new operation", "quantity", "alias", "bytes", "old revision"} {
		t.Run(name, func(t *testing.T) {
			intent, before, lines := guardedRenameFixture()
			_, after, _ := guardedRenameFixture()
			intent.NextName = "Передняя половинка"
			before.LockVersion = 14
			after.LockVersion = 15
			after.Pieces[0].Name = intent.NextName
			after.Patterns[0].SavedRowRevision++
			after.BomItems = []entity.TechCardBomItem{{
				LineKey: lines[0].LineKey, Section: lines[0].Section,
				Purpose: sql.NullString{String: lines[0].Purpose, Valid: true},
			}}
			switch name {
			case "wrong name":
				after.Pieces[0].Name = "Different name"
			case "new operation":
				after.Operations = []entity.TechCardOperation{{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}}}
			case "quantity":
				after.Pieces[0].PiecesPerGarment++
			case "alias":
				after.PieceDxfAliases[0].BlockName = "DRIFT"
			case "bytes":
				after.Patterns[0].URL = "https://host/replacement.dxf"
			case "old revision":
				after.Patterns[0].SavedRowRevision--
			}
			if matchesGuardedPieceRenameResult(before, after, intent) != (name == "current") {
				t.Fatal("unexpected transaction readback result")
			}
		})
	}
}
