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
		"tech_card_id":       sm.TechCardId,
		"purpose":            sm.Purpose,
		"size_id":            sm.SizeId,
		"colorway_id":        sm.ColorwayId,
		"status":             sm.Status,
		"fabric_source":      sm.FabricSource,
		"notes":              sm.Notes,
		"started_at":         sm.StartedAt,
		"finished_at":        sm.FinishedAt,
		"pattern_url":        sm.PatternUrl,
		"pattern_note":       sm.PatternNote,
		"round_number":       sm.RoundNumber,
		"spec_release_id":    sm.SpecReleaseId,
		"previous_sample_id": sm.PreviousSampleId,
		"created_by":         sm.CreatedBy,
		"updated_by":         sm.UpdatedBy,
	}
}

// sampleColumns is the shared read column list (single source of truth for GetSampleById / ListSamples).
const sampleColumns = `id, tech_card_id, number, purpose, size_id, colorway_id, status, fabric_source,
	notes, started_at, finished_at, pattern_url, pattern_note, round_number, spec_release_id,
	previous_sample_id, lock_version, created_by, updated_by, created_at, updated_at`

// validateSampleRoundRefs verifies a sample's optional spec_release_id / previous_sample_id belong to
// its tech card (Q7 chain integrity): a spec snapshot must be one of this style's releases, and the
// previous sample must be another sample of the same style (and not itself). selfID is the sample being
// updated (0 on create) so the chain cannot point at itself.
func validateSampleRoundRefs(ctx context.Context, db dependency.DB, techCardID, selfID int, specReleaseID, previousSampleID sql.NullInt32) error {
	if specReleaseID.Valid {
		card, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, db, `SELECT tech_card_id FROM tech_card_release WHERE id = :rel`,
			map[string]any{"rel": specReleaseID.Int32})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return entity.ErrSampleSpecReleaseForeign
			}
			return fmt.Errorf("check sample spec release: %w", err)
		}
		if card.TechCardId != techCardID {
			return entity.ErrSampleSpecReleaseForeign
		}
	}
	if previousSampleID.Valid {
		if selfID != 0 && int(previousSampleID.Int32) == selfID {
			return entity.ErrSamplePreviousForeign
		}
		prev, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, db, `SELECT tech_card_id FROM sample WHERE id = :prev`,
			map[string]any{"prev": previousSampleID.Int32})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return entity.ErrSamplePreviousForeign
			}
			return fmt.Errorf("check previous sample: %w", err)
		}
		if prev.TechCardId != techCardID {
			return entity.ErrSamplePreviousForeign
		}
	}
	return nil
}

// insertSampleMedia bulk-inserts a sample's photo media in submitted order (B-6). Empty is a no-op.
func insertSampleMedia(ctx context.Context, db dependency.DB, sampleID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for i, mid := range mediaIDs {
		rows = append(rows, map[string]any{"sample_id": sampleID, "media_id": mid, "display_order": i})
	}
	if err := storeutil.BulkInsert(ctx, db, "sample_media", rows); err != nil {
		return fmt.Errorf("insert sample media: %w", err)
	}
	return nil
}

type sampleMediaRow struct {
	SampleID int `db:"sample_id"`
	entity.MediaFull
}

// mediaBySampleIds resolves each sample's photo media (MediaFull), ordered by display_order.
func (s *Store) mediaBySampleIds(ctx context.Context, ids []int) (map[int][]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[sampleMediaRow](ctx, s.DB, `
		SELECT sm.sample_id, m.*
		FROM sample_media sm
		JOIN media m ON m.id = sm.media_id
		WHERE sm.sample_id IN (:ids)
		ORDER BY sm.sample_id, sm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load sample media: %w", err)
	}
	out := make(map[int][]entity.MediaFull, len(ids))
	for _, r := range rows {
		out[r.SampleID] = append(out[r.SampleID], r.MediaFull)
	}
	return out, nil
}

// AddSample inserts a sample, assigning the next per-card number (MAX+1) in a transaction. The
// colorway/size references are validated to belong to the sample's tech card first (a colour-model
// or size from another style would be a silent mislink).
func (s *Store) AddSample(ctx context.Context, sm *entity.SampleInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := validateSampleRefs(ctx, rep.DB(), sm.TechCardId, sm.ColorwayId, sm.SizeId); err != nil {
			return err
		}
		if err := validateSampleRoundRefs(ctx, rep.DB(), sm.TechCardId, 0, sm.SpecReleaseId, sm.PreviousSampleId); err != nil {
			return err
		}
		n, err := storeutil.QueryNamedOne[struct {
			Next  int `db:"next"`
			Round int `db:"round"`
		}](ctx, rep.DB(), `SELECT COALESCE(MAX(number), 0) + 1 AS next, COALESCE(MAX(round_number), 0) + 1 AS round FROM sample WHERE tech_card_id = :tc`,
			map[string]any{"tc": sm.TechCardId})
		if err != nil {
			return fmt.Errorf("next sample number: %w", err)
		}
		params := sampleParams(sm)
		params["number"] = n.Next
		// Auto-assign the round (MAX+1 per card, the spine) when the client did not pin one; a manual
		// round_number is honoured. previous_sample_id is client-set (the iteration it derives from).
		if !sm.RoundNumber.Valid {
			params["round_number"] = n.Round
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO sample (tech_card_id, number, purpose, size_id, colorway_id, status, fabric_source, notes, started_at, finished_at, pattern_url, pattern_note,
				round_number, spec_release_id, previous_sample_id, created_by, updated_by)
			VALUES (:tech_card_id, :number, :purpose, :size_id, :colorway_id, :status, :fabric_source, :notes, :started_at, :finished_at, :pattern_url, :pattern_note,
				:round_number, :spec_release_id, :previous_sample_id, :created_by, :updated_by)`,
			params)
		if err != nil {
			return fmt.Errorf("insert sample: %w", err)
		}
		return insertSampleMedia(ctx, rep.DB(), id, sm.MediaIds)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add sample: %w", err)
	}
	return id, nil
}

