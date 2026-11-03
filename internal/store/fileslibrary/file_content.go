package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SaveFileContent is SaveNoteContent for a file that is not a note: the same transaction, the same
// FOR UPDATE under the viewer predicate, the same compare-and-set, and a conflict that is data. See
// SaveNoteContent for why it is shaped this way.
//
// Two differences. content_excerpt stays as it is: it is derived from a note's text, and the caller
// has no such text. preview_object_key is cleared, because a tile showing the old bytes would be a
// lie; the old preview key goes out with the old object key for removal after the commit.
func (s *Store) SaveFileContent(ctx context.Context, in entity.LibraryFileContentSave) (*entity.LibraryFileContentSaveResult, error) {
	if in.FileId <= 0 {
		return nil, fmt.Errorf("file id is required")
	}
	if strings.TrimSpace(in.ObjectKey) == "" || strings.TrimSpace(in.Sha256) == "" {
		return nil, fmt.Errorf("file object key and sha256 are required (upload the object before saving the row)")
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	type fileContentRow struct {
		ObjectKey        string         `db:"object_key"`
		PreviewObjectKey sql.NullString `db:"preview_object_key"`
		Sha256           string         `db:"sha256"`
		SizeBytes        int64          `db:"size_bytes"`
		ContentUpdatedBy string         `db:"content_updated_by"`
		ContentUpdatedAt sql.NullTime   `db:"content_updated_at"`
	}
	var res entity.LibraryFileContentSaveResult
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Tx retries the callback on transient failures, so the result is built anew each time.
		res = entity.LibraryFileContentSaveResult{}

		params := map[string]any{"id": in.FileId}
		cur, err := storeutil.QueryNamedOne[fileContentRow](ctx, rep.DB(),
			`SELECT lf.object_key, lf.preview_object_key, lf.sha256, lf.size_bytes,
				lf.content_updated_by, lf.content_updated_at
			FROM library_file lf
			WHERE lf.id = :id AND `+v.Where("lf", params)+` FOR UPDATE`, params)
		if err != nil {
			return err // sql.ErrNoRows passes through untouched
		}

		if !in.Force && cur.Sha256 != in.BaseSha256 {
			res.Conflict = true
			res.CurrentSha256 = cur.Sha256
			res.SizeBytes = cur.SizeBytes
			res.LastEditedBy = cur.ContentUpdatedBy
			res.LastEditedAt = cur.ContentUpdatedAt
			return nil
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE library_file SET
				object_key = :objectKey,
				sha256 = :sha256,
				size_bytes = :sizeBytes,
				content_updated_by = :editedBy,
				content_updated_at = NOW(),
				preview_object_key = NULL
			WHERE id = :id`,
			map[string]any{
				"id":        in.FileId,
				"objectKey": in.ObjectKey,
				"sha256":    in.Sha256,
				"sizeBytes": in.SizeBytes,
				"editedBy":  in.EditedBy,
			}); err != nil {
			return fmt.Errorf("failed to update library file content: %w", err)
		}

		// The stamp is read back: what goes out is exactly what lies in the row.
		type stampRow struct {
			ContentUpdatedAt sql.NullTime `db:"content_updated_at"`
		}
		st, err := storeutil.QueryNamedOne[stampRow](ctx, rep.DB(),
			`SELECT lf.content_updated_at FROM library_file lf WHERE lf.id = :id`,
			map[string]any{"id": in.FileId})
		if err != nil {
			return fmt.Errorf("failed to read back library file stamp: %w", err)
		}

		res.CurrentSha256 = in.Sha256
		res.SizeBytes = in.SizeBytes
		res.LastEditedBy = in.EditedBy
		res.LastEditedAt = st.ContentUpdatedAt
		if cur.ObjectKey != "" && cur.ObjectKey != in.ObjectKey {
			res.PreviousObjectKey = cur.ObjectKey
		}
		if cur.PreviewObjectKey.Valid && cur.PreviewObjectKey.String != "" {
			res.PreviousPreviewKey = cur.PreviewObjectKey.String
		}
		return nil
	})
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return &res, nil
}
