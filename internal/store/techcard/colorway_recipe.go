package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type recipeUsageSlot struct {
	BomItemID      int64
	BomItemIDValid bool
	PieceID        int64
	PieceIDValid   bool
}

type recipeUsagePinSlot struct {
	recipeUsageSlot
	Placement string
}

type recipeUsagePinRow struct {
	BomItemID  sql.NullInt64  `db:"bom_item_id"`
	PieceID    sql.NullInt64  `db:"piece_id"`
	Placement  sql.NullString `db:"placement"`
	MaterialID sql.NullInt64  `db:"material_id"`
	// Provenance triple, carried across presence-less writes exactly like the material pin:
	// a stale client that predates consumption_source must not silently reset a marker-sourced
	// norm to 'manual' (that would re-enable the wastage gross-up and shift costing).
	ConsumptionSource string              `db:"consumption_source"`
	WasteSelvedgePct  decimal.NullDecimal `db:"waste_selvedge_pct"`
	WasteCutPct       decimal.NullDecimal `db:"waste_cut_pct"`
}

// usageProvenance is the resolved (source, selvedge, cut) triple written with a usage row.
type usageProvenance struct {
	source   string
	selvedge decimal.NullDecimal
	cut      decimal.NullDecimal
}

// normalized maps the DB shapes onto the canonical triple: an empty source reads as manual,
// and a non-marker source never carries a decomposition.
func (p usageProvenance) normalized() usageProvenance {
	if p.source == "" {
		p.source = entity.ConsumptionSourceManual
	}
	if p.source != entity.ConsumptionSourceMarker {
		p.selvedge, p.cut = decimal.NullDecimal{}, decimal.NullDecimal{}
	}
	return p
}

func (p usageProvenance) equal(o usageProvenance) bool {
	return p.source == o.source && nullDecimalEqual(p.selvedge, o.selvedge) && nullDecimalEqual(p.cut, o.cut)
}

func nullDecimalEqual(a, b decimal.NullDecimal) bool {
	if a.Valid != b.Valid {
		return false
	}
	return !a.Valid || a.Decimal.Equal(b.Decimal)
}

// agreedSlotProvenance returns the provenance shared by EVERY prior row of a slot, when they
// genuinely agree. Multiple whole-garment rows on one slot are legal, so — unlike the material
// pin, where an ambiguous match preserves nothing — a presence-less rewrite must not flip an
// agreeing marker slot back to manual: that would silently re-enable the wastage gross-up, the
// exact invariant this feature exists to hold. Only genuine disagreement falls back to manual.
func agreedSlotProvenance(rows []usageProvenance) (usageProvenance, bool) {
	if len(rows) == 0 {
		return usageProvenance{}, false
	}
	first := rows[0].normalized()
	for _, r := range rows[1:] {
		if !first.equal(r.normalized()) {
			return usageProvenance{}, false
		}
	}
	return first, true
}

// rollGoodsSectionList is THE list of BOM families sold by length — the only rows a marker can lay
// out (so the only ones where consumption_source='marker' is meaningful), and the only ones a
// pattern sheet or a cut-piece alias can bind to. Countable sections never gross wastage anyway;
// thread/trim/decoration are measured and MUST keep their gross-up.
//
// Everything below is DERIVED from this slice, deliberately. The membership map, the named-query
// args and the SQL fragment used to be three hand-written copies sitting next to each other with a
// comment claiming they could not drift. Proximity is not coupling: adding a fifth family to the
// map would have left the fragment at four, and the failure is silent in the worst direction —
// markers would accept the family, pattern/alias binding would refuse it, and no test would notice
// because the extra named arg is simply ignored by sqlx.
var rollGoodsSectionList = []entity.TechCardBomSection{
	entity.BomSectionFabric,
	entity.BomSectionLining,
	entity.BomSectionInterlining,
	entity.BomSectionInsulation,
}

var rollGoodsSections = func() map[string]bool {
	m := make(map[string]bool, len(rollGoodsSectionList))
	for _, s := range rollGoodsSectionList {
		m[string(s)] = true
	}
	return m
}()

