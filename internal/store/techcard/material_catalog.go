package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// materialRow is the flat scan target for a material joined with its current price.
type materialRow struct {
	entity.Material
	LpPrice     decimal.NullDecimal `db:"lp_price"`
	LpCurrency  sql.NullString      `db:"lp_currency"`
	LpValidFrom sql.NullTime        `db:"lp_valid_from"`
	LpSource    sql.NullString      `db:"lp_source"`
	LpNote      sql.NullString      `db:"lp_note"`
}

func (r materialRow) toEntity() entity.MaterialWithPrice {
	out := entity.MaterialWithPrice{Material: r.Material}
	if r.LpPrice.Valid && r.LpCurrency.Valid {
		out.LatestPrice = &entity.MaterialPrice{
			MaterialId: r.Material.Id,
			Price:      r.LpPrice.Decimal,
			Currency:   r.LpCurrency.String,
			ValidFrom:  r.LpValidFrom.Time,
			Source:     r.LpSource.String,
			Note:       r.LpNote,
		}
	}
	return out
}

// materialWithPriceSelect is the shared SELECT that joins each material to its current price —
// the latest row with valid_from <= today, tie-broken by currency. WHERE/ORDER are appended by
// the caller.
const materialWithPriceSelect = `
	WITH latest AS (
		SELECT material_id, price, currency, valid_from, source, note,
			ROW_NUMBER() OVER (PARTITION BY material_id ORDER BY valid_from DESC, currency ASC) AS rn
		FROM material_price
		WHERE valid_from <= CURDATE()
	)
	SELECT m.*,
		l.price AS lp_price, l.currency AS lp_currency, l.valid_from AS lp_valid_from,
		l.source AS lp_source, l.note AS lp_note
	FROM material m
	LEFT JOIN latest l ON l.material_id = m.id AND l.rn = 1`

// CreateMaterial inserts a catalog material and returns its id. The uniqueness guard and the insert
// run in one transaction (SERIALIZABLE) so two concurrent creates of the same code cannot both pass
// the check — there is no DB-level unique index on code (it must stay unique only among non-archived
// rows), so the check must hold the read range.
func (s *Store) CreateMaterial(ctx context.Context, m *entity.MaterialInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := checkMaterialCodeFree(ctx, rep.DB(), m.Code, 0); err != nil {
			return err
		}
		newID, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO material (name, section, supplier, supplier_ref, composition, spec, unit,
				fabric_width, fabric_weight_gsm, code, color, pantone, min_stock, notes)
			VALUES (:name, :section, :supplier, :supplier_ref, :composition, :spec, :unit,
				:fabric_width, :fabric_weight_gsm, :code, :color, :pantone, :min_stock, :notes)`,
			materialParams(m))
		if err != nil {
			return fmt.Errorf("create material: %w", err)
		}
		id = newID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateMaterial updates a catalog material's descriptive fields (not its price history). It is
// optimistically locked on expectedLockVersion (entity.ErrMaterialConflict on a mismatch, S25),
// returns entity.ErrMaterialNotFound when no such material exists, locks the unit of measure once
// the material has stock movements (historical quantities would lose meaning), and keeps the
// internal code unique among non-archived materials. The lock load, both guards and the update run
// in one transaction so a concurrent movement/create/update cannot slip past the checks.
func (s *Store) UpdateMaterial(ctx context.Context, id int, m *entity.MaterialInsert, expectedLockVersion int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(), `SELECT lock_version FROM material WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("update material %d: %w", id, entity.ErrMaterialNotFound)
			}
			return fmt.Errorf("load material %d for update: %w", id, err)
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrMaterialConflict
		}
		if err := checkMaterialCodeFree(ctx, rep.DB(), m.Code, id); err != nil {
			return err
		}
		if err := checkMaterialUnitChange(ctx, rep.DB(), id, m.Unit); err != nil {
			return err
		}
		params := materialParams(m)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE material SET lock_version = lock_version + 1,
				name=:name, section=:section, supplier=:supplier, supplier_ref=:supplier_ref,
				composition=:composition, spec=:spec, unit=:unit, fabric_width=:fabric_width, fabric_weight_gsm=:fabric_weight_gsm,
				code=:code, color=:color, pantone=:pantone, min_stock=:min_stock, notes=:notes
			WHERE id=:id AND lock_version=:expected_lock_version`, params)
		if err != nil {
			return fmt.Errorf("update material %d: %w", id, err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved under us —
		// make the WHERE guard load-bearing, not just the in-Go compare (mirrors tech_card).
		if rows == 0 {
			return entity.ErrMaterialConflict
		}
		return nil
	})
}

// checkMaterialCodeFree fails with ErrMaterialCodeTaken if code duplicates another non-archived
// material's code. An empty code is always free. excludeID skips the material being updated. Runs on
// the given db (pool or tx) so callers can hold the read range in a transaction.
func checkMaterialCodeFree(ctx context.Context, db dependency.DB, code sql.NullString, excludeID int) error {
	if !code.Valid || strings.TrimSpace(code.String) == "" {
		return nil
	}
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM material WHERE archived = FALSE AND code = :code AND id <> :id`,
		map[string]any{"code": strings.TrimSpace(code.String), "id": excludeID})
	if err != nil {
		return fmt.Errorf("check material code: %w", err)
	}
	if n > 0 {
		return entity.ErrMaterialCodeTaken
	}
	return nil
}

