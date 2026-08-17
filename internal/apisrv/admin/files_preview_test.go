package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// servePreview drives the handler through a chi mux mounted at the same path as
// internal/api/http, so chi.URLParam sees the id exactly as it will in
// production — a hand-built route context would prove nothing about the mount.
func servePreview(t *testing.T, s *Server, ctx context.Context, path string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Method(http.MethodPost, "/files/{id}/preview", s.FilePreviewHandler())

	req := httptest.NewRequest(http.MethodPost, path, body).WithContext(ctx)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// previewBody builds a one-part multipart body.
func previewBody(t *testing.T, partName string, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(partName, "preview.png")
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func writerCtx() context.Context {
	return authsrv.PutAdminUsername(
		authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
			Perms: map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessWrite},
		}), "max")
}

// TestFilePreviewHandlerRequiresWrite: the section check lives in the handler,
// because the gRPC interceptor that guards every other admin write never sees a
// plain HTTP route. Read access — and no authorization at all — must be refused.
func TestFilePreviewHandlerRequiresWrite(t *testing.T) {
	s := &Server{}

	body, ct := previewBody(t, "preview", []byte("whatever"))
	if got := servePreview(t, s, context.Background(), "/files/7/preview", body, ct).Code; got != http.StatusForbidden {
		t.Errorf("no authorization: want 403, got %d", got)
	}

	readOnly := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessRead},
	})
	body, ct = previewBody(t, "preview", []byte("whatever"))
	if got := servePreview(t, s, readOnly, "/files/7/preview", body, ct).Code; got != http.StatusForbidden {
		t.Errorf("files:read only: want 403, got %d", got)
	}
}

// TestFilePreviewHandlerRejectsBadRequests covers the shapes that must never
// reach the bucket: a non-numeric id, a body that is not multipart, and a part
// under the wrong name.
func TestFilePreviewHandlerRejectsBadRequests(t *testing.T) {
	s := &Server{}
	ctx := writerCtx()

	body, ct := previewBody(t, "preview", []byte("whatever"))
	if got := servePreview(t, s, ctx, "/files/abc/preview", body, ct).Code; got != http.StatusBadRequest {
		t.Errorf("non-numeric id: want 400, got %d", got)
	}

	if got := servePreview(t, s, ctx, "/files/7/preview", bytes.NewBufferString("{}"), "application/json").Code; got != http.StatusBadRequest {
		t.Errorf("not multipart: want 400, got %d", got)
	}

	body, ct = previewBody(t, "file", []byte("whatever"))
	if got := servePreview(t, s, ctx, "/files/7/preview", body, ct).Code; got != http.StatusBadRequest {
		t.Errorf("wrong part name: want 400, got %d", got)
	}
}

// TestFilePreviewHandlerRefusesInvalidImage: unlike on upload — where a broken
// preview must not throw away a 90 MB file that already landed — here the image
// IS the request, so an invalid one is refused rather than degraded.
func TestFilePreviewHandlerRefusesInvalidImage(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryPreview(mock.Anything, mock.Anything, "").
		Return("", fmt.Errorf("%w: preview is not a PNG or WebP", bucket.ErrInvalidLibraryUpload))

	s := &Server{bucket: fs}
	body, ct := previewBody(t, "preview", []byte("not an image at all"))
	w := servePreview(t, s, writerCtx(), "/files/7/preview", body, ct)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid image: want 400, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestFilePreviewHandlerReplacesAndCleansUp is the whole point of the endpoint:
// the row is updated FIRST and only then the superseded object is dropped, in
// that order — the reverse would leave a file pointing at bytes that are gone.
func TestFilePreviewHandlerReplacesAndCleansUp(t *testing.T) {
	const newKey = "files-library/previews/new.png"
	const oldKey = "files-library/previews/old.png"

	files := mocks.NewMockFiles(t)
	files.EXPECT().SetFilePreview(mock.Anything, 7, newKey).Return(oldKey, nil)
	files.EXPECT().GetFileById(mock.Anything, 7).Return(&entity.LibraryFile{
		Id: 7,
		LibraryFileInsert: entity.LibraryFileInsert{
			FileName:         "mockup.pdf",
			ContentType:      "application/pdf",
			PreviewObjectKey: sql.NullString{String: newKey, Valid: true},
		},
	}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryPreview(mock.Anything, mock.Anything, "").Return(newKey, nil)
	// Only the superseded object is removed, and never the one just stored.
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, oldKey).Return(nil)
	// The file is a pdf (inline-safe), so both urls get minted; the preview adds a
	// third presign. Signing failures are swallowed by design, so what comes back
	// does not matter to this assertion.
	fs.EXPECT().PresignLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return("https://signed.example/x", time.Now().Add(6*time.Hour), nil).Maybe()

	s := &Server{repo: repo, bucket: fs}
	body, ct := previewBody(t, "preview", []byte("PNG-ish bytes"))
	w := servePreview(t, s, writerCtx(), "/files/7/preview", body, ct)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		File struct {
			Id       int32  `json:"id"`
			FileName string `json:"fileName"`
		} `json:"file"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, int32(7), resp.File.Id)
	require.Equal(t, "mockup.pdf", resp.File.FileName)
}

// TestFilePreviewHandlerUnknownFileCleansUpItsOwnBytes: when the row cannot be
// updated, the object just stored belongs to nobody and has to go, or every 404
// leaks a thumbnail into the bucket.
func TestFilePreviewHandlerUnknownFileCleansUpItsOwnBytes(t *testing.T) {
	const newKey = "files-library/previews/orphan.png"

	files := mocks.NewMockFiles(t)
	files.EXPECT().SetFilePreview(mock.Anything, 7, newKey).Return("", sql.ErrNoRows)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryPreview(mock.Anything, mock.Anything, "").Return(newKey, nil)
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, newKey).Return(nil)

	s := &Server{repo: repo, bucket: fs}
	body, ct := previewBody(t, "preview", []byte("PNG-ish bytes"))
	w := servePreview(t, s, writerCtx(), "/files/7/preview", body, ct)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d (%s)", w.Code, w.Body.String())
	}
}
