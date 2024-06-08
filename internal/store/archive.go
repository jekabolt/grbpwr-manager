package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type archiveStore struct {
	*MYSQLStore
}

// Archive returns an object implementing archive interface
func (ms *MYSQLStore) Archive() dependency.Archive {
	return &archiveStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddArchive(ctx context.Context, aNew *entity.ArchiveNew) (int, error) {

	if aNew.Items == nil || len(aNew.Items) == 0 {
		return 0, errors.New("archive items must not be empty")
	}

	if aNew.Archive.Title == "" {
		return 0, errors.New("archive title must not be empty")
	}

	var archiveID int
	var err error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `INSERT INTO archive (title, description) VALUES (:title, :description)`
		archiveID, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"title":       aNew.Archive.Title,
			"description": aNew.Archive.Description,
		})
		if err != nil {
			return fmt.Errorf("failed to add archive: %w", err)
		}

		rows := make([]map[string]any, 0, len(aNew.Items))
		for i, archive := range aNew.Items {
			row := map[string]any{
				"media_id":        archive.MediaId,
				"title":           archive.Title,
				"url":             archive.URL,
				"archive_id":      archiveID,
				"sequence_number": i,
			}
			rows = append(rows, row)
		}

		err := BulkInsert(ctx, rep.DB(), "archive_item", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive items: %w", err)
		}

		return nil
	})
	if err != nil {
		return archiveID, fmt.Errorf("tx failed: %w", err)
	}

	return archiveID, nil
}

func (ms *MYSQLStore) UpdateArchive(ctx context.Context, aId int, aBody *entity.ArchiveBody, aItems []entity.ArchiveItemInsert) error {

	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `DELETE FROM archive_item WHERE archive_id = :archiveId`
		_, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"archiveId": aId,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive items with archive Id %d: %w", aId, err)
		}

		query = `UPDATE archive SET title = :title, description = :description WHERE id = :id`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
			"id":          aId,
			"title":       aBody.Title,
			"description": aBody.Description,
		})
		if err != nil {
			return fmt.Errorf("failed to update archive: %w", err)
		}

		rows := make([]map[string]any, 0, len(aItems))
		for i, archive := range aItems {
			row := map[string]any{
				"archive_id":      aId,
				"media_id":        archive.MediaId,
				"title":           archive.Title,
				"sequence_number": i,
			}
			rows = append(rows, row)
		}

		err = BulkInsert(ctx, rep.DB(), "archive_item", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive items: %w", err)
		}
		return nil
	})
}

type archiveJoin struct {
	Id           int       `db:"id"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
	Title        string    `db:"title"`
	Description  string    `db:"description"`
	ArchiveItems string    `db:"archive_items"`
}

func (ms *MYSQLStore) GetArchivesPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveFull, error) {
	if limit <= 0 || offset < 0 {
		return nil, errors.New("invalid pagination parameters")
	}

	query := fmt.Sprintf(
		`SELECT 
		a.id,
		a.created_at,
		a.updated_at,
		a.title,
		a.description,
		JSON_ARRAYAGG(
			JSON_OBJECT(
				'id', ai.id,
				'media', JSON_OBJECT(
					'id', m.id,
					'full_size', m.full_size,
					'full_size_width', m.full_size_width,
					'full_size_height', m.full_size_height,
					'thumbnail', m.thumbnail,
					'thumbnail_width', m.thumbnail_width,
					'thumbnail_height', m.thumbnail_height,
					'compressed', m.compressed,
					'compressed_width', m.compressed_width,
					'compressed_height', m.compressed_height
				),
				'url', ai.url,
				'title', ai.title,
				'archive_id', ai.archive_id
			)
		) AS archive_items
		FROM 
			archive a
		LEFT JOIN 
			archive_item ai ON a.id = ai.archive_id
		LEFT JOIN
			media m ON ai.media_id = m.id
		GROUP BY 
			a.id
		ORDER BY 
			a.created_at %s
		LIMIT 
			? OFFSET ?`,
		orderFactor.String(),
	)

	// Slice to store the joined data from the query
	var archiveData []archiveJoin

	// Execute the query with context and scan the results into the slice
	err := ms.DB().SelectContext(ctx, &archiveData, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get archives paged: %w", err)
	}

	afs, err := convertArchiveJoinToArchiveFull(archiveData)
	if err != nil {
		return nil, fmt.Errorf("failed to convert archive json to entity %w", err)
	}

	//TODO: remove this log
	slog.Default().Debug("archive data", slog.Any("afs", afs), slog.Int("count", len(afs)))

	return afs, err

}

func convertArchiveJoinToArchiveFull(ajs []archiveJoin) ([]entity.ArchiveFull, error) {
	result := make([]entity.ArchiveFull, len(ajs))

	for i, aj := range ajs {
		var archiveItems []entity.ArchiveItemFull
		if err := json.Unmarshal([]byte(aj.ArchiveItems), &archiveItems); err != nil {
			return nil, err
		}

		// Parse CreatedAt for each MediaFull in archiveItems
		for j, item := range archiveItems {
			createdAtStr := item.Media.CreatedAt.Format("2006-01-02 15:04:05.000000")
			parsedTime, err := time.Parse("2006-01-02 15:04:05.000000", createdAtStr)
			if err != nil {
				return nil, err
			}
			archiveItems[j].Media.CreatedAt = parsedTime
		}

		archive := &entity.Archive{
			ID:        aj.Id,
			CreatedAt: aj.CreatedAt,
			UpdatedAt: aj.UpdatedAt,
			ArchiveBody: entity.ArchiveBody{
				Title:       aj.Title,
				Description: aj.Description,
			},
		}

		result[i] = entity.ArchiveFull{
			Archive: archive,
			Items:   archiveItems,
		}
	}

	return result, nil
}

func (ms *MYSQLStore) DeleteArchiveById(ctx context.Context, id int) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `DELETE FROM archive WHERE id = :id`
		res, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"id": id,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive with ID %d: %w", id, err)
		}

		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("error getting rows affected for archive with ID %d: %w", id, err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("no archive found with ID %d", id)
		}

		return nil
	})
}
