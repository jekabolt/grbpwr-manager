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
	"strings"
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

// TestPatternSemanticSidecarThroughAuthenticatedStack covers the production HTTP listener,
// WithAuth, loopback gRPC, unary auth/RBAC interceptors, real handlers and JSON/base64 marshaling.
// The store is mocked here because the real MySQL serialization is independently exercised by
// TestPatternSemanticSidecarCASAndInvalidation in internal/store.
func TestPatternSemanticSidecarThroughAuthenticatedStack(t *testing.T) {
	const lineKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	dxfSHA := strings.Repeat("a", 64)
	receiptSHA := strings.Repeat("b", 64)
	payload := []byte(fmt.Sprintf(
		`{"schemaVersion":"pattern-import-dxf-semantic-sidecar-document/1","mediaType":"%s","encoding":"canonical-json-utf8","dxf":{"exportSha256":"%s"},"persistence":{"receiptSealSha256":"%s","card":{"techCardId":44,"observedLockVersion":8},"pattern":{"lineKey":"%s","version":2,"sha256":"%s"}}}`,
		entity.PatternSemanticSidecarMediaType, dxfSHA, receiptSHA, lineKey, dxfSHA,
	))
	sidecarSHA := entity.PatternSemanticSidecarPayloadSHA256(payload)
	row := entity.PatternSemanticSidecar{
		PatternRowId: 91, TechCardId: 44, PatternLineKey: lineKey, CardLockVersion: 8,
		DxfSHA256: dxfSHA, PatternVersion: 2, SidecarSHA256: sidecarSHA,
		MediaType: entity.PatternSemanticSidecarMediaType, ByteLength: len(payload),
		Payload: payload, PostSaveReceiptSHA256: receiptSHA, CreatedAt: time.Unix(10, 0), CreatedBy: "ann",
	}

	techCards := mocks.NewMockTechCards(t)
	matchWrite := mock.MatchedBy(func(in entity.PatternSemanticSidecarWrite) bool {
		return in.TechCardId == 44 && in.PatternLineKey == lineKey &&
			in.ExpectedCardLockVersion != nil && *in.ExpectedCardLockVersion == 8 &&
			in.ExpectedDxfSHA256 == dxfSHA && in.ExpectedPatternVersion == 2 &&
			in.ExpectedPostSaveReceiptSHA256 == receiptSHA && in.SidecarSHA256 == sidecarSHA &&
			bytes.Equal(in.Payload, payload) && in.By == "ann"
	})
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, matchWrite).Return(row, nil).Once()
	techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, 44, lineKey).Return(row, nil).Once()
	techCards.EXPECT().PutTechCardPatternSemanticSidecar(mock.Anything, matchWrite).
		Return(entity.PatternSemanticSidecar{}, fmt.Errorf("late put: %w", entity.ErrTechCardPatternSidecarBindingMoved)).Once()
	techCards.EXPECT().GetTechCardPatternSemanticSidecar(mock.Anything, 44, lineKey).
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
		"techCardId": 44, "patternLineKey": lineKey, "expectedCardLockVersion": 8,
		"expectedDxfSha256": dxfSHA, "expectedPatternVersion": 2,
		"expectedPostSaveReceiptSha256": receiptSHA,
		"mediaType":                     entity.PatternSemanticSidecarMediaType, "sidecarSha256": sidecarSHA,
		"sidecarBytes": base64.StdEncoding.EncodeToString(payload),
	})
	require.NoError(t, err)
	path := "/api/admin/tech-card/44/patterns/" + lineKey + "/semantic-sidecar"
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
	require.Contains(t, string(raw), `"cardLockVersion":8`)
	require.Contains(t, string(raw), `"sidecarBytes":"`+base64.StdEncoding.EncodeToString(payload)+`"`)
	statusCode, raw = call(http.MethodGet, readToken, nil)
	require.Equal(t, http.StatusOK, statusCode, "%.300s", raw)
	require.Contains(t, string(raw), `"sidecarSha256":"`+sidecarSHA+`"`)

	statusCode, raw = call(http.MethodPut, writeToken, body)
	require.Equal(t, http.StatusBadRequest, statusCode, "%.300s", raw)
	require.Contains(t, string(raw), "pattern_binding_moved")
	statusCode, raw = call(http.MethodGet, readToken, nil)
	require.Equal(t, http.StatusNotFound, statusCode, "%.300s", raw)
}
