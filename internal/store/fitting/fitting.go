// Package fitting implements garment try-on (fitting) session management.
package fitting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// resolveFittingSample validates/inherits the fitting's tech card from a linked sample (NF-04): a
// sample must belong to the fitting's tech card; when the fitting has no tech card set, it inherits
// the sample's so round auto-numbering and the style's fitting list/rounds include it. No sample
// linked → nothing to do.
func resolveFittingSample(ctx context.Context, db dependency.DB, f *entity.FittingInsert) error {
	if !f.SampleId.Valid {
		return nil
	}
	tc, err := storeutil.QueryNamedOne[struct {
		TechCardId int `db:"tech_card_id"`
	}](ctx, db, `SELECT tech_card_id FROM sample WHERE id = :id`, map[string]any{"id": f.SampleId.Int32})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.ErrSampleForeignToCard
		}
		return fmt.Errorf("load fitting sample %d: %w", f.SampleId.Int32, err)
	}
	if f.TechCardId.Valid {
		if int(f.TechCardId.Int32) != tc.TechCardId {
			return entity.ErrSampleForeignToCard
		}
		return nil
	}
	f.TechCardId = sql.NullInt32{Int32: int32(tc.TechCardId), Valid: true}
	return nil
}

// Pagination bounds for list endpoints: an unset/0 limit falls back to the
// default page size, and any limit is capped to avoid unbounded scans.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Fittings.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new fitting store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddFitting inserts a fitting with its sizes and media, returning the new id.
func (s *Store) AddFitting(ctx context.Context, f *entity.FittingInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// A linked sample must belong to the fitting's tech card; if the fitting has no tech card it
		// inherits the sample's (so round numbering and the style's fitting list include it) — NF-04.
		if err := resolveFittingSample(ctx, rep.DB(), f); err != nil {
			return err
		}
		params := fittingParams(f)
		// Auto-assign the round number within the tx when it is not set and the fitting is
		// anchored to a tech card, so a style's try-ons number themselves 1, 2, 3, …. A manual
		// round_number is honoured; the uniq_fitting_round index guards a concurrent collision.
		if !f.RoundNumber.Valid && f.TechCardId.Valid {
			next, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COALESCE(MAX(round_number), 0) + 1 FROM fitting WHERE tech_card_id = :tc`,
				map[string]any{"tc": f.TechCardId.Int32})
			if err != nil {
				return fmt.Errorf("failed to compute next fitting round: %w", err)
			}
			params["roundNumber"] = next
		}
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO fitting (tech_card_id, product_id, model_id, fitting_date, comment, status, verdict, round_number, outcome, sample_id, created_by, updated_by)
			VALUES (:techCardId, :productId, :modelId, :fittingDate, :comment, :status, :verdict, :roundNumber, :outcome, :sampleId, :createdBy, :updatedBy)`,
			params)
		if err != nil {
			return fmt.Errorf("failed to insert fitting: %w", err)
		}
		if err := insertFittingSizes(ctx, rep.DB(), id, f.Sizes); err != nil {
			return err
		}
		if err := insertFittingMedia(ctx, rep.DB(), id, f.MediaIds); err != nil {
			return err
		}
		if err := insertFittingPatterns(ctx, rep.DB(), id, f.Patterns); err != nil {
			return err
		}
		if err := insertFittingCallouts(ctx, rep.DB(), id, f.Callouts); err != nil {
			return err
		}
		// Initial structured-remark batch (S26). After creation, items are managed via the dedicated
		// change-request CRUD so their id stays stable (carried_from_id / carry-over depend on it).
		return insertFittingChangeRequests(ctx, rep.DB(), id, f.CreatedBy, f.ChangeRequests)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add fitting: %w", err)
	}
	return id, nil
}

