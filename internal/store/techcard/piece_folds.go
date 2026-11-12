package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// PIECE FOLDS (tech_card_piece_fold, 0443) — the store half. The seams' shape (seams.go), another
// payload: the client resolves the line on each size, the server stores it and owns the one fact that
// could be forged — «this fold was drawn on the card's CURRENT patterns». The client never supplies
// the fingerprint: it is the seams' fingerprint over the fold's ONE piece, recomputed on every read.
//
// KEYED, PARTIAL UPSERT; NO lock_version BUMP; the card row is taken FOR UPDATE first and left
// unchanged; a released card refuses. seq is unique per (piece, size_key) over the stored list —
// checked after the write inside the same transaction, so a reorder that swaps two seqs passes.

// pieceFoldRow is one tech_card_piece_fold row as scanned. JSON columns are scanned as bytes.
type pieceFoldRow struct {
	TechCardId        int           `db:"tech_card_id"`
	FoldKey           string        `db:"fold_key"`
	PieceLineKey      string        `db:"piece_line_key"`
	Seq               int           `db:"seq"`
	Kind              string        `db:"kind"`
	Source            string        `db:"source"`
	LineUV            []byte        `db:"line_uv"`
	FlipAt            []byte        `db:"flip_at"`
	EdgeHint          string        `db:"edge_hint"`
	EdgeSamples       []byte        `db:"edge_samples"`
	OffsetMm          float64       `db:"offset_mm"`
	ContourSig        string        `db:"contour_sig"`
	SizeId            sql.NullInt64 `db:"size_id"`
	AnchoredSize      string        `db:"anchored_size"`
	Note              string        `db:"note"`
	SourceFingerprint string        `db:"source_fingerprint"`
	CreatedBy         string        `db:"created_by"`
	CreatedAt         time.Time     `db:"created_at"`
	UpdatedBy         string        `db:"updated_by"`
	UpdatedAt         time.Time     `db:"updated_at"`
}

// GetTechCardPieceFolds returns a card's fold lines in (piece, seq) order, staleness resolved. A card
// with no rows is an empty list, not an error.
func (s *Store) GetTechCardPieceFolds(ctx context.Context, techCardID int) ([]entity.TechCardPieceFold, error) {
	return listTechCardPieceFolds(ctx, s.DB, techCardID)
}

