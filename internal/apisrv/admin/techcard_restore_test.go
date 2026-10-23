package admin

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ПРОБЫ «ВЕРНУТЬ v.N» (RestoreTechCardSnapshot) И СВЯЗИ СНИМОК ↔ УХОДЯЩИЙ ЛИСТ.
//
// Стор замокан: проверяется решение хендлера — что он отказывает, когда должен, и что в стор уходит
// ровно та запись, которую сделал бы UpdateTechCard: payload из снимка, снимок ТЕКУЩЕГО состояния с
// причиной restore:<id>, версия, при которой этот снимок верен. Запись строк истории и их snapshot_id
// — транзакция стора (UpdateTechCardTx → insertTechCardPatterns → archiveTechCardPattern), её здесь
// нет и быть не может: тесты internal/store ходят в живую базу.

const (
	restoreCardID     = 7
	restoreSnapshotID = int64(42)
	restoreHost       = "files.grbpwr.com"
	restoreSheetV6    = "https://files.grbpwr.com/tech-card-patterns/front_v6.dxf"
	restoreSheetV7    = "https://files.grbpwr.com/tech-card-patterns/front_v7.dxf"

	keyFront  = "01JRSTFRONT000000000000000"
	keyCollar = "01JRSTCOLLAR00000000000000"
	keyMain   = "01JRSTMAINFABRIC0000000000"
	keySheet  = "01JRSTSHEET000000000000000"
)

func restoreHosts(t *testing.T) {
	t.Helper()
	dto.SetManagedPatternHosts(restoreHost)
	t.Cleanup(func() { dto.SetManagedPatternHosts() })
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// restoreCardAtV6 is the card while sheet v6 was current: the state the DXF save froze. It is
// NULL-heavy on purpose — every column whose NULL the reader emits as an ABSENT wire field (purpose,
// kind, label part, direction, wastage pair, countable pair, est. usage, cut symmetry, fusing, sheet
// name / binding / hash, callout spec, header prose) is NULL here, because each of those absences
// would mean «keep what is stored» if the read model were echoed as a write.
func restoreCardAtV6() *entity.TechCard {
	return &entity.TechCard{
		Id:          restoreCardID,
		LockVersion: 2,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:     nullStr("TC-RESTORE"),
			Name:            "restore subject at v6",
			Stage:           entity.TechCardStageProto,
			ApprovalState:   entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{1, 2},
			Media: []entity.TechCardMediaItem{
				{MediaId: 55, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
			},
			Callouts: []entity.TechCardCallout{
				{Number: 1, MediaId: sql.NullInt32{Int32: 55, Valid: true}, Part: nullStr("collar")},
			},
			BomItems: []entity.TechCardBomItem{{
				Id:        11,
				LineKey:   keyMain,
				Section:   entity.BomSectionFabric,
				Name:      "shell twill",
				UnitPrice: decimal.NullDecimal{Decimal: decimal.RequireFromString("12.50"), Valid: true},
				Currency:  nullStr("EUR"),
			}},
			Pieces: []entity.TechCardPiece{
				{Id: 21, Name: "FRONT", LineKey: keyFront, PiecesPerGarment: 2},
				{Id: 22, Name: "COLLAR", LineKey: keyCollar, PiecesPerGarment: 1},
			},
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{
				{FabricPurpose: "main", BlockName: "CLR_3", PieceId: 22, PieceLineKey: keyCollar},
			},
			Patterns: []entity.TechCardSizePattern{
				{LineKey: keySheet, URL: restoreSheetV6, Version: 6},
			},
			Operations: []entity.TechCardOperation{
				{OperationType: entity.OpTypeMachine, MachineType: nullStr("lockstitch"), Zone: entity.ZoneOuter, Review: nullStr("split"),
					PieceLineKeys: []string{keyFront}},
				{OperationType: entity.OpTypeMachine, MachineType: nullStr("lockstitch"), Zone: entity.ZoneOuter, PieceLineKeys: []string{keyCollar}},
			},
		},
	}
}

// restoreCardNow is the same card after the v7 upload and a later stage move: the sheet is v7, the
// collar alias points at the replacement block, both steps carry the reconciliation's marks.
func restoreCardNow() *entity.TechCard {
	tc := restoreCardAtV6()
	tc.LockVersion = 3
	tc.Name = "restore subject now"
	tc.Stage = entity.TechCardStageFit
	tc.PieceDxfAliases = []entity.TechCardPieceDxfAlias{{FabricPurpose: "main", BlockName: "CLR_3A", PieceId: 22, PieceLineKey: keyCollar}}
	tc.Patterns = []entity.TechCardSizePattern{{LineKey: keySheet, URL: restoreSheetV7, Version: 7}}
	tc.Operations = []entity.TechCardOperation{
		{OperationType: entity.OpTypeMachine, MachineType: nullStr("lockstitch"), Zone: entity.ZoneOuter, Review: nullStr("remapped"),
			PieceLineKeys: []string{keyFront}},
		{OperationType: entity.OpTypeMachine, MachineType: nullStr("lockstitch"), Zone: entity.ZoneOuter, Review: nullStr("remapped"),
			PieceLineKeys: []string{keyCollar}},
	}
	return tc
}

func restoreSnapshotRow(t *testing.T, card *entity.TechCard) *entity.TechCardSnapshot {
	t.Helper()
	blob, err := marshalTechCardSnapshot(card, dto.CostingFx{})
	require.NoError(t, err)
	return &entity.TechCardSnapshot{Id: restoreSnapshotID, TechCardId: restoreCardID, LockVersion: card.LockVersion,
		Reason: "dxf-reupload", Snapshot: blob, CreatedAt: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)}
}

