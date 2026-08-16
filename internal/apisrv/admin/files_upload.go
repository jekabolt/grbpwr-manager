package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// maxUploadMetaBytes bounds the JSON metadata part. limitBody caps the WHOLE
	// body, which says nothing about how the body is divided between parts, so
	// each part needs its own bound or a "metadata" field could be the whole 95 MiB.
	maxUploadMetaBytes = 64 << 10
	// maxUploadPreviewBytes bounds the preview image part, for the same reason. A
	// preview is a thumbnail of one page; the extra byte is how we detect that the
	// caller went over rather than landing exactly on the limit.
	maxUploadPreviewBytes = 2 << 20
	// maxContentTypeLen mirrors library_file.content_type.
	maxContentTypeLen = 128
	// cleanupTimeout bounds the best-effort bucket cleanup that runs after a failed
	// upload.
	cleanupTimeout = 10 * time.Second
)

// uploadMeta is the first multipart part: what the file should be called and
// which topics it lands in.
type uploadMeta struct {
	FileName  string   `json:"file_name"`
	TopicIds  []int32  `json:"topic_ids"`
	NewTopics []string `json:"new_topics"`
}

// uploadResponse is what the browser gets back. The file is marshalled by
// protojson (NOT hand-rolled snake_case) precisely because the client reuses the
// generated LibraryFile type for it and drops the result into the same query
// cache as the list response — a differently-cased body would deserialise into a
// object of undefined fields, silently, with no error anywhere.
type uploadResponse struct {
	File json.RawMessage `json:"file"`
	// Duplicates are files whose bytes are identical to what was just uploaded.
	// A hint, not a rejection: the upload succeeded either way.
	Duplicates []duplicateHint `json:"duplicates"`
}

type duplicateHint struct {
	Id       int32  `json:"id"`
	FileName string `json:"file_name"`
}

