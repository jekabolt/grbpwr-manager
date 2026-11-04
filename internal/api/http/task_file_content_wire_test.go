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

// A 10 MB SHEET THROUGH THE REAL STACK, BOTH WAYS.
//
// Server.Start on a real port: the HTTP listener, the /api route with its body cap, WithAuth, the
// gateway, the loopback gRPC hop with its message limits, the interceptors and RBAC, the real
// handlers. Only the store and the bucket are mocks. A limit anywhere on that path turns this red, and
// every limit is either the production constant or the production default, because nothing here sets
// one. Outside this repo the platform in front of the app remains unproven.
func TestTaskFileContentTenMegabytesThroughTheRealStack(t *testing.T) {
	const size = 10 << 20
	content := bytes.Repeat([]byte("0123456789abcde\n"), size/16)
	require.Len(t, content, size)

	row := entity.LibraryFile{Id: 7, LibraryFileInsert: entity.LibraryFileInsert{
		ObjectKey: "files-library/plan.csv", FileName: "plan.csv", ContentType: "text/csv", SizeBytes: size, Sha256: "base-sha",
	}}
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().GetTaskById(mock.Anything, 3).Return(&entity.Task{Id: 3, TaskInsert: entity.TaskInsert{FileIds: []int{7}}}, nil)
	lib := mocks.NewMockFiles(t)
	lib.EXPECT().ListFilesByIds(mock.Anything, []int{7}).Return([]entity.LibraryFile{row}, nil)
	lib.EXPECT().SaveFileContent(mock.Anything, mock.MatchedBy(func(in entity.LibraryFileContentSave) bool {
		return in.SizeBytes == size && in.BaseSha256 == "base-sha" && in.EditedBy == "pasha"
	})).Return(&entity.LibraryFileContentSaveResult{CurrentSha256: "new-sha", SizeBytes: size, LastEditedBy: "pasha", PreviousObjectKey: row.ObjectKey}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	repo.EXPECT().Files().Return(lib)

	var uploaded []byte
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, "text/csv", "csv").RunAndReturn(
		func(_ context.Context, r io.Reader, _, _ string) (string, string, int64, error) {
			var err error
			uploaded, err = io.ReadAll(r)
			return "files-library/new.csv", "new-sha", int64(len(uploaded)), err
		})
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, row.ObjectKey).Return(nil)
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
	token, err := jwt.NewAdminToken(authServer.JwtAuth, time.Hour, "pasha", false, []string{"tasks:write"}, nil)
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

	call := func(t *testing.T, method, path string, body []byte) []byte {
		t.Helper()
		req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.AuthMetadataKey, "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%.300s", raw)
		return raw
	}

	t.Run("save", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"taskId": 3, "fileId": 7, "baseSha256": "base-sha", "content": base64.StdEncoding.EncodeToString(content),
		})
		require.NoError(t, err)
		t.Logf("json body: %d bytes", len(body))

		var out struct {
			CurrentSha256 string `json:"currentSha256"`
			SizeBytes     string `json:"sizeBytes"`
		}
		require.NoError(t, json.Unmarshal(call(t, http.MethodPost, "/api/admin/task/file/content/save", body), &out))
		require.Equal(t, "new-sha", out.CurrentSha256)
		require.Equal(t, fmt.Sprint(size), out.SizeBytes)
		require.True(t, bytes.Equal(content, uploaded), "the bucket got other bytes (%d)", len(uploaded))
	})

	t.Run("read", func(t *testing.T) {
		var out struct {
			Content string `json:"content"`
			Sha256  string `json:"sha256"`
		}
		require.NoError(t, json.Unmarshal(call(t, http.MethodGet, "/api/admin/task/3/file/7/content", nil), &out))
		got, err := base64.StdEncoding.DecodeString(out.Content)
		require.NoError(t, err)
		require.True(t, bytes.Equal(content, got), "the client got other bytes (%d)", len(got))
		require.Equal(t, "base-sha", out.Sha256)
	})
}