func restoreCtx() context.Context {
	return authsrv.PutAdminUsername(fullAccessCtx(), "restorer")
}

type restoreRig struct {
	repo  *mocks.MockRepository
	cards *mocks.MockTechCards
	srv   *Server
}

func newRestoreRig(t *testing.T) restoreRig {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	cards.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil).Maybe()
	return restoreRig{repo: repo, cards: cards, srv: &Server{repo: repo}}
}

func restoreReq(expected int32) *pb_admin.RestoreTechCardSnapshotRequest {
	return &pb_admin.RestoreTechCardSnapshotRequest{
		TechCardId: restoreCardID, SnapshotId: restoreSnapshotID, ExpectedLockVersion: expected,
	}
}

// buildCapturedSnapshot mirrors the part of UpdateTechCardTx that a mocked store cannot perform:
// after locking and transactionally re-reading the card it invokes the handler-supplied builder.
func buildCapturedSnapshot(t *testing.T, tc *entity.TechCardInsert, locked *entity.TechCard) {
	t.Helper()
	require.NotNil(t, tc.BuildSnapshotFromLockedCard)
	blob, err := tc.BuildSnapshotFromLockedCard(locked)
	require.NoError(t, err)
	tc.SnapshotJSON = blob
	tc.SnapshotLockVersion = locked.LockVersion
}

// A SNAPSHOT OF ANOTHER CARD IS NOT FOUND. The store reads it by (id, tech_card_id), so a foreign id
// comes back as no row — and the answer must be NotFound, never a restore of somebody else's card.
func TestRestoreSnapshotOfAnotherCardIsNotFound(t *testing.T) {
	rig := newRestoreRig(t)
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(nil, sql.ErrNoRows)

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	// No card read, no write: the strict mock fails the test on any further call.
}

// A STALE LOCK IS THE SAVE'S CONFLICT. Same code and words as UpdateTechCard (Aborted, «reload and
// retry»), and nothing reaches the store's write.
func TestRestoreOnAStaleLockIsAborted(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(2)) // the card is at 3
	require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "reload and retry")
}

// The store's own lock check (a save that landed between the read and the transaction) maps the same.
func TestRestoreLosingTheRaceInTheStoreIsAborted(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).Return(nil, entity.ErrTechCardConflict)

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
}

func TestRestoreRefusesAReleasedCard(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	now := restoreCardNow()
	now.ApprovalState = entity.TechCardApprovalReleased
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(now, nil)

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "released")
}

