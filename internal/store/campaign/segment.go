package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type segmentRow struct {
	ID          int           `db:"id"`
	Name        string        `db:"name"`
	Description string        `db:"description"`
	Predicate   []byte        `db:"predicate"`
	LastCount   sql.NullInt64 `db:"last_count"`
	LastCountAt sql.NullTime  `db:"last_count_at"`
	CreatedBy   string        `db:"created_by"`
	CreatedAt   sql.NullTime  `db:"created_at"`
	UpdatedAt   sql.NullTime  `db:"updated_at"`
}

func segmentRowToEntity(row segmentRow) (*entity.EmailSegment, error) {
	var predicate entity.SegmentPredicate
	if err := json.Unmarshal(row.Predicate, &predicate); err != nil {
		return nil, fmt.Errorf("unmarshal email segment %d predicate: %w", row.ID, err)
	}
	out := &entity.EmailSegment{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Predicate:   predicate,
		CreatedBy:   row.CreatedBy,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
	if row.LastCount.Valid {
		value := int(row.LastCount.Int64)
		out.LastCount = &value
	}
	if row.LastCountAt.Valid {
		value := row.LastCountAt.Time
		out.LastCountAt = &value
	}
	return out, nil
}

// UpsertEmailSegment inserts or updates a saved segment definition. id=0
// creates a new segment.
func (s *Store) UpsertEmailSegment(ctx context.Context, id int, segment *entity.EmailSegment) (int, error) {
	if segment == nil {
		return 0, errors.New("segment is required")
	}
	predicate, err := json.Marshal(segment.Predicate)
	if err != nil {
		return 0, fmt.Errorf("marshal email segment predicate: %w", err)
	}
	var lastCount any
	if segment.LastCount != nil {
		lastCount = *segment.LastCount
	}
	params := map[string]any{
		"name":          segment.Name,
		"description":   segment.Description,
		"predicate":     predicate,
		"last_count":    lastCount,
		"last_count_at": segment.LastCountAt,
		"created_by":    segment.CreatedBy,
	}
	if id == 0 {
		newID, err := storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO email_segment
				(name, description, predicate, last_count, last_count_at, created_by)
			VALUES
				(:name, :description, :predicate, :last_count, :last_count_at, :created_by)`, params)
		if err != nil {
			return 0, fmt.Errorf("insert email segment: %w", err)
		}
		return newID, nil
	}
	count, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM email_segment WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return 0, fmt.Errorf("check email segment exists: %w", err)
	}
	if count == 0 {
		return 0, fmt.Errorf("email segment %d not found: %w", id, sql.ErrNoRows)
	}
	params["id"] = id
	if _, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_segment SET
			name = :name,
			description = :description,
			predicate = :predicate,
			last_count = :last_count,
			last_count_at = :last_count_at
		WHERE id = :id`, params); err != nil {
		return 0, fmt.Errorf("update email segment %d: %w", id, err)
	}
	return id, nil
}

func (s *Store) GetEmailSegmentByID(ctx context.Context, id int) (*entity.EmailSegment, error) {
	row, err := storeutil.QueryNamedOne[segmentRow](ctx, s.DB, `
		SELECT id, name, description, predicate, last_count, last_count_at,
		       created_by, created_at, updated_at
		FROM email_segment
		WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("get email segment %d: %w", id, err)
	}
	return segmentRowToEntity(row)
}

func (s *Store) ListEmailSegments(ctx context.Context) ([]entity.EmailSegment, error) {
	rows, err := storeutil.QueryListNamed[segmentRow](ctx, s.DB, `
		SELECT id, name, description, predicate, last_count, last_count_at,
		       created_by, created_at, updated_at
		FROM email_segment
		ORDER BY name, id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list email segments: %w", err)
	}
	out := make([]entity.EmailSegment, 0, len(rows))
	for _, row := range rows {
		segment, err := segmentRowToEntity(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *segment)
	}
	return out, nil
}

func (s *Store) DeleteEmailSegment(ctx context.Context, id int) error {
	result, err := s.DB.NamedExecContext(ctx,
		`DELETE FROM email_segment WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("delete email segment %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("email segment %d rows affected: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("email segment %d not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

var _ dependency.Campaigns = (*Store)(nil)