// UpdateFitting updates a fitting and replaces its sizes and media. Returns
// sql.ErrNoRows when no fitting with the given id exists.
func (s *Store) UpdateFitting(ctx context.Context, id int, f *entity.FittingInsert, expectedLockVersion int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Load the lock version (also the existence check: a bare UPDATE reports 0 rows for both a
		// missing id and a no-op, so we can't rely on it).
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(), `SELECT lock_version FROM fitting WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("load fitting %d: %w", id, err)
		}
		// Double-guard optimistic lock (S25, golden standard): in-Go compare + WHERE lock_version guard.
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrFittingConflict
		}
		if err := resolveFittingSample(ctx, rep.DB(), f); err != nil {
			return err
		}
		params := fittingParams(f)
		params["id"] = id
		params["expectedLockVersion"] = expectedLockVersion
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE fitting SET
				lock_version = lock_version + 1,
				tech_card_id = :techCardId,
				product_id = :productId,
				model_id = :modelId,
				fitting_date = :fittingDate,
				comment = :comment,
				status = :status,
				verdict = :verdict,
				round_number = :roundNumber,
				outcome = :outcome,
				sample_id = :sampleId,
				updated_by = :updatedBy
			WHERE id = :id AND lock_version = :expectedLockVersion`, params)
		if err != nil {
			return fmt.Errorf("failed to update fitting: %w", err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved under us.
		if rows == 0 {
			return entity.ErrFittingConflict
		}
		// Sizes / media / patterns / callouts stay full-replace children. Structured change-requests do
		// NOT: they are managed via the dedicated CRUD so their id is stable (carry-over depends on it),
		// so a fitting edit must not wipe them.
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_size WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting sizes: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_media WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting media: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_pattern WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting patterns: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fitting_callout WHERE fitting_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear fitting callouts: %w", err)
		}
		if err := insertFittingSizes(ctx, rep.DB(), id, f.Sizes); err != nil {
			return err
		}
		if err := insertFittingMedia(ctx, rep.DB(), id, f.MediaIds); err != nil {
			return err
		}
		if err := insertFittingPatterns(ctx, rep.DB(), id, f.Patterns); err != nil {
			return err
		}
		return insertFittingCallouts(ctx, rep.DB(), id, f.Callouts)
	})
	if err != nil {
		return fmt.Errorf("can't update fitting: %w", err)
	}
	return nil
}

// DeleteFitting deletes a fitting by id (sizes and media cascade).
func (s *Store) DeleteFitting(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM fitting WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete fitting: %w", err)
	}
	return nil
}

// GetFittingById returns a fitting with its sizes and resolved media.
func (s *Store) GetFittingById(ctx context.Context, id int) (*entity.Fitting, error) {
	f, err := storeutil.QueryNamedOne[entity.Fitting](ctx, s.DB,
		`SELECT * FROM fitting WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get fitting: %w", err)
	}
	sizes, err := s.sizesByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	media, err := s.mediaByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	patterns, err := s.patternsByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	callouts, err := s.calloutsByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	changeRequests, err := s.changeRequestsByFittingIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	f.Sizes = sizes[id]
	f.Media = media[id]
	f.Patterns = patterns[id]
	f.Callouts = callouts[id]
	f.ChangeRequests = changeRequests[id]
	return &f, nil
}

// ListFittings returns a paged list of fittings, optionally filtered by tech card,
// product and/or model (pass 0 to ignore a filter), with sizes and resolved media,
// plus the total number of matching fittings (ignoring pagination).
func (s *Store) ListFittings(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, productID, modelID, techCardID int) ([]entity.Fitting, int, error) {
	limit, offset = clampPagination(limit, offset)

	// Shared filter for both the count and the page query.
	filterParams := map[string]any{}
	where := ""
	if techCardID != 0 {
		where += " AND tech_card_id = :techCardId"
		filterParams["techCardId"] = techCardID
	}
	if productID != 0 {
		where += " AND product_id = :productId"
		filterParams["productId"] = productID
	}
	if modelID != 0 {
		where += " AND model_id = :modelId"
		filterParams["modelId"] = modelID
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM fitting WHERE 1=1%s`, where), filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count fittings: %w", err)
	}

	// Reuse the same param map for the page query (filter + pagination).
	filterParams["limit"] = limit
	filterParams["offset"] = offset
	query := fmt.Sprintf(`
		SELECT * FROM fitting
		WHERE 1=1%s
		ORDER BY id %s
		LIMIT :limit OFFSET :offset`, where, orderFactor.String())

	fittings, err := storeutil.QueryListNamed[entity.Fitting](ctx, s.DB, query, filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list fittings: %w", err)
	}
	if len(fittings) == 0 {
		return fittings, total, nil
	}
	ids := make([]int, 0, len(fittings))
	for _, f := range fittings {
		ids = append(ids, f.Id)
	}
	sizes, err := s.sizesByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	media, err := s.mediaByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	patterns, err := s.patternsByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	callouts, err := s.calloutsByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	changeRequests, err := s.changeRequestsByFittingIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range fittings {
		fittings[i].Sizes = sizes[fittings[i].Id]
		fittings[i].Media = media[fittings[i].Id]
		fittings[i].Patterns = patterns[fittings[i].Id]
		fittings[i].Callouts = callouts[fittings[i].Id]
		fittings[i].ChangeRequests = changeRequests[fittings[i].Id]
	}
	return fittings, total, nil
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

