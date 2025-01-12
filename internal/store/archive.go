package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
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

func (ms *MYSQLStore) AddArchive(ctx context.Context, aNew *entity.ArchiveInsert) (int, error) {

	if len(aNew.MediaIds) == 0 {
		return 0, errors.New("archive items must not be empty")
	}

	var aid int
	var err error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		query := `INSERT INTO archive (title, description, tag) VALUES (:title, :description, :tag)`
		aid, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"title":       aNew.Title,
			"description": aNew.Description,
			"tag":         aNew.Tag,
		})
		if err != nil {
			return fmt.Errorf("failed to add archive: %w", err)
		}

		rows := make([]map[string]any, 0, len(aNew.MediaIds))
		for _, mid := range aNew.MediaIds {
			row := map[string]any{
				"archive_id": aid,
				"media_id":   mid,
			}
			rows = append(rows, row)
		}

		err = BulkInsert(ctx, rep.DB(), "archive_item", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive items: %w", err)
		}

		return nil
	})
	if err != nil {
		return aid, fmt.Errorf("tx failed: %w", err)
	}

	return aid, nil
}

func (ms *MYSQLStore) UpdateArchive(ctx context.Context, aid int, aInsert *entity.ArchiveInsert) error {

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// If no items are provided, delete the archive and return
		if len(aInsert.MediaIds) == 0 {
			af, err := rep.Archive().GetArchiveById(ctx, aid)
			if err != nil {
				return fmt.Errorf("failed to get archive with ID %d: %w", aid, err)
			}
			if len(af.Media) == 0 {
				return nil
			}
		}

		// Delete existing archive items
		query := `DELETE FROM archive_item WHERE archive_id = :archiveId`
		_, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"archiveId": aid,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive items with archive Id %d: %w", aid, err)
		}

		// Update the archive itself
		query = `UPDATE archive SET title = :title, description = :description, tag = :tag WHERE id = :id`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
			"id":          aid,
			"title":       aInsert.Title,
			"description": aInsert.Description,
			"tag":         aInsert.Tag,
		})
		if err != nil {
			return fmt.Errorf("failed to update archive: %w", err)
		}

		// Insert new archive items
		rows := make([]map[string]any, 0, len(aInsert.MediaIds))
		for _, mid := range aInsert.MediaIds {
			row := map[string]any{
				"archive_id": aid,
				"media_id":   mid,
			}
			rows = append(rows, row)
		}

		err = BulkInsert(ctx, rep.DB(), "archive_item", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive items: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("tx failed: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) GetArchivesPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveFull, int, error) {
	if limit <= 0 || offset < 0 {
		return nil, 0, errors.New("invalid pagination parameters")
	}
	// Build and execute the queries
	listQuery, countQuery := buildArchiveQuery(limit, offset)
	count, err := QueryCountNamed(ctx, ms.db, countQuery, map[string]any{})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get archive count: %w", err)
	}

	// Fetch products
	afs, err := QueryListNamed[entity.ArchiveFull](ctx, ms.db, listQuery, map[string]any{})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get archives: %w", err)
	}

	for _, af := range afs {
		af.Slug = dto.GetArchiveSlug()
	}

	return archives, count, nil

}

// buildQuery refactored to use named parameters and to include limit and offset
func buildArchiveQuery(limit int, offset int) (string, string) {
	baseQuery := `
	SELECT 
		a.*,
		m.full_size,
		m.full_size_width,
		m.full_size_height,
		m.thumbnail,
		m.thumbnail_width,
		m.thumbnail_height,
		m.compressed,
		m.compressed_width,
		m.compressed_height,
		m.blur_hash
	FROM 
		archive a
	JOIN 
		archive_item ai
	ON 
		a.id = ai.archive_id
	JOIN
		media m ON ai.media_id = m.id
	ORDER BY a.created_at DESC
	LIMIT :limit OFFSET :offset
	`

	countQuery := "SELECT COUNT(*) FROM archive"

	return baseQuery, countQuery
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

		query = `DELETE FROM archive_item WHERE archive_id = :id`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"id": id,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive items with ID %d: %w", id, err)
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

func (ms *MYSQLStore) GetArchiveById(ctx context.Context, id int) (*entity.ArchiveFull, error) {

	query := `SELECT * FROM archive a WHERE a.id = :id`
	args := map[string]any{
		"id": id,
	}

	// Fetch products
	af, err := QueryNamedOne[entity.ArchiveFull](ctx, ms.db, query, args)
	if err != nil {
		return nil, fmt.Errorf("can't get archive full: %w", err)
	}

	// TODO: add next and prev
	query = `
	SELECT 
		m.*
	FROM 
		archive_item ai
	WHERE 
		ai.archive_id = :id
	JOIN
		media m 
	ON 
		ai.media_id = m.id
	`

	af.Media, err = QueryListNamed[entity.MediaFull](ctx, ms.db, query, args)
	if err != nil {
		return nil, fmt.Errorf("can't get media items: %w", err)
	}

	return &af, nil

}
