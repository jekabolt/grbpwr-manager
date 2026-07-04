package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// marshalArchiveBody marshals the timeline body, normalising a nil slice to an
// empty JSON array ("[]") instead of "null", so the stored shape is consistent.
func marshalArchiveBody(items []entity.ArchiveItemInsert) ([]byte, error) {
	if items == nil {
		items = []entity.ArchiveItemInsert{}
	}
	return json.Marshal(items)
}

// AddArchive adds a new archive: metadata + translations, and the ordered
// timeline body (typed blocks — main media, media lines, products, etc.) stored
// as JSON in the `body` column.
func (s *Store) AddArchive(ctx context.Context, aNew *entity.ArchiveInsert) (int, error) {
	bodyJSON, err := marshalArchiveBody(aNew.Items)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal archive body: %w", err)
	}

	var aid int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {

		query := `INSERT INTO archive (tag, thumbnail_id, body) VALUES (:tag, :thumbnailId, :body)`
		aid, err = storeutil.ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"tag":         aNew.Tag,
			"thumbnailId": aNew.ThumbnailId,
			"body":        bodyJSON,
		})
		if err != nil {
			return fmt.Errorf("failed to add archive: %w", err)
		}

		// Insert translations
		rows := make([]map[string]any, 0, len(aNew.Translations))
		for _, t := range aNew.Translations {
			row := map[string]any{
				"archive_id":  aid,
				"language_id": t.LanguageId,
				"heading":     t.Heading,
			}
			rows = append(rows, row)
		}

		err = storeutil.BulkInsert(ctx, rep.DB(), "archive_translation", rows)
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

