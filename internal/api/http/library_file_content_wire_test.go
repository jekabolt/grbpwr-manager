package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The real stack again, as in the task file test: a 10 MB file reaches the panel over the gateway's
// default 4 MiB receive cap, and RBAC refuses the route to a caller without files:read before the
// handler runs.
func TestLibraryFileContentThroughTheRealStack(t *testing.T) {
	const size = 10 << 20
	content := bytes.Repeat([]byte("0123456789abcde\n"), size/16)
	require.Len(t, content, size)

	row := &entity.LibraryFile{Id: 7, LibraryFileInsert: entity.LibraryFileInsert{ObjectKey: "files-library/plan.csv", SizeBytes: size}}
	lib := mocks.NewMockFiles(t)
	lib.EXPECT().GetFileById(mock.Anything, 7).Return(row, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(lib)
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, row.ObjectKey, int64(size)).Return(content, nil)

	adminServer, err := admin.New(repo, fs, nil, nil, nil, nil, nil, nil, nil, nil, entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)
	admins := mocks.NewMockAdmin(t)
	admins.EXPECT().GetAdminByUsername(mock.Anything, "pasha").Return(&entity.Admin{Username: "pasha"}, nil).Maybe()
	authServer, err := auth.New(&auth.Config{
		JWTSecret: "wire-test-secret-wire-test-secret-wire-test-secret", MasterPassword: "wire-test-master-password",
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "1h",
	}, admins)
	require.NoError(t, err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(context.Background())
	s := New(&Config{Address: "127.0.0.1", Port: fmt.Sprint(port)})
	require.NoError(t, s.Start(ctx, adminServer, &frontend.Server{}, authServer))
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.Shutdown(stop)
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		resp, err := http.Get(base + "/livez")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)

	get := func(t *testing.T, perm string) (int, []byte) {
		t.Helper()
		token, err := jwt.NewAdminToken(authServer.JwtAuth, time.Hour, "pasha", false, []string{perm}, nil)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodGet, base+"/api/admin/files/7/content", nil)
		require.NoError(t, err)
		req.Header.Set(auth.AuthMetadataKey, "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, raw
	}

	t.Run("without files:read", func(t *testing.T) {
		code, raw := get(t, "tasks:write")
		require.Equal(t, http.StatusForbidden, code, "%.300s", raw)
		lib.AssertNotCalled(t, "GetFileById", mock.Anything, mock.Anything)
	})

	t.Run("with files:read", func(t *testing.T) {
		code, raw := get(t, "files:read")
		require.Equal(t, http.StatusOK, code, "%.300s", raw)
		var out struct {
			Content string `json:"content"`
		}
		require.NoError(t, json.Unmarshal(raw, &out))
		got, err := base64.StdEncoding.DecodeString(out.Content)
		require.NoError(t, err)
		require.True(t, bytes.Equal(content, got), "the client got other bytes (%d)", len(got))
	})
}
