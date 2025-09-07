package store

import (
	"context"
	"database/sql"
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

		query := `INSERT INTO archive (tag, main_media_id, thumbnail_id) VALUES (:tag, :mainMediaId, :thumbnailId)`
		aid, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"tag":         aNew.Tag,
			"mainMediaId": aNew.MainMediaId,
			"thumbnailId": aNew.ThumbnailId,
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

		// Insert translations
		rows = make([]map[string]any, 0, len(aNew.Translations))
		for _, t := range aNew.Translations {
			row := map[string]any{
				"archive_id":  aid,
				"language_id": t.LanguageId,
				"heading":     t.Heading,
				"description": t.Description,
			}
			rows = append(rows, row)
		}

		err = BulkInsert(ctx, rep.DB(), "archive_translation", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive translations: %w", err)
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
		query = `
		UPDATE archive SET 
			tag = :tag,
			main_media_id = :main_media_id,
			thumbnail_id = :thumbnail_id 
		WHERE id = :id`

		_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
			"id":            aid,
			"tag":           aInsert.Tag,
			"main_media_id": aInsert.MainMediaId,
			"thumbnail_id":  aInsert.ThumbnailId,
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

		// Delete existing translations
		query = `DELETE FROM archive_translation WHERE archive_id = :archiveId`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"archiveId": aid,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive translations with archive Id %d: %w", aid, err)
		}

		// Insert new translations
		rows = make([]map[string]any, 0, len(aInsert.Translations))
		for _, t := range aInsert.Translations {
			row := map[string]any{
				"archive_id":  aid,
				"language_id": t.LanguageId,
				"heading":     t.Heading,
				"description": t.Description,
			}
			rows = append(rows, row)
		}

		err = BulkInsert(ctx, rep.DB(), "archive_translation", rows)
		if err != nil {
			return fmt.Errorf("failed to add archive translations: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("tx failed: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) GetArchivesPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveList, int, error) {
	if limit <= 0 {
		return nil, 0, errors.New("limit must be greater than 0")
	}
	if offset < 0 {
		return nil, 0, errors.New("offset must be >= 0")
	}

	// Query for total count
	countQuery := `SELECT COUNT(DISTINCT a.id) FROM archive a`
	total, err := QueryCountNamed(ctx, ms.DB(), countQuery, map[string]any{})
	if err != nil {
		return nil, 0, err
	}

	// Query for paged archives with joined media
	query := `
	SELECT 
		a.id, a.tag, a.created_at,
		mt.id AS thumbnail_id, mt.full_size AS thumbnail_full_size, mt.full_size_width AS thumbnail_full_size_width, mt.full_size_height AS thumbnail_full_size_height, mt.thumbnail AS thumbnail_thumbnail, mt.thumbnail_width AS thumbnail_thumbnail_width, mt.thumbnail_height AS thumbnail_thumbnail_height, mt.compressed AS thumbnail_compressed, mt.compressed_width AS thumbnail_compressed_width, mt.compressed_height AS thumbnail_compressed_height, mt.blur_hash AS thumbnail_blur_hash
	FROM archive a
	LEFT JOIN media mt ON a.thumbnail_id = mt.id
	ORDER BY a.created_at ` + orderFactor.String() + `
	LIMIT :limit OFFSET :offset`

	// Use MakeQuery to expand named parameters to positional arguments
	sqlStr, args, err := MakeQuery(query, map[string]any{"limit": limit + 1, "offset": offset})
	if err != nil {
		return nil, 0, err
	}
	rows, err := ms.DB().QueryxContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var archives []entity.ArchiveList
	for rows.Next() {
		var al entity.ArchiveList
		var thumbnail entity.MediaFull
		var thumbnailBlurHash sql.NullString

		err := rows.Scan(
			&al.Id, &al.Tag, &al.CreatedAt,
			&thumbnail.Id,
			&thumbnail.MediaItem.FullSizeMediaURL, &thumbnail.MediaItem.FullSizeWidth, &thumbnail.MediaItem.FullSizeHeight, &thumbnail.MediaItem.ThumbnailMediaURL, &thumbnail.MediaItem.ThumbnailWidth, &thumbnail.MediaItem.ThumbnailHeight, &thumbnail.MediaItem.CompressedMediaURL, &thumbnail.MediaItem.CompressedWidth, &thumbnail.MediaItem.CompressedHeight, &thumbnailBlurHash,
		)
		if err != nil {
			return nil, 0, err
		}

		if thumbnailBlurHash.Valid {
			thumbnail.MediaItem.BlurHash = thumbnailBlurHash
		} else {
			thumbnail.MediaItem.BlurHash = sql.NullString{}
		}

		al.Thumbnail = thumbnail
		archives = append(archives, al)
	}

	// Fetch translations for each archive
	for i := range archives {
		translations, err := ms.GetArchiveTranslations(ctx, archives[i].Id)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to get translations for archive %d: %w", archives[i].Id, err)
		}
		archives[i].Translations = translations

		// Generate slug using first translation's heading if available
		if len(translations) > 0 {
			archives[i].Slug = dto.GetArchiveSlug(archives[i].Id, translations[0].Heading, archives[i].Tag)
		} else {
			archives[i].Slug = dto.GetArchiveSlug(archives[i].Id, "", archives[i].Tag)
		}
	}

	// Trim to limit if we fetched extra records
	if len(archives) > limit {
		archives = archives[:limit]
	}

	return archives, total, nil
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

		query = `DELETE FROM archive_translation WHERE archive_id = :id`
		_, err = rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"id": id,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive translations with ID %d: %w", id, err)
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
	SELECT 
		a.id, a.tag, a.created_at, a.main_media_id, a.thumbnail_id,
		mt.id AS thumbnail_id, mt.full_size AS thumbnail_full_size, mt.full_size_width AS thumbnail_full_size_width, mt.full_size_height AS thumbnail_full_size_height, mt.thumbnail AS thumbnail_thumbnail, mt.thumbnail_width AS thumbnail_thumbnail_width, mt.thumbnail_height AS thumbnail_thumbnail_height, mt.compressed AS thumbnail_compressed, mt.compressed_width AS thumbnail_compressed_width, mt.compressed_height AS thumbnail_compressed_height, mt.blur_hash AS thumbnail_blur_hash,
		mm.id AS main_media_id, mm.full_size AS main_media_full_size, mm.full_size_width AS main_media_full_size_width, mm.full_size_height AS main_media_full_size_height, mm.thumbnail AS main_media_thumbnail, mm.thumbnail_width AS main_media_thumbnail_width, mm.thumbnail_height AS main_media_thumbnail_height, mm.compressed AS main_media_compressed, mm.compressed_width AS main_media_compressed_width, mm.compressed_height AS main_media_compressed_height, mm.blur_hash AS main_media_blur_hash
	FROM archive a
	LEFT JOIN media mt ON a.thumbnail_id = mt.id
	LEFT JOIN media mm ON a.main_media_id = mm.id
	WHERE a.id = :id`

	sqlStr, args, err := MakeQuery(query, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	rows, err := ms.DB().QueryxContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, errors.New("archive not found")
	}

	var al entity.ArchiveList
	var thumbnail entity.MediaFull
	var mainMediaId sql.NullInt64
	// Internal struct for nullable mainMedia fields
	type mainMediaScan struct {
		FullSizeMediaURL   sql.NullString
		FullSizeWidth      sql.NullInt64
		FullSizeHeight     sql.NullInt64
		ThumbnailMediaURL  sql.NullString
		ThumbnailWidth     sql.NullInt64
		ThumbnailHeight    sql.NullInt64
		CompressedMediaURL sql.NullString
		CompressedWidth    sql.NullInt64
		CompressedHeight   sql.NullInt64
		BlurHash           sql.NullString
	}
	var mainMediaS mainMediaScan
	err = rows.Scan(
		&al.Id, &al.Tag, &al.CreatedAt, &mainMediaId, &thumbnail.Id,
		&thumbnail.Id, &thumbnail.MediaItem.FullSizeMediaURL, &thumbnail.MediaItem.FullSizeWidth, &thumbnail.MediaItem.FullSizeHeight, &thumbnail.MediaItem.ThumbnailMediaURL, &thumbnail.MediaItem.ThumbnailWidth, &thumbnail.MediaItem.ThumbnailHeight, &thumbnail.MediaItem.CompressedMediaURL, &thumbnail.MediaItem.CompressedWidth, &thumbnail.MediaItem.CompressedHeight, &thumbnail.MediaItem.BlurHash,
		&mainMediaId,
		&mainMediaS.FullSizeMediaURL, &mainMediaS.FullSizeWidth, &mainMediaS.FullSizeHeight, &mainMediaS.ThumbnailMediaURL, &mainMediaS.ThumbnailWidth, &mainMediaS.ThumbnailHeight, &mainMediaS.CompressedMediaURL, &mainMediaS.CompressedWidth, &mainMediaS.CompressedHeight, &mainMediaS.BlurHash,
	)
	if err != nil {
		return nil, err
	}
	var mainMedia entity.MediaFull
	if mainMediaId.Valid {
		mainMedia.Id = int(mainMediaId.Int64)
		mainMedia.MediaItem.FullSizeMediaURL = mainMediaS.FullSizeMediaURL.String
		mainMedia.MediaItem.FullSizeWidth = int(mainMediaS.FullSizeWidth.Int64)
		mainMedia.MediaItem.FullSizeHeight = int(mainMediaS.FullSizeHeight.Int64)
		mainMedia.MediaItem.ThumbnailMediaURL = mainMediaS.ThumbnailMediaURL.String
		mainMedia.MediaItem.ThumbnailWidth = int(mainMediaS.ThumbnailWidth.Int64)
		mainMedia.MediaItem.ThumbnailHeight = int(mainMediaS.ThumbnailHeight.Int64)
		mainMedia.MediaItem.CompressedMediaURL = mainMediaS.CompressedMediaURL.String
		mainMedia.MediaItem.CompressedWidth = int(mainMediaS.CompressedWidth.Int64)
		mainMedia.MediaItem.CompressedHeight = int(mainMediaS.CompressedHeight.Int64)
		mainMedia.MediaItem.BlurHash = mainMediaS.BlurHash
	} else {
		mainMedia = entity.MediaFull{}
	}
	al.Thumbnail = thumbnail

	// Fetch translations for this archive
	translations, err := ms.GetArchiveTranslations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get translations for archive %d: %w", id, err)
	}
	al.Translations = translations

	// Generate slug using first translation's heading if available
	if len(translations) > 0 {
		al.Slug = dto.GetArchiveSlug(al.Id, translations[0].Heading, al.Tag)
	} else {
		al.Slug = dto.GetArchiveSlug(al.Id, "", al.Tag)
	}

	// Query all media for this archive
	mediaQuery := `
	SELECT 
		m.id, m.created_at, m.full_size, m.full_size_width, m.full_size_height, m.thumbnail, m.thumbnail_width, m.thumbnail_height, m.compressed, m.compressed_width, m.compressed_height, m.blur_hash
	FROM archive_item ai
	JOIN media m ON ai.media_id = m.id
	WHERE ai.archive_id = :archiveId`
	media, err := QueryListNamed[entity.MediaFull](ctx, ms.DB(), mediaQuery, map[string]any{"archiveId": id})
	if err != nil {
		return nil, err
	}

	return &entity.ArchiveFull{
		ArchiveList: al,
		MainMedia:   mainMedia,
		Media:       media,
	}, nil
}

func (ms *MYSQLStore) GetArchiveTranslations(ctx context.Context, id int) ([]entity.ArchiveTranslation, error) {
	query := `
	SELECT 
		at.language_id, at.heading, at.description
	FROM archive_translation at
	WHERE at.archive_id = :id`
	translations, err := QueryListNamed[entity.ArchiveTranslation](ctx, ms.DB(), query, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return translations, nil
}