func listTechCardPieceFolds(ctx context.Context, db dependency.DB, techCardID int) ([]entity.TechCardPieceFold, error) {
	rows, err := storeutil.QueryListNamed[pieceFoldRow](ctx, db, `
		SELECT tech_card_id, fold_key, piece_line_key, seq, kind, source, line_uv, flip_at, edge_hint,
		       edge_samples, offset_mm, contour_sig, size_id, anchored_size, note, source_fingerprint,
		       created_by, created_at, updated_by, updated_at
		FROM tech_card_piece_fold
		WHERE tech_card_id = :id
		ORDER BY piece_line_key, seq, id`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load piece folds of tech card %d: %w", techCardID, err)
	}
	if len(rows) == 0 {
		return []entity.TechCardPieceFold{}, nil
	}
	src, err := loadSeamSources(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	out := make([]entity.TechCardPieceFold, 0, len(rows))
	for _, r := range rows {
		fold := entity.TechCardPieceFold{
			TechCardPieceFoldInput: entity.TechCardPieceFoldInput{
				FoldKey:      r.FoldKey,
				PieceLineKey: r.PieceLineKey,
				Seq:          r.Seq,
				Kind:         entity.TechCardPieceFoldKind(r.Kind),
				Source:       entity.TechCardPieceFoldSource(r.Source),
				EdgeHint:     r.EdgeHint,
				OffsetMm:     r.OffsetMm,
				ContourSig:   r.ContourSig,
				AnchoredSize: r.AnchoredSize,
				Note:         r.Note,
			},
			TechCardId:        r.TechCardId,
			SizeId:            r.SizeId,
			SourceFingerprint: r.SourceFingerprint,
			CreatedBy:         r.CreatedBy,
			CreatedAt:         r.CreatedAt,
			UpdatedBy:         r.UpdatedBy,
			UpdatedAt:         r.UpdatedAt,
		}
		// A column that does not decode is a corrupted row, not a reason to fail the whole card read
		// (this runs inside GetTechCard): it comes back stale so the screen offers to redo or delete it.
		errLine := json.Unmarshal(r.LineUV, &fold.Line)
		errFlip := json.Unmarshal(r.FlipAt, &fold.FlipAt)
		errEdge := json.Unmarshal(r.EdgeSamples, &fold.EdgeSamples)
		fold.Stale = errLine != nil || errFlip != nil || errEdge != nil ||
			!src.fresh(pieceFoldKeys(fold.PieceLineKey), r.SourceFingerprint)
		out = append(out, fold)
	}
	return out, nil
}

// pieceFoldKeys is the piece set a fold's fingerprint covers: its one piece, normalised as
// TechCardSeamInput.PieceLineKeys normalises.
func pieceFoldKeys(pieceLineKey string) []string {
	return []string{strings.ToUpper(strings.TrimSpace(pieceLineKey))}
}

// UpsertTechCardPieceFolds inserts or replaces the named folds whole and returns the card's full list.
func (s *Store) UpsertTechCardPieceFolds(ctx context.Context, in entity.TechCardPieceFoldsWrite) ([]entity.TechCardPieceFold, int, error) {
	// Re-validated here, not only trusted from the handler: the store is the last gate before the
	// columns' JSON format.
	if err := entity.ValidateTechCardPieceFoldsWrite(&in); err != nil {
		return nil, 0, err
	}
	var out []entity.TechCardPieceFold
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// THE CARD ROW LOCK — FIRST ACTION, BEFORE ANY READ (seams.go explains why).
		if err := lockTechCardRow(ctx, db, in.TechCardId); err != nil {
			return err
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, in.TechCardId); err != nil {
			return err
		}
		if err := writeTechCardPieceFoldsTx(ctx, db, in.TechCardId, in.Folds, in.By, pieceFoldWriteOpts{}); err != nil {
			return err
		}
		// The echo is read in the same transaction. Assigned, never appended — txFunc may retry.
		list, err := listTechCardPieceFolds(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, len(in.Folds), nil
}

// ReplaceTechCardPieceFolds deletes and upserts folds atomically, then returns the card's full list.
func (s *Store) ReplaceTechCardPieceFolds(ctx context.Context, in entity.TechCardPieceFoldsReplace) ([]entity.TechCardPieceFold, int, int, error) {
	if err := entity.ValidateTechCardPieceFoldsReplace(&in); err != nil {
		return nil, 0, 0, err
	}
	keys, err := normalizeTechCardPieceFoldDeleteKeys(in.DeleteFoldKeys, "delete_fold_keys")
	if err != nil {
		return nil, 0, 0, err
	}
	var (
		out     []entity.TechCardPieceFold
		deleted int
	)
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// THE CARD ROW LOCK — FIRST ACTION, BEFORE ANY READ (seams.go explains why).
		if err := lockTechCardRow(ctx, db, in.TechCardId); err != nil {
			return err
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, in.TechCardId); err != nil {
			return err
		}
		n, err := deleteTechCardPieceFoldsTx(ctx, db, in.TechCardId, keys)
		if err != nil {
			return err
		}
		// This helper writes every row before judging the result-wide seq, count and step-order rules.
		if err := writeTechCardPieceFoldsTx(ctx, db, in.TechCardId, in.Folds, in.By, pieceFoldWriteOpts{}); err != nil {
			return err
		}
		list, err := listTechCardPieceFolds(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		out, deleted = list, n
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return out, len(in.Folds), deleted, nil
}

// pieceFoldWriteOpts is what differs when a fold write is not a live RPC.
type pieceFoldWriteOpts struct {
	// stale: fold keys written with seamFingerprintStale instead of today's fingerprint. An archive
	// import brings a fold the source read as stale, and it must not land as «drawn on these patterns».
	stale map[string]bool
	// restamp: the rows are the ones this transaction already wrote, with the same piece and seq, and
	// only their fingerprint is taken again (restampRestoredPieceFolds). The step links were settled
	// against exactly these rows, so the step-order rule they were judged by cannot have moved; judging
	// it again in another walk order must not fail the restore.
	restamp bool
}

// insertRestoredPieceFolds re-creates the snapshot folds of the pieces a restore brings back
// (TechCardInsert.RestorePieceFolds, entity.PlanTechCardPieceFoldsRestore). It runs inside the card
// save, after the piece upsert has dropped the folds of the pieces this save deletes and before the
// steps' fold links settle against the table, so the snapshot's links to these rows survive. A row
// that cannot land is skipped and counted, never refused. A re-created key leaves dropped: the links
// below must keep it although the piece it sat on today is gone.
func insertRestoredPieceFolds(ctx context.Context, db dependency.DB, techCardID int, tc *entity.TechCardInsert, dropped map[string]bool) error {
	r := tc.RestorePieceFolds
	if r == nil {
		return nil
	}
	r.Recreated = nil // txFunc may retry the transaction: never carry rows of an aborted attempt
	if len(r.Folds) == 0 {
		return nil
	}
	// The store is the last gate before the JSON columns: each row is validated again, alone.
	valid := make([]entity.TechCardPieceFoldInput, 0, len(r.Folds))
	invalid := 0
	for _, f := range r.Folds {
		one := entity.TechCardPieceFoldsWrite{TechCardId: techCardID, By: tc.UpdatedBy, Folds: []entity.TechCardPieceFoldInput{f}}
		if err := entity.ValidateTechCardPieceFoldsWrite(&one); err != nil {
			invalid++
			continue
		}
		valid = append(valid, one.Folds[0])
	}
	pieces, err := storeutil.QueryScalarListNamed[string](ctx, db, `
		SELECT line_key FROM tech_card_piece
		WHERE tech_card_id = :id AND line_key IS NOT NULL`, map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load piece keys of tech card %d: %w", techCardID, err)
	}
	held, err := storeutil.QueryListNamed[struct {
		FoldKey      string `db:"fold_key"`
		PieceLineKey string `db:"piece_line_key"`
	}](ctx, db, `
		SELECT fold_key, piece_line_key FROM tech_card_piece_fold WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load folds of tech card %d: %w", techCardID, err)
	}
	today := make([]entity.TechCardPieceFoldHeld, 0, len(held))
	for _, h := range held {
		today = append(today, entity.TechCardPieceFoldHeld{FoldKey: h.FoldKey, PieceLineKey: h.PieceLineKey})
	}
	plan, skipped := entity.PlanTechCardPieceFoldsRestore(valid, pieces, today)
	if invalid > 0 {
		if skipped == nil {
			skipped = map[string]int{}
		}
		skipped[entity.TechCardFoldRestoreInvalid] += invalid
	}
	if len(skipped) > 0 {
		attrs := []any{slog.Int("tech_card_id", techCardID), slog.Int("recreated", len(plan))}
		for reason, n := range skipped {
			attrs = append(attrs, slog.Int("skipped_"+reason, n))
		}
		slog.Default().InfoContext(ctx, "restore re-creates folds of the pieces it brings back; some rows stay out", attrs...)
	}
	if len(plan) == 0 {
		return nil
	}
	if err := writeTechCardPieceFoldsTx(ctx, db, techCardID, plan, tc.UpdatedBy, pieceFoldWriteOpts{stale: r.Stale}); err != nil {
		return err
	}
	for _, f := range plan {
		delete(dropped, entity.NormalizeTechCardSeamKey(f.FoldKey)) // dropped is keyed normalised (materials.go)
	}
	r.Recreated = plan
	return nil
}

// restampRestoredPieceFolds takes the fingerprint of the re-created folds again once the restore has
// put the snapshot's sheets and scope data in place: the early write (insertRestoredPieceFolds) ran
// before them, and a fingerprint over that half-restored geometry would read stale, or fresh, for the
// wrong reason. The same write, the same rows; a key the snapshot read as stale stays stale.
func restampRestoredPieceFolds(ctx context.Context, db dependency.DB, techCardID int, tc *entity.TechCardInsert) error {
	r := tc.RestorePieceFolds
	if r == nil || len(r.Recreated) == 0 {
		return nil
	}
	return writeTechCardPieceFoldsTx(ctx, db, techCardID, r.Recreated, tc.UpdatedBy,
		pieceFoldWriteOpts{stale: r.Stale, restamp: true})
}

// writeTechCardPieceFoldsTx IS THE FOLD WRITE; the caller holds the card row and checked it is mutable.
func writeTechCardPieceFoldsTx(ctx context.Context, db dependency.DB, techCardID int, folds []entity.TechCardPieceFoldInput, by string, opts pieceFoldWriteOpts) error {
	pieces, err := storeutil.QueryScalarListNamed[string](ctx, db, `
		SELECT line_key FROM tech_card_piece
		WHERE tech_card_id = :id AND line_key IS NOT NULL`, map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load piece keys of tech card %d: %w", techCardID, err)
	}
	mine := make(map[string]bool, len(pieces))
	for _, p := range pieces {
		mine[strings.ToUpper(strings.TrimSpace(p))] = true
	}
	for i, f := range folds {
		if !mine[strings.ToUpper(f.PieceLineKey)] {
			return entity.NewFieldViolation(fmt.Sprintf("folds[%d].piece_line_key", i),
				"piece_not_on_card", f.PieceLineKey,
				"the fold names a piece this card does not have — reload the card; the piece may have been removed")
		}
	}
	// The per-card cap counts what the card WILL hold: existing rows not named here + this call.
	existing, err := storeutil.QueryScalarListNamed[string](ctx, db, `
		SELECT fold_key FROM tech_card_piece_fold WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load fold keys of tech card %d: %w", techCardID, err)
	}
	named := make(map[string]bool, len(folds))
	for _, f := range folds {
		named[f.FoldKey] = true
	}
	total := len(folds)
	for _, k := range existing {
		if !named[entity.NormalizeTechCardSeamKey(k)] {
			total++
		}
	}
	if total > entity.TechCardPieceFoldMaxRowsPerCard {
		return entity.NewFieldViolation("folds", "too_many", "",
			fmt.Sprintf("a card holds at most %d folds; delete orphan or obsolete rows first", entity.TechCardPieceFoldMaxRowsPerCard))
	}
	src, err := loadSeamSources(ctx, db, techCardID)
	if err != nil {
		return err
	}
	for _, f := range folds {
		line, err := json.Marshal(f.Line)
		if err != nil {
			return fmt.Errorf("encode fold %s line: %w", f.FoldKey, err)
		}
		flip, err := json.Marshal(f.FlipAt)
		if err != nil {
			return fmt.Errorf("encode fold %s flip_at: %w", f.FoldKey, err)
		}
		edge := f.EdgeSamples
		if edge == nil {
			edge = []entity.TechCardSeamSample{} // '[]', never 'null' — the column is NOT NULL JSON
		}
		edgeJSON, err := json.Marshal(edge)
		if err != nil {
			return fmt.Errorf("encode fold %s edge_samples: %w", f.FoldKey, err)
		}
		fingerprint := src.fingerprint(pieceFoldKeys(f.PieceLineKey))
		if opts.stale[f.FoldKey] {
			fingerprint = seamFingerprintStale
		}
		// size_id stays NULL (v1 has no per-size rows), so the key is (card, fold_key, 0).
		// created_* survive a replace; updated_* name the latest write.
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_piece_fold
				(tech_card_id, fold_key, piece_line_key, seq, kind, source, line_uv, flip_at, edge_hint,
				 edge_samples, offset_mm, contour_sig, anchored_size, note, source_fingerprint,
				 created_by, updated_by)
			VALUES
				(:tech_card_id, :fold_key, :piece_line_key, :seq, :kind, :source, :line_uv, :flip_at,
				 :edge_hint, :edge_samples, :offset_mm, :contour_sig, :anchored_size, :note,
				 :source_fingerprint, :by, :by)
			ON DUPLICATE KEY UPDATE
				piece_line_key = VALUES(piece_line_key), seq = VALUES(seq), kind = VALUES(kind),
				source = VALUES(source), line_uv = VALUES(line_uv), flip_at = VALUES(flip_at),
				edge_hint = VALUES(edge_hint), edge_samples = VALUES(edge_samples),
				offset_mm = VALUES(offset_mm), contour_sig = VALUES(contour_sig),
				anchored_size = VALUES(anchored_size), note = VALUES(note),
				source_fingerprint = VALUES(source_fingerprint), updated_by = VALUES(updated_by),
				updated_at = CURRENT_TIMESTAMP`,
			map[string]any{
				"tech_card_id":       techCardID,
				"fold_key":           f.FoldKey,
				"piece_line_key":     f.PieceLineKey,
				"seq":                f.Seq,
				"kind":               string(f.Kind),
				"source":             string(f.Source),
				"line_uv":            string(line),
				"flip_at":            string(flip),
				"edge_hint":          f.EdgeHint,
				"edge_samples":       string(edgeJSON),
				"offset_mm":          f.OffsetMm,
				"contour_sig":        f.ContourSig,
				"anchored_size":      f.AnchoredSize,
				"note":               f.Note,
				"source_fingerprint": fingerprint,
				"by":                 by,
			}); err != nil {
			return fmt.Errorf("upsert fold %s of tech card %d: %w", f.FoldKey, techCardID, err)
		}
	}
	if err := requireUniqueFoldSeqTx(ctx, db, techCardID, folds); err != nil {
		return err
	}
	if opts.restamp {
		return nil
	}
	return requireFoldLinkOrderTx(ctx, db, techCardID, folds)
}

// requireUniqueFoldSeqTx refuses a write after which two folds of one piece share a seq — judged on
// the whole stored list, inside the write's transaction (the error rolls the write back).
func requireUniqueFoldSeqTx(ctx context.Context, db dependency.DB, techCardID int, folds []entity.TechCardPieceFoldInput) error {
	type clash struct {
		PieceLineKey string `db:"piece_line_key"`
		Seq          int    `db:"seq"`
	}
	clashes, err := storeutil.QueryListNamed[clash](ctx, db, `
		SELECT piece_line_key, seq FROM tech_card_piece_fold
		WHERE tech_card_id = :id
		GROUP BY piece_line_key, seq, size_key
		HAVING COUNT(*) > 1
		ORDER BY piece_line_key, seq
		LIMIT 1`, map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("check fold order of tech card %d: %w", techCardID, err)
	}
	if len(clashes) == 0 {
		return nil
	}
	c := clashes[0]
	at := "folds"
	for i, f := range folds {
		if strings.EqualFold(f.PieceLineKey, c.PieceLineKey) && f.Seq == c.Seq {
			at = fmt.Sprintf("folds[%d].seq", i)
			break
		}
	}
	return entity.NewFieldViolation(at, "duplicate", fmt.Sprintf("%d", c.Seq),
		"another fold of this piece already has this seq — reload the card, or send both folds renumbered")
}

// DeleteTechCardPieceFolds removes folds by key and returns the card's full list and the removed count.
func (s *Store) DeleteTechCardPieceFolds(ctx context.Context, techCardID int, foldKeys []string) ([]entity.TechCardPieceFold, int, error) {
	if techCardID <= 0 {
		return nil, 0, entity.NewFieldViolation("tech_card_id", "required", "", "")
	}
	if len(foldKeys) == 0 {
		return nil, 0, entity.NewFieldViolation("fold_keys", "empty", "", "name at least one fold_key")
	}
	if len(foldKeys) > entity.TechCardPieceFoldMaxRowsPerCard {
		return nil, 0, entity.NewFieldViolation("fold_keys", "too_many", "",
			fmt.Sprintf("at most %d keys per call", entity.TechCardPieceFoldMaxRowsPerCard))
	}
	keys, err := normalizeTechCardPieceFoldDeleteKeys(foldKeys, "fold_keys")
	if err != nil {
		return nil, 0, err
	}
	var (
		out     []entity.TechCardPieceFold
		deleted int
	)
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := lockTechCardRow(ctx, db, techCardID); err != nil {
			return err
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, techCardID); err != nil {
			return err
		}
		n, err := deleteTechCardPieceFoldsTx(ctx, db, techCardID, keys)
		if err != nil {
			return err
		}
		list, err := listTechCardPieceFolds(ctx, db, techCardID)
		if err != nil {
			return err
		}
		out, deleted = list, n
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, deleted, nil
}

func normalizeTechCardPieceFoldDeleteKeys(foldKeys []string, field string) ([]string, error) {
	keys := make([]string, 0, len(foldKeys))
	seen := map[string]bool{}
	for i, k := range foldKeys {
		k = entity.NormalizeTechCardSeamKey(k)
		if !entity.ValidTechCardSeamKey(k) {
			return nil, entity.NewFieldViolation(fmt.Sprintf("%s[%d]", field, i), "invalid", "", "a 26-char ULID")
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// deleteTechCardPieceFoldsTx removes folds and their step links. The caller holds the card row lock
// and has already checked that the card is mutable.
func deleteTechCardPieceFoldsTx(ctx context.Context, db dependency.DB, techCardID int, keys []string) (int, error) {
	deleted := 0
	for _, k := range keys {
		affected, err := storeutil.ExecNamedRows(ctx, db, `
			DELETE FROM tech_card_piece_fold WHERE tech_card_id = :id AND fold_key = :k`,
			map[string]any{"id": techCardID, "k": k})
		if err != nil {
			return 0, fmt.Errorf("delete fold %s of tech card %d: %w", k, techCardID, err)
		}
		deleted += int(affected)
		// The steps let go of it in this same transaction: a step never names a fold that is gone.
		if err := unlinkFoldFromOperationsTx(ctx, db, techCardID, k); err != nil {
			return 0, err
		}
	}
	return deleted, nil
}
