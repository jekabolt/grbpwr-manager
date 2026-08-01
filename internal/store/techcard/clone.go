package techcard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CloneTechCardForSeason creates a new card from the already-converted source payload and carries
// the style-owned data that is not part of TechCardInsert. The source version is rechecked under a
// row lock in the same transaction as the insert, so a successful clone cannot be based on a source
// version that committed before this write began.
func (s *Store) CloneTechCardForSeason(ctx context.Context, sourceID, expectedSourceVersion int, tc *entity.TechCardInsert) (int, error) {
	if err := s.ensureDictionaryFresh(ctx, "season clone"); err != nil {
		return 0, err
	}
	s.stampApprovalTimes(tc, "", sql.NullTime{}, sql.NullTime{})
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		source, err := storeutil.QueryNamedOne[struct {
			LockVersion int `db:"lock_version"`
		}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id FOR UPDATE`, map[string]any{"id": sourceID})
		if err != nil {
			return err
		}
		if source.LockVersion != expectedSourceVersion {
			return entity.ErrTechCardConflict
		}

		params, err := techCardInsertHeaderParams(tc)
		if err != nil {
			return err
		}
		id, err = storeutil.ExecNamedLastId(ctx, db,
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			params)
		if err != nil {
			return fmt.Errorf("insert cloned tech card: %w", err)
		}
		if err := syncStyleCategoryTriple(ctx, db, id, tc.CategoryId); err != nil {
			return err
		}
		if err := insertTechCardChildren(ctx, db, id, tc); err != nil {
			return err
		}
		if err := copySeasonCloneCarryover(ctx, db, sourceID, id, tc.CreatedBy); err != nil {
			return err
		}
		if err := remintCardProducts(ctx, db, id, nil); err != nil {
			return err
		}
		return appendTechCardRevision(ctx, db, id, tc.CreatedBy, "header", "created", "tech card cloned for season")
	})
	if err != nil {
		return 0, fmt.Errorf("can't clone tech card: %w", err)
	}
	return id, nil
}

// copySeasonCloneCarryover copies style-owned authoring data that the TechCard converters do not
// carry. Size ids and measurement-name ids are global dictionary ids, and assembly components point
// at independent auxiliary cards, so only the owning style id is remapped. Size-scoped rows are
// intersected with the clone's just-inserted size range as a defensive guard.
func copySeasonCloneCarryover(ctx context.Context, db dependency.DB, sourceID, targetID int, actor string) error {
	params := map[string]any{"source": sourceID, "target": targetID, "actor": actor}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_size_measurement
			(tech_card_id, size_id, measurement_name_id, measurement_value)
		SELECT :target, src.size_id, src.measurement_name_id, src.measurement_value
		FROM tech_card_size_measurement src
		JOIN tech_card_size target_size
		  ON target_size.tech_card_id = :target AND target_size.size_id = src.size_id
		WHERE src.tech_card_id = :source`, params); err != nil {
		return fmt.Errorf("copy cloned style size chart: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_grade_rule (tech_card_id, measurement_name_id, step)
		SELECT :target, measurement_name_id, step
		FROM tech_card_grade_rule
		WHERE tech_card_id = :source`, params); err != nil {
		return fmt.Errorf("copy cloned style grade rule: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE tech_card dst
		JOIN tech_card src_card ON src_card.id = :source
		SET dst.grade_base_size_id = CASE
			WHEN src_card.grade_base_size_id IS NULL THEN NULL
			WHEN EXISTS (
				SELECT 1 FROM tech_card_size z
				WHERE z.tech_card_id = :target AND z.size_id = src_card.grade_base_size_id
			) THEN src_card.grade_base_size_id
			ELSE NULL
		END
		WHERE dst.id = :target`, params); err != nil {
		return fmt.Errorf("copy cloned style grade base: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO style_assembly
			(style_id, component_tech_card_id, size_id, qty, print_note, position_note,
			 active, lock_version, created_by, updated_by)
		SELECT :target, src.component_tech_card_id, src.size_id, src.qty, src.print_note,
		       src.position_note, src.active, 0, :actor, :actor
		FROM style_assembly src
		WHERE src.style_id = :source
		  AND (src.size_id IS NULL OR EXISTS (
			SELECT 1 FROM tech_card_size z
			WHERE z.tech_card_id = :target AND z.size_id = src.size_id
		  ))
		ORDER BY src.id`, params); err != nil {
		return fmt.Errorf("copy cloned style assembly: %w", err)
	}
	return nil
}
