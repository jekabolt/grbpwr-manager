// Package fitting implements garment try-on (fitting) session management.
package fitting

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Pagination bounds for list endpoints: an unset/0 limit falls back to the
// default page size, and any limit is capped to avoid unbounded scans.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Fittings.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new fitting store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddFitting inserts a fitting with its sizes and media, returning the new id.
func (s *Store) AddFitting(ctx context.Context, f *entity.FittingInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO fitting (tech_card_id, product_id, model_id, fitting_date, comment, status, verdict, recorded_by)
			VALUES (:techCardId, :productId, :modelId, :fittingDate, :comment, :status, :verdict, :recordedBy)`,
			fittingParams(f))
		if err != nil {
			return fmt.Errorf("failed to insert fitting: %w", err)
		}
		if err := insertFittingSizes(ctx, rep.DB(), id, f.Sizes); err != nil {
			return err
		}
		if err := insertFittingMedia(ctx, rep.DB(), id, f.MediaIds); err != nil {
			return err
		}
		return insertFittingPatterns(ctx, rep.DB(), id, f.Patterns)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add fitting: %w", err)
	}
	return id, nil
}

// UpdateFitting updates a fitting and replaces its sizes and media. Returns
// sql.ErrNoRows when no fitting with the given id exists.
func (s *Store) UpdateFitting(ctx context.Context, id int, f *entity.FittingInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Existence check up front: a bare UPDATE reports 0 rows affected both
		// for a missing id and for a no-op update, so we can't rely on it.
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM fitting WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check fitting existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		params := fittingParams(f)
		params["id"] = id
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE fitting SET
				tech_card_id = :techCardId,
				product_id = :productId,
				model_id = :modelId,
				fitting_date = :fittingDate,
				comment = :comment,
				status = :status,
				verdict = :verdict,
				recorded_by = :recordedBy
			WHERE id = :id`, params); err != nil {
			return fmt.Errorf("failed to update fitting: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_size WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting sizes: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_media WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting media: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_pattern WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting patterns: %w", err)
		}
		if err := insertFittingSizes(ctx, rep.DB(), id, f.Sizes); err != nil {
			return err
		}
		if err := insertFittingMedia(ctx, rep.DB(), id, f.MediaIds); err != nil {
			return err
		}
		return insertFittingPatterns(ctx, rep.DB(), id, f.Patterns)
	})
	if err != nil {
		return fmt.Errorf("can't update fitting: %w", err)
	}
	return nil
}

// DeleteFitting deletes a fitting by id (sizes and media cascade).
func (s *Store) DeleteFitting(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM fitting WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete fitting: %w", err)
	}
	return nil
}

// GetFittingById returns a fitting with its sizes and resolved media.
func (s *Store) GetFittingById(ctx context.Context, id int) (*entity.Fitting, error) {
	f, err := storeutil.QueryNamedOne[entity.Fitting](ctx, s.DB,
		`SELECT * FROM fitting WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get fitting: %w", err)
	}
	sizes, err := s.sizesByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	media, err := s.mediaByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	patterns, err := s.patternsByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	f.Sizes = sizes[id]
	f.Media = media[id]
	f.Patterns = patterns[id]
	return &f, nil
}

// ListFittings returns a paged list of fittings, optionally filtered by tech card,
// product and/or model (pass 0 to ignore a filter), with sizes and resolved media,
// plus the total number of matching fittings (ignoring pagination).
func (s *Store) ListFittings(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, productID, modelID, techCardID int) ([]entity.Fitting, int, error) {
	limit, offset = clampPagination(limit, offset)

	// Shared filter for both the count and the page query.
	filterParams := map[string]any{}
	where := ""
	if techCardID != 0 {
		where += " AND tech_card_id = :techCardId"
		filterParams["techCardId"] = techCardID
	}
	if productID != 0 {
		where += " AND product_id = :productId"
		filterParams["productId"] = productID
	}
	if modelID != 0 {
		where += " AND model_id = :modelId"
		filterParams["modelId"] = modelID
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM fitting WHERE 1=1%s`, where), filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count fittings: %w", err)
	}

	// Reuse the same param map for the page query (filter + pagination).
	filterParams["limit"] = limit
	filterParams["offset"] = offset
	query := fmt.Sprintf(`
		SELECT * FROM fitting
		WHERE 1=1%s
		ORDER BY id %s
		LIMIT :limit OFFSET :offset`, where, orderFactor.String())

	fittings, err := storeutil.QueryListNamed[entity.Fitting](ctx, s.DB, query, filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list fittings: %w", err)
	}
	if len(fittings) == 0 {
		return fittings, total, nil
	}
	ids := make([]int, 0, len(fittings))
	for _, f := range fittings {
		ids = append(ids, f.Id)
	}
	sizes, err := s.sizesByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	media, err := s.mediaByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	patterns, err := s.patternsByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range fittings {
		fittings[i].Sizes = sizes[fittings[i].Id]
		fittings[i].Media = media[fittings[i].Id]
		fittings[i].Patterns = patterns[fittings[i].Id]
	}
	return fittings, total, nil
}