// FileUploadHandler streams one multipart upload into private object storage and
// records it. It is mounted at POST /api/files/upload, OUTSIDE the gRPC gateway,
// because the payload cannot fit inside a single gRPC message.
//
// The parts are read in order — meta, file, preview — and never buffered to disk:
// MultipartReader hands them over as a stream, so the file goes straight from the
// socket into the bucket, and memory stays bounded by the upload part size rather
// than the file size.
func (s *Server) FileUploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		started := time.Now()

		// Section check lives here, not in the middleware: the middleware
		// authenticates, the handler decides what its own section requires. Fails
		// closed — an absent authorization has no permissions at all.
		authz, ok := authsrv.GetAdminAuthz(ctx)
		if !ok || !(authz.FullAccess() || authz.Perms[rbac.SectionFiles].Covers(entity.AccessWrite)) {
			writeUploadError(w, http.StatusForbidden, "files:write is required to upload")
			return
		}
		username := authsrv.GetAdminUsername(ctx)

		mr, err := r.MultipartReader()
		if err != nil {
			writeUploadError(w, http.StatusBadRequest, "request must be multipart/form-data")
			return
		}

		meta, err := readUploadMeta(mr)
		if err != nil {
			writeUploadError(w, http.StatusBadRequest, err.Error())
			return
		}
		fileName, err := dto.ValidateLibraryFileName(meta.FileName)
		if err != nil {
			writeUploadError(w, http.StatusBadRequest, err.Error())
			return
		}
		topicIDs, newTopics, err := dto.ConvertPbTopicSelectionToEntity(meta.TopicIds, meta.NewTopics)
		if err != nil {
			writeUploadError(w, http.StatusBadRequest, err.Error())
			return
		}

		filePart, err := mr.NextPart()
		if err != nil || filePart.FormName() != "file" {
			writeUploadError(w, http.StatusBadRequest, `expected a "file" part after "meta"`)
			return
		}
		contentType := filePart.Header.Get("Content-Type")
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			contentType = mt
		}
		// content_type is VARCHAR(128); an over-long declared type would fail the
		// INSERT with a data-truncation error after the bytes are already stored.
		if len(contentType) > maxContentTypeLen {
			contentType = contentType[:maxContentTypeLen]
		}
		ext := strings.TrimPrefix(filepath.Ext(fileName), ".")

		objectKey, sha256hex, size, err := s.bucket.UploadLibraryObject(ctx, filePart, contentType, ext)
		if err != nil {
			// A truncated body is the one failure that must stay distinguishable: it
			// is what an infrastructure request-size ceiling looks like from in here,
			// and it is otherwise indistinguishable from someone closing the tab.
			if isTruncatedBody(err) {
				slog.Default().WarnContext(ctx, "library upload body truncated",
					slog.String("username", username),
					slog.String("file_name", fileName),
					slog.String("content_length", r.Header.Get("Content-Length")),
					slog.String("err", err.Error()))
				writeUploadError(w, http.StatusBadRequest,
					"the upload was cut short — the connection dropped or the file exceeded the size the server accepts")
				return
			}
			if errors.Is(err, http.ErrHandlerTimeout) {
				writeUploadError(w, http.StatusRequestTimeout, "the upload timed out")
				return
			}
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeUploadError(w, http.StatusRequestEntityTooLarge, "the file is too large")
				return
			}
			slog.Default().ErrorContext(ctx, "can't store library object",
				slog.String("username", username), slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "could not store the file")
			return
		}
		if size == 0 {
			cleanupObjects(ctx, s.bucket, objectKey)
			writeUploadError(w, http.StatusBadRequest, "the file is empty")
			return
		}

		// A preview is a convenience, not part of the file. Only a preview that is
		// genuinely INVALID is worth refusing over; a transient bucket failure must
		// not throw away a 90 MB upload that already succeeded and then blame the
		// person for it.
		previewKey, err := s.readAndStorePreview(r, mr)
		if err != nil {
			if errors.Is(err, bucket.ErrInvalidLibraryUpload) {
				cleanupObjects(ctx, s.bucket, objectKey)
				writeUploadError(w, http.StatusBadRequest, err.Error())
				return
			}
			slog.Default().ErrorContext(ctx, "library preview failed, storing without one",
				slog.String("username", username), slog.String("err", err.Error()))
			previewKey = ""
		}

		insert := &entity.LibraryFileInsert{
			ObjectKey:        objectKey,
			PreviewObjectKey: nullStringOrEmpty(previewKey),
			FileName:         fileName,
			ContentType:      contentType,
			SizeBytes:        size,
			Sha256:           sha256hex,
			UploadedBy:       username,
		}
		id, err := s.repo.Files().AddFile(ctx, insert, topicIDs, newTopics)
		if err != nil {
			// The bytes are already in the bucket and nothing points at them now.
			// Best-effort removal; if that fails too, the keys are logged so they can
			// be swept rather than lost.
			cleanupObjects(ctx, s.bucket, objectKey, previewKey)
			if s.repo.IsErrForeignKeyViolation(err) {
				writeUploadError(w, http.StatusBadRequest, "topic_id does not reference an existing topic")
				return
			}
			slog.Default().ErrorContext(ctx, "can't record library file",
				slog.String("username", username), slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "could not record the file")
			return
		}

		stored, err := s.repo.Files().GetFileById(ctx, id)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't read back library file", slog.String("err", err.Error()))
			writeUploadError(w, http.StatusInternalServerError, "the file was stored but could not be read back")
			return
		}
		pb := s.withLibraryURLs(ctx, stored, dto.ConvertEntityLibraryFileToPb(stored))

		slog.Default().InfoContext(ctx, "library file uploaded",
			slog.String("username", username),
			slog.Int("id", id),
			slog.String("file_name", fileName),
			slog.String("content_type", contentType),
			slog.Int64("size_bytes", size),
			slog.String("sha256", sha256hex),
			slog.Bool("has_preview", previewKey != ""),
			slog.Duration("took", time.Since(started)))

		writeUploadSuccess(w, pb, s.duplicatesFor(ctx, sha256hex, id))
	})
}