// AN UNREADABLE OR MISFILED SNAPSHOT IS REFUSED IN WORDS — never restored «as far as it goes».
func TestRestoreRefusesAnUnreadableSnapshot(t *testing.T) {
	restoreHosts(t)
	cases := map[string]func(*entity.TechCardSnapshot){
		"empty":          func(s *entity.TechCardSnapshot) { s.Snapshot = "" },
		"not json":       func(s *entity.TechCardSnapshot) { s.Snapshot = "{not json" },
		"no contract":    func(s *entity.TechCardSnapshot) { s.Snapshot = `{"id": 7}` },
		"another card's": func(s *entity.TechCardSnapshot) { s.TechCardId = 8 },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newRestoreRig(t)
			row := restoreSnapshotRow(t, restoreCardAtV6())
			spoil(row)
			rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).Return(row, nil)

			_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
			require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), "cannot be restored")
		})
	}
}

// A snapshot with fewer sizes would let the store prune chart rows it cannot give back.
func TestRestoreRefusesASnapshotWithFewerSizes(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	v6 := restoreCardAtV6()
	v6.SizeIds = []int{1}
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, v6), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "fewer sizes")
}

// THE RESTORE IS THE SAVE OF THE SNAPSHOT, PLUS A SNAPSHOT OF NOW.
//
// What reaches the store is (1) the v6 content — sheet url and number, pieces, the alias that pointed
// at the old block, and EXACTLY the review marks v6 had (one «split», one none — not cleared, not
// re-marked); (2) the card's current identity and lifecycle; (3) a snapshot of the CURRENT card
// (sheet v7, the «remapped» marks) under reason restore:42, built from the store's locked version.
func TestRestoreWritesTheSnapshotThroughTheSave(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	now := restoreCardNow()
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(now, nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) {
			buildCapturedSnapshot(t, tc, now)
			written = tc
		}).
		Return(nil, entity.ErrTechCardConflict) // stop before the post-commit work

	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.Aborted, status.Code(err), "the payload must reach the store: %v", err)
	require.NotNil(t, written)

	// (3) the undo point.
	require.Equal(t, "restore:42", written.SnapshotReason)
	require.Equal(t, []string{"restored 23.10 10:00"}, written.SnapshotChangedPaths)
	require.True(t, written.SnapshotPinned)
	require.Equal(t, 3, written.SnapshotLockVersion, "the snapshot is valid only at the version it was read at")
	var frozenNow pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(written.SnapshotJSON), &frozenNow))
	require.True(t, proto.Equal(dto.ConvertEntityTechCardToPb(now, rig.srv.costingFx(restoreCtx())), &frozenNow),
		"the new snapshot must be the card as it is NOW, so the restore can itself be undone")

	// (1) the content of v6.
	require.Equal(t, "restore subject at v6", written.Name)
	require.Len(t, written.Patterns, 1)
	require.Equal(t, restoreSheetV6, written.Patterns[0].URL)
	require.Equal(t, 6, written.Patterns[0].Version)
	require.Equal(t, keySheet, written.Patterns[0].LineKey, "the same sheet row, so v7 goes to the history")
	require.True(t, written.PieceDxfAliasesSet)
	require.Len(t, written.PieceDxfAliases, 1)
	require.Equal(t, "CLR_3", written.PieceDxfAliases[0].BlockName)
	require.Len(t, written.Pieces, 2)
	require.Equal(t, keyFront, written.Pieces[0].LineKey)
	require.Equal(t, keyCollar, written.Pieces[1].LineKey)
	require.Len(t, written.Operations, 2)
	require.Equal(t, sql.NullString{String: "split", Valid: true}, written.Operations[0].Review)
	require.False(t, written.Operations[1].Review.Valid, "v6 had no mark on this step; the restore must not keep «remapped»")

	// (2) identity and lifecycle are today's.
	require.Equal(t, entity.TechCardStageFit, written.Stage)
	require.Equal(t, entity.TechCardApprovalDraft, written.ApprovalState)
	require.Equal(t, "TC-RESTORE", written.StyleNumber.String)
}