func fittingParams(f *entity.FittingInsert) map[string]any {
	return map[string]any{
		"techCardId":  f.TechCardId,
		"productId":   f.ProductId,
		"modelId":     f.ModelId,
		"fittingDate": f.FittingDate,
		"comment":     f.Comment,
		"status":      string(f.Status),
		"verdict":     string(f.Verdict),
		"roundNumber": f.RoundNumber,
		"outcome":     f.Outcome,
		"sampleId":    f.SampleId,
		"createdBy":   f.CreatedBy,
		"updatedBy":   f.UpdatedBy,
	}
}

// changeRequestParams builds the write params for one structured change-request item (S26).
func changeRequestParams(cr *entity.FittingChangeRequest) map[string]any {
	return map[string]any{
		"fitting_id":      cr.FittingId,
		"target":          cr.Target,
		"note":            cr.Note,
		"callout_number":  cr.CalloutNumber,
		"zone":            cr.Zone,
		"piece_id":        cr.PieceId,
		"status":          cr.Status,
		"carried_from_id": cr.CarriedFromId,
		"created_by":      cr.CreatedBy,
	}
}

func insertFittingSizes(ctx context.Context, db dependency.DB, fittingID int, sizes []entity.FittingSize) error {
	if len(sizes) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(sizes))
	for _, sz := range sizes {
		rows = append(rows, map[string]any{
			"fitting_id": fittingID,
			"size_id":    sz.SizeId,
			"fit_note":   sz.FitNote,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_size", rows); err != nil {
		return fmt.Errorf("failed to insert fitting sizes: %w", err)
	}
	return nil
}

func insertFittingPatterns(ctx context.Context, db dependency.DB, fittingID int, patterns []entity.FittingPattern) error {
	if len(patterns) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(patterns))
	for i, p := range patterns {
		rows = append(rows, map[string]any{
			"fitting_id":    fittingID,
			"size_id":       p.SizeId,
			"url":           p.URL,
			"filename":      p.Filename,
			"size_bytes":    p.SizeBytes,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_pattern", rows); err != nil {
		return fmt.Errorf("failed to insert fitting patterns: %w", err)
	}
	return nil
}

func insertFittingCallouts(ctx context.Context, db dependency.DB, fittingID int, callouts []entity.FittingCallout) error {
	if len(callouts) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(callouts))
	for i, c := range callouts {
		rows = append(rows, map[string]any{
			"fitting_id":     fittingID,
			"callout_number": c.Number,
			"note":           c.Note,
			"media_id":       c.MediaId,
			"pos_x":          c.PosX,
			"pos_y":          c.PosY,
			"display_order":  i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_callout", rows); err != nil {
		return fmt.Errorf("failed to insert fitting callouts: %w", err)
	}
	return nil
}

// insertFittingChangeRequests inserts the initial structured-remark batch on create (S26). createdBy is
// the acting admin (the fitting's creator) — the items are stamped with it.
func insertFittingChangeRequests(ctx context.Context, db dependency.DB, fittingID int, createdBy string, crs []entity.FittingChangeRequest) error {
	if len(crs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(crs))
	for i, c := range crs {
		rows = append(rows, map[string]any{
			"fitting_id":      fittingID,
			"target":          c.Target,
			"note":            c.Note,
			"callout_number":  c.CalloutNumber,
			"zone":            c.Zone,
			"piece_id":        c.PieceId,
			"status":          c.Status,
			"carried_from_id": c.CarriedFromId,
			"created_by":      createdBy,
			"display_order":   i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_change_request", rows); err != nil {
		return fmt.Errorf("failed to insert fitting change requests: %w", err)
	}
	return nil
}

func insertFittingMedia(ctx context.Context, db dependency.DB, fittingID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for i, mid := range mediaIDs {
		rows = append(rows, map[string]any{
			"fitting_id":    fittingID,
			"media_id":      mid,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "fitting_media", rows); err != nil {
		return fmt.Errorf("failed to insert fitting media: %w", err)
	}
	return nil
}

type fittingSizeRow struct {
	FittingID int `db:"fitting_id"`
	entity.FittingSize
}

func (s *Store) sizesByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingSize, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingSize{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingSizeRow](ctx, s.DB, `
		SELECT fitting_id, size_id, fit_note
		FROM fitting_size
		WHERE fitting_id IN (:ids)
		ORDER BY id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting sizes: %w", err)
	}
	out := make(map[int][]entity.FittingSize, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.FittingSize)
	}
	return out, nil
}

type fittingMediaRow struct {
	FittingID int `db:"fitting_id"`
	entity.MediaFull
}

type fittingPatternRow struct {
	FittingID int `db:"fitting_id"`
	entity.FittingPattern
}

type fittingCalloutRow struct {
	FittingID int `db:"fitting_id"`
	entity.FittingCallout
}

func (s *Store) changeRequestsByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingChangeRequest, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingChangeRequest{}, nil
	}
	rows, err := storeutil.QueryListNamed[entity.FittingChangeRequest](ctx, s.DB, `
		SELECT id, fitting_id, target, note, callout_number, zone, piece_id, status, carried_from_id, created_by
		FROM fitting_change_request
		WHERE fitting_id IN (:ids)
		ORDER BY fitting_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting change requests: %w", err)
	}
	out := make(map[int][]entity.FittingChangeRequest, len(ids))
	for _, r := range rows {
		out[r.FittingId] = append(out[r.FittingId], r)
	}
	return out, nil
}

// AddFittingChangeRequest inserts one structured remark item and returns its (stable) id (S26). The
// display_order is appended after any existing items so the fitting's list keeps insertion order.
func (s *Store) AddFittingChangeRequest(ctx context.Context, cr *entity.FittingChangeRequest) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		ord, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(display_order), -1) + 1 FROM fitting_change_request WHERE fitting_id = :fid`,
			map[string]any{"fid": cr.FittingId})
		if err != nil {
			return fmt.Errorf("next change-request order: %w", err)
		}
		params := changeRequestParams(cr)
		params["display_order"] = ord
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO fitting_change_request (fitting_id, target, note, callout_number, zone, piece_id, status, carried_from_id, created_by, display_order)
			VALUES (:fitting_id, :target, :note, :callout_number, :zone, :piece_id, :status, :carried_from_id, :created_by, :display_order)`,
			params)
		if err != nil {
			return fmt.Errorf("insert fitting change request: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateFittingChangeRequest updates one item in place (S26) — its id stays stable, so carried_from_id
// links and the carry-over view survive edits (unlike the fitting full-replace). The fitting_id is not
// reassigned. Returns sql.ErrNoRows when no such item exists.
func (s *Store) UpdateFittingChangeRequest(ctx context.Context, id int, cr *entity.FittingChangeRequest) error {
	params := changeRequestParams(cr)
	params["id"] = id
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE fitting_change_request SET
			target = :target, note = :note, callout_number = :callout_number, zone = :zone,
			piece_id = :piece_id, status = :status, carried_from_id = :carried_from_id
		WHERE id = :id`, params)
	if err != nil {
		return fmt.Errorf("update fitting change request %d: %w", id, err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteFittingChangeRequest deletes one item (S26). A successor's carried_from_id is SET NULL by the
// FK. Returns sql.ErrNoRows when none exists.
func (s *Store) DeleteFittingChangeRequest(ctx context.Context, id int) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`DELETE FROM fitting_change_request WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("delete fitting change request %d: %w", id, err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListOpenFittingChangeRequests returns a style's OPEN structured remarks from earlier rounds — the
// carry-over view (task 2, acceptance E.15). A round is derived from the item's fitting's sample
// (round_number). Items already continued by a later item (their id appears as some carried_from_id)
// are excluded, so only the current tip of each carry chain is shown. before_round > 0 scopes to items
// raised strictly before that round; 0 returns every round's open tips. Product-only fittings (no
// sample, hence no round) are not part of the round spine and are excluded.
func (s *Store) ListOpenFittingChangeRequests(ctx context.Context, techCardID, beforeRound int) ([]entity.FittingChangeRequest, error) {
	params := map[string]any{"tc": techCardID}
	roundFilter := ""
	if beforeRound > 0 {
		roundFilter = " AND s.round_number < :before"
		params["before"] = beforeRound
	}
	rows, err := storeutil.QueryListNamed[entity.FittingChangeRequest](ctx, s.DB, fmt.Sprintf(`
		SELECT cr.id, cr.fitting_id, cr.target, cr.note, cr.callout_number, cr.zone, cr.piece_id,
			cr.status, cr.carried_from_id, cr.created_by, s.round_number
		FROM fitting_change_request cr
		JOIN fitting f ON f.id = cr.fitting_id
		JOIN sample s ON s.id = f.sample_id
		WHERE s.tech_card_id = :tc AND cr.status = 'open'%s
			AND cr.id NOT IN (SELECT carried_from_id FROM fitting_change_request WHERE carried_from_id IS NOT NULL)
		ORDER BY s.round_number, cr.id`, roundFilter), params)
	if err != nil {
		return nil, fmt.Errorf("list open change requests: %w", err)
	}
	return rows, nil
}

func (s *Store) calloutsByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingCallout, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingCallout{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingCalloutRow](ctx, s.DB, `
		SELECT fitting_id, callout_number, note, media_id, pos_x, pos_y
		FROM fitting_callout
		WHERE fitting_id IN (:ids)
		ORDER BY fitting_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting callouts: %w", err)
	}
	out := make(map[int][]entity.FittingCallout, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.FittingCallout)
	}
	return out, nil
}

func (s *Store) patternsByFittingIds(ctx context.Context, ids []int) (map[int][]entity.FittingPattern, error) {
	if len(ids) == 0 {
		return map[int][]entity.FittingPattern{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingPatternRow](ctx, s.DB, `
		SELECT fitting_id, size_id, url, filename, size_bytes
		FROM fitting_pattern
		WHERE fitting_id IN (:ids)
		ORDER BY fitting_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting patterns: %w", err)
	}
	out := make(map[int][]entity.FittingPattern, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.FittingPattern)
	}
	return out, nil
}

func (s *Store) mediaByFittingIds(ctx context.Context, ids []int) (map[int][]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[fittingMediaRow](ctx, s.DB, `
		SELECT fm.fitting_id, m.*
		FROM fitting_media fm
		JOIN media m ON m.id = fm.media_id
		WHERE fm.fitting_id IN (:ids)
		ORDER BY fm.fitting_id, fm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load fitting media: %w", err)
	}
	out := make(map[int][]entity.MediaFull, len(ids))
	for _, r := range rows {
		out[r.FittingID] = append(out[r.FittingID], r.MediaFull)
	}
	return out, nil
}
