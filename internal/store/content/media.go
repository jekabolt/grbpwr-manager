package content

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// normalizedContentHash reduces a content hash to its one canonical spelling — lowercase
// hex, no surrounding blanks — or to SQL NULL when there is nothing to store.
//
// Both ends go through this, so a hash written by an upload and a hash looked up from an
// archive manifest can only ever differ by their bytes, not by their casing. Doing it on
// exactly one side would be worse than doing it on neither: the column's collation is
// case-insensitive, so a mismatch introduced here would hide until the day the column moves
// to a binary collation and every de-duplication silently stops matching.
func normalizedContentHash(h sql.NullString) any {
	if !h.Valid {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(h.String))
	if v == "" {
		return nil
	}
	return v
}

// addMediaQuery and findMediaByContentHashQuery are package-level so a test can bind them
// without a database. sqlx's named-parameter scanner walks the raw text and does not skip
// comments or string literals, so a stray ':' turns into an empty bind name and the query
// dies at runtime, not at compile time — the same trap already guarded for mediaUsageQuery.
const addMediaQuery = `INSERT INTO media (
		full_size, full_size_width, full_size_height,
		compressed, compressed_width, compressed_height,
		thumbnail, thumbnail_width, thumbnail_height, blur_hash,
		content_hash
	) VALUES (
		:fullSize, :fullSizeWidth, :fullSizeHeight,
		:compressed, :compressedWidth, :compressedHeight,
		:thumbnail, :thumbnailWidth, :thumbnailHeight, :blurHash,
		:contentHash
	);`

const findMediaByContentHashQuery = `SELECT * FROM media WHERE content_hash = :hash LIMIT 1`

// AddMedia adds a new media item to the database.
func (s *Store) AddMedia(ctx context.Context, media *entity.MediaItem) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, addMediaQuery, map[string]any{
		"fullSize":         media.FullSizeMediaURL,
		"fullSizeWidth":    media.FullSizeWidth,
		"fullSizeHeight":   media.FullSizeHeight,
		"compressed":       media.CompressedMediaURL,
		"compressedWidth":  media.CompressedWidth,
		"compressedHeight": media.CompressedHeight,
		"thumbnail":        media.ThumbnailMediaURL,
		"thumbnailWidth":   media.ThumbnailWidth,
		"thumbnailHeight":  media.ThumbnailHeight,
		"blurHash":         media.BlurHash,
		// sql.NullString carries the difference that matters: an invalid one binds SQL NULL
		// ("not computed"), never the empty string. An empty-string hash would be a real
		// value that every other empty-string hash matches, and de-duplication would then
		// happily fuse unrelated files.
		"contentHash": normalizedContentHash(media.ContentHash),
	})
	if err != nil {
		return id, fmt.Errorf("failed to add media: %w", err)
	}
	return id, nil
}

// GetMediaById retrieves a media item by its ID.
func (s *Store) GetMediaById(ctx context.Context, id int) (*entity.MediaFull, error) {
	query := `SELECT * FROM media WHERE id = :id`
	media, err := storeutil.QueryNamedOne[entity.MediaFull](ctx, s.DB, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}
	return &media, nil
}

// GetMediaByIds retrieves multiple media items in a single query, returned as a
// map keyed by id. Ids with no matching row are simply absent from the map.
// Used to batch-load hero media instead of issuing one SELECT per reference.
func (s *Store) GetMediaByIds(ctx context.Context, ids []int) (map[int]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB,
		`SELECT * FROM media WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to get media by ids: %w", err)
	}
	out := make(map[int]entity.MediaFull, len(rows))
	for _, m := range rows {
		out[m.Id] = m
	}
	return out, nil
}

// FindMediaByContentHash answers the only question de-duplication asks: "is a file with
// exactly these bytes already in media?" It returns (nil, nil) when the answer is no —
// absence is the ordinary outcome on a fresh import, not a failure, and making the caller
// unwrap sql.ErrNoRows on the common path is how not-found ends up mistaken for broken.
//
// An empty hash short-circuits without touching the database. It has to: rows written
// before migration 0336 carry NULL, and an empty-string comparison would neither match them nor
// mean anything — but it would still cost a query per archive file.
//
// LIMIT 1 with no ORDER BY is deliberate. The column is intentionally not unique (the same
// file legitimately sits in media several times), and every row that matches describes the
// same bytes, so any one of them is as good an answer as another; asking for an order would
// buy nothing and cost a sort.
func (s *Store) FindMediaByContentHash(ctx context.Context, hash string) (*entity.MediaFull, error) {
	h := strings.ToLower(strings.TrimSpace(hash))
	if h == "" {
		return nil, nil
	}
	rows, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB,
		findMediaByContentHashQuery, map[string]any{"hash": h})
	if err != nil {
		return nil, fmt.Errorf("failed to find media by content hash: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// DeleteMediaById deletes a media item by its ID.
func (s *Store) DeleteMediaById(ctx context.Context, id int) error {
	query := `DELETE FROM media WHERE id = :id`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return fmt.Errorf("failed to delete media: %w", err)
	}
	return nil
}

// ListMediaPaged retrieves a paginated list of media items.
func (s *Store) ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.MediaFull, error) {
	if limit <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination parameters")
	}

	query := fmt.Sprintf(`SELECT * FROM media ORDER BY id %s LIMIT :limit OFFSET :offset`, orderFactor.String())
	mediaPage, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB, query, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}

	return mediaPage, nil
}