// THE FULL ROUND: the answer is the card as GetTechCard answers it, read after the commit, and the
// post-commit work of the save runs (here: nothing to delete, no linked products, not released).
func TestRestoreAnswersWithTheCardAsRead(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	acct := mocks.NewMockAccounting(t)
	rig.repo.EXPECT().Accounting().Return(acct).Maybe()
	acct.EXPECT().GetVatRatesFor(mock.Anything, mock.Anything).Return(nil, nil).Maybe()

	after := restoreCardAtV6()
	after.LockVersion = 4
	after.Stage = entity.TechCardStageFit
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
		Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil).Once()
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(after, nil)
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).Return(nil, nil)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, restoreCardID).Return(after, nil)

	resp, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.NoError(t, err)
	require.EqualValues(t, 4, resp.GetTechCard().GetLockVersion())
	require.Equal(t, restoreSheetV6, resp.GetTechCard().GetTechCard().GetPatterns()[0].GetUrl())
}

// A COSTING-BLIND RESTORE. Without costing:write the payload may carry no money, and the pipeline puts
// the CURRENT money back — a faithful restore only when the snapshot's money is the same. Different
// money is refused; equal money goes through with the stored prices.
func TestRestoreWithoutCostingWrite(t *testing.T) {
	restoreHosts(t)
	blindCtx := authsrv.PutAdminUsername(authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite},
	}), "content-manager")

	t.Run("different money is refused", func(t *testing.T) {
		rig := newRestoreRig(t)
		v6 := restoreCardAtV6()
		v6.BomItems[0].UnitPrice = decimal.NullDecimal{Decimal: decimal.RequireFromString("9.90"), Valid: true}
		rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
			Return(restoreSnapshotRow(t, v6), nil)
		rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)

		_, err := rig.srv.RestoreTechCardSnapshot(blindCtx, restoreReq(3))
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("same money goes through with the stored prices", func(t *testing.T) {
		rig := newRestoreRig(t)
		rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).
			Return(restoreSnapshotRow(t, restoreCardAtV6()), nil)
		rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)
		var written *entity.TechCardInsert
		rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
			mock.AnythingOfType("*entity.TechCardInsert"), 3).
			Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
			Return(nil, entity.ErrTechCardConflict)

		_, err := rig.srv.RestoreTechCardSnapshot(blindCtx, restoreReq(3))
		require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
		require.NotNil(t, written)
		require.Len(t, written.BomItems, 1)
		require.True(t, written.BomItems[0].UnitPrice.Decimal.Equal(decimal.RequireFromString("12.50")))
	})
}

// THE LINK BETWEEN A SNAPSHOT AND THE SHEET IT RETIRES, HANDLER HALF.
//
// A save with snapshot_reason freezes the card it REPLACES: the blob is the transactionally re-read
// card (sheet v6), never the payload (sheet v7), and carries the version held by the card-row lock.
// The store half — the snapshot row is inserted first in the same
// transaction and its id goes onto the history row of v6, the OUTGOING sheet, while v7 gets no history
// row — is UpdateTechCardTx → insertTechCardChildren(…, snapshotIDForArchive) → insertTechCardPatterns →
// archiveTechCardPattern, which only ever archives replaced or removed rows.
func TestSaveWithSnapshotReasonFreezesTheOutgoingCard(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	stored := restoreCardAtV6()
	stored.LockVersion = 3
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(stored, nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) {
			buildCapturedSnapshot(t, tc, stored)
			written = tc
		}).
		Return(nil, entity.ErrTechCardConflict)

	payload := dto.ConvertEntityTechCardToPb(restoreCardNow(), dto.CostingFx{}).GetTechCard()
	payload.OperationReviewAware = true
	payload.MachineFieldsAware = true
	payload.LabelsAware = true
	_, err := rig.srv.UpdateTechCard(restoreCtx(), &pb_admin.UpdateTechCardRequest{
		Id: restoreCardID, ExpectedLockVersion: 3, TechCard: payload, SnapshotReason: "dxf-reupload",
	})
	require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
	require.NotNil(t, written)
	require.Equal(t, "dxf-reupload", written.SnapshotReason)
	require.Equal(t, 3, written.SnapshotLockVersion)
	require.Equal(t, restoreSheetV7, written.Patterns[0].URL, "the save writes v7")
	var frozen pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(written.SnapshotJSON), &frozen))
	require.Equal(t, restoreSheetV6, frozen.GetTechCard().GetPatterns()[0].GetUrl(),
		"the snapshot is the card v7 replaces — restoring it brings back v6")
	require.Empty(t, frozen.GetTechCard().GetOperations()[1].GetReview(),
		"and the steps as they were before the reconciliation marked them")
}

