package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

		query := `INSERT INTO archive (heading, description, tag) VALUES (:heading, :description, :tag)`
		aid, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"heading":     aNew.Heading,
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
				"is_video":   false,
			}
			rows = append(rows, row)
		}

		if aNew.VideoId.Valid {
			rows = append(rows, map[string]any{
				"archive_id": aid,
				"media_id":   int(aNew.VideoId.Int32),
				"is_video":   true,
			})
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
		query = `UPDATE archive SET heading = :heading, description = :description, tag = :tag WHERE id = :id`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
			"id":          aid,
			"heading":     aInsert.Heading,
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
				"is_video":   false,
			}
			rows = append(rows, row)
		}

		if aInsert.VideoId.Valid {
			rows = append(rows, map[string]any{
				"archive_id": aid,
				"media_id":   int(aInsert.VideoId.Int32),
				"is_video":   true,
			})
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

	_, countQuery := buildArchiveQuery(limit, offset)
	count, err := QueryCountNamed(ctx, ms.db, countQuery, map[string]any{})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get archive count: %w", err)
	}

	// Determine the actual limit to fetch
	actualLimit := limit
	if offset+limit < count {
		actualLimit = limit + 1 // Fetch one extra to check for "next"
	}

	// Build the list query with the determined limit
	listQuery, _ := buildArchiveQuery(actualLimit, offset)

	type archive struct {
		Id               int       `db:"id"`
		CreatedAt        time.Time `db:"created_at"`
		Heading          string    `db:"heading"`
		Description      string    `db:"description"`
		Tag              string    `db:"tag"`
		FullSize         string    `db:"full_size"`
		FullSizeWidth    int       `db:"full_size_width"`
		FullSizeHeight   int       `db:"full_size_height"`
		Thumbnail        string    `db:"thumbnail"`
		ThumbnailWidth   int       `db:"thumbnail_width"`
		ThumbnailHeight  int       `db:"thumbnail_height"`
		Compressed       string    `db:"compressed"`
		CompressedWidth  int       `db:"compressed_width"`
		CompressedHeight int       `db:"compressed_height"`
		BlurHash         string    `db:"blur_hash"`
	}

	archives, err := QueryListNamed[archive](ctx, ms.db, listQuery, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get archives: %w", err)
	}

	slog.Default().InfoContext(ctx, "archives", slog.Any("archives", archives))

	afs := make([]entity.ArchiveFull, 0, len(archives))
	for _, a := range archives {
		afs = append(afs, entity.ArchiveFull{
			Id:          a.Id,
			Heading:     a.Heading,
			Description: a.Description,
			Tag:         a.Tag,
			Slug:        dto.GetArchiveSlug(a.Id, a.Heading, a.Tag),
			NextSlug:    "",
			CreatedAt:   a.CreatedAt,
			Media: []entity.MediaFull{
				{
					Id:        a.Id,
					CreatedAt: a.CreatedAt,
					MediaItem: entity.MediaItem{
						FullSizeMediaURL:   a.FullSize,
						FullSizeWidth:      a.FullSizeWidth,
						FullSizeHeight:     a.FullSizeHeight,
						ThumbnailMediaURL:  a.Thumbnail,
						ThumbnailWidth:     a.ThumbnailWidth,
						ThumbnailHeight:    a.ThumbnailHeight,
						CompressedMediaURL: a.Compressed,
						CompressedWidth:    a.CompressedWidth,
						CompressedHeight:   a.CompressedHeight,
						BlurHash:           sql.NullString{String: a.BlurHash, Valid: a.BlurHash != ""},
					},
				},
			},
		})
	}

	// slog.Default().InfoContext(ctx, "afs", slog.Any("afs", afs))

	// Check if we fetched an extra record
	hasMore := len(afs) > limit
	if hasMore {
		afs = afs[:limit] // Trim the extra record
	}

	// Generate Slug and NextSlug
	for i := range afs {
		afs[i].Slug = dto.GetArchiveSlug(afs[i].Id, afs[i].Heading, afs[i].Tag)
		if hasMore && i == len(afs)-1 { // Last item and there's more
			afs[i].NextSlug = dto.GetArchiveSlug(afs[i].Id+1, afs[i].Heading, afs[i].Tag)
		} else if i+offset+1 < count { // General case: next archive exists
			afs[i].NextSlug = dto.GetArchiveSlug(afs[i].Id+1, afs[i].Heading, afs[i].Tag)
		} else { // Last item with no more archives
			afs[i].NextSlug = ""
		}
	}

	return afs, count, nil
}