// checkMaterialUnitChange fails with ErrMaterialUnitLocked if the unit is being changed on a
// material that already has stock movements.
func checkMaterialUnitChange(ctx context.Context, db dependency.DB, id int, newUnit sql.NullString) error {
	cur, err := storeutil.QueryNamedOne[struct {
		Unit sql.NullString `db:"unit"`
	}](ctx, db, `SELECT unit FROM material WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // update will report not-found
		}
		return fmt.Errorf("read material unit: %w", err)
	}
	if strings.TrimSpace(cur.Unit.String) == strings.TrimSpace(newUnit.String) {
		return nil
	}
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM material_stock_movement WHERE material_id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("check material movements: %w", err)
	}
	if n > 0 {
		return entity.ErrMaterialUnitLocked
	}
	return nil
}

// ArchiveMaterial sets a material's archived flag (soft delete — linked BOM lines are
// unaffected since they keep their own snapshot fields).
func (s *Store) ArchiveMaterial(ctx context.Context, id int, archived bool) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`UPDATE material SET archived=:archived WHERE id=:id`,
		map[string]any{"id": id, "archived": archived})
	if err != nil {
		return fmt.Errorf("archive material %d: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("archive material %d: %w", id, entity.ErrMaterialNotFound)
	}
	return nil
}

// GetMaterial returns a material with its current price, or an error if not found.
func (s *Store) GetMaterial(ctx context.Context, id int) (*entity.MaterialWithPrice, error) {
	rows, err := storeutil.QueryListNamed[materialRow](ctx, s.DB,
		materialWithPriceSelect+` WHERE m.id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("get material %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("get material %d: %w", id, entity.ErrMaterialNotFound)
	}
	out := rows[0].toEntity()
	return &out, nil
}

// ListMaterials returns catalog materials with their current price, optionally filtered by
// section, excluding archived unless includeArchived is set. Ordered by section then name.
func (s *Store) ListMaterials(ctx context.Context, section string, includeArchived bool) ([]entity.MaterialWithPrice, error) {
	rows, err := storeutil.QueryListNamed[materialRow](ctx, s.DB,
		materialWithPriceSelect+`
		WHERE (:section = '' OR m.section = :section)
		AND (:includeArchived OR m.archived = FALSE)
		ORDER BY m.section, m.name`,
		map[string]any{"section": strings.ToLower(strings.TrimSpace(section)), "includeArchived": includeArchived})
	if err != nil {
		return nil, fmt.Errorf("list materials: %w", err)
	}
	out := make([]entity.MaterialWithPrice, len(rows))
	for i, r := range rows {
		out[i] = r.toEntity()
	}
	return out, nil
}

// AddMaterialPrice appends a price point to a material's history (upsert by material+date+
// currency, so re-entering a same-day correction overwrites rather than duplicates).
func (s *Store) AddMaterialPrice(ctx context.Context, p entity.MaterialPrice) error {
	source := strings.TrimSpace(p.Source)
	if source == "" {
		source = entity.MaterialPriceSourceManual
	}
	err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO material_price (material_id, price, currency, valid_from, source, note)
		VALUES (:material_id, :price, :currency, :valid_from, :source, :note)
		ON DUPLICATE KEY UPDATE price = VALUES(price), source = VALUES(source), note = VALUES(note)`,
		map[string]any{
			"material_id": p.MaterialId,
			"price":       p.Price,
			"currency":    strings.ToUpper(strings.TrimSpace(p.Currency)),
			"valid_from":  p.ValidFrom,
			"source":      source,
			"note":        p.Note,
		})
	if err != nil {
		return fmt.Errorf("add material price for %d: %w", p.MaterialId, err)
	}
	return nil
}

// ListMaterialPrices returns a material's full price history, newest first.
func (s *Store) ListMaterialPrices(ctx context.Context, materialID int) ([]entity.MaterialPrice, error) {
	rows, err := storeutil.QueryListNamed[entity.MaterialPrice](ctx, s.DB,
		`SELECT material_id, price, currency, valid_from, source, note
		 FROM material_price WHERE material_id = :id ORDER BY valid_from DESC, currency ASC`,
		map[string]any{"id": materialID})
	if err != nil {
		return nil, fmt.Errorf("list material prices for %d: %w", materialID, err)
	}
	return rows, nil
}

// materialParams maps a MaterialInsert to named query params, normalising name and section.
func materialParams(m *entity.MaterialInsert) map[string]any {
	return map[string]any{
		"name":              strings.TrimSpace(m.Name),
		"section":           strings.ToLower(strings.TrimSpace(m.Section)),
		"supplier":          m.Supplier,
		"supplier_ref":      m.SupplierRef,
		"composition":       m.Composition,
		"spec":              m.Spec,
		"unit":              m.Unit,
		"fabric_width":      nullDecimalParam(m.FabricWidth),
		"fabric_weight_gsm": nullDecimalParam(m.FabricWeightGsm),
		"code":              m.Code,
		"color":             m.Color,
		"pantone":           m.Pantone,
		"min_stock":         nullDecimalParam(m.MinStock),
		"notes":             m.Notes,
	}
}

// nullDecimalParam yields nil for an invalid NullDecimal so the column is written NULL.
func nullDecimalParam(d decimal.NullDecimal) any {
	if !d.Valid {
		return nil
	}
	return d.Decimal
}
