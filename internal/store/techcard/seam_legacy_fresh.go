package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// LEGACY STAMPS AND A REGENERATED FUSING SHEET (prod 10.11: «кукла вся сломалась»).
//
// #183 took the interlining scopes out of the seam/fold fingerprint and kept rows stamped in the old,
// interlining-inclusive form fresh while that form still matched. It matched only until the fusing
// DXF was regenerated: the new interlining sheet (url, version) moved the legacy fingerprint, the row
// matched neither form and read stale — every seam and fold of every fused piece at once, although no
// shell outline moved. The doll skips stale rows, so waistbands, yokes and flaps fell off.
//
// A legacy stamp is therefore also fresh when it equals the legacy fingerprint re-computed with ONE
// interlining sheet put back to a ref it held before (tech_card_size_pattern_history). The match is a
// hash equality over every other source — shell sheets and every block link — so it proves the row
// was decided on today's shell. A shell re-upload, a moved link, a restored-stale stamp never match.

// loadInterliningSheetHistory returns, per sheet line key that only the legacy (interlining-inclusive)
// scopes hold, the refs it held before. Empty when the card has no interlining sheet.
func loadInterliningSheetHistory(ctx context.Context, db dependency.DB, techCardID int, src *seamSources) (map[string][]entity.PatternSheetRef, error) {
	cloth := map[string]bool{}
	for _, refs := range src.sheets {
		for _, r := range refs {
			cloth[r.LineKey] = true
		}
	}
	var keys []string
	seen := map[string]bool{}
	for scope, refs := range src.legacySheets {
		if _, kept := src.sheets[scope]; kept {
			continue
		}
		for _, r := range refs {
			if r.LineKey != "" && !cloth[r.LineKey] && !seen[r.LineKey] {
				seen[r.LineKey] = true
				keys = append(keys, r.LineKey)
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		LineKey string `db:"line_key"`
		URL     string `db:"url"`
		Version int    `db:"version"`
	}](ctx, db, `
		SELECT DISTINCT line_key, url, version
		FROM tech_card_size_pattern_history
		WHERE tech_card_id = :id AND line_key IN (:keys)`,
		map[string]any{"id": techCardID, "keys": keys})
	if err != nil {
		return nil, fmt.Errorf("load interlining sheet history of tech card %d: %w", techCardID, err)
	}
	out := map[string][]entity.PatternSheetRef{}
	for _, r := range rows {
		out[r.LineKey] = append(out[r.LineKey], entity.PatternSheetRef{LineKey: r.LineKey, URL: r.URL, Version: r.Version})
	}
	return out, nil
}

// legacyFresh reports whether a stored stamp is the interlining-inclusive form over today's sources,
// or over today's sources with one interlining sheet at a ref it held before.
func (s *seamSources) legacyFresh(pieceKeys []string, stored string) bool {
	if stored == "" || stored == seamFingerprintStale {
		return false
	}
	if stored == s.legacyFingerprint(pieceKeys) {
		return true
	}
	for lineKey, olds := range s.interliningHistory {
		for _, old := range olds {
			if s.legacyFingerprintWith(pieceKeys, lineKey, old) == stored {
				return true
			}
		}
	}
	return false
}

// legacyFingerprintWith is legacyFingerprint with every ref of sheet lineKey replaced by old.
func (s *seamSources) legacyFingerprintWith(pieceKeys []string, lineKey string, old entity.PatternSheetRef) string {
	memoKey := fmt.Sprintf("%s\x01%s\x01%d", lineKey, old.URL, old.Version)
	if s.historyMemo == nil {
		s.historyMemo = map[string]map[string]string{}
	}
	memo := s.historyMemo[memoKey]
	if memo == nil {
		memo = map[string]string{}
		s.historyMemo[memoKey] = memo
	}
	sheets := make(map[string][]entity.PatternSheetRef, len(s.legacySheets))
	for scope, refs := range s.legacySheets {
		cp := make([]entity.PatternSheetRef, len(refs))
		for i, r := range refs {
			if r.LineKey == lineKey {
				r = old
			}
			cp[i] = r
		}
		sheets[scope] = cp
	}
	return s.fingerprintOver(pieceKeys, sheets, s.legacyBlocks, memo)
}

// RestampLegacySeamFingerprints re-stamps, in today's form, every seam and fold row whose stamp is a
// legacy one that still reads fresh (legacyFresh). The decision does not change — the row read fresh
// before and reads fresh after — but the stamp no longer depends on the interlining sheet or on its
// history being retained. Rows that read stale (shell moved, restored-stale, undecodable) and rows
// already in today's form are not touched. Idempotent: a second run finds nothing.
func (s *Store) RestampLegacySeamFingerprints(ctx context.Context) (seams, folds int, err error) {
	ids, err := storeutil.QueryScalarListNamed[int](ctx, s.DB, `
		SELECT tech_card_id FROM tech_card_seam
		UNION SELECT tech_card_id FROM tech_card_piece_fold`, map[string]any{})
	if err != nil {
		return 0, 0, fmt.Errorf("list cards with seams or folds: %w", err)
	}
	for _, id := range ids {
		ns, nf, cErr := restampLegacyCard(ctx, s.DB, id)
		if cErr != nil {
			return seams, folds, cErr
		}
		seams += ns
		folds += nf
	}
	return seams, folds, nil
}

func restampLegacyCard(ctx context.Context, db dependency.DB, techCardID int) (int, int, error) {
	src, err := loadSeamSources(ctx, db, techCardID)
	if err != nil {
		return 0, 0, err
	}
	seamRows, err := storeutil.QueryListNamed[struct {
		Id                int64  `db:"id"`
		SideA             []byte `db:"side_a"`
		SideB             []byte `db:"side_b"`
		SourceFingerprint string `db:"source_fingerprint"`
	}](ctx, db, `
		SELECT id, side_a, side_b, source_fingerprint FROM tech_card_seam WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return 0, 0, fmt.Errorf("load seam stamps of tech card %d: %w", techCardID, err)
	}
	ns := 0
	for _, r := range seamRows {
		var seam entity.TechCardSeam
		if json.Unmarshal(r.SideA, &seam.SideA) != nil || json.Unmarshal(r.SideB, &seam.SideB) != nil {
			continue
		}
		keys := seam.PieceLineKeys()
		cur := src.fingerprint(keys)
		if r.SourceFingerprint == cur || !src.legacyFresh(keys, r.SourceFingerprint) {
			continue
		}
		n, uErr := restampRow(ctx, db, "tech_card_seam", r.Id, r.SourceFingerprint, cur)
		if uErr != nil {
			return ns, 0, uErr
		}
		ns += n
	}
	foldRows, err := storeutil.QueryListNamed[struct {
		Id                int64  `db:"id"`
		PieceLineKey      string `db:"piece_line_key"`
		SourceFingerprint string `db:"source_fingerprint"`
	}](ctx, db, `
		SELECT id, piece_line_key, source_fingerprint FROM tech_card_piece_fold WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return ns, 0, fmt.Errorf("load fold stamps of tech card %d: %w", techCardID, err)
	}
	nf := 0
	for _, r := range foldRows {
		keys := pieceFoldKeys(r.PieceLineKey)
		cur := src.fingerprint(keys)
		if r.SourceFingerprint == cur || !src.legacyFresh(keys, r.SourceFingerprint) {
			continue
		}
		n, uErr := restampRow(ctx, db, "tech_card_piece_fold", r.Id, r.SourceFingerprint, cur)
		if uErr != nil {
			return ns, nf, uErr
		}
		nf += n
	}
	if ns+nf > 0 {
		slog.Default().InfoContext(ctx, "re-stamped legacy seam fingerprints",
			slog.Int("tech_card_id", techCardID), slog.Int("seams", ns), slog.Int("folds", nf))
	}
	return ns, nf, nil
}

// restampRow writes the new stamp only if the row still carries the one it was judged by, and keeps
// updated_at: the decision did not change, only its proof.
func restampRow(ctx context.Context, db dependency.DB, table string, id int64, old, cur string) (int, error) {
	if !strings.HasPrefix(table, "tech_card_") {
		return 0, fmt.Errorf("restamp: unexpected table %q", table)
	}
	res, err := db.ExecContext(ctx, `UPDATE `+table+`
		SET source_fingerprint = ?, updated_at = updated_at
		WHERE id = ? AND source_fingerprint = ?`, cur, id, old)
	if err != nil {
		return 0, fmt.Errorf("restamp %s %d: %w", table, id, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