// A CLIENT THAT DOES NOT KNOW snapshot_reason STILL HANDS THE STORE THE SAME PRE-SAVE CARD.
// SnapshotReason deliberately stays empty: UpdateTechCardTx uses the blob only if pattern
// reconciliation archives an outgoing row, and the ordinary save keeps its one generic journal row.
func TestSaveWithoutSnapshotReasonStillFreezesTheOutgoingCardForPatternHistory(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	stored := restoreCardAtV6()
	stored.LockVersion = 3
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(stored, nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) {
			buildCapturedSnapshot(t, tc, stored)
			written = tc
		}).
		Return(nil, entity.ErrTechCardConflict)

	payload := dto.ConvertEntityTechCardToPb(restoreCardNow(), dto.CostingFx{}).GetTechCard()
	payload.OperationReviewAware = true
	payload.MachineFieldsAware = true
	payload.LabelsAware = true
	_, err := rig.srv.UpdateTechCard(restoreCtx(), &pb_admin.UpdateTechCardRequest{
		Id: restoreCardID, ExpectedLockVersion: 3, TechCard: payload,
	})
	require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
	require.NotNil(t, written)
	require.Empty(t, written.SnapshotReason, "an implicit pattern snapshot must not alter the revision journal")
	require.Equal(t, 3, written.SnapshotLockVersion)
	var frozen pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(written.SnapshotJSON), &frozen))
	require.Equal(t, restoreSheetV6, frozen.GetTechCard().GetPatterns()[0].GetUrl(),
		"the lazy store snapshot must restore the outgoing sheet, not the incoming replacement")
}

