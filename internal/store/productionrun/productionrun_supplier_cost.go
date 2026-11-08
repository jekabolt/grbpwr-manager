package productionrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// reconcileSupplierPriceCost applies the bought-aux supplier price article (entity.
// ReconcileSupplierPriceCost) to r.Costs inside the run's write transaction, before the costs are
// written. runID is 0 on a create. It returns the record to append once the costs are written; nil
// when this save changes nothing. Only open runs are judged: received and closed runs never reach
// it, and a cancelled or partially received run is left as it is.
func reconcileSupplierPriceCost(ctx context.Context, db dependency.DB, runID, techCardID int, r *entity.ProductionRunInsert) (*entity.SupplierPriceSeedRecord, error) {
	if !entity.SupplierPriceSeedOpen(r.Status) {
		return nil, nil
	}
	card, err := storeutil.QueryNamedOne[struct {
		Purpose          string         `db:"purpose"`
		AuxMadeBy        sql.NullString `db:"aux_made_by"`
		OutputMaterialId sql.NullInt64  `db:"output_material_id"`
	}](ctx, db, `SELECT purpose, aux_made_by, output_material_id FROM tech_card WHERE id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load tech card %d for the supplier price: %w", techCardID, err)
	}
	if card.Purpose != string(entity.TechCardPurposeAuxiliary) {
		return nil, nil
	}
	var last *entity.SupplierPriceSeedRecord
	if runID > 0 {
		var readable bool
		last, readable, err = lastSupplierPriceSeed(ctx, db, runID)
		if err != nil {
			return nil, err
		}
		if !readable {
			return nil, nil
		}
	}
	bought := card.AuxMadeBy.Valid && card.AuxMadeBy.String == entity.AuxMadeBySupplier
	if !bought && (last == nil || len(last.Articles) == 0) {
		return nil, nil
	}
	var seed entity.SupplierPriceSeed
	if bought {
		seed, err = supplierPriceSeed(ctx, db, techCardID, card.OutputMaterialId, r.Lines)
		if err != nil {
			return nil, err
		}
	}
	costs, rec := entity.ReconcileSupplierPriceCost(r.Costs, seed, last)
	if rec == nil {
		return nil, nil
	}
	r.Costs = costs
	return rec, nil
}

// recordSupplierPriceSeed appends the supplier_price_seeded event a reconcile decided on.
func recordSupplierPriceSeed(ctx context.Context, db dependency.DB, runID int, actor string, rec *entity.SupplierPriceSeedRecord) error {
	if rec == nil {
		return nil
	}
	payload := map[string]any{"articles": rec.Articles}
	if len(rec.Basis) > 0 {
		payload["basis"] = rec.Basis
	}
	return recordRunEvent(ctx, db, runID, entity.ProductionRunEventSupplierPriceSeeded, actor, "", payload)
}

// lastSupplierPriceSeed reads the run's last supplier_price_seeded record. nil = never seeded.
// readable=false: the payload cannot be parsed, so whether the articles are still the system's is
// unknown — the caller then leaves the costs alone rather than guess.
func lastSupplierPriceSeed(ctx context.Context, db dependency.DB, runID int) (*entity.SupplierPriceSeedRecord, bool, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Payload sql.NullString `db:"payload"`
	}](ctx, db, `
		SELECT payload FROM production_run_event
		WHERE run_id = :run_id AND event_type = :event_type
		ORDER BY id DESC LIMIT 1`,
		map[string]any{"run_id": runID, "event_type": entity.ProductionRunEventSupplierPriceSeeded})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("load the supplier price record of run %d: %w", runID, err)
	}
	rec := &entity.SupplierPriceSeedRecord{}
	if !row.Payload.Valid || json.Unmarshal([]byte(row.Payload.String), rec) != nil {
		slog.Default().WarnContext(ctx, "supplier price record unreadable; costs left as they are",
			slog.Int("run_id", runID))
		return nil, false, nil
	}
	return rec, true, nil
}

// supplierPriceSeed prices the run's plan from the card's output materials, folded to base.
func supplierPriceSeed(ctx context.Context, db dependency.DB, techCardID int, legacyMaterial sql.NullInt64,
	lines []entity.ProductionRunLine) (entity.SupplierPriceSeed, error) {
	variants, err := storeutil.QueryListNamed[struct {
		Id         int `db:"id"`
		MaterialId int `db:"material_id"`
	}](ctx, db, `SELECT id, material_id FROM tech_card_output_variant WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return entity.SupplierPriceSeed{}, fmt.Errorf("load output colours of tech card %d: %w", techCardID, err)
	}
	variantMaterial := make(map[int]int, len(variants))
	for _, v := range variants {
		variantMaterial[v.Id] = v.MaterialId
	}
	matSet := map[int]bool{}
	for i := range lines {
		if lines[i].PlannedQty <= 0 {
			continue
		}
		if lines[i].OutputVariantId.Valid {
			if m, ok := variantMaterial[int(lines[i].OutputVariantId.Int32)]; ok {
				matSet[m] = true
			}
		} else if legacyMaterial.Valid {
			matSet[int(legacyMaterial.Int64)] = true
		}
	}
	if len(matSet) == 0 {
		return entity.SupplierPriceSeed{}, nil
	}
	ids := make([]int, 0, len(matSet))
	for m := range matSet {
		ids = append(ids, m)
	}
	rows, err := storeutil.QueryListNamed[entity.MaterialPrice](ctx, db, `
		SELECT material_id, price, currency, valid_from, source, note
		FROM material_price
		WHERE material_id IN (:ids) AND source IN (:sources)`,
		map[string]any{"ids": ids, "sources": []string{entity.MaterialPriceSourceManual, entity.MaterialPriceSourcePurchase}})
	if err != nil {
		return entity.SupplierPriceSeed{}, fmt.Errorf("load supplier prices: %w", err)
	}
	byMaterial := map[int][]entity.MaterialPrice{}
	for _, p := range rows {
		byMaterial[p.MaterialId] = append(byMaterial[p.MaterialId], p)
	}
	// One date rule for the seed and the readiness gate: entity.SupplierPrice drops a row dated ahead.
	now := time.Now()
	prices := make(map[int]entity.MaterialPrice, len(byMaterial))
	for m, ps := range byMaterial {
		if p, ok := entity.SupplierPrice(ps, now); ok {
			prices[m] = p
		}
	}
	seed := entity.ComputeSupplierPriceSeed(lines, variantMaterial, legacyMaterial, prices)
	if len(seed.Articles) == 0 {
		return seed, nil
	}
	fx, err := costingFxInTx(ctx, db)
	if err != nil {
		return entity.SupplierPriceSeed{}, err
	}
	dto.FoldProductionRunCostsToBase(seed.Articles, fx)
	return seed, nil
}

// costingFxInTx reads today's costing rates on the run's transaction — the same rate set the
// handler folds typed articles with (techcard.GetCostingFxRatesToBase + the cached base currency).
func costingFxInTx(ctx context.Context, db dependency.DB) (dto.CostingFx, error) {
	rows, err := storeutil.QueryListNamed[entity.CostingFxRate](ctx, db, `
		SELECT f.currency, f.rate_to_base, f.valid_from
		FROM costing_fx_rate f
		JOIN (
			SELECT currency, MAX(valid_from) AS vf
			FROM costing_fx_rate
			WHERE valid_from <= CURDATE()
			GROUP BY currency
		) m ON m.currency = f.currency AND m.vf = f.valid_from`,
		map[string]any{})
	if err != nil {
		return dto.CostingFx{}, fmt.Errorf("load costing fx rates: %w", err)
	}
	toBase := make(map[string]decimal.Decimal, len(rows))
	for _, r := range rows {
		toBase[strings.ToUpper(r.Currency)] = r.RateToBase
	}
	return dto.CostingFx{ToBase: toBase, Base: cache.GetBaseCurrency()}, nil
}