// kindEligibleSectionList is THE list of BOM families that ЧТО ЭТО ЗА ПОЗИЦИЯ (kind, 0278) may
// classify. It is the COMPLEMENT of the roll-goods list above, minus labels — and it is DERIVED for
// exactly the reason that list's own header gives: a hand-written copy of a complement is the
// worst kind of copy, because adding a fifth roll-goods family above would leave the copy still
// offering kinds on cloth and no test would notice.
//
//   - roll goods are excluded because they already have their own axis (purpose, 0265) and the two
//     answer different questions about materials that are measured, not counted;
//   - `label` is excluded because tech_card_label.label_type ALREADY owns that vocabulary, several
//     label specs may point at one bom_item_id, and labelsProjection hashes label_type into the
//     SIGNED labels digest — a `kind` there would be a second, unsigned answer free to disagree.
//
// The three sets (roll goods, {label}, this one) are asserted to PARTITION
// entity.ValidTechCardBomSections by TestBomKindSectionsPartitionValidSections: a section added to
// the enum and to none of the three fails there instead of silently becoming un-classifiable.
//
// Sorted, because map iteration order is randomised per run and this list is rendered into the
// operator-facing refusal below — an error message whose wording reshuffles between two identical
// requests is a bug report waiting to happen.
var kindEligibleSectionList = func() []entity.TechCardBomSection {
	out := make([]entity.TechCardBomSection, 0, len(entity.ValidTechCardBomSections))
	for s := range entity.ValidTechCardBomSections {
		if rollGoodsSections[string(s)] || s == entity.BomSectionLabel {
			continue
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}()

var kindEligibleSections = func() map[entity.TechCardBomSection]bool {
	m := make(map[entity.TechCardBomSection]bool, len(kindEligibleSectionList))
	for _, s := range kindEligibleSectionList {
		m[s] = true
	}
	return m
}()

// kindEligibleSectionNames renders the eligible families for an error message, from the one list.
func kindEligibleSectionNames() string {
	names := make([]string, 0, len(kindEligibleSectionList))
	for _, s := range kindEligibleSectionList {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

// rollGoodsSectionParam is the named parameter for one family; the fragment and the args helper
// call it, so the two cannot name different things.
func rollGoodsSectionParam(s entity.TechCardBomSection) string { return "sec_" + string(s) }

// rollGoodsSectionArgs binds the families as named query params, on top of whatever the caller
// already put in the map.
func rollGoodsSectionArgs(args map[string]any) map[string]any {
	for _, s := range rollGoodsSectionList {
		args[rollGoodsSectionParam(s)] = string(s)
	}
	return args
}

// rollGoodsSectionIn is the matching SQL fragment: `section IN (:sec_fabric, …)`. Concatenated
// after an `AND `, never containing a ':' inside a SQL comment (that combination is what breaks
// sqlx binding with "could not find name  in map").
var rollGoodsSectionIn = rollGoodsSectionInOn("")

// rollGoodsSectionInOn is the same fragment qualified by a table alias, for a query that joins
// something else carrying a `section` column of its own — `material` does, so an unqualified
// `section` there is not merely unclear, it is an error MySQL refuses the statement over. An empty
// alias yields the bare form.
func rollGoodsSectionInOn(alias string) string {
	names := make([]string, 0, len(rollGoodsSectionList))
	for _, s := range rollGoodsSectionList {
		names = append(names, ":"+rollGoodsSectionParam(s))
	}
	if alias != "" {
		alias += "."
	}
	return alias + "section IN (" + strings.Join(names, ", ") + ")"
}

func newRecipeUsagePinSlot(slot recipeUsageSlot, placement sql.NullString) recipeUsagePinSlot {
	normalizedPlacement := ""
	if placement.Valid {
		normalizedPlacement = strings.ToLower(strings.TrimSpace(placement.String))
	}
	return recipeUsagePinSlot{recipeUsageSlot: slot, Placement: normalizedPlacement}
}

type materialPinStateRow struct {
	ID       int64 `db:"id"`
	Archived bool  `db:"archived"`
}

type resolvedRecipeUsage struct {
	usage      *entity.TechCardColorwayUsage
	bomItemID  sql.NullInt64
	pieceID    sql.NullInt64
	materialID sql.NullInt64
	provenance usageProvenance
}

// UpdateColorwayRecipe replaces a colourway's material recipe (usages), restoring the write-path cut
// in the R1 merge — ColorwayDevelopmentInsert.usages was accepted on the wire but never written (the
// silent no-op, A3.4). The recipe is a colourway-owned sub-aggregate (R2/R4): it is optimistically
// locked on the shared tech_card.lock_version and each usage references a style BOM line by its
// stable line_key (S2/S3), resolved to a real bom_item_id FK. Returns the bumped lock_version. A
// mismatched version yields ErrTechCardConflict; a missing colourway yields sql.ErrNoRows.
func (s *Store) UpdateColorwayRecipe(ctx context.Context, colorwayID, expectedVersion int, usages []entity.TechCardColorwayUsage) (int, error) {
	newVersion := expectedVersion + 1
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// The colourway's optimistic version is its style's shared tech_card.lock_version (R2/R4).
		cur, err := storeutil.QueryNamedOne[struct {
			StyleID     int `db:"style_id"`
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(),
			`SELECT p.style_id, t.lock_version FROM product p JOIN tech_card t ON t.id = p.style_id WHERE p.id = :id`,
			map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d recipe lock: %w", colorwayID, err) // sql.ErrNoRows -> NotFound upstream
		}
		if err := storeutil.RequireMutableTechCard(ctx, rep.DB(), cur.StyleID); err != nil {
			return err
		}
		if cur.LockVersion != expectedVersion {
			return entity.ErrTechCardConflict
		}

		// Per-size consumption may only be stated for sizes the STYLE makes. The parser cannot check
		// this (it never sees the style) and the FK is on size(id), so an off-range norm used to
		// persist as a rule no production run can ever apply.
		rng, err := storeutil.LoadTechCardSizeRange(ctx, rep.DB(), cur.StyleID)
		if err != nil {
			return err
		}
		for i := range usages {
			for j, sc := range usages[i].SizeConsumptions {
				if err := rng.Require(fmt.Sprintf("usages[%d].size_consumptions[%d].size_id", i, j), sc.SizeId); err != nil {
					return err
				}
			}
		}

		// Resolve the style's BOM: by stable line_key (preferred) and ordered for the legacy index ref.
		bomRows, err := storeutil.QueryListNamed[bomExistingRow](ctx, rep.DB(),
			`SELECT id, line_key, section FROM tech_card_bom_item WHERE tech_card_id = :id ORDER BY display_order, id`,
			map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style bom for recipe: %w", err)
		}
		byKey := make(map[string]int, len(bomRows))
		ordered := make([]int, 0, len(bomRows))
		sectionByBomID := make(map[int64]string, len(bomRows))
		for _, r := range bomRows {
			byKey[r.LineKey] = r.Id
			ordered = append(ordered, r.Id)
			sectionByBomID[int64(r.Id)] = strings.ToLower(strings.TrimSpace(r.Section.String))
		}

		// Resolve the style's cut-pieces the same way (WS4): by stable line_key (preferred) and ordered
		// for the legacy piece_index ref. This is what lets usage.piece_id carry a real FK now that
		// pieces are keyed — the recipe write-path never wrote piece_id before (only piece_index).
		pieceRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, rep.DB(),
			`SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :id ORDER BY display_order, id`,
			map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style pieces for recipe: %w", err)
		}
		pieceByKey := make(map[string]int, len(pieceRows))
		pieceOrdered := make([]int, 0, len(pieceRows))
		for _, r := range pieceRows {
			pieceByKey[r.LineKey] = r.Id
			pieceOrdered = append(pieceOrdered, r.Id)
		}

		// Capture the old pins before the full replace. Presence-less writes come from clients that
		// predate material_id, so an unambiguous logical usage retains its pin. The logical identity is
		// the resolved (bom_item_id, piece_id, normalized placement) tuple, not either legacy positional
		// index. Placement distinguishes repeatable whole-garment rows on the same BOM slot.
		priorRows, err := storeutil.QueryListNamed[recipeUsagePinRow](ctx, rep.DB(), `
			SELECT bom_item_id, piece_id, placement, material_id, consumption_source, waste_selvedge_pct, waste_cut_pct
			FROM tech_card_colorway_usage
			WHERE colorway_id = :id`, map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d existing recipe pins: %w", colorwayID, err)
		}
		priorBySlot := make(map[recipeUsagePinSlot][]sql.NullInt64, len(priorRows))
		priorProvenanceBySlot := make(map[recipeUsagePinSlot][]usageProvenance, len(priorRows))
		for _, row := range priorRows {
			slot := newRecipeUsagePinSlot(newRecipeUsageSlot(row.BomItemID, row.PieceID), row.Placement)
			priorBySlot[slot] = append(priorBySlot[slot], row.MaterialID)
			priorProvenanceBySlot[slot] = append(priorProvenanceBySlot[slot],
				usageProvenance{source: row.ConsumptionSource, selvedge: row.WasteSelvedgePct, cut: row.WasteCutPct})
		}

		// Resolve and validate the entire replacement before its first write. In particular, MySQL
		// cannot enforce uniqueness when piece_id is NULL, and the material FK alone would turn a
		// missing article into an internal error instead of an actionable field violation.
		resolved := make([]resolvedRecipeUsage, 0, len(usages))
		seen := make(map[recipeUsageSlot]int, len(usages))
		materialIDs := make([]int64, 0, len(usages))
		seenMaterialIDs := make(map[int64]bool, len(usages))
		for i := range usages {
			u := &usages[i]
			bomItemID, err := resolveUsageBom(u, byKey, ordered, i)
			if err != nil {
				return err
			}
			pieceID, err := resolveUsagePiece(u, pieceByKey, pieceOrdered, i)
			if err != nil {
				return err
			}
			slot := newRecipeUsageSlot(bomItemID, pieceID)
			// The one-usage-per-(slot, piece) invariant is enforced only for fully-resolved
			// PIECE-BOUND rows. Two carve-outs, both for legacy data that must stay savable:
			// a usage with no resolvable BOM line hashes to the same (NULL, NULL) slot as every
			// other such row, and whole-garment rows (piece NULL) legitimately repeat a slot at
			// different placements ("buttons — front placket" / "buttons — cuff"). Pin
			// preservation for those stays safe: an ambiguous prior match preserves nothing.
			if bomItemID.Valid && pieceID.Valid {
				if previous, exists := seen[slot]; exists {
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d]", i), "duplicate_slot",
						fmt.Sprintf("%s (already used by usages[%d])", recipeUsageSlotName(u, slot), previous),
						"keep only one usage for each BOM-line and cut-piece pair")
				}
				seen[slot] = i
			}
			pinSlot := newRecipeUsagePinSlot(slot, u.Placement)

			materialID := sql.NullInt64{}
			if u.MaterialIdSet {
				// Any explicitly-present non-positive value clears the pin, even for direct store
				// callers that did not pass through the DTO normaliser.
				if u.MaterialId.Valid && u.MaterialId.Int64 > 0 {
					materialID = u.MaterialId
					if !seenMaterialIDs[materialID.Int64] {
						seenMaterialIDs[materialID.Int64] = true
						materialIDs = append(materialIDs, materialID.Int64)
					}
				}
			} else if oldPins := priorBySlot[pinSlot]; len(oldPins) == 1 {
				// Multiple old rows for one slot are ambiguous legacy data: do not guess which pin
				// belongs to the replacement usage. A single NULL is preserved as inheritance.
				materialID = oldPins[0]
			}
			// Provenance triple: present -> written as sent ('' normalises to manual, which
			// clears the pcts); absent (stale client) -> preserved when EVERY prior row of the
			// slot agrees (repeatable whole-garment rows collide into one slot legally — an
			// agreeing pair must not flip marker back to manual and re-enable the gross-up);
			// no prior / genuine disagreement -> manual.
			prov := usageProvenance{source: entity.ConsumptionSourceManual}
			if u.ConsumptionSource.Valid {
				if u.ConsumptionSource.String == entity.ConsumptionSourceMarker {
					prov = usageProvenance{
						source:   entity.ConsumptionSourceMarker,
						selvedge: u.WasteSelvedgePct,
						cut:      u.WasteCutPct,
					}
				}
			} else if agreed, ok := agreedSlotProvenance(priorProvenanceBySlot[pinSlot]); ok {
				prov = agreed
			}
			// Marker provenance is meaningful only on roll goods a marker can lay out. Sent
			// explicitly on anything else -> the client is wrong, refuse. Carried forward onto
			// a row that no longer qualifies (legacy data, section edits) -> demote to manual
			// quietly rather than fail a stale client's presence-less save.
			if prov.source == entity.ConsumptionSourceMarker &&
				(!bomItemID.Valid || !rollGoodsSections[sectionByBomID[bomItemID.Int64]]) {
				if u.ConsumptionSource.Valid {
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d].consumption_source", i),
						"marker_not_roll_goods", recipeUsageSlotName(u, slot),
						"marker consumption applies only to fabric, lining, interlining or insulation BOM lines")
				}
				prov = usageProvenance{source: entity.ConsumptionSourceManual}
			}
			resolved = append(resolved, resolvedRecipeUsage{
				usage: u, bomItemID: bomItemID, pieceID: pieceID, materialID: materialID,
				provenance: prov,
			})
		}

		materialStates := make(map[int64]bool, len(materialIDs))
		if len(materialIDs) > 0 {
			rows, err := storeutil.QueryListNamed[materialPinStateRow](ctx, rep.DB(), `
				SELECT id, archived FROM material WHERE id IN (:ids)`, map[string]any{"ids": materialIDs})
			if err != nil {
				return fmt.Errorf("validate colourway recipe material pins: %w", err)
			}
			for _, row := range rows {
				materialStates[row.ID] = row.Archived
			}
		}
		for i := range resolved {
			u := resolved[i].usage
			if !u.MaterialIdSet || !resolved[i].materialID.Valid {
				continue
			}
			id := resolved[i].materialID.Int64
			archived, exists := materialStates[id]
			if !exists {
				return entity.NewFieldViolation(fmt.Sprintf("usages[%d].material_id", i), "material_not_found",
					fmt.Sprintf("material %d", id), "choose an existing active material or clear the pin")
			}
			if archived {
				// An UNCHANGED round-trip of an already-stored pin is allowed even when the
				// article was archived after pinning — the client deliberately re-sends what it
				// read, and rejecting it would block every unrelated consumption edit on the
				// recipe. Only ASSIGNING an archived article is refused.
				slot := newRecipeUsagePinSlot(
					newRecipeUsageSlot(resolved[i].bomItemID, resolved[i].pieceID), u.Placement,
				)
				if prior := priorBySlot[slot]; len(prior) == 1 && prior[0].Valid && prior[0].Int64 == id {
					continue
				}
				return entity.NewFieldViolation(fmt.Sprintf("usages[%d].material_id", i), "material_archived",
					fmt.Sprintf("material %d", id), "choose an active material or clear the pin")
			}
		}

		// Full-replace this colourway's usages (per-size consumptions cascade on delete).
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card_colorway_usage WHERE colorway_id = :id`, map[string]any{"id": colorwayID}); err != nil {
			return fmt.Errorf("clear colourway %d usages: %w", colorwayID, err)
		}
		for i := range resolved {
			r := &resolved[i]
			u := r.usage
			usageID, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
				INSERT INTO tech_card_colorway_usage
					(colorway_id, bom_item_id, bom_item_index, placement, color, pantone, consumption, consumption_source, waste_selvedge_pct, waste_cut_pct, quantity, piece_id, piece_index, material_id, display_order)
				VALUES (:colorway_id, :bom_item_id, :bom_item_index, :placement, :color, :pantone, :consumption, :consumption_source, :waste_selvedge_pct, :waste_cut_pct, :quantity, :piece_id, :piece_index, :material_id, :display_order)`,
				map[string]any{
					"colorway_id":        colorwayID,
					"bom_item_id":        r.bomItemID,
					"bom_item_index":     u.BomItemIndex,
					"placement":          u.Placement,
					"color":              u.Color,
					"pantone":            u.Pantone,
					"consumption":        u.Consumption,
					"consumption_source": r.provenance.source,
					"waste_selvedge_pct": r.provenance.selvedge,
					"waste_cut_pct":      r.provenance.cut,
					"quantity":           u.Quantity,
					"piece_id":           r.pieceID,
					"piece_index":        u.PieceIndex,
					"material_id":        r.materialID,
					"display_order":      i,
				})
			if err != nil {
				return fmt.Errorf("insert colourway usage: %w", err)
			}
			for j := range u.SizeConsumptions {
				sc := &u.SizeConsumptions[j]
				if err := storeutil.ExecNamed(ctx, rep.DB(), `
					INSERT INTO tech_card_colorway_usage_consumption (usage_id, size_id, consumption, display_order)
					VALUES (:usage_id, :size_id, :consumption, :display_order)`,
					map[string]any{"usage_id": usageID, "size_id": sc.SizeId, "consumption": sc.Consumption, "display_order": j}); err != nil {
					return fmt.Errorf("insert usage consumption: %w", err)
				}
			}
		}

		// P4-flyover M1 (04-MAZE-FLYOVER.md): a recipe write is a mutation of the style aggregate
		// (Ф5 plan), so re-derive the structural composition (S17) the same way UpdateStyle does — a
		// manual override already on file is preserved (entity.ReconcileStyleComposition).
		if err := product.ReconcileStyleCompositionTx(ctx, rep.DB(), cur.StyleID); err != nil {
			return err
		}

		// Bump the shared lock under the guard — a recipe write is a mutation of the style aggregate,
		// so a concurrent style/colourway edit holding the old version is rejected.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id AND lock_version = :ver`,
			map[string]any{"id": cur.StyleID, "ver": expectedVersion})
		if err != nil {
			return fmt.Errorf("bump lock for recipe: %w", err)
		}
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, entity.ErrTechCardConflict) ||
			errors.Is(err, entity.ErrTechCardReleased) {
			return 0, err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return 0, err
		}
		return 0, fmt.Errorf("can't update colourway %d recipe: %w", colorwayID, err)
	}
	return newVersion, nil
}

