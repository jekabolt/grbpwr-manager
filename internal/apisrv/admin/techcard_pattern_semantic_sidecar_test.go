package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const patternSemanticSidecarHandlerKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func patternSemanticSidecarHandlerRequest() *pb_admin.PutTechCardPatternSemanticSidecarRequest {
	dxfSHA := strings.Repeat("a", 64)
	receiptSHA := strings.Repeat("b", 64)
	payload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","dxf":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":7,"observedLockVersion":8},"pattern":{"lineKey":"%s","version":3,"sha256":"%s"}}}`,
		entity.PatternSemanticSidecarMediaType, dxfSHA, receiptSHA, patternSemanticSidecarHandlerKey, dxfSHA,
	))
	request := &pb_admin.PutTechCardPatternSemanticSidecarRequest{
		TechCardId:                    7,
		PatternLineKey:                strings.ToLower(patternSemanticSidecarHandlerKey),
		ExpectedCardLockVersion:       new(int32),
		ExpectedDxfSha256:             dxfSHA,
		ExpectedPatternVersion:        3,
		ExpectedPostSaveReceiptSha256: receiptSHA,
		MediaType:                     entity.PatternSemanticSidecarMediaType,
		SidecarSha256:                 entity.PatternSemanticSidecarPayloadSHA256(payload),
		SidecarBytes:                  payload,
	}
	*request.ExpectedCardLockVersion = 8
	return request
}

func patternSemanticSidecarHandlerRow(payload []byte) entity.PatternSemanticSidecar {
	return entity.PatternSemanticSidecar{
		PatternRowId:          9,
		TechCardId:            7,
		PatternLineKey:        patternSemanticSidecarHandlerKey,
		CardLockVersion:       8,
		DxfSHA256:             strings.Repeat("a", 64),
		PatternVersion:        3,
		SavedRowRevision:      4,
		SidecarSHA256:         entity.PatternSemanticSidecarPayloadSHA256(payload),
		MediaType:             entity.PatternSemanticSidecarMediaType,
		ByteLength:            len(payload),
		Payload:               append([]byte(nil), payload...),
		PostSaveReceiptSHA256: strings.Repeat("b", 64),
		CreatedAt:             time.Unix(10, 0),
		CreatedBy:             "ann",
	}
}

func TestPutTechCardPatternSemanticSidecarPassesOwnedBytesAndEchoesStoredRow(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	req := patternSemanticSidecarHandlerRequest()
	wantPayload := append([]byte(nil), req.GetSidecarBytes()...)
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, mock.MatchedBy(func(in entity.PatternSemanticSidecarWrite) bool {
		return in.TechCardId == 7 && in.PatternLineKey == patternSemanticSidecarHandlerKey &&
			in.ExpectedCardLockVersion != nil && *in.ExpectedCardLockVersion == 8 &&
			in.ExpectedDxfSHA256 == strings.Repeat("a", 64) && in.ExpectedPatternVersion == 3 &&
			in.ExpectedPostSaveReceiptSHA256 == strings.Repeat("b", 64) && in.By == "ann" &&
			string(in.Payload) == string(wantPayload)
	})).Return(patternSemanticSidecarHandlerRow(wantPayload), nil)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "ann")
	res, err := (&Server{repo: repo}).PutTechCardPatternSemanticSidecar(ctx, req)
	require.NoError(t, err)
	require.Equal(t, patternSemanticSidecarHandlerKey, res.GetSemanticSidecar().GetPatternLineKey())
	require.EqualValues(t, 8, res.GetSemanticSidecar().GetCardLockVersion())
	require.EqualValues(t, 4, res.GetSemanticSidecar().GetSavedRowRevision())
	require.Equal(t, wantPayload, res.GetSemanticSidecar().GetSidecarBytes())
	require.EqualValues(t, len(wantPayload), res.GetSemanticSidecar().GetByteLength())
}

func TestPutTechCardPatternSemanticSidecarRefusesBeforeStoreAndMapsMovedBinding(t *testing.T) {
	strict := mocks.NewMockRepository(t)
	missingLock := patternSemanticSidecarHandlerRequest()
	missingLock.ExpectedCardLockVersion = nil
	_, err := (&Server{repo: strict}).PutTechCardPatternSemanticSidecar(fullAccessCtx(), missingLock)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	bad := patternSemanticSidecarHandlerRequest()
	bad.SidecarBytes[0] ^= 1
	_, err = (&Server{repo: strict}).PutTechCardPatternSemanticSidecar(fullAccessCtx(), bad)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, mock.Anything).
		Return(entity.PatternSemanticSidecar{}, fmt.Errorf("cas: %w", entity.ErrTechCardPatternSidecarBindingMoved))
	_, err = (&Server{repo: repo}).PutTechCardPatternSemanticSidecar(
		authsrv.PutAdminUsername(fullAccessCtx(), "ann"), patternSemanticSidecarHandlerRequest())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "pattern_binding_moved")
}

func TestGetTechCardPatternSemanticSidecarMapsMissingAndReturnsVerifiedBytes(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		techCards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(techCards)
		payload := patternSemanticSidecarHandlerRequest().GetSidecarBytes()
		techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, 7, patternSemanticSidecarHandlerKey).
			Return(patternSemanticSidecarHandlerRow(payload), nil)
		res, err := (&Server{repo: repo}).GetTechCardPatternSemanticSidecar(context.Background(),
			&pb_admin.GetTechCardPatternSemanticSidecarRequest{TechCardId: 7, PatternLineKey: strings.ToLower(patternSemanticSidecarHandlerKey)})
		require.NoError(t, err)
		require.Equal(t, payload, res.GetSemanticSidecar().GetSidecarBytes())
	})

	t.Run("missing", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		techCards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(techCards)
		techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, 7, patternSemanticSidecarHandlerKey).
			Return(entity.PatternSemanticSidecar{}, sql.ErrNoRows)
		_, err := (&Server{repo: repo}).GetTechCardPatternSemanticSidecar(context.Background(),
			&pb_admin.GetTechCardPatternSemanticSidecarRequest{TechCardId: 7, PatternLineKey: patternSemanticSidecarHandlerKey})
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

func TestGetTechCardSavedAssemblySnapshotPassesExactRowAndEchoesAuthority(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	sha := strings.Repeat("a", 64)
	req := &pb_admin.GetTechCardSavedAssemblySnapshotRequest{
		TechCardId: 7, PatternLineKey: patternSemanticSidecarHandlerKey,
		ExpectedScopeKey: "fabric:outer", ExpectedSavedRowRevision: 4,
		ExpectedCardLockVersion: 8, ExpectedUrl: "https://cdn.test/patterns/front.dxf",
		ExpectedSizeBytes: 1234, ExpectedSha256: sha, SelectedOperationNumber: 10,
	}
	wantSidecar := patternSemanticSidecarHandlerRow(patternSemanticSidecarHandlerRequest().GetSidecarBytes())
	techCards.EXPECT().GetTechCardSavedAssemblySnapshot(mock.Anything, mock.MatchedBy(func(in entity.SavedAssemblySnapshotRequest) bool {
		row := in.ExpectedRow
		return row.TechCardID == 7 && row.ScopeKey == "fabric:outer" &&
			row.PatternLineKey == patternSemanticSidecarHandlerKey && row.SavedRowRevision == 4 &&
			row.CardLockVersion == 8 && row.URL == "https://cdn.test/patterns/front.dxf" &&
			row.SizeBytes == 1234 && row.SHA256 == sha && in.SelectedOperationNumber == 10
	})).Return(entity.SavedAssemblySnapshot{
		Row: entity.SavedPatternRowIdentity{
			TechCardID: 7, ScopeKey: "fabric:outer", PatternLineKey: patternSemanticSidecarHandlerKey,
			SavedRowRevision: 4, CardLockVersion: 8, URL: "https://cdn.test/patterns/front.dxf",
			SizeBytes: 1234, SHA256: sha, PatternVersion: 3, Filename: "front.dxf",
		},
		SelectedOperationNumber: 10,
		Card: &entity.TechCard{TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			Work:            sql.NullString{String: "join_lockstitch", Valid: true},
		}}}},
		ServerAssemblyRevision: "asm:v1:0123456789abcdef0123456789abcdef:9",
		TopologySHA256:         strings.Repeat("c", 64),
		TopologyCanonicalJSON:  []byte(`{"operations":[]}`),
		SemanticSidecar:        &wantSidecar,
	}, nil)

	res, err := (&Server{repo: repo}).GetTechCardSavedAssemblySnapshot(context.Background(), req)
	require.NoError(t, err)
	require.EqualValues(t, 4, res.GetRow().GetSavedRowRevision())
	require.Equal(t, "https://cdn.test/patterns/front.dxf", res.GetRow().GetUrl())
	require.Equal(t, "", res.GetRow().GetDownloadUrl(), "nil URL service is deliberately best effort")
	require.Len(t, res.GetOperations(), 1)
	require.EqualValues(t, 10, res.GetOperations()[0].GetOperationNumber())
	require.Equal(t, wantSidecar.Payload, res.GetSemanticSidecar().GetSidecarBytes())
	require.Equal(t, []byte(`{"operations":[]}`), res.GetTopologyCanonicalJson())
}

func TestGetTechCardSavedAssemblySnapshotMapsCorruptCarrierToDataLoss(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().GetTechCardSavedAssemblySnapshot(mock.Anything, mock.Anything).
		Return(entity.SavedAssemblySnapshot{}, fmt.Errorf("read: %w", entity.ErrTechCardPatternSidecarCorrupt))
	_, err := (&Server{repo: repo}).GetTechCardSavedAssemblySnapshot(context.Background(),
		&pb_admin.GetTechCardSavedAssemblySnapshotRequest{TechCardId: 7})
	require.Equal(t, codes.DataLoss, status.Code(err))
}