// buildQuery refactored to use named parameters and to include limit and offset
func buildArchiveQuery(limit int, offset int) (string, string) {
	baseQuery := `
	SELECT 
    a.id, 
    a.created_at, 
    a.heading, 
    a.description, 
    a.tag,
		MAX(single_media.full_size) AS full_size,
		MAX(single_media.full_size_width) AS full_size_width,
		MAX(single_media.full_size_height) AS full_size_height,
		MAX(single_media.thumbnail) AS thumbnail,
		MAX(single_media.thumbnail_width) AS thumbnail_width,
		MAX(single_media.thumbnail_height) AS thumbnail_height,
		MAX(single_media.compressed) AS compressed,
		MAX(single_media.compressed_width) AS compressed_width,
		MAX(single_media.compressed_height) AS compressed_height,
		MAX(single_media.blur_hash) AS blur_hash
	FROM archive a 
	LEFT JOIN (
		SELECT 
			ai.archive_id, 
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
		FROM archive_item ai 
		JOIN media m ON ai.media_id = m.id
	) AS single_media ON a.id = single_media.archive_id
	GROUP BY a.id, a.created_at, a.heading, a.description, a.tag
	ORDER BY a.created_at DESC 
	LIMIT :limit OFFSET :offset;
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

	query := `
		SELECT a.* FROM archive a
		CROSS JOIN (
			SELECT created_at as target_date 
			FROM archive 
			WHERE id = :id
		) t
		WHERE a.created_at >= t.target_date
		OR a.created_at = (
			SELECT MIN(created_at) 
			FROM archive
		)
		ORDER BY 
			CASE WHEN a.created_at = t.target_date THEN 0 ELSE 1 END,
			a.created_at
		LIMIT 2
	`
	args := map[string]any{
		"id": id,
	}

	afs, err := QueryListNamed[entity.ArchiveFull](ctx, ms.db, query, args)
	if err != nil {
		return nil, fmt.Errorf("can't get archive full: %w", err)
	}

	if len(afs) == 0 {
		return nil, fmt.Errorf("no archive found with ID %d", id)
	}

	af := afs[0]

	// Handle next slug only if we have more than one archive
	if len(afs) > 1 {
		afNext := afs[1]
		af.NextSlug = dto.GetArchiveSlug(afNext.Id, afNext.Heading, afNext.Tag)
	}

	query = `
	SELECT m.*, ai.is_video
	FROM archive_item ai
	JOIN media m ON ai.media_id = m.id
	WHERE ai.archive_id = :id
	ORDER BY ai.id
	`

	type media struct {
		entity.MediaFull
		IsVideo bool `db:"is_video"`
	}

	mediaItems, err := QueryListNamed[media](ctx, ms.db, query, args)
	if err != nil {
		return nil, fmt.Errorf("can't get media items: %w", err)
	}

	af.Media = make([]entity.MediaFull, 0, len(mediaItems))
	for _, mi := range mediaItems {
		if !mi.IsVideo {
			af.Media = append(af.Media, entity.MediaFull{
				Id:        mi.Id,
				CreatedAt: mi.CreatedAt,
				MediaItem: entity.MediaItem{
					FullSizeMediaURL:   mi.FullSizeMediaURL,
					FullSizeWidth:      mi.FullSizeWidth,
					FullSizeHeight:     mi.FullSizeHeight,
					ThumbnailMediaURL:  mi.ThumbnailMediaURL,
					ThumbnailWidth:     mi.ThumbnailWidth,
					ThumbnailHeight:    mi.ThumbnailHeight,
					CompressedMediaURL: mi.CompressedMediaURL,
					CompressedWidth:    mi.CompressedWidth,
					CompressedHeight:   mi.CompressedHeight,
					BlurHash:           mi.BlurHash,
				},
			})
		} else {
			af.Video = entity.MediaFull{
				Id:        mi.Id,
				CreatedAt: mi.CreatedAt,
				MediaItem: entity.MediaItem{
					FullSizeMediaURL:   mi.FullSizeMediaURL,
					FullSizeWidth:      mi.FullSizeWidth,
					FullSizeHeight:     mi.FullSizeHeight,
					ThumbnailMediaURL:  mi.ThumbnailMediaURL,
					ThumbnailWidth:     mi.ThumbnailWidth,
					ThumbnailHeight:    mi.ThumbnailHeight,
					CompressedMediaURL: mi.CompressedMediaURL,
					CompressedWidth:    mi.CompressedWidth,
					CompressedHeight:   mi.CompressedHeight,
					BlurHash:           mi.BlurHash,
				},
			}
		}
	}

	af.Slug = dto.GetArchiveSlug(af.Id, af.Heading, af.Tag)

	return &af, nil

}
