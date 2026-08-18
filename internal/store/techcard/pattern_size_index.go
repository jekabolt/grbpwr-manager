package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ИНДЕКС РАЗМЕРОВ ВЫКРОЕК (Ф6.3, 0280) — the store half.
//
// The whole safety property of this table is that THE CLIENT NEVER SUPPLIES THE FINGERPRINT. It
// sends the tokens it parsed and the list of sheets it parsed them from; the server looks the sheets
// up in its own tech_card_size_pattern rows, refuses a list that does not match the scope's current
// membership, and computes the fingerprint from what IT found. So «I parsed exactly these files»
// cannot be forged in the dangerous direction (claiming coverage that is not there), and re-uploading
// any sheet moves its url/version, breaks the stored fingerprint and turns the index back into an
// honest «no verdict» with nobody having to notice.

// patternSheetsQuery loads a card's sheets with both halves of the binding scope. The resolution
// («purpose first, else the legacy line») is NOT done in SQL: entity.FabricScopeKey is the ONE place
// that rule lives, and a COALESCE here would be a second copy that drifts the day somebody clears a
// назначение on the BOM tab.
//
// Its consumers bucket the rows through entity.FabricScopeIdentity, not through FabricScopeKey: the
// key a sheet is FILED under and the ТКАНЬ it addresses are two different questions, and a JOIN onto
// tech_card_bom_item here to answer the second one would be exactly the third copy the paragraph
// above forbids. Both answers stay in Go, over the lines loadRollGoodsLines returns.
const patternSheetsQuery = `
	SELECT line_key, url, version,
	       COALESCE(bom_line_key, '') AS bom_line_key,
	       COALESCE(fabric_purpose, '') AS fabric_purpose
	FROM tech_card_size_pattern
	WHERE tech_card_id = :id`

type patternSheetRow struct {
	LineKey       string `db:"line_key"`
	URL           string `db:"url"`
	Version       int    `db:"version"`
	BomLineKey    string `db:"bom_line_key"`
	FabricPurpose string `db:"fabric_purpose"`
}

// GetTechCardPatternSizeIndex returns every stored scope row of a card, keyed by scope_key.
//
// A card with no rows is neither an error nor an empty verdict: the reader turns a missing key into
// «nobody has run the audit», which is a different sentence from «the files carry no size coding»
// and leads to a different action.
func (s *Store) GetTechCardPatternSizeIndex(ctx context.Context, techCardID int) (map[string]entity.PatternSizeIndexRow, error) {
	rows, err := storeutil.QueryListNamed[entity.PatternSizeIndexRow](ctx, s.DB, `
		SELECT tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by, parsed_at
		FROM tech_card_pattern_size_index
		WHERE tech_card_id = :id`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load pattern size index of tech card %d: %w", techCardID, err)
	}
	out := make(map[string]entity.PatternSizeIndexRow, len(rows))
	for _, r := range rows {
		out[r.ScopeKey] = r
	}
	return out, nil
}

// PutTechCardPatternSizeIndex stores one scope's parsed size tokens and returns what it recorded.
//
// It runs in a transaction because the sheet set it fingerprints and the row it writes must be ONE
// snapshot: reading the sheets, then having somebody replace one, then writing a fingerprint of the
// OLD set would produce an index claiming to be current for files it never saw — exactly the forgery
// the fingerprint exists to prevent, reached by accident instead of by malice.
func (s *Store) PutTechCardPatternSizeIndex(ctx context.Context, in entity.PatternSizeIndexWrite) (entity.PatternSizeIndexResult, error) {
	var out entity.PatternSizeIndexResult
	scopeKey := strings.TrimSpace(in.ScopeKey)
	if scopeKey == "" {
		return out, entity.NewFieldViolation("scope_key", "required", "",
			"name the fabric scope: its purpose, or the BOM line's line_key when the card has not been sorted")
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		lines, err := loadRollGoodsLines(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		// The scope's CURRENT membership, bucketed by the ТКАНЬ each sheet addresses — the same
		// grouping the panel shows and the same key dto.patternScopesOfCard reads this row back
		// under. Bucketing by what the sheet itself names would strand every sheet of a card the
		// moment its BOM line got a назначение, and the audit would refuse «в этом скоупе нет
		// листов» about files sitting right there.
		sheetsByScope, err := scopeSheetRefs(ctx, db, in.TechCardId, lines)
		if err != nil {
			return err
		}
		mine := sheetsByScope[scopeKey]
		if len(mine) == 0 {
			return entity.NewFieldViolation("scope_key", "scope_has_no_sheets", scopeKey,
				"this fabric scope carries no pattern sheets on the server — upload them, or check that the scope key matches the card's purpose / line_key")
		}
		// THE MISMATCH REFUSAL. An index computed over the wrong files is worse than no index: it
		// answers confidently about sheets nobody parsed. BOTH directions are refused — a client that
		// parsed fewer sheets than the scope holds has not seen the whole set (and deriveBlockSizes is
		// not compositional, so a partial parse gives a DIFFERENT answer, not a subset of one), and one
		// that names a sheet the scope does not hold is describing a different card.
		if diff := sheetSetDiff(mine, in.SheetLineKeys); diff != "" {
			return entity.NewFieldViolation("sheet_line_keys", "sheet_set_mismatch", diff,
				"re-run “⌕ sizes in the files” — the scope's sheets changed between the parse and this call, and an index built over a different set of files would answer for files nobody read")
		}
		fingerprint := entity.PatternSheetFingerprint(mine)
		tokens := entity.NormalizeSizeTokens(in.SizeTokens)
		blob, err := json.Marshal(tokens)
		if err != nil {
			return fmt.Errorf("encode size tokens: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_pattern_size_index
				(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by)
			VALUES (:card, :scope, :fp, :tokens, :by)
			ON DUPLICATE KEY UPDATE
				sheet_fingerprint = VALUES(sheet_fingerprint),
				size_tokens       = VALUES(size_tokens),
				parsed_by         = VALUES(parsed_by)`,
			map[string]any{
				"card": in.TechCardId, "scope": scopeKey, "fp": fingerprint,
				"tokens": string(blob), "by": in.ParsedBy,
			}); err != nil {
			return fmt.Errorf("write pattern size index: %w", err)
		}
		sizeRows, err := storeutil.QueryListNamed[struct {
			SizeId int `db:"size_id"`
		}](ctx, db, `SELECT size_id FROM tech_card_size WHERE tech_card_id = :id`,
			map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load size range of tech card %d: %w", in.TechCardId, err)
		}
		out.SheetFingerprint = fingerprint
		out.StoredTokens = tokens
		out.CardSizeIds = make([]int, 0, len(sizeRows))
		for _, r := range sizeRows {
			out.CardSizeIds = append(out.CardSizeIds, r.SizeId)
		}
		return nil
	})
	if err != nil {
		return entity.PatternSizeIndexResult{}, err
	}
	return out, nil
}

// sheetSetDiff returns "" when the two sets of line keys are the same, and a readable description of
// the difference otherwise. Comparison is by SET, not by order: the client walks its own list and
// that order is not a fact about the card.
func sheetSetDiff(server []entity.PatternSheetRef, client []string) string {
	want := make(map[string]bool, len(server))
	for _, s := range server {
		want[strings.ToUpper(strings.TrimSpace(s.LineKey))] = true
	}
	got := make(map[string]bool, len(client))
	for _, c := range client {
		if k := strings.ToUpper(strings.TrimSpace(c)); k != "" {
			got[k] = true
		}
	}
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "not parsed: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "not in this scope: "+strings.Join(extra, ", "))
	}
	return strings.Join(parts, "; ")
}