// validateSampleRefs verifies a sample's optional colorway_id / size_id belong to its tech card. A
// colorway must be one of the card's colours; a size (when the card declares a size grid) must be in
// it — an early-stage card with no sizes yet accepts any size. Runs on the given db (tx or pool).
func validateSampleRefs(ctx context.Context, db dependency.DB, techCardID int, colorwayID, sizeID sql.NullInt32) error {
	if colorwayID.Valid {
		n, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM product WHERE id = :cw AND style_id = :tc`,
			map[string]any{"cw": colorwayID.Int32, "tc": techCardID})
		if err != nil {
			return fmt.Errorf("check sample colorway: %w", err)
		}
		if n == 0 {
			return entity.ErrSampleColorwayForeign
		}
	}
	if sizeID.Valid {
		grid, err := storeutil.QueryNamedOne[struct {
			Total int `db:"total"`
			Match int `db:"m"`
		}](ctx, db, `
			SELECT COUNT(*) AS total, COALESCE(SUM(size_id = :sz), 0) AS m
			FROM tech_card_size WHERE tech_card_id = :tc`,
			map[string]any{"sz": sizeID.Int32, "tc": techCardID})
		if err != nil {
			return fmt.Errorf("check sample size: %w", err)
		}
		if grid.Total > 0 && grid.Match == 0 {
			return entity.ErrSampleSizeForeign
		}
	}
	return nil
}

// UpdateSample updates a sample's editable fields (not its number or tech card). Existence is
// checked explicitly (a no-op UPDATE affects 0 rows and can't be told from a missing id), and the
// colorway/size are validated against the sample's own tech card. Returns sql.ErrNoRows when the
// sample does not exist.
func (s *Store) UpdateSample(ctx context.Context, id int, sm *entity.SampleInsert, expectedLockVersion int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			TechCardId  int `db:"tech_card_id"`
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(), `SELECT tech_card_id, lock_version FROM sample WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("load sample %d: %w", id, err)
		}
		// Double-guard optimistic lock (S25, golden standard): in-Go compare + WHERE lock_version guard.
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrSampleConflict
		}
		if err := validateSampleRefs(ctx, rep.DB(), cur.TechCardId, sm.ColorwayId, sm.SizeId); err != nil {
			return err
		}
		if err := validateSampleRoundRefs(ctx, rep.DB(), cur.TechCardId, id, sm.SpecReleaseId, sm.PreviousSampleId); err != nil {
			return err
		}
		params := sampleParams(sm)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE sample SET lock_version = lock_version + 1, purpose=:purpose, size_id=:size_id, colorway_id=:colorway_id,
				status=:status, fabric_source=:fabric_source, notes=:notes, started_at=:started_at, finished_at=:finished_at,
				pattern_url=:pattern_url, pattern_note=:pattern_note,
				spec_release_id=:spec_release_id, previous_sample_id=:previous_sample_id, updated_by=:updated_by
			WHERE id=:id AND lock_version=:expected_lock_version`, params)
		if err != nil {
			return fmt.Errorf("can't update sample %d: %w", id, err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved under us.
		if rows == 0 {
			return entity.ErrSampleConflict
		}
		// Full-replace the sample's photo media in the same tx (mirrors fitting media).
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM sample_media WHERE sample_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("clear sample media %d: %w", id, err)
		}
		return insertSampleMedia(ctx, rep.DB(), id, sm.MediaIds)
	})
}

// DeleteSample deletes a sample, refusing when it has material stock movements (applied facts). The
// movement guard and the delete run in one transaction so an issue committed concurrently cannot slip
// between the check and the delete and orphan its cost. Returns ErrSampleHasMovements in that case and
// sql.ErrNoRows when the sample does not exist.
func (s *Store) DeleteSample(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM material_stock_movement WHERE sample_id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("check sample movements: %w", err)
		}
		if n > 0 {
			return entity.ErrSampleHasMovements
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `DELETE FROM sample WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("can't delete sample %d: %w", id, err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// GetSampleById returns a sample with its composed cost block, or sql.ErrNoRows when none exists.
func (s *Store) GetSampleById(ctx context.Context, id int) (*entity.Sample, error) {
	sm, err := storeutil.QueryNamedOne[entity.Sample](ctx, s.DB,
		`SELECT `+sampleColumns+` FROM sample WHERE id = :id`, map[string]any{"id": id})
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
	media, err := s.mediaBySampleIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	sm.Media = media[id]
	return &sm, nil
}

// ListSamples returns a tech card's samples (newest number first), with the total count. Cost is
// nil on list rows.
func (s *Store) ListSamples(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, techCardID int, statusFilter, purposeFilter string) ([]entity.Sample, int, error) {
	limit, offset = clampPagination(limit, offset)
	// All three filters are optional: techCardID <= 0 lists samples across every style (the cross-style
	// «sewing queue»); an empty status/purpose is not filtered. Built dynamically so the same query
	// serves both the per-style card and the queue screen (gap-05/B-4).
	params := map[string]any{}
	where := ""
	if techCardID > 0 {
		where += " AND tech_card_id = :tc"
		params["tc"] = techCardID
	}
	if statusFilter != "" {
		where += " AND status = :status"
		params["status"] = statusFilter
	}
	if purposeFilter != "" {
		where += " AND purpose = :purpose"
		params["purpose"] = purposeFilter
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM sample WHERE 1=1%s`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("count samples: %w", err)
	}
	params["limit"] = limit
	params["offset"] = offset
	// Cross-style listing orders by tech_card then number so one style's samples stay grouped; a
	// single-style listing collapses to just number.
	rows, err := storeutil.QueryListNamed[entity.Sample](ctx, s.DB, fmt.Sprintf(`
		SELECT `+sampleColumns+`
		FROM sample WHERE 1=1%s
		ORDER BY tech_card_id, number %s LIMIT :limit OFFSET :offset`, where, orderFactor.String()),
		params)
	if err != nil {
		return nil, 0, fmt.Errorf("list samples: %w", err)
	}
	// Resolve photo media for the page in one batched query (thumbnails for the sewing-queue grid).
	ids := make([]int, len(rows))
	for i := range rows {
		ids[i] = rows[i].Id
	}
	media, err := s.mediaBySampleIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].Media = media[rows[i].Id]
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

func substitutionParams(sub *entity.SampleSubstitutionInsert) map[string]any {
	return map[string]any{
		"sample_id":               sub.SampleId,
		"bom_item_id":             sub.BomItemId,
		"original_material_id":    sub.OriginalMaterialId,
		"substituted_material_id": sub.SubstitutedMaterialId,
		"reason":                  sub.Reason,
		"planned_qty":             sub.PlannedQty,
		"actual_qty":              sub.ActualQty,
		"created_by":              sub.CreatedBy,
	}
}

// AddSampleSubstitution records a dev-time material substitution on a sample (§2.7). It verifies the
// sample exists and, when a BOM line is named, that the line belongs to the sample's tech card (a line
// from another style would be a silent mislink). Q2 invariant: this never touches product cost. Returns
// sql.ErrNoRows when the sample does not exist.
func (s *Store) AddSampleSubstitution(ctx context.Context, sub *entity.SampleSubstitutionInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		card, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, rep.DB(), `SELECT tech_card_id FROM sample WHERE id = :id`, map[string]any{"id": sub.SampleId})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("load sample %d: %w", sub.SampleId, err)
		}
		if sub.BomItemId.Valid {
			n, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM tech_card_bom_item WHERE id = :bi AND tech_card_id = :tc`,
				map[string]any{"bi": sub.BomItemId.Int32, "tc": card.TechCardId})
			if err != nil {
				return fmt.Errorf("check substitution bom line: %w", err)
			}
			if n == 0 {
				return entity.ErrSampleSubstitutionBomForeign
			}
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO sample_substitution (sample_id, bom_item_id, original_material_id, substituted_material_id, reason, planned_qty, actual_qty, created_by)
			VALUES (:sample_id, :bom_item_id, :original_material_id, :substituted_material_id, :reason, :planned_qty, :actual_qty, :created_by)`,
			substitutionParams(sub))
		if err != nil {
			return fmt.Errorf("insert sample substitution: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ListSampleSubstitutions returns a sample's substitutions (oldest first).
func (s *Store) ListSampleSubstitutions(ctx context.Context, sampleID int) ([]entity.SampleSubstitution, error) {
	rows, err := storeutil.QueryListNamed[entity.SampleSubstitution](ctx, s.DB, `
		SELECT id, sample_id, bom_item_id, original_material_id, substituted_material_id, reason, planned_qty, actual_qty, created_by, created_at
		FROM sample_substitution WHERE sample_id = :id ORDER BY id`, map[string]any{"id": sampleID})
	if err != nil {
		return nil, fmt.Errorf("list sample substitutions: %w", err)
	}
	return rows, nil
}

// DeleteSampleSubstitution deletes a substitution by id, returning sql.ErrNoRows when none exists.
func (s *Store) DeleteSampleSubstitution(ctx context.Context, id int) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`DELETE FROM sample_substitution WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("delete sample substitution %d: %w", id, err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
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