// GetColorwayRecipe returns a colourway's material recipe (usages, with their per-size
// consumption), the read side of UpdateColorwayRecipe (H1 fix: `techCardUsagesToPb` existed but was
// never wired into a read path, A3.4 — a full-replace write with no matching read is unsafe to edit
// partially). Ordered by display_order, matching write order. Empty (not an error) when the
// colourway has no recipe yet.
func (s *Store) GetColorwayRecipe(ctx context.Context, colorwayID int) ([]entity.TechCardColorwayUsage, error) {
	usages, err := storeutil.QueryListNamed[entity.TechCardColorwayUsage](ctx, s.DB, `
		SELECT id, bom_item_id, piece_id, material_id, bom_item_index, placement, color, pantone, consumption, consumption_source, waste_selvedge_pct, waste_cut_pct, quantity, piece_index
		FROM tech_card_colorway_usage
		WHERE colorway_id = :id
		ORDER BY display_order`, map[string]any{"id": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("load colourway %d recipe: %w", colorwayID, err)
	}
	if len(usages) == 0 {
		return usages, nil
	}
	usageByID := make(map[int]*entity.TechCardColorwayUsage, len(usages))
	usageIDs := make([]int, 0, len(usages))
	for i := range usages {
		usageByID[usages[i].Id] = &usages[i]
		usageIDs = append(usageIDs, usages[i].Id)
	}
	consRows, err := storeutil.QueryListNamed[techCardUsageConsumptionRow](ctx, s.DB, `
		SELECT usage_id, size_id, consumption
		FROM tech_card_colorway_usage_consumption
		WHERE usage_id IN (:ids)
		ORDER BY usage_id, display_order`, map[string]any{"ids": usageIDs})
	if err != nil {
		return nil, fmt.Errorf("load colourway %d recipe consumption: %w", colorwayID, err)
	}
	for _, c := range consRows {
		if u, ok := usageByID[c.UsageID]; ok {
			u.SizeConsumptions = append(u.SizeConsumptions, c.TechCardBomSizeConsumption)
		}
	}
	return usages, nil
}

// resolveUsageBom resolves a usage's BOM reference to a real bom_item id: by stable line_key
// (preferred), else the legacy positional index, else SQL NULL. Unknown keys and explicit invalid
// indexes are field-tagged; an absent reference remains NULL.
func resolveUsageBom(u *entity.TechCardColorwayUsage, byKey map[string]int, ordered []int, i int) (sql.NullInt64, error) {
	if key := strings.TrimSpace(u.BomLineKey); key != "" {
		if id, ok := byKey[key]; ok {
			return sql.NullInt64{Int64: int64(id), Valid: true}, nil
		}
		return sql.NullInt64{}, entity.NewFieldViolation(fmt.Sprintf("usages[%d].bom_line_key", i),
			fmt.Sprintf("no BOM line %q in this style", key), "", "reference an existing BOM line by its line_key")
	}
	if u.BomItemIndex.Valid {
		idx := int(u.BomItemIndex.Int32)
		if idx >= 0 && idx < len(ordered) {
			return sql.NullInt64{Int64: int64(ordered[idx]), Valid: true}, nil
		}
		reason := "cannot be set because this style has no BOM lines"
		if len(ordered) > 0 {
			reason = fmt.Sprintf("must be in the valid range [0, %d]", len(ordered)-1)
		}
		return sql.NullInt64{}, entity.NewFieldViolation(fmt.Sprintf("usages[%d].bom_item_index", i),
			reason, fmt.Sprintf("index %d", idx), "reference an existing BOM line or omit the index")
	}
	return sql.NullInt64{}, nil
}

// resolveUsagePiece turns a usage's cut-piece reference into a real tech_card_piece id for the
// usage.piece_id FK (WS4): by stable piece line_key (preferred; unknown → field-tagged) or the legacy
// positional piece_index. An explicit invalid index is field-tagged; an absent reference remains SQL
// NULL (the norm is about the whole garment, not a specific piece).
func resolveUsagePiece(u *entity.TechCardColorwayUsage, byKey map[string]int, ordered []int, i int) (sql.NullInt64, error) {
	if key := strings.TrimSpace(u.PieceLineKey); key != "" {
		if id, ok := byKey[key]; ok {
			return sql.NullInt64{Int64: int64(id), Valid: true}, nil
		}
		return sql.NullInt64{}, entity.NewFieldViolation(fmt.Sprintf("usages[%d].piece_line_key", i),
			fmt.Sprintf("no cut-piece %q in this style", key), "", "reference an existing cut-piece by its line_key")
	}
	if u.PieceIndex.Valid {
		idx := int(u.PieceIndex.Int32)
		if idx >= 0 && idx < len(ordered) {
			return sql.NullInt64{Int64: int64(ordered[idx]), Valid: true}, nil
		}
		reason := "cannot be set because this style has no cut-pieces"
		if len(ordered) > 0 {
			reason = fmt.Sprintf("must be in the valid range [0, %d]", len(ordered)-1)
		}
		return sql.NullInt64{}, entity.NewFieldViolation(fmt.Sprintf("usages[%d].piece_index", i),
			reason, fmt.Sprintf("index %d", idx), "reference an existing cut-piece or omit the index")
	}
	return sql.NullInt64{}, nil
}

func newRecipeUsageSlot(bomItemID, pieceID sql.NullInt64) recipeUsageSlot {
	return recipeUsageSlot{
		BomItemID: bomItemID.Int64, BomItemIDValid: bomItemID.Valid,
		PieceID: pieceID.Int64, PieceIDValid: pieceID.Valid,
	}
}

func recipeUsageSlotName(u *entity.TechCardColorwayUsage, slot recipeUsageSlot) string {
	bom := "no BOM line"
	if key := strings.TrimSpace(u.BomLineKey); key != "" {
		bom = fmt.Sprintf("BOM line %q", key)
	} else if slot.BomItemIDValid {
		bom = fmt.Sprintf("BOM item %d", slot.BomItemID)
	}
	piece := "whole garment"
	if key := strings.TrimSpace(u.PieceLineKey); key != "" {
		piece = fmt.Sprintf("cut-piece %q", key)
	} else if slot.PieceIDValid {
		piece = fmt.Sprintf("cut-piece %d", slot.PieceID)
	}
	return bom + " / " + piece
}
