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

func (ms *MYSQLStore) Media() dependency.Media {
	return &mediaStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddMedia(ctx context.Context, media *entity.MediaInsert) (int, error) {
	query := `INSERT INTO media (full_size, compressed, thumbnail) VALUES (:fullSize, :compressed, :thumbnail)`
	id, err := ExecNamedLastId(ctx, ms.DB(), query, map[string]any{
		"fullSize":   media.FullSize,
		"compressed": media.Compressed,
		"thumbnail":  media.Thumbnail,
	})
	if err != nil {
		return id, fmt.Errorf("failed to add media: %w", err)
	}
	return id, nil
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
func (ms *MYSQLStore) ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.Media, error) {
	if limit <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid pagination parameters")
	}

	query := `SELECT * FROM media ORDER BY id ` + string(orderFactor) + ` LIMIT :limit OFFSET :offset`
	mediaPage, err := QueryListNamed[entity.Media](ctx, ms.DB(), query, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}

	return mediaPage, nil
}
