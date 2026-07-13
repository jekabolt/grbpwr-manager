// Package sample implements sample (сэмпл/образец) management (new-flow NF-04): a sewn prototype of
// a style with its own size, purpose and status. A sample's cost is composed on read from the
// material issues tied to it (NF-01) plus the manual dev-expense journal — nothing is materialised.
package sample

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc runs f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Samples.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new sample store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

func sampleParams(sm *entity.SampleInsert) map[string]any {
	return map[string]any{
		"tech_card_id":  sm.TechCardId,
		"purpose":       sm.Purpose,
		"size_id":       sm.SizeId,
		"colorway_id":   sm.ColorwayId,
		"status":        sm.Status,
		"fabric_source": sm.FabricSource,
		"notes":         sm.Notes,
		"started_at":    sm.StartedAt,
		"finished_at":   sm.FinishedAt,
	}
}

// AddSample inserts a sample, assigning the next per-card number (MAX+1) in a transaction.
func (s *Store) AddSample(ctx context.Context, sm *entity.SampleInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.QueryNamedOne[struct {
			Next int `db:"next"`
		}](ctx, rep.DB(), `SELECT COALESCE(MAX(number), 0) + 1 AS next FROM sample WHERE tech_card_id = :tc`,
			map[string]any{"tc": sm.TechCardId})
		if err != nil {
			return fmt.Errorf("next sample number: %w", err)
		}
		params := sampleParams(sm)
		params["number"] = n.Next
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO sample (tech_card_id, number, purpose, size_id, colorway_id, status, fabric_source, notes, started_at, finished_at)
			VALUES (:tech_card_id, :number, :purpose, :size_id, :colorway_id, :status, :fabric_source, :notes, :started_at, :finished_at)`,
			params)
		if err != nil {
			return fmt.Errorf("insert sample: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("can't add sample: %w", err)
	}
	return id, nil
}

// UpdateSample updates a sample's editable fields (not its number or tech card). Returns
// sql.ErrNoRows when the sample does not exist.
func (s *Store) UpdateSample(ctx context.Context, id int, sm *entity.SampleInsert) error {
	params := sampleParams(sm)
	params["id"] = id
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE sample SET purpose=:purpose, size_id=:size_id, colorway_id=:colorway_id,
			status=:status, fabric_source=:fabric_source, notes=:notes, started_at=:started_at, finished_at=:finished_at
		WHERE id=:id`, params)
	if err != nil {
		return fmt.Errorf("can't update sample %d: %w", id, err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteSample deletes a sample, refusing when it has material stock movements (applied facts).
// Returns ErrSampleHasMovements in that case and sql.ErrNoRows when the sample does not exist.
func (s *Store) DeleteSample(ctx context.Context, id int) error {
	n, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM material_stock_movement WHERE sample_id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("check sample movements: %w", err)
	}
	if n > 0 {
		return entity.ErrSampleHasMovements
	}
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, `DELETE FROM sample WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("can't delete sample %d: %w", id, err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetSampleById returns a sample with its composed cost block, or sql.ErrNoRows when none exists.
func (s *Store) GetSampleById(ctx context.Context, id int) (*entity.Sample, error) {
	sm, err := storeutil.QueryNamedOne[entity.Sample](ctx, s.DB,
		`SELECT id, tech_card_id, number, purpose, size_id, colorway_id, status, fabric_source, notes, started_at, finished_at, created_at, updated_at
		 FROM sample WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("can't get sample %d: %w", id, err)
	}
	cost, err := s.sampleCost(ctx, id)
	if err != nil {
		return nil, err
	}
	sm.Cost = cost
	return &sm, nil
}

// ListSamples returns a tech card's samples (newest number first), with the total count. Cost is
// nil on list rows.
func (s *Store) ListSamples(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, techCardID int) ([]entity.Sample, int, error) {
	limit, offset = clampPagination(limit, offset)
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM sample WHERE tech_card_id = :tc`, map[string]any{"tc": techCardID})
	if err != nil {
		return nil, 0, fmt.Errorf("count samples: %w", err)
	}
	rows, err := storeutil.QueryListNamed[entity.Sample](ctx, s.DB, `
		SELECT id, tech_card_id, number, purpose, size_id, colorway_id, status, fabric_source, notes, started_at, finished_at, created_at, updated_at
		FROM sample WHERE tech_card_id = :tc
		ORDER BY number DESC LIMIT :limit OFFSET :offset`,
		map[string]any{"tc": techCardID, "limit": limit, "offset": offset})
	if err != nil {
		return nil, 0, fmt.Errorf("list samples: %w", err)
	}
	return rows, total, nil
}

// sampleCost composes a sample's cost (base currency): net material issues (issue_sample −
// return_sample) valued at the frozen average, plus the manual dev-expense rows tied to the sample.
func (s *Store) sampleCost(ctx context.Context, sampleID int) (*entity.SampleCost, error) {
	out := &entity.SampleCost{}

	type mv struct {
		Type     string              `db:"movement_type"`
		Quantity decimal.Decimal     `db:"quantity"`
		Base     decimal.NullDecimal `db:"unit_cost_base"`
	}
	movements, err := storeutil.QueryListNamed[mv](ctx, s.DB,
		`SELECT movement_type, quantity, unit_cost_base FROM material_stock_movement WHERE sample_id = :id`,
		map[string]any{"id": sampleID})
	if err != nil {
		return nil, fmt.Errorf("sample material cost: %w", err)
	}
	for _, m := range movements {
		if !m.Base.Valid {
			out.HasUncosted = true
			continue
		}
		line := m.Quantity.Mul(m.Base.Decimal)
		switch entity.MaterialMovementType(m.Type) {
		case entity.MaterialMovementIssueSample:
			out.MaterialsBase = out.MaterialsBase.Add(line)
		case entity.MaterialMovementReturnSample:
			out.MaterialsBase = out.MaterialsBase.Sub(line)
		}
	}

	type dev struct {
		Base decimal.NullDecimal `db:"amount_base"`
	}
	expenses, err := storeutil.QueryListNamed[dev](ctx, s.DB,
		`SELECT amount_base FROM tech_card_dev_expense WHERE sample_id = :id`, map[string]any{"id": sampleID})
	if err != nil {
		return nil, fmt.Errorf("sample dev cost: %w", err)
	}
	for _, e := range expenses {
		if !e.Base.Valid {
			out.HasUncosted = true
			continue
		}
		out.ManualBase = out.ManualBase.Add(e.Base.Decimal)
	}

	out.MaterialsBase = out.MaterialsBase.Round(2)
	out.ManualBase = out.ManualBase.Round(2)
	out.TotalBase = out.MaterialsBase.Add(out.ManualBase).Round(2)
	return out, nil
}

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
