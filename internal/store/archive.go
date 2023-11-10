package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type archiveStore struct {
	*MYSQLStore
}

func (ms *MYSQLStore) Archive() dependency.Archive {
	return &archiveStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddArchive(ctx context.Context, archiveNew *entity.ArchiveNew) (int, error) {
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
		query := `DELETE FROM archive_item WHERE id = :id`
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

		return nil
	})
}

type archiveJoin struct {
	entity.Archive
	ItemID    int            `db:"item_id"`
	ItemMedia string         `db:"media"`
	ItemURL   sql.NullString `db:"url"`
	ItemTitle sql.NullString `db:"item_title"`
}

// GetArchivesPaged retrieves a paginated list of archives with sorting based on an order factor.
func (ms *MYSQLStore) GetArchivesPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveFull, error) {
	if limit <= 0 || offset < 0 {
		return nil, errors.New("invalid pagination parameters")
	}

	// Prepare the query with dynamic ordering and pagination placeholders
	query := `
    SELECT a.id, a.created_at, a.updated_at, a.title, a.description,
           ai.id AS item_id, ai.media, ai.url, ai.title AS item_title
    FROM archive a
    LEFT JOIN archive_item ai ON a.id = ai.archive_id
    ORDER BY a.created_at %s LIMIT ? OFFSET ?`
	query = fmt.Sprintf(query, orderFactor)

	// Slice to store the joined data from the query
	var archiveData []archiveJoin

	// Execute the query with context and scan the results into the slice
	err := ms.DB().SelectContext(ctx, &archiveData, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get archives paged: %w", err)
	}

	// Group the flat data into structured archives
	grouped := groupArchives(archiveData)
	// Flatten the map into a slice for the final output
	return flattenArchives(grouped), nil
}

// groupArchives groups the flat join data by archive ID, creating structured archive entities.
func groupArchives(data []archiveJoin) map[int]entity.ArchiveFull {
	// Map to hold grouped archives
	grouped := make(map[int]entity.ArchiveFull)
	for _, d := range data {
		// Check if the archive is already in the map
		archive, ok := grouped[d.ID]
		if !ok {
			// If not, initialize it
			archive = entity.ArchiveFull{Archive: &d.Archive}
		}

		// If an item is present, add it to the archive's items
		if d.ItemID != 0 {
			archive.Items = append(archive.Items, entity.ArchiveItem{
				ID:        d.ItemID,
				ArchiveID: d.ID,
				ArchiveItemInsert: entity.ArchiveItemInsert{
					Media: d.ItemMedia,
					URL:   d.ItemURL,
					Title: d.ItemTitle,
				},
			})
		}

		// Update the map with the newly added or modified archive
		grouped[d.ID] = archive
	}
	return grouped
}

// flattenArchives converts the map of archives into a slice.
func flattenArchives(grouped map[int]entity.ArchiveFull) []entity.ArchiveFull {
	// Slice to hold the final flattened archive list
	archives := make([]entity.ArchiveFull, 0, len(grouped))
	for _, archive := range grouped {
		// Append each archive to the slice
		archives = append(archives, archive)
	}
	return archives
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
