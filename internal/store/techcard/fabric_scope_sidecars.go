package techcard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// refuseNewlyUnresolvedPatterns compares the stored and prospective graphs by sheet identity. An
// already-unresolved sheet is grandfathered so the card remains saveable and repairable; every
// other unresolved survivor is a regression and aborts the surrounding transaction.
func refuseNewlyUnresolvedPatterns(before []patternHistoryRow, beforeLines []entity.RollGoodsLine,
	after []patternHistoryRow, afterLines []entity.RollGoodsLine) error {
	grandfathered := make(map[string]bool, len(before)*2)
	for _, row := range before {
		if entity.ResolveFabricScope(row.FabricPurpose.String, row.BomLineKey.String, beforeLines).Live() {
			continue
		}
		for _, key := range patternComparisonKeys(row) {
			grandfathered[key] = true
		}
	}

	var names []string
	seenNames := make(map[string]bool)
	for _, row := range after {
		// An unbound row (legacy PDF, pre-0260 sheet) names no scope, so no BOM edit can orphan it;
		// a new unbound DXF is refused separately in insertTechCardPatterns.
		if strings.TrimSpace(row.FabricPurpose.String) == "" && strings.TrimSpace(row.BomLineKey.String) == "" {
			continue
		}
		if entity.ResolveFabricScope(row.FabricPurpose.String, row.BomLineKey.String, afterLines).Live() {
			continue
		}
		wasUnresolved := false
		for _, key := range patternComparisonKeys(row) {
			wasUnresolved = wasUnresolved || grandfathered[key]
		}
		if wasUnresolved {
			continue
		}
		name := patternHistoryName(row)
		if !seenNames[name] {
			names = append(names, name)
			seenNames[name] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return entity.NewFieldViolation("patterns", "new_unresolved_sheets", strings.Join(names, ", "),
		"bind every named sheet to a live fabric purpose or roll-goods BOM line, or restore the BOM scope it already uses")
}

func patternComparisonKeys(row patternHistoryRow) []string {
	keys := []string{"pair:" + patternHistoryKey(row.SizeId, row.URL)}
	if key := strings.ToUpper(strings.TrimSpace(row.LineKey)); key != "" {
		keys = append(keys, "line:"+key)
	}
	return keys
}

func patternHistoryName(row patternHistoryRow) string {
	for _, candidate := range []string{row.Name.String, row.Filename.String, row.LineKey, row.URL} {
		if name := strings.TrimSpace(candidate); name != "" {
			return name
		}
	}
	return "unnamed sheet"
}

// reconcileMovedFabricScopeSidecars carries scope-derived rows (size index, measured areas) along an
// UNAMBIGUOUS sheet move: every sheet leaving the old scope names one destination, the old scope has
// no sheets left, and the destination holds no rows of that table yet. Every other shape — a split
// into several scopes, a merge into a scope that already has rows — leaves the rows PARKED under the
// old key. Nothing here deletes: a card save is not a measurement, and an operator re-sorting the BOM
// back must get the measured set back with it (REPORT §2.2).
//
// WHY A PARKED SET CANNOT POSE AS ANOTHER SLOT'S DATA. A dead key is read by no line at all
// (FabricScopeReadKeys lists live keys only). A key that is live again is compared against the
// fingerprint the server computes from the sheets that key addresses TODAY (scopeFingerprints,
// PatternSheetFingerprint), and both fingerprints cover sheet identity (line_key, url, version) — the
// area one also the block→piece links. So a parked set reads fresh only when exactly the inputs it was
// measured from address that key again, which is the restore; any other sheet set reads it as stale.
func reconcileMovedFabricScopeSidecars(ctx context.Context, db dependency.DB, techCardID int,
	before []patternHistoryRow, beforeLines []entity.RollGoodsLine,
	after []patternHistoryRow, afterLines []entity.RollGoodsLine) error {
	beforeByKey := make(map[string]patternHistoryRow, len(before))
	for _, row := range before {
		if key := strings.ToUpper(strings.TrimSpace(row.LineKey)); key != "" {
			beforeByKey[key] = row
		}
	}
	afterCounts := make(map[string]int, len(afterLines))
	for _, row := range after {
		scope := entity.FabricScopeIdentity(row.FabricPurpose.String, row.BomLineKey.String, afterLines)
		if scope != "" {
			afterCounts[strings.ToLower(scope)]++
		}
	}
	type move struct {
		from string
		to   map[string]string
	}
	moves := make(map[string]*move)
	for _, row := range after {
		key := strings.ToUpper(strings.TrimSpace(row.LineKey))
		old, ok := beforeByKey[key]
		if !ok || key == "" {
			continue
		}
		newResolved := entity.ResolveFabricScope(row.FabricPurpose.String, row.BomLineKey.String, afterLines)
		if !newResolved.Live() {
			continue
		}
		oldScope := entity.FabricScopeIdentity(old.FabricPurpose.String, old.BomLineKey.String, beforeLines)
		newScope := entity.FabricScopeIdentity(row.FabricPurpose.String, row.BomLineKey.String, afterLines)
		if oldScope == "" || newScope == "" || strings.EqualFold(oldScope, newScope) {
			continue
		}
		fromKey := strings.ToLower(oldScope)
		m := moves[fromKey]
		if m == nil {
			m = &move{from: oldScope, to: make(map[string]string)}
			moves[fromKey] = m
		}
		m.to[strings.ToLower(newScope)] = newScope
	}
	for fromKey, m := range moves {
		if afterCounts[fromKey] != 0 {
			continue
		}
		if len(m.to) != 1 {
			// A split has no truthful single destination: park, never discard.
			continue
		}
		var target string
		for _, target = range m.to {
		}
		if err := rekeyFabricScopeSidecars(ctx, db, techCardID, m.from, target); err != nil {
			return err
		}
	}
	return nil
}

// rekeyFabricScopeSidecars refiles one scope's rows under another key, table by table, and only into
// a vacant destination. An occupied destination wins — it describes that scope's own measurement —
// and the source stays parked under its old key instead of being discarded.
func rekeyFabricScopeSidecars(ctx context.Context, db dependency.DB, techCardID int, from, to string) error {
	for _, table := range []string{"tech_card_pattern_size_index", "tech_card_piece_area"} {
		targetRows, err := storeutil.QueryCountNamed(ctx, db,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE tech_card_id = :card AND scope_key = :scope`, table),
			map[string]any{"card": techCardID, "scope": to})
		if err != nil {
			return fmt.Errorf("count %s target scope %q: %w", table, to, err)
		}
		if targetRows > 0 {
			continue
		}
		if _, err := storeutil.ExecNamedRows(ctx, db,
			fmt.Sprintf(`UPDATE %s SET scope_key = :to WHERE tech_card_id = :card AND scope_key = :from`, table),
			map[string]any{"card": techCardID, "from": from, "to": to}); err != nil {
			return fmt.Errorf("rekey %s scope %q to %q: %w", table, from, to, err)
		}
	}
	return nil
}

type fabricScopeSidecarCleanup struct {
	SizeIndexRows int64
	PieceAreaRows int64
}

// adoptRawLineFabricScopeSidecars moves rows filed under the RAW line key of an ordinary roll-goods
// line that has since been sorted into a purpose onto that line's effective scope
// (entity.LiveFabricScopeKey). Such rows are not dead: a sheet bound to line L keeps naming L after L
// gets purpose «main», reconcileMovedFabricScopeSidecars sees no identity move (main → main), and the
// rows written under L before the sort (or by an older client publishing the raw binding) would
// otherwise sit in a bucket no reader asks for. The destination wins when it already holds rows — the
// same rule as a merge in reconcileMovedFabricScopeSidecars — and the raw source then stays parked
// under its own key (un-sorting the line makes it live again). Sources are visited in sorted order so
// two raw keys folding into one scope resolve deterministically. Never deletes.
func adoptRawLineFabricScopeSidecars(ctx context.Context, db dependency.DB, techCardID int,
	lines []entity.RollGoodsLine) error {
	targets := make(map[string]string)
	for _, table := range []string{"tech_card_pattern_size_index", "tech_card_piece_area"} {
		scopes, err := storeutil.QueryListNamed[struct {
			ScopeKey string `db:"scope_key"`
		}](ctx, db, fmt.Sprintf(`SELECT DISTINCT scope_key FROM %s WHERE tech_card_id = :card`, table),
			map[string]any{"card": techCardID})
		if err != nil {
			return fmt.Errorf("load %s scopes: %w", table, err)
		}
		for _, scope := range scopes {
			if entity.FabricScopeKeyLive(scope.ScopeKey, lines) {
				continue
			}
			if to, ok := entity.LiveFabricScopeKey(scope.ScopeKey, lines); ok {
				targets[scope.ScopeKey] = to
			}
		}
	}
	from := make([]string, 0, len(targets))
	for key := range targets {
		from = append(from, key)
	}
	sort.Strings(from)
	for _, key := range from {
		if err := rekeyFabricScopeSidecars(ctx, db, techCardID, key, targets[key]); err != nil {
			return err
		}
	}
	return nil
}

// cleanupDeadFabricScopeSidecars first adopts raw-line buckets into their line's live scope, then
// deletes only what still resolves to nothing on the card.
//
// ARCHIVE IMPORT ONLY (import.go): the card is brand new and the imported graph is authoritative, so
// a bucket nothing on it resolves to has no earlier state to be restored into. A card save never
// calls this — UpdateTechCard parks such buckets (adoptRawLineFabricScopeSidecars only), because a
// purpose re-sort that is undone must bring its measurements back.
func cleanupDeadFabricScopeSidecars(ctx context.Context, db dependency.DB, techCardID int,
	lines []entity.RollGoodsLine) (fabricScopeSidecarCleanup, error) {
	var out fabricScopeSidecarCleanup
	if err := adoptRawLineFabricScopeSidecars(ctx, db, techCardID, lines); err != nil {
		return out, err
	}
	for _, target := range []struct {
		table string
		rows  *int64
	}{
		{"tech_card_pattern_size_index", &out.SizeIndexRows},
		{"tech_card_piece_area", &out.PieceAreaRows},
	} {
		scopes, err := storeutil.QueryListNamed[struct {
			ScopeKey string `db:"scope_key"`
		}](ctx, db, fmt.Sprintf(`SELECT DISTINCT scope_key FROM %s WHERE tech_card_id = :card`, target.table),
			map[string]any{"card": techCardID})
		if err != nil {
			return out, fmt.Errorf("load %s scopes: %w", target.table, err)
		}
		for _, scope := range scopes {
			if entity.FabricScopeKeyLive(scope.ScopeKey, lines) {
				continue
			}
			deleted, err := storeutil.ExecNamedRows(ctx, db,
				fmt.Sprintf(`DELETE FROM %s WHERE tech_card_id = :card AND scope_key = :scope`, target.table),
				map[string]any{"card": techCardID, "scope": scope.ScopeKey})
			if err != nil {
				return out, fmt.Errorf("delete dead %s scope %q: %w", target.table, scope.ScopeKey, err)
			}
			*target.rows += deleted
		}
	}
	return out, nil
}
