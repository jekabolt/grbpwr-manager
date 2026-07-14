package techcard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SaveTechCardRelease persists one immutable release snapshot (task 11). The apisrv layer builds
// the proto-JSON blob and the base-currency unit_cost (the store stays proto/dto-free); Id and
// CreatedAt are DB-generated. A released card is frozen, so its content cannot change while
// released — a snapshot taken any time during a released episode is identical, which is what makes
// writing it just after the release transition (rather than inside the same transaction) safe.
func (s *Store) SaveTechCardRelease(ctx context.Context, rel entity.TechCardRelease) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO tech_card_release (tech_card_id, version, released_by, snapshot, unit_cost, currency)
		VALUES (:tech_card_id, :version, :released_by, :snapshot, :unit_cost, :currency)`,
		map[string]any{
			"tech_card_id": rel.TechCardId,
			"version":      rel.Version,
			"released_by":  rel.ReleasedBy,
			"snapshot":     rel.Snapshot,
			"unit_cost":    rel.UnitCost,
			"currency":     rel.Currency,
		}); err != nil {
		return fmt.Errorf("failed to save tech card release: %w", err)
	}
	return nil
}

// ListTechCardReleases returns a card's release history newest-first, metadata only (no blob).
func (s *Store) ListTechCardReleases(ctx context.Context, techCardID int) ([]entity.TechCardReleaseMeta, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardReleaseMeta](ctx, s.DB, `
		SELECT id, tech_card_id, version, released_by, unit_cost, currency, created_at
		FROM tech_card_release
		WHERE tech_card_id = :tech_card_id
		ORDER BY created_at DESC`,
		map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("can't list tech card releases: %w", err)
	}
	return rows, nil
}

// GetTechCardRelease returns a full release snapshot (metadata + raw proto-JSON blob) by id,
// or sql.ErrNoRows when none exists. The blob is opaque here; the caller parses and degrades.
func (s *Store) GetTechCardRelease(ctx context.Context, id int) (*entity.TechCardRelease, error) {
	rel, err := storeutil.QueryNamedOne[entity.TechCardRelease](ctx, s.DB, `
		SELECT id, tech_card_id, version, released_by, snapshot, unit_cost, currency, created_at
		FROM tech_card_release WHERE id = :id`,
		map[string]any{"id": id})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("can't get tech card release: %w", err)
	}
	return &rel, nil
}
