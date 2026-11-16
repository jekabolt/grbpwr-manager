package admin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
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

type patternSemanticSidecarV5HandlerFixture struct {
	Row struct {
		TechCardID      int    `json:"techCardId"`
		PatternLineKey  string `json:"patternLineKey"`
		Revision        uint32 `json:"revision"`
		CardLockVersion int    `json:"cardLockVersion"`
		SHA256          string `json:"sha256"`
	} `json:"row"`
	PatternVersion                int    `json:"patternVersion"`
	PostSaveReceiptSHA256         string `json:"postSaveReceiptSha256"`
	ExpectedPreviousSidecarSHA256 string `json:"expectedPreviousSidecarSha256"`
	ServerAssemblyRevision        string `json:"serverAssemblyRevision"`
	AssemblyTopologySHA256        string `json:"assemblyTopologySha256"`
	SelectedOperationNumber       int    `json:"selectedOperationNumber"`
	OuterBase64                   string `json:"outerBase64"`
	OuterSHA256                   string `json:"outerSha256"`
	OuterByteLength               int    `json:"outerByteLength"`
	Payload                       []byte `json:"-"`
	V4Payload                     []byte `json:"-"`
	V4SHA256                      string `json:"-"`
	V4PreviousSHA256              string `json:"-"`
}

func loadPatternSemanticSidecarV5HandlerFixture(t *testing.T) patternSemanticSidecarV5HandlerFixture {
	t.Helper()
	var fixture patternSemanticSidecarV5HandlerFixture
	body, err := os.ReadFile("../../entity/testdata/pattern_product_composite_v5_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &fixture))
	fixture.Payload, err = base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)
	require.Len(t, fixture.Payload, fixture.OuterByteLength)
	require.Equal(t, fixture.OuterSHA256, entity.PatternSemanticSidecarPayloadSHA256(fixture.Payload))
	var outer struct {
		Components []struct {
			Kind   string `json:"kind"`
			Base64 string `json:"base64"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(fixture.Payload, &outer))
	require.NotEmpty(t, outer.Components)
	require.Equal(t, "composite-v4", outer.Components[0].Kind)
	fixture.V4Payload, err = base64.StdEncoding.Strict().DecodeString(outer.Components[0].Base64)
	require.NoError(t, err)
	fixture.V4SHA256 = entity.PatternSemanticSidecarPayloadSHA256(fixture.V4Payload)
	var v4 struct {
		WriteCAS struct {
			ExpectedPreviousSidecarSHA256 string `json:"expectedPreviousSidecarSha256"`
		} `json:"writeCas"`
	}
	require.NoError(t, json.Unmarshal(fixture.V4Payload, &v4))
	fixture.V4PreviousSHA256 = v4.WriteCAS.ExpectedPreviousSidecarSHA256
	return fixture
}

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

func TestPutTechCardPatternSemanticSidecarMapsCompositeV4V5AssemblyCASAndEchoesBytes(t *testing.T) {
	fixture := loadPatternSemanticSidecarV5HandlerFixture(t)
	for _, test := range []struct {
		name, media, sha, previous string
		payload                    []byte
	}{
		{name: "Composite V4", media: entity.PatternProductCompositeSidecarV4MediaType, sha: fixture.V4SHA256, previous: fixture.V4PreviousSHA256, payload: fixture.V4Payload},
		{name: "Composite V5", media: entity.PatternProductCompositeSidecarV5MediaType, sha: fixture.OuterSHA256, previous: fixture.ExpectedPreviousSidecarSHA256, payload: fixture.Payload},
	} {
		t.Run(test.name, func(t *testing.T) {
			lockVersion := int32(fixture.Row.CardLockVersion)
			savedRowRevision := fixture.Row.Revision
			previousSHA := test.previous
			assemblyRevision := fixture.ServerAssemblyRevision
			topologySHA := fixture.AssemblyTopologySHA256
			req := &pb_admin.PutTechCardPatternSemanticSidecarRequest{
				TechCardId: int32(fixture.Row.TechCardID), PatternLineKey: fixture.Row.PatternLineKey,
				ExpectedCardLockVersion: &lockVersion, ExpectedSavedRowRevision: &savedRowRevision,
				ExpectedPreviousSidecarSha256:  &previousSHA,
				ExpectedServerAssemblyRevision: &assemblyRevision,
				ExpectedAssemblyTopologySha256: &topologySHA,
				SelectedOperationNumber:        int32(fixture.SelectedOperationNumber),
				ExpectedDxfSha256:              fixture.Row.SHA256, ExpectedPatternVersion: int32(fixture.PatternVersion),
				ExpectedPostSaveReceiptSha256: fixture.PostSaveReceiptSHA256,
				MediaType:                     test.media, SidecarSha256: test.sha, SidecarBytes: append([]byte(nil), test.payload...),
			}

			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, mock.MatchedBy(func(in entity.PatternSemanticSidecarWrite) bool {
				return in.TechCardId == fixture.Row.TechCardID && in.PatternLineKey == fixture.Row.PatternLineKey &&
					in.ExpectedCardLockVersion != nil && *in.ExpectedCardLockVersion == fixture.Row.CardLockVersion &&
					in.ExpectedSavedRowRevision != nil && *in.ExpectedSavedRowRevision == fixture.Row.Revision &&
					in.ExpectedPreviousSidecarSHA256 != nil && *in.ExpectedPreviousSidecarSHA256 == test.previous &&
					in.ExpectedServerAssemblyRevision != nil && *in.ExpectedServerAssemblyRevision == fixture.ServerAssemblyRevision &&
					in.ExpectedAssemblyTopologySHA256 != nil && *in.ExpectedAssemblyTopologySHA256 == fixture.AssemblyTopologySHA256 &&
					in.SelectedOperationNumber == fixture.SelectedOperationNumber && in.MediaType == test.media &&
					in.SidecarSHA256 == test.sha && string(in.Payload) == string(test.payload) && in.By == "ann"
			})).Return(entity.PatternSemanticSidecar{
				TechCardId: fixture.Row.TechCardID, PatternLineKey: fixture.Row.PatternLineKey,
				CardLockVersion: fixture.Row.CardLockVersion, SavedRowRevision: fixture.Row.Revision,
				DxfSHA256: fixture.Row.SHA256, PatternVersion: fixture.PatternVersion,
				MediaType: test.media, SidecarSHA256: test.sha, ByteLength: len(test.payload), Payload: test.payload,
				PostSaveReceiptSHA256: fixture.PostSaveReceiptSHA256, CreatedBy: "ann",
			}, nil)

			ctx := authsrv.PutAdminUsername(fullAccessCtx(), "ann")
			res, err := (&Server{repo: repo}).PutTechCardPatternSemanticSidecar(ctx, req)
			require.NoError(t, err)
			require.Equal(t, test.media, res.GetSemanticSidecar().GetMediaType())
			require.Equal(t, test.payload, res.GetSemanticSidecar().GetSidecarBytes())
			require.Equal(t, test.sha, res.GetSemanticSidecar().GetSidecarSha256())
			require.EqualValues(t, fixture.Row.Revision, res.GetSemanticSidecar().GetSavedRowRevision())
		})
	}
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
