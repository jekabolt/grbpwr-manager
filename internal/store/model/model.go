// Package model implements fit/fashion model profile management.
package model

import (
	"context"
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
			INSERT INTO model (name, comment, gender, default_sample_size_id)
			VALUES (:name, :comment, :gender, :defaultSampleSizeId)`,
			map[string]any{
				"name":                m.Name,
				"comment":             m.Comment,
				"gender":              m.Gender,
				"defaultSampleSizeId": m.DefaultSampleSizeId,
			})
		if err != nil {
			return fmt.Errorf("failed to insert model: %w", err)
		}
		return insertModelMeasurements(ctx, rep.DB(), id, m.Measurements)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add model: %w", err)
	}
	return id, nil
}

// UpdateModel updates a model profile and replaces its measurements.
func (s *Store) UpdateModel(ctx context.Context, id int, m *entity.ModelInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE model SET
				name = :name,
				comment = :comment,
				gender = :gender,
				default_sample_size_id = :defaultSampleSizeId
			WHERE id = :id`,
			map[string]any{
				"id":                  id,
				"name":                m.Name,
				"comment":             m.Comment,
				"gender":              m.Gender,
				"defaultSampleSizeId": m.DefaultSampleSizeId,
			}); err != nil {
			return fmt.Errorf("failed to update model: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM model_measurement WHERE model_id = :id`,
			map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear model measurements: %w", err)
		}
		return insertModelMeasurements(ctx, rep.DB(), id, m.Measurements)
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
	byModel, err := s.measurementsByModelIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	m.Measurements = byModel[id]
	return &m, nil
}

// ListModels returns a paged list of model profiles with their measurements,
// plus the total number of models (ignoring pagination).
func (s *Store) ListModels(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.Model, int, error) {
	limit, offset = clampPagination(limit, offset)

	total, err := storeutil.QueryCountNamed(ctx, s.DB, `SELECT COUNT(*) FROM model`, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count models: %w", err)
	}

	models, err := storeutil.QueryListNamed[entity.Model](ctx, s.DB, fmt.Sprintf(`
		SELECT * FROM model
		ORDER BY id %s
		LIMIT :limit OFFSET :offset`, orderFactor.String()),
		map[string]any{"limit": limit, "offset": offset})
	if err != nil {
		return nil, 0, fmt.Errorf("can't list models: %w", err)
	}
	if len(models) == 0 {
		return models, total, nil
	}
	ids := make([]int, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.Id)
	}
	byModel, err := s.measurementsByModelIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range models {
		models[i].Measurements = byModel[models[i].Id]
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
