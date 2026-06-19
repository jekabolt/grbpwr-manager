// Package model implements fit/fashion model profile management.
package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

// Store implements dependency.Models.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new model store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddModel inserts a model profile and its measurements, returning the new id.
func (s *Store) AddModel(ctx context.Context, m *entity.ModelInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO model (name, comment, gender, thumbnail_id)
			VALUES (:name, :comment, :gender, :thumbnailId)`,
			map[string]any{
				"name":        m.Name,
				"comment":     m.Comment,
				"gender":      m.Gender,
				"thumbnailId": m.ThumbnailId,
			})
		if err != nil {
			return fmt.Errorf("failed to insert model: %w", err)
		}
		if err := insertModelMeasurements(ctx, rep.DB(), id, m.Measurements); err != nil {
			return err
		}
		if err := insertModelMedia(ctx, rep.DB(), id, m.MediaIds); err != nil {
			return err
		}
		return insertModelDefaultSizes(ctx, rep.DB(), id, m.DefaultSizeIds)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add model: %w", err)
	}
	return id, nil
}

// UpdateModel updates a model profile and replaces its measurements. Returns
// sql.ErrNoRows when no model with the given id exists.
func (s *Store) UpdateModel(ctx context.Context, id int, m *entity.ModelInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Existence check up front: a bare UPDATE reports 0 rows affected both
		// for a missing id and for a no-op update, so we can't rely on it.
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM model WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check model existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE model SET
				name = :name,
				comment = :comment,
				gender = :gender,
				thumbnail_id = :thumbnailId
			WHERE id = :id`,
			map[string]any{
				"id":          id,
				"name":        m.Name,
				"comment":     m.Comment,
				"gender":      m.Gender,
				"thumbnailId": m.ThumbnailId,
			}); err != nil {
			return fmt.Errorf("failed to update model: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM model_measurement WHERE model_id = :id`,
			map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear model measurements: %w", err)
		}
		if err := insertModelMeasurements(ctx, rep.DB(), id, m.Measurements); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM model_media WHERE model_id = :id`,
			map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear model media: %w", err)
		}
		if err := insertModelMedia(ctx, rep.DB(), id, m.MediaIds); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM model_default_size WHERE model_id = :id`,
			map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear model default sizes: %w", err)
		}
		return insertModelDefaultSizes(ctx, rep.DB(), id, m.DefaultSizeIds)
	})
	if err != nil {
		return fmt.Errorf("can't update model: %w", err)
	}
	return nil
}

// DeleteModel deletes a model profile by id (measurements cascade).
func (s *Store) DeleteModel(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM model WHERE id = :id`,
		map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}
	return nil
}

// GetModelById returns a model profile with its measurements.
func (s *Store) GetModelById(ctx context.Context, id int) (*entity.Model, error) {
	m, err := storeutil.QueryNamedOne[entity.Model](ctx, s.DB,
		`SELECT * FROM model WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get model: %w", err)
	}
	models := []entity.Model{m}
	if err := s.enrich(ctx, models); err != nil {
		return nil, err
	}
	return &models[0], nil
}

// ListModels returns a paged list of model profiles with their measurements,
// optionally filtered by gender (empty = no filter) and a case-insensitive
// substring match on name (empty = no filter), plus the total number of
// matching models (ignoring pagination).
func (s *Store) ListModels(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, gender, nameSearch string) ([]entity.Model, int, error) {
	limit, offset = clampPagination(limit, offset)

	// Shared filter for both the count and the page query.
	filterParams := map[string]any{}
	where := ""
	if gender != "" {
		where += " AND gender = :gender"
		filterParams["gender"] = gender
	}
	if nameSearch != "" {
		where += " AND name LIKE :nameSearch"
		filterParams["nameSearch"] = "%" + escapeLike(nameSearch) + "%"
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM model WHERE 1=1%s`, where), filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count models: %w", err)
	}

	filterParams["limit"] = limit
	filterParams["offset"] = offset
	models, err := storeutil.QueryListNamed[entity.Model](ctx, s.DB, fmt.Sprintf(`
		SELECT * FROM model
		WHERE 1=1%s
		ORDER BY id %s
		LIMIT :limit OFFSET :offset`, where, orderFactor.String()),
		filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list models: %w", err)
	}
	if err := s.enrich(ctx, models); err != nil {
		return nil, 0, err
	}
	return models, total, nil
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

func insertModelMeasurements(ctx context.Context, db dependency.DB, modelID int, ms []entity.ModelMeasurement) error {
	if len(ms) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		rows = append(rows, map[string]any{
			"model_id":             modelID,
			"measurement_name":     string(m.Name),
			"measurement_value_mm": m.ValueMM,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "model_measurement", rows); err != nil {
		return fmt.Errorf("failed to insert model measurements: %w", err)
	}
	return nil
}

type modelMeasurementRow struct {
	ModelID int `db:"model_id"`
	entity.ModelMeasurement
}

func (s *Store) measurementsByModelIds(ctx context.Context, ids []int) (map[int][]entity.ModelMeasurement, error) {
	if len(ids) == 0 {
		return map[int][]entity.ModelMeasurement{}, nil
	}
	rows, err := storeutil.QueryListNamed[modelMeasurementRow](ctx, s.DB, `
		SELECT model_id, measurement_name, measurement_value_mm
		FROM model_measurement
		WHERE model_id IN (:ids)
		ORDER BY id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load model measurements: %w", err)
	}
	out := make(map[int][]entity.ModelMeasurement, len(ids))
	for _, r := range rows {
		out[r.ModelID] = append(out[r.ModelID], r.ModelMeasurement)
	}
	return out, nil
}