func (s *Store) UpdateArchive(ctx context.Context, aid int, aInsert *entity.ArchiveInsert) error {
	bodyJSON, err := marshalArchiveBody(aInsert.Items)
	if err != nil {
		return fmt.Errorf("failed to marshal archive body: %w", err)
	}

	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Ensure the archive exists so an update to a missing id fails loudly
		// instead of silently affecting zero rows (an UPDATE with unchanged
		// values reports 0 affected rows, so RowsAffected can't be trusted here).
		cnt, err := storeutil.QueryCountNamed(ctx, rep.DB(), `SELECT COUNT(*) FROM archive WHERE id = :id`, map[string]any{"id": aid})
		if err != nil {
			return fmt.Errorf("failed to check archive %d exists: %w", aid, err)
		}
		if cnt == 0 {
			return fmt.Errorf("archive %d not found: %w", aid, sql.ErrNoRows)
		}

		// Update the archive itself (incl. the timeline body)
		query := `
		UPDATE archive SET
			tag = :tag,
			thumbnail_id = :thumbnail_id,
			body = :body
		WHERE id = :id`

		_, err = rep.DB().NamedExecContext(ctx, query, map[string]any{
			"id":           aid,
			"tag":          aInsert.Tag,
			"thumbnail_id": aInsert.ThumbnailId,
			"body":         bodyJSON,
		})
		if err != nil {
			return fmt.Errorf("failed to update archive: %w", err)
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
		rows := make([]map[string]any, 0, len(aInsert.Translations))
		for _, t := range aInsert.Translations {
			row := map[string]any{
				"archive_id":  aid,
				"language_id": t.LanguageId,
				"heading":     t.Heading,
			}
			rows = append(rows, row)
		}

		err = storeutil.BulkInsert(ctx, rep.DB(), "archive_translation", rows)
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

func (s *Store) GetArchivesPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveList, int, error) {
	if limit <= 0 {
		return nil, 0, errors.New("limit must be greater than 0")
	}
	if offset < 0 {
		return nil, 0, errors.New("offset must be >= 0")
	}

	// Query for total count
	countQuery := `SELECT COUNT(DISTINCT a.id) FROM archive a`
	total, err := storeutil.QueryCountNamed(ctx, s.DB, countQuery, map[string]any{})
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
	sqlStr, args, err := storeutil.MakeQuery(query, map[string]any{"limit": limit + 1, "offset": offset})
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryxContext(ctx, sqlStr, args...)
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
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows iteration error: %w", err)
	}

	// Fetch translations for each archive
	for i := range archives {
		translations, err := s.GetArchiveTranslations(ctx, archives[i].Id)
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

func (s *Store) DeleteArchiveById(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `DELETE FROM archive WHERE id = :id`
		res, err := rep.DB().NamedExecContext(ctx, query, map[string]interface{}{
			"id": id,
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive with ID %d: %w", id, err)
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

func (s *Store) GetArchiveById(ctx context.Context, id int) (*entity.ArchiveFull, error) {
	query := `
	SELECT
		a.id, a.tag, a.created_at, a.thumbnail_id, a.body,
		mt.id AS thumbnail_id, mt.full_size AS thumbnail_full_size, mt.full_size_width AS thumbnail_full_size_width, mt.full_size_height AS thumbnail_full_size_height, mt.thumbnail AS thumbnail_thumbnail, mt.thumbnail_width AS thumbnail_thumbnail_width, mt.thumbnail_height AS thumbnail_thumbnail_height, mt.compressed AS thumbnail_compressed, mt.compressed_width AS thumbnail_compressed_width, mt.compressed_height AS thumbnail_compressed_height, mt.blur_hash AS thumbnail_blur_hash
	FROM archive a
	LEFT JOIN media mt ON a.thumbnail_id = mt.id
	WHERE a.id = :id`

	sqlStr, args, err := storeutil.MakeQuery(query, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryxContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("rows iteration error: %w", err)
		}
		// Wrap sql.ErrNoRows so callers' errors.Is(err, sql.ErrNoRows) maps to 404.
		return nil, fmt.Errorf("archive %d not found: %w", id, sql.ErrNoRows)
	}

	var al entity.ArchiveList
	var thumbnail entity.MediaFull
	var body []byte
	err = rows.Scan(
		&al.Id, &al.Tag, &al.CreatedAt, &thumbnail.Id, &body,
		&thumbnail.Id, &thumbnail.MediaItem.FullSizeMediaURL, &thumbnail.MediaItem.FullSizeWidth, &thumbnail.MediaItem.FullSizeHeight, &thumbnail.MediaItem.ThumbnailMediaURL, &thumbnail.MediaItem.ThumbnailWidth, &thumbnail.MediaItem.ThumbnailHeight, &thumbnail.MediaItem.CompressedMediaURL, &thumbnail.MediaItem.CompressedWidth, &thumbnail.MediaItem.CompressedHeight, &thumbnail.MediaItem.BlurHash,
	)
	if err != nil {
		return nil, err
	}
	al.Thumbnail = thumbnail

	// Fetch translations for this archive
	translations, err := s.GetArchiveTranslations(ctx, id)
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

	// Decode the stored timeline body (typed blocks) and resolve it. A malformed
	// blob degrades to an empty timeline rather than failing the read: GetArchiveById
	// runs at boot via hero FEATURED_ARCHIVE resolution, so a single bad body must
	// not crash App.Start (mirrors the hero getHeroInsert boot-resilience rule).
	var storedItems []entity.ArchiveItemInsert
	if len(body) > 0 {
		if err := json.Unmarshal(body, &storedItems); err != nil {
			slog.ErrorContext(ctx, "failed to unmarshal archive body, serving empty timeline",
				slog.Int("archive_id", id), slog.String("err", err.Error()))
			storedItems = nil
		}
	}

	// Resolve on the active connection (tx-scoped when GetArchiveById is called
	// from within hero resolution, root otherwise) — never a nested transaction.
	items, err := resolveArchiveItems(ctx, s.repFunc(), storedItems)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve archive items for %d: %w", id, err)
	}

	return &entity.ArchiveFull{
		ArchiveList: al,
		Items:       items,
	}, nil
}

// resolveArchiveItems resolves stored timeline blocks (Insert form) into their
// read shape, fetching media/products as needed. A block whose reference is
// missing/hidden is skipped (logged) so a deleted product never breaks the whole
// archive; a genuine DB error, by contrast, is propagated so transient failures
// surface as an error instead of silently dropping content.
func resolveArchiveItems(ctx context.Context, rep dependency.Repository, items []entity.ArchiveItemInsert) ([]entity.ArchiveItemFull, error) {
	out := make([]entity.ArchiveItemFull, 0, len(items))
	for _, it := range items {
		full := entity.ArchiveItemFull{Type: it.Type}

		switch it.Type {
		case entity.ArchiveItemTypeMainMedia:
			if it.MainMedia == nil || it.MainMedia.MediaId == 0 {
				slog.WarnContext(ctx, "archive main_media block has no media id, skipping")
				continue
			}
			m, ok, err := resolveMediaById(ctx, rep, it.MainMedia.MediaId, "main_media")
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			full.MainMedia = &entity.ArchiveMainMediaFull{Media: *m, AspectRatio: it.MainMedia.AspectRatio}

		case entity.ArchiveItemTypeMediaLine:
			if it.MediaLine == nil || len(it.MediaLine.MediaIds) == 0 {
				slog.WarnContext(ctx, "archive media_line block has no media ids, skipping")
				continue
			}
			media, err := resolveMediaByIds(ctx, rep, it.MediaLine.MediaIds, "media_line")
			if err != nil {
				return nil, err
			}
			if len(media) == 0 {
				continue
			}
			full.MediaLine = &entity.ArchiveMediaLineFull{Media: media, AspectRatio: it.MediaLine.AspectRatio}

		case entity.ArchiveItemTypeText:
			if it.Text == nil || !hasArchiveText(it.Text.Translations) {
				slog.WarnContext(ctx, "archive text block has no copy, skipping")
				continue
			}
			full.Text = &entity.ArchiveTextFull{Translations: it.Text.Translations}

		case entity.ArchiveItemTypeEmbed:
			if it.Embed == nil || it.Embed.EmbedUrl == "" {
				slog.WarnContext(ctx, "archive embed block has no url, skipping")
				continue
			}
			full.Embed = &entity.ArchiveEmbedFull{EmbedUrl: it.Embed.EmbedUrl, Translations: it.Embed.Translations}

		case entity.ArchiveItemTypeMediaWithCaption:
			if it.MediaWithCaption == nil || it.MediaWithCaption.MediaId == 0 {
				slog.WarnContext(ctx, "archive media_with_caption block has no media id, skipping")
				continue
			}
			m, ok, err := resolveMediaById(ctx, rep, it.MediaWithCaption.MediaId, "media_with_caption")
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			full.MediaWithCaption = &entity.ArchiveMediaWithCaptionFull{
				Media:        *m,
				Link:         it.MediaWithCaption.Link,
				AspectRatio:  it.MediaWithCaption.AspectRatio,
				Translations: it.MediaWithCaption.Translations,
			}

		case entity.ArchiveItemTypeProduct:
			if it.Product == nil || it.Product.ProductId == 0 {
				slog.WarnContext(ctx, "archive product block has no product id, skipping")
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, []int{it.Product.ProductId})
			if err != nil {
				return nil, fmt.Errorf("failed to get archive product %d: %w", it.Product.ProductId, err)
			}
			prds := filterVisibleProducts(products)
			if len(prds) == 0 {
				slog.WarnContext(ctx, "archive product block references missing/hidden product, skipping", slog.Int("product_id", it.Product.ProductId))
				continue
			}
			p := prds[0]
			full.Product = &entity.ArchiveProductFull{Product: &p, Translations: it.Product.Translations}

		case entity.ArchiveItemTypeProductsTag:
			if it.ProductsTag == nil || it.ProductsTag.Tag == "" {
				slog.WarnContext(ctx, "archive products-by-tag block has no tag, skipping")
				continue
			}
			products, err := rep.Products().GetProductsByTag(ctx, it.ProductsTag.Tag)
			if err != nil {
				return nil, fmt.Errorf("failed to get archive products by tag %q: %w", it.ProductsTag.Tag, err)
			}
			prds := filterVisibleProducts(products)
			if it.ProductsTag.Limit > 0 && len(prds) > it.ProductsTag.Limit {
				prds = prds[:it.ProductsTag.Limit]
			}
			if len(prds) == 0 {
				continue
			}
			full.ProductsTag = &entity.ArchiveProductsTagFull{Tag: it.ProductsTag.Tag, Products: prds, Translations: it.ProductsTag.Translations}

		case entity.ArchiveItemTypeProductsManual:
			if it.ProductsManual == nil || len(it.ProductsManual.ProductIds) == 0 {
				slog.WarnContext(ctx, "archive manual-products block has no product ids, skipping")
				continue
			}
			products, err := rep.Products().GetProductsByIds(ctx, it.ProductsManual.ProductIds)
			if err != nil {
				return nil, fmt.Errorf("failed to get archive manual products: %w", err)
			}
			// Preserve the hand-picked order (GetProductsByIds returns DB order).
			prds := orderProductsByIds(filterVisibleProducts(products), it.ProductsManual.ProductIds)
			if len(prds) == 0 {
				continue
			}
			full.ProductsManual = &entity.ArchiveProductsManualFull{Products: prds, Translations: it.ProductsManual.Translations}

		default:
			slog.ErrorContext(ctx, "unknown archive block type, skipping", slog.Int("type", int(it.Type)))
			continue
		}

		out = append(out, full)
	}
	return out, nil
}

// resolveMediaById fetches a single media by id. It returns ok=false (and no
// error) when the media row is gone, so callers skip just that reference; a
// genuine DB failure is returned as an error.
func resolveMediaById(ctx context.Context, rep dependency.Repository, id int, block string) (*entity.MediaFull, bool, error) {
	m, err := rep.Media().GetMediaById(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.WarnContext(ctx, "archive "+block+" references missing media, skipping", slog.Int("media_id", id))
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to get archive %s media %d: %w", block, id, err)
	}
	return m, true, nil
}

// resolveMediaByIds fetches media by id in order, skipping (with a warning) any
// id that is zero or no longer resolves to a row, and propagating genuine DB errors.
func resolveMediaByIds(ctx context.Context, rep dependency.Repository, ids []int, block string) ([]entity.MediaFull, error) {
	media := make([]entity.MediaFull, 0, len(ids))
	for _, mid := range ids {
		if mid == 0 {
			continue
		}
		m, ok, err := resolveMediaById(ctx, rep, mid, block)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		media = append(media, *m)
	}
	return media, nil
}

// hasArchiveText reports whether any translation carries non-blank body copy, so
// a TEXT block with no actual text is dropped rather than rendered empty.
func hasArchiveText(ts []entity.ArchiveItemTranslation) bool {
	for _, t := range ts {
		if strings.TrimSpace(t.Text) != "" {
			return true
		}
	}
	return false
}

// orderProductsByIds returns products ordered to match ids, dropping any id with
// no corresponding (visible) product.
func orderProductsByIds(products []entity.Product, ids []int) []entity.Product {
	byId := make(map[int]entity.Product, len(products))
	for _, p := range products {
		byId[p.Id] = p
	}
	ordered := make([]entity.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := byId[id]; ok {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

func (s *Store) GetArchiveTranslations(ctx context.Context, id int) ([]entity.ArchiveTranslation, error) {
	query := `
	SELECT
		at.language_id, at.heading
	FROM archive_translation at
	WHERE at.archive_id = :id`
	translations, err := storeutil.QueryListNamed[entity.ArchiveTranslation](ctx, s.DB, query, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return translations, nil
}