func TestSaveSessionEnablesAutosaveHistoryWithoutChangingExplicitSnapshotSemantics(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	stored := restoreCardAtV6()
	stored.LockVersion = 3
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(stored, nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
		Return(nil, entity.ErrTechCardConflict)

	payload := dto.ConvertEntityTechCardToPb(restoreCardNow(), dto.CostingFx{}).GetTechCard()
	payload.OperationReviewAware = true
	payload.MachineFieldsAware = true
	payload.LabelsAware = true
	_, err := rig.srv.UpdateTechCard(restoreCtx(), &pb_admin.UpdateTechCardRequest{
		Id: restoreCardID, ExpectedLockVersion: 3, TechCard: payload,
		SaveSessionId: "68d21d4d-5c6f-4e4f-9290-0c70e62395c7",
	})
	require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
	require.NotNil(t, written)
	require.Empty(t, written.SnapshotReason, "autosave history must not create an explicit journal reason")
	require.Equal(t, "68d21d4d-5c6f-4e4f-9290-0c70e62395c7", written.SnapshotSaveSessionId)
	require.NotEmpty(t, written.SnapshotChangedPaths)
}

// TestRestorePayloadSpeaksEveryField — the read model is not a write payload: for a dozen fields an
// ABSENT value means «keep what is stored», and the reader emits absence for NULL. A restore payload
// in which any such absence survived would keep today's value where the snapshot had none. Every
// presence flag the converter derives must therefore come out «spoken».
func TestRestorePayloadSpeaksEveryField(t *testing.T) {
	restoreHosts(t)
	now := restoreCardNow()
	now.MoodNote = nullStr("today's mood")
	now.BomItems[0].Purpose = nullStr("main")
	snap := restoreSnapshotRow(t, restoreCardAtV6())
	frozen, err := decodeTechCardSnapshot(snap)
	require.NoError(t, err)
	current := dto.ConvertEntityTechCardToPb(now, dto.CostingFx{}).GetTechCard()

	// The probe has teeth: the read model echoed verbatim leaves absences behind on this fixture
	// (today: the callout spec, the BOM countable pair and est. usage — and, outside the *Omitted
	// flags, the nil wrappers and sections asserted below).
	raw, err := dto.ConvertPbTechCardInsertToEntity(proto.Clone(frozen.GetTechCard()).(*pb_common.TechCardInsert))
	require.NoError(t, err)
	var rawUnspoken []string
	walkPresenceFlags(reflect.ValueOf(raw).Elem(), "card", func(path string, omitted bool) {
		if omitted {
			rawUnspoken = append(rawUnspoken, path)
		}
	})
	require.NotEmpty(t, rawUnspoken, "the fixture must leave absences in the raw read model, or the probe proves nothing")

	in, err := restoreTechCardPayload(frozen.GetTechCard(), now, current)
	require.NoError(t, err)
	tc, err := dto.ConvertPbTechCardInsertToEntity(in)
	require.NoError(t, err)

	var unspoken []string
	walkPresenceFlags(reflect.ValueOf(tc).Elem(), "card", func(path string, omitted bool) {
		if omitted {
			unspoken = append(unspoken, path)
		}
	})
	require.Empty(t, unspoken, "fields the restore payload left absent — the store would keep today's value")

	require.True(t, tc.MeasurementUnitSet)
	require.True(t, tc.PieceDxfAliasesSet)
	require.True(t, tc.LabelsAware)
	for _, section := range []any{tc.Construction, tc.Packaging, tc.CareLabel, tc.Costing} {
		require.False(t, reflect.ValueOf(section).IsNil(), "a nil 1:1 section keeps the stored record")
	}
	require.NotNil(t, tc.Construction.EquipmentDefaults)
	require.Len(t, tc.Patterns, 1)
	p := tc.Patterns[0]
	require.True(t, p.Name.Valid, "pattern name: absent keeps the stored name")
	require.True(t, p.BomLineKey.Valid, "pattern bom_line_key: absent keeps the stored binding")
	require.True(t, p.FabricPurpose.Valid, "pattern fabric_purpose: absent keeps the stored binding")
	require.Nil(t, p.History)
	require.Len(t, tc.Callouts, 1)
	require.False(t, tc.Callouts[0].SpecOmitted)
	require.False(t, tc.Callouts[0].KindOmitted)
	require.False(t, tc.MoodNoteOmitted, "today's mood note must not survive a snapshot that had none")
	require.False(t, tc.BomItems[0].Purpose.Valid, "today's purpose must not survive a snapshot that had none")
}

// walkPresenceFlags visits every bool field named *Omitted in the entity tree.
func walkPresenceFlags(v reflect.Value, path string, visit func(string, bool)) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkPresenceFlags(v.Elem(), path, visit)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkPresenceFlags(v.Index(i), path+"["+strconv.Itoa(i)+"]", visit)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			fv := v.Field(i)
			if fv.Kind() == reflect.Bool && strings.HasSuffix(f.Name, "Omitted") {
				visit(path+"."+f.Name, fv.Bool())
				continue
			}
			walkPresenceFlags(fv, path+"."+f.Name, visit)
		}
	}
}

func TestRestoreLabelBomLinksReaddressByKey(t *testing.T) {
	readdress := restoreLabelBomLinks([]*pb_common.TechCardBomItem{{Id: 11, LineKey: "LBL"}, {Id: 12}})
	tc := &entity.TechCardInsert{
		GarmentLabels: []entity.TechCardGarmentLabel{
			{BomItemId: sql.NullInt32{Int32: 11, Valid: true}},
			{BomItemId: sql.NullInt32{Int32: 12, Valid: true}}, // no key in the snapshot: left as is
		},
		PackagingItems: []entity.TechCardPackagingItem{{BomItemId: sql.NullInt32{Int32: 11, Valid: true}}},
	}
	readdress(tc)
	require.Equal(t, "LBL", tc.GarmentLabels[0].BomLineKey)
	require.False(t, tc.GarmentLabels[0].BomItemId.Valid)
	require.Equal(t, int32(12), tc.GarmentLabels[1].BomItemId.Int32)
	require.Equal(t, "LBL", tc.PackagingItems[0].BomLineKey)
	require.False(t, tc.PackagingItems[0].BomItemId.Valid)
}

func TestSizesDroppedBy(t *testing.T) {
	require.Empty(t, sizesDroppedBy([]int{1, 2}, []int32{2, 1, 3}))
	require.Equal(t, []int{1, 4}, sizesDroppedBy([]int{4, 2, 1}, []int32{2}))
}
