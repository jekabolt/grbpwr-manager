package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type mediaStore struct {
	*MYSQLStore
}

// Media returns an object implementing media interface
func (ms *MYSQLStore) Media() dependency.Media {
	return &mediaStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddMedia(ctx context.Context, media *entity.MediaItem) (int, error) {
	query := `INSERT INTO media (
		full_size, full_size_width, full_size_height,
		compressed, compressed_width, compressed_height,
		thumbnail, thumbnail_width, thumbnail_height, blur_hash
	) VALUES (
		:fullSize, :fullSizeWidth, :fullSizeHeight,
		:compressed, :compressedWidth, :compressedHeight,
		:thumbnail, :thumbnailWidth, :thumbnailHeight, :blurHash
	);`
	id, err := ExecNamedLastId(ctx, ms.DB(), query, map[string]any{
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

func (ms *MYSQLStore) GetMediaById(ctx context.Context, id int) (*entity.MediaFull, error) {
	query := `SELECT * FROM media WHERE id = :id`
	media, err := QueryNamedOne[entity.MediaFull](ctx, ms.DB(), query, map[string]any{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}
	return &media, nil
}

func (ms *MYSQLStore) DeleteMediaById(ctx context.Context, id int) error {
	query := `DELETE FROM media WHERE id = :id`
	err := ExecNamed(ctx, ms.DB(), query, map[string]any{
		"id": id,
	})
	if err != nil {
		return fmt.Errorf("failed to delete media: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.MediaFull, error) {
	if limit <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination parameters")
	}

	query := fmt.Sprintf(`SELECT * FROM media ORDER BY id %s LIMIT :limit OFFSET :offset`, orderFactor.String())
	mediaPage, err := QueryListNamed[entity.MediaFull](ctx, ms.DB(), query, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}

	return mediaPage, nil
}
