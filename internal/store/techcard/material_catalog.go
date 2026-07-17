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
	composition, err := entity.NormalizeMaterialComposition(m.CompositionEntries)
	if err != nil {
		return 0, err
	}
	var id int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := checkMaterialCodeFree(ctx, rep.DB(), m.Code, 0); err != nil {
			return err
		}
		newID, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO material (name, section, supplier, supplier_ref, composition, spec, unit,
				fabric_width, fabric_weight_gsm, code, color, pantone, min_stock, notes,
				material_class, other_attrs, created_by, updated_by)
			VALUES (:name, :section, :supplier, :supplier_ref, :composition, :spec, :unit,
				:fabric_width, :fabric_weight_gsm, :code, :color, :pantone, :min_stock, :notes,
				:material_class, :other_attrs, :created_by, :updated_by)`,
			materialParams(m))
		if err != nil {
			return fmt.Errorf("create material: %w", err)
		}
		if err := upsertMaterialAttrs(ctx, rep.DB(), newID, m); err != nil {
			return fmt.Errorf("create material %d attrs: %w", newID, err)
		}
		if err := writeMaterialComposition(ctx, rep.DB(), newID, composition); err != nil {
			return err
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
	composition, err := entity.NormalizeMaterialComposition(m.CompositionEntries)
	if err != nil {
		return err
	}
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
				code=:code, color=:color, pantone=:pantone, min_stock=:min_stock, notes=:notes,
				material_class=:material_class, other_attrs=:other_attrs, updated_by=:updated_by
			WHERE id=:id AND lock_version=:expected_lock_version`, params)
		if err != nil {
			return fmt.Errorf("update material %d: %w", id, err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved under us —
		// make the WHERE guard load-bearing, not just the in-Go compare (mirrors tech_card).
		if rows == 0 {
			return entity.ErrMaterialConflict
		}
		if err := upsertMaterialAttrs(ctx, rep.DB(), id, m); err != nil {
			return fmt.Errorf("update material %d attrs: %w", id, err)
		}
		if err := writeMaterialComposition(ctx, rep.DB(), id, composition); err != nil {
			return err
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
	if err := s.attachMaterialAttrs(ctx, []*entity.MaterialWithPrice{&out}); err != nil {
		return nil, fmt.Errorf("get material %d attrs: %w", id, err)
	}
	if err := s.attachMaterialComposition(ctx, []*entity.MaterialWithPrice{&out}); err != nil {
		return nil, fmt.Errorf("get material %d composition: %w", id, err)
	}
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
	ptrs := make([]*entity.MaterialWithPrice, len(rows))
	for i, r := range rows {
		out[i] = r.toEntity()
		ptrs[i] = &out[i]
	}
	if err := s.attachMaterialAttrs(ctx, ptrs); err != nil {
		return nil, fmt.Errorf("list materials attrs: %w", err)
	}
	if err := s.attachMaterialComposition(ctx, ptrs); err != nil {
		return nil, fmt.Errorf("list materials composition: %w", err)
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

// materialParams maps a MaterialInsert to named query params, normalising name, section and class.
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
		"material_class":    normalizeMaterialClass(m.MaterialClass),
		"other_attrs":       nullJSONParam(m.OtherAttrs),
		"created_by":        m.CreatedBy,
		"updated_by":        m.UpdatedBy,
	}
}

// normalizeMaterialClass lower-cases/trims the class and defaults an empty one to 'other'. An
// out-of-range value is left as-is for the DB CHECK (chk_material_class) to reject.
func normalizeMaterialClass(class string) string {
	c := strings.ToLower(strings.TrimSpace(class))
	if c == "" {
		return string(entity.MaterialClassOther)
	}
	return c
}

// nullJSONParam yields nil (SQL NULL) for empty JSON so an unset escape-hatch is not stored as "".
func nullJSONParam(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// materialAttrTables are the four CTI side-tables, in a stable order for the full-replace clear.
var materialAttrTables = []string{
	"material_fabric_attr", "material_hardware_attr", "material_thread_attr", "material_packaging_attr",
}

// upsertMaterialAttrs full-replaces a material's typed side-tables: it clears all four (so a class
// change cannot strand stale attributes) and inserts the one row matching the material's class, if
// attributes were supplied. Runs inside the material write transaction.
func upsertMaterialAttrs(ctx context.Context, db dependency.DB, id int, m *entity.MaterialInsert) error {
	for _, tbl := range materialAttrTables {
		if err := storeutil.ExecNamed(ctx, db,
			fmt.Sprintf(`DELETE FROM %s WHERE material_id = :id`, tbl), map[string]any{"id": id}); err != nil {
			return fmt.Errorf("clear %s: %w", tbl, err)
		}
	}
	switch normalizeMaterialClass(m.MaterialClass) {
	case string(entity.MaterialClassFabric):
		if m.FabricAttr == nil {
			return nil
		}
		a := m.FabricAttr
		return storeutil.ExecNamed(ctx, db, `
			INSERT INTO material_fabric_attr (material_id, width_cm, weight_gsm, fabric_direction, shrinkage_pct, roll_length_m)
			VALUES (:id, :width_cm, :weight_gsm, :fabric_direction, :shrinkage_pct, :roll_length_m)`,
			map[string]any{"id": id, "width_cm": nullDecimalParam(a.WidthCm), "weight_gsm": nullDecimalParam(a.WeightGsm),
				"fabric_direction": a.FabricDirection, "shrinkage_pct": nullDecimalParam(a.ShrinkagePct), "roll_length_m": nullDecimalParam(a.RollLengthM)})
	case string(entity.MaterialClassHardware):
		if m.HardwareAttr == nil {
			return nil
		}
		a := m.HardwareAttr
		return storeutil.ExecNamed(ctx, db, `
			INSERT INTO material_hardware_attr (material_id, diameter_mm, dimensions, finish, base_material, weight_g)
			VALUES (:id, :diameter_mm, :dimensions, :finish, :base_material, :weight_g)`,
			map[string]any{"id": id, "diameter_mm": nullDecimalParam(a.DiameterMm), "dimensions": a.Dimensions,
				"finish": a.Finish, "base_material": a.BaseMaterial, "weight_g": nullDecimalParam(a.WeightG)})
	case string(entity.MaterialClassThread):
		if m.ThreadAttr == nil {
			return nil
		}
		a := m.ThreadAttr
		return storeutil.ExecNamed(ctx, db, `
			INSERT INTO material_thread_attr (material_id, ticket_tex, length_per_cone_m, needle_reco)
			VALUES (:id, :ticket_tex, :length_per_cone_m, :needle_reco)`,
			map[string]any{"id": id, "ticket_tex": a.TicketTex, "length_per_cone_m": nullDecimalParam(a.LengthPerConeM), "needle_reco": a.NeedleReco})
	case string(entity.MaterialClassPackaging):
		if m.PackagingAttr == nil {
			return nil
		}
		a := m.PackagingAttr
		return storeutil.ExecNamed(ctx, db, `
			INSERT INTO material_packaging_attr (material_id, substrate, dimensions, gsm, print_method)
			VALUES (:id, :substrate, :dimensions, :gsm, :print_method)`,
			map[string]any{"id": id, "substrate": a.Substrate, "dimensions": a.Dimensions, "gsm": nullDecimalParam(a.Gsm), "print_method": a.PrintMethod})
	}
	return nil // 'other' keeps its attributes in material.other_attrs, not a side-table
}

type materialFabricAttrRow struct {
	MaterialID int `db:"material_id"`
	entity.MaterialFabricAttr
}
type materialHardwareAttrRow struct {
	MaterialID int `db:"material_id"`
	entity.MaterialHardwareAttr
}
type materialThreadAttrRow struct {
	MaterialID int `db:"material_id"`
	entity.MaterialThreadAttr
}
type materialPackagingAttrRow struct {
	MaterialID int `db:"material_id"`
	entity.MaterialPackagingAttr
}

// attachMaterialAttrs loads each material's typed side-table row (at most one, matching its class)
// and attaches it. A material with no attributes simply keeps nil pointers.
func (s *Store) attachMaterialAttrs(ctx context.Context, mats []*entity.MaterialWithPrice) error {
	if len(mats) == 0 {
		return nil
	}
	ids := make([]int, 0, len(mats))
	byID := make(map[int]*entity.MaterialWithPrice, len(mats))
	for _, m := range mats {
		ids = append(ids, m.Id)
		byID[m.Id] = m
	}
	fRows, err := storeutil.QueryListNamed[materialFabricAttrRow](ctx, s.DB,
		`SELECT material_id, width_cm, weight_gsm, fabric_direction, shrinkage_pct, roll_length_m
		 FROM material_fabric_attr WHERE material_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load fabric attrs: %w", err)
	}
	for _, r := range fRows {
		if m, ok := byID[r.MaterialID]; ok {
			a := r.MaterialFabricAttr
			m.FabricAttr = &a
		}
	}
	hRows, err := storeutil.QueryListNamed[materialHardwareAttrRow](ctx, s.DB,
		`SELECT material_id, diameter_mm, dimensions, finish, base_material, weight_g
		 FROM material_hardware_attr WHERE material_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load hardware attrs: %w", err)
	}
	for _, r := range hRows {
		if m, ok := byID[r.MaterialID]; ok {
			a := r.MaterialHardwareAttr
			m.HardwareAttr = &a
		}
	}
	tRows, err := storeutil.QueryListNamed[materialThreadAttrRow](ctx, s.DB,
		`SELECT material_id, ticket_tex, length_per_cone_m, needle_reco
		 FROM material_thread_attr WHERE material_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load thread attrs: %w", err)
	}
	for _, r := range tRows {
		if m, ok := byID[r.MaterialID]; ok {
			a := r.MaterialThreadAttr
			m.ThreadAttr = &a
		}
	}
	pRows, err := storeutil.QueryListNamed[materialPackagingAttrRow](ctx, s.DB,
		`SELECT material_id, substrate, dimensions, gsm, print_method
		 FROM material_packaging_attr WHERE material_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load packaging attrs: %w", err)
	}
	for _, r := range pRows {
		if m, ok := byID[r.MaterialID]; ok {
			a := r.MaterialPackagingAttr
			m.PackagingAttr = &a
		}
	}
	return nil
}

// materialCompositionRow scans a material_composition row joined with its fibre display name for the
// batch read (attachMaterialComposition).
type materialCompositionRow struct {
	MaterialID int `db:"material_id"`
	entity.CompositionEntry
}

// attachMaterialComposition loads each material's structured fibre composition (S17,
// material_composition) resolved with the fibre dictionary display name, ordered by descending percent
// then code — the same projection shape as the style read (composition_read.go). A material with no
// composition simply keeps an empty slice.
func (s *Store) attachMaterialComposition(ctx context.Context, mats []*entity.MaterialWithPrice) error {
	if len(mats) == 0 {
		return nil
	}
	ids := make([]int, 0, len(mats))
	byID := make(map[int]*entity.MaterialWithPrice, len(mats))
	for _, m := range mats {
		ids = append(ids, m.Id)
		byID[m.Id] = m
	}
	rows, err := storeutil.QueryListNamed[materialCompositionRow](ctx, s.DB, `
		SELECT mc.material_id, mc.fiber_code, COALESCE(f.name, mc.fiber_code) AS name, mc.percent
		FROM material_composition mc
		LEFT JOIN fiber f ON f.code = mc.fiber_code
		WHERE mc.material_id IN (:ids)
		ORDER BY mc.material_id, mc.percent DESC, mc.fiber_code`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load material composition: %w", err)
	}
	for _, r := range rows {
		if m, ok := byID[r.MaterialID]; ok {
			m.CompositionEntries = append(m.CompositionEntries, r.CompositionEntry)
		}
	}
	return nil
}

// writeMaterialComposition full-replaces a material's structured fibre composition (S17): it clears
// the existing rows (a no-op on create) and, for a non-empty set, verifies every referenced fibre
// exists (a field-tagged error rather than a raw FK 500) then inserts the entries. entries must
// already be normalised/validated by entity.NormalizeMaterialComposition. Runs in the write tx.
func writeMaterialComposition(ctx context.Context, db dependency.DB, materialID int, entries []entity.CompositionEntry) error {
	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM material_composition WHERE material_id = :id`, map[string]any{"id": materialID}); err != nil {
		return fmt.Errorf("clear material %d composition: %w", materialID, err)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := checkFibersExist(ctx, db, entries); err != nil {
		return err
	}
	for i := range entries {
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO material_composition (material_id, fiber_code, percent)
			VALUES (:material_id, :fiber_code, :percent)`,
			map[string]any{"material_id": materialID, "fiber_code": entries[i].FiberCode, "percent": entries[i].Percent}); err != nil {
			return fmt.Errorf("insert material %d composition: %w", materialID, err)
		}
	}
	return nil
}

// checkFibersExist verifies every referenced fibre code is present in the dictionary, returning a
// field-tagged violation naming the first unknown code (clearer than the FK's raw 1452). Archived
// fibres still exist and are accepted — the composition FK requires existence, not active status.
func checkFibersExist(ctx context.Context, db dependency.DB, entries []entity.CompositionEntry) error {
	codes := make([]string, 0, len(entries))
	for i := range entries {
		codes = append(codes, entries[i].FiberCode)
	}
	rows, err := storeutil.QueryListNamed[struct {
		Code string `db:"code"`
	}](ctx, db, `SELECT code FROM fiber WHERE code IN (:codes)`, map[string]any{"codes": codes})
	if err != nil {
		return fmt.Errorf("check fibres exist: %w", err)
	}
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.Code] = true
	}
	for i := range entries {
		if !known[entries[i].FiberCode] {
			return entity.NewFieldViolation(fmt.Sprintf("composition_entries[%d].fiber_code", i),
				fmt.Sprintf("unknown fibre %s", entries[i].FiberCode), "",
				"reference a fibre from the dictionary")
		}
	}
	return nil
}

// nullDecimalParam yields nil for an invalid NullDecimal so the column is written NULL.
func nullDecimalParam(d decimal.NullDecimal) any {
	if !d.Valid {
		return nil
	}
	return d.Decimal
}
