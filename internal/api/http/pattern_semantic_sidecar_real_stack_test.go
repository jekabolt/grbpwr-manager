package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type patternSemanticSidecarV5StackFixture struct {
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
}

func loadPatternSemanticSidecarV5StackFixture(t *testing.T) patternSemanticSidecarV5StackFixture {
	t.Helper()
	var fixture patternSemanticSidecarV5StackFixture
	body, err := os.ReadFile("../../entity/testdata/pattern_product_composite_v5_ts_kat.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &fixture))
	fixture.Payload, err = base64.StdEncoding.Strict().DecodeString(fixture.OuterBase64)
	require.NoError(t, err)
	require.Len(t, fixture.Payload, fixture.OuterByteLength)
	require.Equal(t, fixture.OuterSHA256, entity.PatternSemanticSidecarPayloadSHA256(fixture.Payload))
	return fixture
}

// TestPatternSemanticSidecarThroughAuthenticatedStack covers the production HTTP listener,
// WithAuth, loopback gRPC, unary auth/RBAC interceptors, real handlers and JSON/base64 marshaling.
// The store is mocked here because the real MySQL serialization is independently exercised by
// TestPatternSemanticSidecarCASAndInvalidation in internal/store.
func TestPatternSemanticSidecarThroughAuthenticatedStack(t *testing.T) {
	fixture := loadPatternSemanticSidecarV5StackFixture(t)
	lineKey := fixture.Row.PatternLineKey
	dxfSHA := fixture.Row.SHA256
	receiptSHA := fixture.PostSaveReceiptSHA256
	payload := fixture.Payload
	sidecarSHA := fixture.OuterSHA256
	row := entity.PatternSemanticSidecar{
		PatternRowId: 91, TechCardId: fixture.Row.TechCardID, PatternLineKey: lineKey,
		CardLockVersion: fixture.Row.CardLockVersion, SavedRowRevision: fixture.Row.Revision,
		DxfSHA256: dxfSHA, PatternVersion: fixture.PatternVersion, SidecarSHA256: sidecarSHA,
		MediaType: entity.PatternProductCompositeSidecarV5MediaType, ByteLength: len(payload),
		Payload: payload, PostSaveReceiptSHA256: receiptSHA, CreatedAt: time.Unix(10, 0), CreatedBy: "ann",
	}

	techCards := mocks.NewMockTechCards(t)
	matchWrite := mock.MatchedBy(func(in entity.PatternSemanticSidecarWrite) bool {
		return in.TechCardId == fixture.Row.TechCardID && in.PatternLineKey == lineKey &&
			in.ExpectedCardLockVersion != nil && *in.ExpectedCardLockVersion == fixture.Row.CardLockVersion &&
			in.ExpectedSavedRowRevision != nil && *in.ExpectedSavedRowRevision == fixture.Row.Revision &&
			in.ExpectedPreviousSidecarSHA256 != nil && *in.ExpectedPreviousSidecarSHA256 == fixture.ExpectedPreviousSidecarSHA256 &&
			in.ExpectedServerAssemblyRevision != nil && *in.ExpectedServerAssemblyRevision == fixture.ServerAssemblyRevision &&
			in.ExpectedAssemblyTopologySHA256 != nil && *in.ExpectedAssemblyTopologySHA256 == fixture.AssemblyTopologySHA256 &&
			in.SelectedOperationNumber == fixture.SelectedOperationNumber &&
			in.ExpectedDxfSHA256 == dxfSHA && in.ExpectedPatternVersion == fixture.PatternVersion &&
			in.ExpectedPostSaveReceiptSHA256 == receiptSHA && in.SidecarSHA256 == sidecarSHA &&
			in.MediaType == entity.PatternProductCompositeSidecarV5MediaType && bytes.Equal(in.Payload, payload) && in.By == "ann"
	})
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, matchWrite).Return(row, nil).Once()
	techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, fixture.Row.TechCardID, lineKey).Return(row, nil).Once()
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, matchWrite).
		Return(entity.PatternSemanticSidecar{}, fmt.Errorf("late put: %w", entity.ErrTechCardPatternSidecarBindingMoved)).Once()
	techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, fixture.Row.TechCardID, lineKey).
		Return(entity.PatternSemanticSidecar{}, sql.ErrNoRows).Once()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().TechCards().Return(techCards).Times(4)

	adminServer, err := admin.New(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)
	admins := mocks.NewMockAdmin(t)
	admins.EXPECT().GetAdminByUsername(mock.Anything, "ann").Return(&entity.Admin{Username: "ann"}, nil).Maybe()
	authServer, err := auth.New(&auth.Config{
		JWTSecret: "sidecar-wire-secret-sidecar-wire-secret-sidecar", MasterPassword: "wire-master-password",
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "1h",
	}, admins)
	require.NoError(t, err)
	token := func(permission string) string {
		value, tokenErr := jwt.NewAdminToken(authServer.JwtAuth, time.Hour, "ann", false, []string{permission}, nil)
		require.NoError(t, tokenErr)
		return value
	}
	writeToken := token("tech_cards:write")
	readToken := token("tech_cards:read")
	wrongToken := token("tasks:write")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(context.Background())
	server := New(&Config{Address: "127.0.0.1", Port: fmt.Sprint(port)})
	require.NoError(t, server.Start(ctx, adminServer, &frontend.Server{}, authServer))
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(stop)
	})
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		response, requestErr := http.Get(baseURL + "/livez")
		if requestErr != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)

	body, err := json.Marshal(map[string]any{
		"techCardId": fixture.Row.TechCardID, "patternLineKey": lineKey,
		"expectedCardLockVersion":        fixture.Row.CardLockVersion,
		"expectedSavedRowRevision":       fixture.Row.Revision,
		"expectedPreviousSidecarSha256":  fixture.ExpectedPreviousSidecarSHA256,
		"expectedServerAssemblyRevision": fixture.ServerAssemblyRevision,
		"expectedAssemblyTopologySha256": fixture.AssemblyTopologySHA256,
		"selectedOperationNumber":        fixture.SelectedOperationNumber,
		"expectedDxfSha256":              dxfSHA, "expectedPatternVersion": fixture.PatternVersion,
		"expectedPostSaveReceiptSha256": receiptSHA,
		"mediaType":                     entity.PatternProductCompositeSidecarV5MediaType, "sidecarSha256": sidecarSHA,
		"sidecarBytes": base64.StdEncoding.EncodeToString(payload),
	})
	require.NoError(t, err)
	path := fmt.Sprintf("/api/admin/tech-card/%d/patterns/%s/semantic-sidecar", fixture.Row.TechCardID, lineKey)
	call := func(method, bearer string, requestBody []byte) (int, []byte) {
		request, requestErr := http.NewRequest(method, baseURL+path, bytes.NewReader(requestBody))
		require.NoError(t, requestErr)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.AuthMetadataKey, "Bearer "+bearer)
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		raw, requestErr := io.ReadAll(response.Body)
		require.NoError(t, requestErr)
		return response.StatusCode, raw
	}

	statusCode, raw := call(http.MethodPut, wrongToken, body)
	require.Equal(t, http.StatusForbidden, statusCode, "%.300s", raw)
	statusCode, raw = call(http.MethodGet, wrongToken, nil)
	require.Equal(t, http.StatusForbidden, statusCode, "%.300s", raw)

	statusCode, raw = call(http.MethodPut, writeToken, body)
	require.Equal(t, http.StatusOK, statusCode, "%.300s", raw)
	require.Contains(t, string(raw), fmt.Sprintf(`"cardLockVersion":%d`, fixture.Row.CardLockVersion))
	require.Contains(t, string(raw), `"mediaType":"`+entity.PatternProductCompositeSidecarV5MediaType+`"`)
	require.Contains(t, string(raw), `"sidecarBytes":"`+base64.StdEncoding.EncodeToString(payload)+`"`)
	statusCode, raw = call(http.MethodGet, readToken, nil)
	require.Equal(t, http.StatusOK, statusCode, "%.300s", raw)
	require.Contains(t, string(raw), `"sidecarSha256":"`+sidecarSHA+`"`)
	require.Contains(t, string(raw), `"mediaType":"`+entity.PatternProductCompositeSidecarV5MediaType+`"`)
	require.Contains(t, string(raw), `"sidecarBytes":"`+base64.StdEncoding.EncodeToString(payload)+`"`)

	statusCode, raw = call(http.MethodPut, writeToken, body)
	require.Equal(t, http.StatusBadRequest, statusCode, "%.300s", raw)
	require.Contains(t, string(raw), "pattern_binding_moved")
	statusCode, raw = call(http.MethodGet, readToken, nil)
	require.Equal(t, http.StatusNotFound, statusCode, "%.300s", raw)
}