func insertModelMedia(ctx context.Context, db dependency.DB, modelID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for i, mid := range mediaIDs {
		rows = append(rows, map[string]any{
			"model_id":      modelID,
			"media_id":      mid,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "model_media", rows); err != nil {
		return fmt.Errorf("failed to insert model media: %w", err)
	}
	return nil
}

// enrich loads and attaches measurements, the photo gallery, and the resolved
// thumbnail for each model in the slice (mutates in place).
func (s *Store) enrich(ctx context.Context, models []entity.Model) error {
	if len(models) == 0 {
		return nil
	}
	ids := make([]int, 0, len(models))
	thumbIDs := make([]int, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.Id)
		if m.ThumbnailId.Valid {
			thumbIDs = append(thumbIDs, int(m.ThumbnailId.Int32))
		}
	}

	measurements, err := s.measurementsByModelIds(ctx, ids)
	if err != nil {
		return err
	}
	gallery, err := s.mediaByModelIds(ctx, ids)
	if err != nil {
		return err
	}
	thumbs, err := s.mediaByIds(ctx, thumbIDs)
	if err != nil {
		return err
	}
	defaultSizes, err := s.defaultSizesByModelIds(ctx, ids)
	if err != nil {
		return err
	}

	for i := range models {
		models[i].Measurements = measurements[models[i].Id]
		models[i].Media = gallery[models[i].Id]
		models[i].DefaultSizeIds = defaultSizes[models[i].Id]
		if models[i].ThumbnailId.Valid {
			if mf, ok := thumbs[int(models[i].ThumbnailId.Int32)]; ok {
				t := mf
				models[i].Thumbnail = &t
			}
		}
	}
	return nil
}

func insertModelDefaultSizes(ctx context.Context, db dependency.DB, modelID int, sizeIDs []int) error {
	if len(sizeIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(sizeIDs))
	for _, sid := range sizeIDs {
		rows = append(rows, map[string]any{
			"model_id": modelID,
			"size_id":  sid,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "model_default_size", rows); err != nil {
		return fmt.Errorf("failed to insert model default sizes: %w", err)
	}
	return nil
}

type modelDefaultSizeRow struct {
	ModelID int `db:"model_id"`
	SizeID  int `db:"size_id"`
}

func (s *Store) defaultSizesByModelIds(ctx context.Context, ids []int) (map[int][]int, error) {
	if len(ids) == 0 {
		return map[int][]int{}, nil
	}
	rows, err := storeutil.QueryListNamed[modelDefaultSizeRow](ctx, s.DB, `
		SELECT model_id, size_id
		FROM model_default_size
		WHERE model_id IN (:ids)
		ORDER BY id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load model default sizes: %w", err)
	}
	out := make(map[int][]int, len(ids))
	for _, r := range rows {
		out[r.ModelID] = append(out[r.ModelID], r.SizeID)
	}
	return out, nil
}

// escapeLike escapes LIKE wildcards in a user-supplied search term so they are
// matched literally (the surrounding %…% is added by the caller).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

type modelMediaRow struct {
	ModelID int `db:"model_id"`
	entity.MediaFull
}

func (s *Store) mediaByModelIds(ctx context.Context, ids []int) (map[int][]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[modelMediaRow](ctx, s.DB, `
		SELECT mm.model_id, m.*
		FROM model_media mm
		JOIN media m ON m.id = mm.media_id
		WHERE mm.model_id IN (:ids)
		ORDER BY mm.model_id, mm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load model media: %w", err)
	}
	out := make(map[int][]entity.MediaFull, len(ids))
	for _, r := range rows {
		out[r.ModelID] = append(out[r.ModelID], r.MediaFull)
	}
	return out, nil
}

// mediaByIds loads media rows by id (used to resolve thumbnails).
func (s *Store) mediaByIds(ctx context.Context, ids []int) (map[int]entity.MediaFull, error) {
	out := make(map[int]entity.MediaFull, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB,
		`SELECT * FROM media WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load media: %w", err)
	}
	for i := range rows {
		out[rows[i].Id] = rows[i]
	}
	return out, nil
}