// readUploadMeta reads and validates the first part.
func readUploadMeta(mr *multipart.Reader) (*uploadMeta, error) {
	part, err := mr.NextPart()
	if err != nil {
		return nil, fmt.Errorf(`missing "meta" part`)
	}
	if part.FormName() != "meta" {
		return nil, fmt.Errorf(`the first part must be "meta", got %q`, part.FormName())
	}
	raw, err := io.ReadAll(io.LimitReader(part, maxUploadMetaBytes+1))
	if err != nil {
		return nil, fmt.Errorf(`could not read the "meta" part`)
	}
	if len(raw) > maxUploadMetaBytes {
		return nil, fmt.Errorf(`the "meta" part is too large`)
	}
	var m uploadMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf(`the "meta" part is not valid JSON`)
	}
	return &m, nil
}

// readAndStorePreview consumes the optional third part. An absent preview is
// normal — plenty of types have nothing sensible to show — so only a present
// but broken one is an error.
func (s *Server) readAndStorePreview(r *http.Request, mr *multipart.Reader) (string, error) {
	part, err := mr.NextPart()
	if err == io.EOF {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("could not read the preview part")
	}
	if part.FormName() != "preview" {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(part, maxUploadPreviewBytes+1))
	if err != nil {
		return "", fmt.Errorf("could not read the preview part")
	}
	if len(raw) == 0 {
		return "", nil
	}
	if len(raw) > maxUploadPreviewBytes {
		return "", fmt.Errorf("the preview image is too large")
	}
	key, err := s.bucket.UploadLibraryPreview(r.Context(), raw, "")
	if err != nil {
		return "", fmt.Errorf("the preview image must be a PNG or WebP")
	}
	return key, nil
}

// writeUploadSuccess marshals the response. protojson with EmitUnpopulated and
// camelCase names — byte-for-byte the shape the gateway produces for every other
// admin response.
func writeUploadSuccess(w http.ResponseWriter, pb *pb_admin.LibraryFile, duplicates []duplicateHint) {
	fileJSON, err := protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: false}.Marshal(pb)
	if err != nil {
		slog.Default().Error("can't marshal library file response", slog.String("err", err.Error()))
		writeUploadError(w, http.StatusInternalServerError, "could not encode the response")
		return
	}
	resp := uploadResponse{File: fileJSON, Duplicates: duplicates}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Default().Error("can't write upload response", slog.String("err", err.Error()))
	}
}

// duplicatesFor looks up files with identical bytes, excluding the one just
// stored. A failure here is not worth failing the upload over — the file is
// already in — so it degrades to "no duplicates known".
func (s *Server) duplicatesFor(ctx context.Context, sha string, selfID int) []duplicateHint {
	out := []duplicateHint{}
	if sha == "" {
		return out
	}
	dupes, err := s.repo.Files().FindFilesBySha256(ctx, sha)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't look up duplicates", slog.String("err", err.Error()))
		return out
	}
	for _, d := range dupes {
		if d.Id == selfID {
			continue
		}
		out = append(out, duplicateHint{Id: int32(d.Id), FileName: d.FileName})
	}
	return out
}

// writeUploadError emits a JSON error body with the given status.
func writeUploadError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// isTruncatedBody reports whether the error means the request body ended early.
func isTruncatedBody(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "unexpected EOF") ||
		strings.Contains(err.Error(), "connection reset")
}

func nullStringOrEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// cleanupObjects removes bucket objects after a failed upload.
//
// It deliberately detaches from the request context. The commonest way to reach
// here is the client going away mid-upload — and that is exactly when the request
// context is already cancelled, so a cleanup call riding on it would fail every
// single time and orphan the object it was written to remove.
func cleanupObjects(ctx context.Context, b dependency.FileStore, keys ...string) {
	if b == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := b.RemoveObjectsByKeys(cleanupCtx, keys...); err != nil {
		slog.Default().ErrorContext(cleanupCtx, "orphaned library objects",
			slog.Any("keys", keys), slog.String("err", err.Error()))
	}
}
