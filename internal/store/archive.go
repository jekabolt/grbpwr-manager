package store

import (
	"context"
	"database/sql"
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

func (ms *MYSQLStore) AddArchive(ctx context.Context, archiveNew *entity.ArchiveNew) (int, error) {

	if archiveNew.Items == nil || len(archiveNew.Items) == 0 {
		return 0, errors.New("archive items must not be empty")
	}

	if archiveNew.Archive.Title == "" {
		return 0, errors.New("archive title must not be empty")
	}

	var archiveID int
	var err error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `INSERT INTO archive (title, description) VALUES (:title, :description)`
		archiveID, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"title":       archiveNew.Archive.Title,
			"description": archiveNew.Archive.Description,
		})
		if err != nil {
			return fmt.Errorf("failed to add archive: %w", err)
		}

		// Insert the Archive Items
		query = `INSERT INTO archive_item (archive_id, media, url, title) VALUES (:archive_id, :media, :url, :title)`
		for _, i := range archiveNew.Items {
			item := &entity.ArchiveItem{
				ArchiveItemInsert: i,
				ArchiveID:         archiveID,
			}
			_, err := rep.DB().NamedExecContext(ctx, query, item)
			if err != nil {
				return fmt.Errorf("failed to add archive item: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return archiveID, fmt.Errorf("tx failed: %w", err)
	}

	return archiveID, nil
}

func (ms *MYSQLStore) UpdateArchive(ctx context.Context, id int, archiveUpd *entity.ArchiveInsert) error {
	query := `UPDATE archive SET title = :title, description = :description WHERE id = :id`
	_, err := ms.DB().NamedExecContext(ctx, query, map[string]any{
		"id":          id,
		"title":       archiveUpd.Title,
		"description": archiveUpd.Description,
	})
	if err != nil {
		return fmt.Errorf("failed to update archive: %w", err)
	}

	return nil
}

// AddArchiveItems adds new items to an existing archive.
func (ms *MYSQLStore) AddArchiveItems(ctx context.Context, archiveId int, archiveItemNew []entity.ArchiveItemInsert) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `INSERT INTO archive_item (archive_id, media, url, title) VALUES (:archiveId, :media, :url, :title)`
		for _, item := range archiveItemNew {
			_, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
				"archiveId": archiveId,
				"media":     item.Media,
				"url":       item.URL,
				"title":     item.Title,
			})
			if err != nil {
				return fmt.Errorf("failed to add archive item with ID %d: %w", archiveId, err)
			}
		}
		return nil
	})
}

// DeleteArchiveItem deletes an item from an archive by its ID.
func (ms *MYSQLStore) DeleteArchiveItem(ctx context.Context, archiveItemID int) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		// Check if the archive item is the last item in the archive
		query := `
		SELECT archive_id, COUNT(id) AS item_count
		FROM archive_item
		WHERE archive_id = (SELECT archive_id FROM archive_item WHERE id = :archiveItemID)
		GROUP BY archive_id;`
		type archiveItemCount struct {
			ArchiveID int `db:"archive_id"`
			ItemCount int `db:"item_count"`
		}
		count, err := QueryNamedOne[archiveItemCount](ctx, rep.DB(), query, map[string]interface{}{
			"archiveItemID": archiveItemID,
		})
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("failed to get archive item count: %w", err)
			}
		}

		slog.Default().Debug("archive item count", slog.Int("count", count.ItemCount), slog.Int("archive_id", count.ArchiveID))

		query = `DELETE FROM archive_item WHERE id = :id`
		res, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"id": archiveItemID,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive item with ID %d: %w", archiveItemID, err)
		}

		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("error getting rows affected for archive item with ID %d: %w", archiveItemID, err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("no archive item found with ID %d", archiveItemID)
		}

		// If the item was the last item in the archive, delete the archive as well
		if count.ItemCount == 1 {
			query = `DELETE FROM archive WHERE id = :id`
			_, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
				"id": count.ArchiveID,
			})
			if err != nil {
				return fmt.Errorf("failed to delete archive with ID %d: %w", count.ArchiveID, err)
			}
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
				'media', ai.media,
				'url', ai.url,
				'title', ai.title,
				'archive_id', ai.archive_id
			)
			) AS archive_items
		FROM 
			archive a
		LEFT JOIN 
			archive_item ai ON a.id = ai.archive_id
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

	slog.Default().Debug("archive data", slog.Any("afs", afs), slog.Int("count", len(afs)))

	return afs, err

}

func convertArchiveJoinToArchiveFull(ajs []archiveJoin) ([]entity.ArchiveFull, error) {
	result := make([]entity.ArchiveFull, len(ajs))

	for i, aj := range ajs {
		var archiveItems []entity.ArchiveItem
		if err := json.Unmarshal([]byte(aj.ArchiveItems), &archiveItems); err != nil {
			return nil, err
		}

		archive := &entity.Archive{
			ID:        aj.Id,
			CreatedAt: aj.CreatedAt,
			UpdatedAt: aj.UpdatedAt,
			ArchiveInsert: entity.ArchiveInsert{
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

func (ms *MYSQLStore) GetArchiveById(ctx context.Context, id int) (*entity.ArchiveFull, error) {
	var archiveFull entity.ArchiveFull
	var archive entity.Archive
	var items []entity.ArchiveItem
	baseQuery := `
	SELECT a.id, a.created_at, a.updated_at, a.title, a.description
	FROM archive a
	WHERE a.id = ?`

	itemQuery := `
	SELECT ai.id, ai.archive_id, ai.media, ai.url, ai.title
	FROM archive_item ai
	WHERE ai.archive_id = ?`

	// First, get the archive
	err := ms.DB().GetContext(ctx, &archive, baseQuery, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get archive by ID: %w", err)
	}

	// Now, get the items for the archive
	err = ms.DB().SelectContext(ctx, &items, itemQuery, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get archive items by archive ID: %w", err)
	}

	archiveFull.Archive = &archive
	archiveFull.Items = items

	return &archiveFull, nil
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
