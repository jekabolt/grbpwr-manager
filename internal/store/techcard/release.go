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
	// release_number is the user-facing "Rev.N" (Q1): auto MAX+1 per card. The derived table `m`
	// materialises MAX(release_number) before the INSERT, so the whole thing is one atomic statement
	// (MySQL forbids selecting the INSERT target directly, but a derived subquery is allowed). A rare
	// concurrent double-release trips UNIQUE(tech_card_id, release_number) and is retried on the next
	// re-release — the caller (snapshotReleaseIfReleased) is best-effort.
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO tech_card_release (tech_card_id, release_number, version, released_by, snapshot, unit_cost, currency)
		SELECT :tech_card_id, COALESCE(m.max_rn, 0) + 1, :version, :released_by, :snapshot, :unit_cost, :currency
		FROM (SELECT MAX(release_number) AS max_rn FROM tech_card_release WHERE tech_card_id = :tech_card_id) m`,
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
		SELECT id, tech_card_id, release_number, version, released_by, unit_cost, currency, created_at
		FROM tech_card_release
		WHERE tech_card_id = :tech_card_id
		ORDER BY release_number DESC`,
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
		SELECT id, tech_card_id, release_number, version, released_by, snapshot, unit_cost, currency, created_at
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
