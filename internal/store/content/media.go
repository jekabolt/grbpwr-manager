package content

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AddMedia adds a new media item to the database.
func (s *Store) AddMedia(ctx context.Context, media *entity.MediaItem) (int, error) {
	query := `INSERT INTO media (
		full_size, full_size_width, full_size_height,
		compressed, compressed_width, compressed_height,
		thumbnail, thumbnail_width, thumbnail_height, blur_hash
	) VALUES (
		:fullSize, :fullSizeWidth, :fullSizeHeight,
		:compressed, :compressedWidth, :compressedHeight,
		:thumbnail, :thumbnailWidth, :thumbnailHeight, :blurHash
	);`
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, query, map[string]any{
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