// clampPagination normalizes a client-supplied limit/offset: a non-positive
// limit becomes the default page size, the limit is capped, and a negative
// offset becomes zero.
func clampPagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func fittingParams(f *entity.FittingInsert) map[string]any {
	return map[string]any{
		"techCardId":  f.TechCardId,
		"productId":   f.ProductId,
		"modelId":     f.ModelId,
		"fittingDate": f.FittingDate,
		"comment":     f.Comment,
		"status":      string(f.Status),
		"verdict":     string(f.Verdict),
		"recordedBy":  f.RecordedBy,
	}
}

func insertFittingSizes(ctx context.Context, db dependency.DB, fittingID int, sizes []entity.FittingSize) error {
	if len(sizes) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(sizes))
	for _, sz := range sizes {
		rows = append(rows, map[string]any{
			"fitting_id": fittingID,
			"size_id":    sz.SizeId,
			"fit_note":   sz.FitNote,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_size", rows); err != nil {
		return fmt.Errorf("failed to insert fitting sizes: %w", err)
	}
	return nil
}

func insertFittingPatterns(ctx context.Context, db dependency.DB, fittingID int, patterns []entity.FittingPattern) error {
	if len(patterns) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(patterns))
	for i, p := range patterns {
		rows = append(rows, map[string]any{
			"fitting_id":    fittingID,
			"size_id":       p.SizeId,
			"url":           p.URL,
			"filename":      p.Filename,
			"size_bytes":    p.SizeBytes,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_pattern", rows); err != nil {
		return fmt.Errorf("failed to insert fitting patterns: %w", err)
	}
	return nil
}

func insertFittingMedia(ctx context.Context, db dependency.DB, fittingID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for i, mid := range mediaIDs {
		rows = append(rows, map[string]any{
			"fitting_id":    fittingID,
			"media_id":      mid,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_media", rows); err != nil {
		return fmt.Errorf("failed to insert fitting media: %w", err)
	}
	return nil
}

type fittingSizeRow struct {
	FittingID int `db:"fitting_id"`
	entity.FittingSize
}

func (s *Store) sizesByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingSize, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingSize{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingSizeRow](ctx, s.DB, `
		SELECT fitting_id, size_id, fit_note
		FROM fitting_size
		WHERE fitting_id IN (:ids)
		ORDER BY id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting sizes: %w", err)
	}
	out := make(map[int][]entity.FittingSize, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.FittingSize)
	}
	return out, nil
}

type fittingMediaRow struct {
	FittingID int `db:"fitting_id"`
	entity.MediaFull
}

type fittingPatternRow struct {
	FittingID int `db:"fitting_id"`
	entity.FittingPattern
}

func (s *Store) patternsByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingPattern, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingPattern{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingPatternRow](ctx, s.DB, `
		SELECT fitting_id, size_id, url, filename, size_bytes
		FROM fitting_pattern
		WHERE fitting_id IN (:ids)
		ORDER BY fitting_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting patterns: %w", err)
	}
	out := make(map[int][]entity.FittingPattern, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.FittingPattern)
	}
	return out, nil
}

func (s *Store) mediaByFittingIds(ctx context.Context, ids []int) (map[int][]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingMediaRow](ctx, s.DB, `
		SELECT fm.fitting_id, m.*
		FROM fitting_media fm
		JOIN media m ON m.id = fm.media_id
		WHERE fm.fitting_id IN (:ids)
		ORDER BY fm.fitting_id, fm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting media: %w", err)
	}
	out := make(map[int][]entity.MediaFull, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.MediaFull)
	}
	return out, nil
}
