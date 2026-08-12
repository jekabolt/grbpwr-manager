package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	// The Ф6.8 stamp (0291) rides the SAME carry, for the same reason: a client that knows about
	// 'marker' but not about the stamp is today's deployed client, and its presence-less save must
	// not blank the audit that says WHICH раскладка the norm came from.
	NormMarkerID  sql.NullInt64 `db:"norm_marker_id"`
	NormAppliedAt sql.NullTime  `db:"norm_applied_at"`
}

// usageProvenance is the resolved provenance written with a usage row: the source, its display
// decomposition, and the Ф6.8 stamp (which раскладка the norm came from, and when it was applied).
type usageProvenance struct {
	source   string
	selvedge decimal.NullDecimal
	cut      decimal.NullDecimal
	markerID sql.NullInt64
	// appliedAt is SERVER-OWNED. It is never parsed off the wire and never compared by equal();
	// see stampAppliedAt for the one rule that moves it.
	appliedAt sql.NullTime
}

// normalized maps the DB shapes onto the canonical form: an empty source reads as manual, and a
// non-marker source never carries a decomposition — nor a stamp. That last clause is what makes the
// silent demotion below (a 'marker' row whose BOM line is no longer roll goods) clear the stamp
// together with the pcts, instead of leaving a norm attributed to a раскладка it no longer claims.
func (p usageProvenance) normalized() usageProvenance {
	if p.source == "" {
		p.source = entity.ConsumptionSourceManual
	}
	if p.source != entity.ConsumptionSourceMarker {
		p.selvedge, p.cut = decimal.NullDecimal{}, decimal.NullDecimal{}
		p.markerID, p.appliedAt = sql.NullInt64{}, sql.NullTime{}
	}
	// A stamp with no раскладка is not a stamp: the time alone answers no question, and carrying it
	// would let «применено тогда-то» outlive the id it was about.
	if !p.markerID.Valid {
		p.appliedAt = sql.NullTime{}
	}
	return p
}

// equal compares everything a caller may DECIDE, which is the source, the decomposition and the
// marker id — appliedAt is deliberately excluded because it is derived from those three by
// stampAppliedAt, not chosen. Including it would make two agreeing rows of one slot (legal for
// repeatable whole-garment usages) read as a disagreement purely because they were applied at
// different moments, and the fallback for a disagreement is manual — silently re-enabling the
// wastage gross-up, the exact invariant agreedSlotProvenance exists to hold.
func (p usageProvenance) equal(o usageProvenance) bool {
	return p.source == o.source && nullDecimalEqual(p.selvedge, o.selvedge) &&
		nullDecimalEqual(p.cut, o.cut) && nullInt64Equal(p.markerID, o.markerID)
}

func nullInt64Equal(a, b sql.NullInt64) bool {
	if a.Valid != b.Valid {
		return false
	}
	return !a.Valid || a.Int64 == b.Int64
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
// Ф6.8 widened what «agree» covers: the stamp's marker id is part of equal(), so two prior rows of
// one slot applied from DIFFERENT раскладки no longer count as agreeing and neither of them wins by
// accident. Their appliedAt is settled separately — see below.
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
	// appliedAt is NOT settled here. It is not part of the agreement — stampAppliedAt reads the
	// slot's rows itself, including the ones that disagree, because a stamp can be carried from a
	// row whose OTHER fields disagreed. See there.
	return first, true
}

// carriedSlotStamp answers «какой штамп у этого слота был», когда клиент про штамп промолчал.
//
// Смотрит строки с ТЕМ ЖЕ источником, что у разрешаемой строки, и отдаёт их раскладку — только если
// она у них ОДНА. Две разные раскладки на одном слоте — это неоднозначность, и угадывать здесь
// нельзя ровно по той же причине, по которой не угадывается пин материала строкой выше: выбранная
// наугад раскладка выглядела бы как факт.
//
// Почему по источнику, а не по согласию всей четвёрки: слот законно держит несколько повторяющихся
// строк с РАЗНЫМ провенансом (одна набрана руками, другая снята с раскладки). Согласия там нет
// никогда, а штамп у марочной строки есть, и терять его на каждом сохранении нельзя.
func carriedSlotStamp(rows []usageProvenance, source string) sql.NullInt64 {
	var found sql.NullInt64
	for _, r := range rows {
		n := r.normalized()
		if n.source != source || !n.markerID.Valid {
			continue
		}
		if found.Valid && found.Int64 != n.markerID.Int64 {
			return sql.NullInt64{}
		}
		found = n.markerID
	}
	return found
}

// slotHoldsStamp reports whether id is a stamp THIS SLOT ALREADY CARRIES — i.e. whether the client
// is echoing back a раскладка the row was stamped with earlier, rather than naming a new one.
//
// Это МЕМБЕРШИП, А НЕ ВЫБОР, и потому здесь нет оговорки «ровно одна прежняя строка», которой
// связаны пин материала и carriedSlotStamp. Тем двоим неоднозначность мешает по существу: они
// РЕШАЮТ, какое значение перенести, и наугад выбранное выглядело бы фактом. Здесь же вопрос
// закрытый — «это число уже лежало на слоте?» — и на него две законные повторяющиеся строки
// отвечают ничуть не хуже одной.
//
// Читается нормализованная форма: у ручной строки штампа не бывает по построению (normalized его
// чистит), так что несовпадение источника отсеивается само.
func slotHoldsStamp(rows []usageProvenance, id int64) bool {
	for _, r := range rows {
		if n := r.normalized(); n.markerID.Valid && n.markerID.Int64 == id {
			return true
		}
	}
	return false
}

// stampAppliedAt settles the Ф6.8 timestamp for a resolved row. THE ONE RULE: the stamp moves to
// `now` only when the slot held NO row with this same (source, marker id) pair; a matching row hands
// its moment across verbatim.
//
// The alternative — stamping every recipe write — is the trap this function exists to avoid. Editing
// any NEIGHBOURING field of the row would refresh the stamp past the раскладка's updated_at, the
// «раскладка изменена» indicator would go dark, and the disappearance would be indistinguishable
// from the divergence having been resolved. A stale audit that says so is recoverable; one that
// quietly reports agreement is not.
//
// СМОТРИМ ВСЕ СТРОКИ СЛОТА, А НЕ ТОЛЬКО СОГЛАСОВАННУЮ. Согласие (agreedSlotProvenance) отвечает на
// другой вопрос — «что переносить, когда клиент промолчал», — и требует, чтобы совпало ВСЁ. Пусти
// штамп через него, и слот с двумя законными повторяющимися строками, одна из которых марочная, а
// другая ручная, объявился бы «без прошлого»: пара (marker, #5) уцелела бы, а отметка сдвинулась бы
// на сейчас — то есть индикатор расхождения ПОГАС БЫ у настоящей раскладки, перемеренной вчера.
// Ровно та поломка, ради которой правило и заведено, только через чёрный ход.
//
// Из подходящих берётся САМАЯ РАННЯЯ: индикатор загорается, когда раскладку правили ПОСЛЕ отметки,
// поэтому ранняя может лишь показать расхождение, которое поздняя спрятала бы. Показать лишний раз —
// это второй взгляд на раскладку; спрятать — это неверное число в костинге навсегда.
func stampAppliedAt(prov usageProvenance, priors []usageProvenance, now time.Time) usageProvenance {
	if !prov.markerID.Valid {
		prov.appliedAt = sql.NullTime{}
		return prov
	}
	var carried sql.NullTime
	for _, p := range priors {
		n := p.normalized()
		if !n.appliedAt.Valid || n.source != prov.source || !nullInt64Equal(n.markerID, prov.markerID) {
			continue
		}
		if !carried.Valid || n.appliedAt.Time.Before(carried.Time) {
			carried = n.appliedAt
		}
	}
	if carried.Valid {
		prov.appliedAt = carried
		return prov
	}
	prov.appliedAt = sql.NullTime{Time: now, Valid: true}
	return prov
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
// Since Ф6 the slice itself lives in internal/entity (entity.RollGoodsSectionList): the readiness
// gate resolves pattern/alias bindings and asks направление ткани over the same four families from
// internal/dto, and a second literal there would have been precisely the silent drift this header
// forbids — one package further away. Everything below still derives from the one slice; only its
// home moved.
var rollGoodsSectionList = entity.RollGoodsSectionList

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

// loadRollGoodsLines reads the card's cloth lines in the shape the ONE binding rule takes
// (entity.ResolveFabricScope / entity.FabricScopeIdentity): the stable line_key and its назначение.
//
// One loader for every path that has to resolve a binding, because the ARGUMENTS of that rule drift
// as easily as the rule itself: a caller that forgot the purpose column, or narrowed the families to
// three, would resolve every binding of a sorted card to its legacy line and be indistinguishable
// from a card nobody sorted. entity.RollGoodsLinesOfBom is the same projection for callers that
// already hold an enriched card and must not pay for a second read.
func loadRollGoodsLines(ctx context.Context, db dependency.DB, techCardID int) ([]entity.RollGoodsLine, error) {
	rows, err := storeutil.QueryListNamed[struct {
		LineKey string `db:"line_key"`
		Purpose string `db:"purpose"`
	}](ctx, db, `SELECT COALESCE(line_key, '') AS line_key, COALESCE(purpose, '') AS purpose
		FROM tech_card_bom_item
		WHERE tech_card_id = :id AND `+rollGoodsSectionIn,
		rollGoodsSectionArgs(map[string]any{"id": techCardID}))
	if err != nil {
		return nil, fmt.Errorf("load roll-goods bom lines of tech card %d: %w", techCardID, err)
	}
	out := make([]entity.RollGoodsLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, entity.RollGoodsLine{LineKey: r.LineKey, Purpose: r.Purpose})
	}
	return out, nil
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
			`SELECT id, line_key, section, purpose, name FROM tech_card_bom_item WHERE tech_card_id = :id ORDER BY display_order, id`,
			map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style bom for recipe: %w", err)
		}
		byKey := make(map[string]int, len(bomRows))
		ordered := make([]int, 0, len(bomRows))
		sectionByBomID := make(map[int64]string, len(bomRows))
		// Derived layer role per BOM line (T4): purpose when sorted, else the section fallback —
		// the ONE rule, entity.DerivePieceLayerRole. Feeds the «две основные» refusal below.
		roleByBomID := make(map[int64]entity.TechCardBomPurpose, len(bomRows))
		bomNameByID := make(map[int64]string, len(bomRows))
		for _, r := range bomRows {
			byKey[r.LineKey] = r.Id
			ordered = append(ordered, r.Id)
			section := strings.ToLower(strings.TrimSpace(r.Section.String))
			sectionByBomID[int64(r.Id)] = section
			role, _ := entity.DerivePieceLayerRole(entity.TechCardBomSection(section), r.Purpose.String)
			roleByBomID[int64(r.Id)] = role
			bomNameByID[int64(r.Id)] = r.Name
		}

		// Resolve the style's cut-pieces the same way (WS4): by stable line_key (preferred) and ordered
		// for the legacy piece_index ref. This is what lets usage.piece_id carry a real FK now that
		// pieces are keyed — the recipe write-path never wrote piece_id before (only piece_index).
		pieceRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, rep.DB(),
			`SELECT id, line_key, name FROM tech_card_piece WHERE tech_card_id = :id ORDER BY display_order, id`,
			map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style pieces for recipe: %w", err)
		}
		pieceByKey := make(map[string]int, len(pieceRows))
		pieceOrdered := make([]int, 0, len(pieceRows))
		pieceNameByID := make(map[int64]string, len(pieceRows))
		for _, r := range pieceRows {
			pieceByKey[r.LineKey] = r.Id
			pieceOrdered = append(pieceOrdered, r.Id)
			pieceNameByID[int64(r.Id)] = r.Name
		}

		// Capture the old pins before the full replace. Presence-less writes come from clients that
		// predate material_id, so an unambiguous logical usage retains its pin. The logical identity is
		// the resolved (bom_item_id, piece_id, normalized placement) tuple, not either legacy positional
		// index. Placement distinguishes repeatable whole-garment rows on the same BOM slot.
		priorRows, err := storeutil.QueryListNamed[recipeUsagePinRow](ctx, rep.DB(), `
			SELECT bom_item_id, piece_id, placement, material_id, consumption_source, waste_selvedge_pct, waste_cut_pct,
			       norm_marker_id, norm_applied_at
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
				usageProvenance{source: row.ConsumptionSource, selvedge: row.WasteSelvedgePct, cut: row.WasteCutPct,
					markerID: row.NormMarkerID, appliedAt: row.NormAppliedAt})
		}

		// ОДИН ЧАС НА ВСЮ ЗАПИСЬ, И ЧАСЫ БАЗЫ, А НЕ ПРИЛОЖЕНИЯ. norm_applied_at существует ровно
		// для того, чтобы клиент сравнил его с tech_card_marker.updated_at — а тот ставит СУБД
		// (ON UPDATE CURRENT_TIMESTAMP, 0257). Возьми время из Go, и расхождение часов приложения
		// и базы читалось бы как «раскладка изменена после применения» на раскладке, которую никто
		// не трогал. Читается один раз до цикла, поэтому все строки одной записи штампуются одним
		// значением и порядок строк на них не влияет.
		nowRow, err := storeutil.QueryNamedOne[struct {
			Now time.Time `db:"now"`
		}](ctx, rep.DB(), `SELECT NOW() AS now`, map[string]any{})
		if err != nil {
			return fmt.Errorf("read db clock for colourway %d recipe stamp: %w", colorwayID, err)
		}
		now := nowRow.Now

		// Resolve and validate the entire replacement before its first write. In particular, MySQL
		// cannot enforce uniqueness when piece_id is NULL, and the material FK alone would turn a
		// missing article into an internal error instead of an actionable field violation.
		resolved := make([]resolvedRecipeUsage, 0, len(usages))
		seen := make(map[recipeUsageSlot]int, len(usages))
		// mainByPiece — П1 (T4): у детали не бывает ДВУХ строк роли «основная ткань». Роль — вывод
		// из строки BOM (roleByBomID выше), поэтому ловятся только явные main×2: fabric-строка без
		// назначения даёт роль «не разложено», и отказывать по догадке здесь нельзя — её случай
		// называет гейт готовности (предупреждение), а кат-план останавливает наряд. Конфликт от
		// смены назначения на вкладке BOM сейв КАРТОЧКИ сознательно не отбивает (правка BOM легитимна
		// сама по себе) — его тоже ловит гейт.
		type mainLayerRef struct {
			idx   int
			bomID int64
		}
		mainByPiece := make(map[int64]mainLayerRef, len(usages))
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
				// П1 (T4): вторая ОСНОВНАЯ на одной цельной детали — ошибка данных, а не слой.
				if roleByBomID[bomItemID.Int64] == entity.BomPurposeMain {
					if previous, exists := mainByPiece[pieceID.Int64]; exists {
						pieceName := pieceNameByID[pieceID.Int64]
						if pieceName == "" {
							pieceName = fmt.Sprintf("#%d", pieceID.Int64)
						}
						return entity.NewFieldViolation(fmt.Sprintf("usages[%d]", i), "duplicate_main_fabric",
							fmt.Sprintf("piece %q already has a main fabric %q (usages[%d]) — a second main fabric on a solid piece is impossible: a solid piece is cut from a single main fabric",
								pieceName, bomNameByID[previous.bomID], previous.idx),
							"assign the second fabric its purpose on the BOM tab (lining, interfacing, contrast…) — or split the piece in two")
					}
					mainByPiece[pieceID.Int64] = mainLayerRef{idx: i, bomID: bomItemID.Int64}
				}
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
			agreed, hadAgreed := agreedSlotProvenance(priorProvenanceBySlot[pinSlot])
			prov := usageProvenance{source: entity.ConsumptionSourceManual}
			if u.ConsumptionSource.Valid {
				// ЯВНО ПРИСЛАННЫЙ ИСТОЧНИК РАЗБИРАЕТСЯ ПО ИМЕНИ, А НЕ «ВСЁ, ЧТО НЕ MARKER → MANUAL».
				// Раньше здесь стояло одно сравнение с 'marker', и любое другое присланное значение
				// молча ложилось как manual. Для двух значений это читалось как нормализация; с
				// приходом 'dxf' (0294) та же строка стала бы тихой ложью: карточка сохранилась бы,
				// экран показал бы «введено руками», и фича выглядела бы отключённой, а не сломанной.
				// dxf не несёт разложения отходов (netto площадь деталей — не измеренная раскладка);
				// dto это уже запрещает, а normalized() ниже чистит их и у прямых вызовов стора.
				switch u.ConsumptionSource.String {
				case entity.ConsumptionSourceMarker:
					prov = usageProvenance{
						source:   entity.ConsumptionSourceMarker,
						selvedge: u.WasteSelvedgePct,
						cut:      u.WasteCutPct,
					}
				case entity.ConsumptionSourceDxf:
					prov = usageProvenance{source: entity.ConsumptionSourceDxf}
				}
			} else if hadAgreed {
				prov = agreed
			}
			// ШТАМП НОРМЫ (Ф6.8) РЕШАЕТСЯ ОТДЕЛЬНО ОТ ИСТОЧНИКА, и это не симметрия ради симметрии:
			// присутствие norm_marker_id — СВОЙ протокол, независимый от присутствия
			// consumption_source. Клиент, который знает про 'marker', но не знает про штамп, — это
			// ровно сегодняшний задеплоенный клиент; сложи два присутствия в одно, и его обычное
			// сохранение рецепта обнулило бы аудит, ради которого колонка заведена.
			//
			//   присутствует, > 0  -> пишем как прислано
			//   присутствует, 0    -> снять штамп (явное решение клиента)
			//   отсутствует        -> перенести штамп слота, взятый по СВОЕМУ ИСТОЧНИКУ
			//
			// Перенос НЕ идёт через agreedSlotProvenance, и это разница между «работает» и «работает
			// у большинства». Согласие требует, чтобы совпало ВСЁ, и слот с двумя законными
			// повторяющимися строками — одна ручная, другая марочная — согласия не даёт никогда.
			// Возьми штамп оттуда, и сегодняшний задеплоенный клиент (шлёт consumption_source, не
			// знает про штамп) СТИРАЛ БЫ аудит на каждом сохранении рецепта такого слота — ровно то,
			// ради чего протокол присутствия и заведён, только через чёрный ход.
			if u.NormMarkerIdSet {
				prov.markerID = sql.NullInt64{}
				if u.NormMarkerId.Valid && u.NormMarkerId.Int64 > 0 {
					prov.markerID = u.NormMarkerId
				}
			} else {
				prov.markerID = carriedSlotStamp(priorProvenanceBySlot[pinSlot], prov.source)
			}
			// DERIVED provenance is meaningful only on roll goods. Sent explicitly on anything else
			// -> the client is wrong, refuse. Carried forward onto a row that no longer qualifies
			// (legacy data, section edits) -> demote to manual quietly rather than fail a stale
			// client's presence-less save.
			//
			// Обе производные нормы попадают под одно правило по ОДНОЙ причине, но разными словами:
			// раскладку кладут только на рулонное (её длина иначе не значит ничего), а выкройки
			// привязываются к тем же четырём семействам (rollGoodsSectionList — их же список), и
			// площадь деталей, поделённая на ширину полотна, у пуговицы не имеет смысла. Ручная
			// норма остаётся единственным путём для нерулонного — тесьма на метраж, нитка, фурнитура.
			if prov.source != entity.ConsumptionSourceManual &&
				(!bomItemID.Valid || !rollGoodsSections[sectionByBomID[bomItemID.Int64]]) {
				if u.ConsumptionSource.Valid {
					code, what := "marker_not_roll_goods", "marker consumption"
					if prov.source == entity.ConsumptionSourceDxf {
						code, what = "dxf_not_roll_goods", "pattern-derived consumption"
					}
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d].consumption_source", i),
						code, recipeUsageSlotName(u, slot),
						what+" applies only to fabric, lining, interlining or insulation BOM lines")
				}
				prov = usageProvenance{source: entity.ConsumptionSourceManual}
			}
			// normalized() FIRST, then the stamp: demotion to manual (explicit or the silent one
			// above) must clear the marker id before the pair is compared, or a row that just lost
			// its 'marker' source would still be measured against the stored раскладка.
			prov = stampAppliedAt(prov.normalized(), priorProvenanceBySlot[pinSlot], now)
			resolved = append(resolved, resolvedRecipeUsage{
				usage: u, bomItemID: bomItemID, pieceID: pieceID, materialID: materialID,
				provenance: prov,
			})
		}

		// ЯВНО ПРИСЛАННЫЙ ШТАМП ОБЯЗАН УКАЗЫВАТЬ НА РАСКЛАДКУ ЭТОЙ КАРТОЧКИ (Ф6.8).
		//
		// FK нет намеренно (0291) — раскладки удаляют, и висящий id читается как «раскладка удалена».
		// Но «удалена» — это про то, что БЫЛО и ушло; id, которого на этой карточке не было никогда,
		// той же строкой не оправдывается. Без проверки экран печатал бы «раскладка удалена» на
		// клиентской опечатке и на чужой карточке одинаково, то есть врал бы правдоподобно.
		//
		// ПРОВЕРЯЕТСЯ НОВОЕ, А НЕ ЭХО. Перенесённый штамп (клиент про поле промолчал) не проверяется
		// никогда, и ТОЧНО ТАК ЖЕ проходит явно присланный штамп, КОТОРЫЙ НА ЭТОМ СЛОТЕ УЖЕ ЛЕЖИТ.
		// Здесь раньше стояла одна оговорка про перенос, и она описывала не то, что делает код:
		// сегодняшний клиент читает хранимый штамп и шлёт его обратно ЯВНО на каждом полном
		// перезаписывании рецепта, так что после удаления раскладки отказ срабатывал ровно там, где
		// комментарий обещал, что не сработает, — и запирал правку рецепта до ручной демотации нормы.
		//
		// Гвард на удаление раскладки был бы вторым отказом вместо первого: человек оказался бы между
		// «нельзя удалить, на неё ссылается рецепт» и «нельзя сохранить рецепт, раскладки нет».
		// Освобождение НЕИЗМЕНЁННОГО значения — тот же приём, которым в этом файле уже разрешён
		// круговой рейс пина на АРХИВНЫЙ артикул (см. material_archived ниже): клиент сознательно
		// возвращает то, что прочитал, и отказ на этом бил бы по правке соседнего поля, ничего не
		// починив. Новое или ИЗМЕНЁННОЕ значение проверяется как прежде — им клиент делает
		// утверждение, а не повторяет чужое.
		explicitStamps := make([]int64, 0, len(resolved))
		for i := range resolved {
			if resolved[i].usage.NormMarkerIdSet && resolved[i].provenance.markerID.Valid {
				explicitStamps = append(explicitStamps, resolved[i].provenance.markerID.Int64)
			}
		}
		if len(explicitStamps) > 0 {
			type stampRow struct {
				Id          int64         `db:"id"`
				Name        string        `db:"name"`
				RunId       sql.NullInt64 `db:"run_id"`
				IsDraft     bool          `db:"is_draft"`
				PlacedCount int           `db:"placed_count"`
				TotalCount  int           `db:"total_count"`
			}
			known, err := storeutil.QueryListNamed[stampRow](ctx, rep.DB(), `
				SELECT id, name, run_id, is_draft, placed_count, total_count
				FROM tech_card_marker WHERE tech_card_id = :card AND id IN (:ids)`,
				map[string]any{"card": cur.StyleID, "ids": explicitStamps})
			if err != nil {
				return fmt.Errorf("validate colourway recipe norm stamps: %w", err)
			}
			onCard := make(map[int64]stampRow, len(known))
			for _, row := range known {
				onCard[row.Id] = row
			}
			for i := range resolved {
				if !resolved[i].usage.NormMarkerIdSet || !resolved[i].provenance.markerID.Valid {
					continue
				}
				id := resolved[i].provenance.markerID.Int64
				row, onThisCard := onCard[id]
				if !onThisCard {
					// ЭХО УЖЕ ХРАНИМОГО ШТАМПА ПРОХОДИТ, И ИМЕННО ЗДЕСЬ — на «раскладки нет», потому
					// что это единственный отказ, который висящий id вызывает законно: раскладку
					// удалили после применения. Слот берётся тот же, что у пина материала строкой
					// ниже (bom_item_id, piece_id, нормализованное placement).
					pinSlot := newRecipeUsagePinSlot(
						newRecipeUsageSlot(resolved[i].bomItemID, resolved[i].pieceID),
						resolved[i].usage.Placement,
					)
					if slotHoldsStamp(priorProvenanceBySlot[pinSlot], id) {
						continue
					}
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d].norm_marker_id", i),
						"marker_not_on_card", fmt.Sprintf("раскладка %d", id),
						"the norm stamp must name a раскладка of this tech card — send 0 to clear it")
				}
				// РАСКРОЙНАЯ РАСКЛАДКА ПРОГОНА ИСТОЧНИКОМ НОРМЫ НЕ БЫВАЕТ (Ф4, 0282). Она принадлежит
				// прогону, умирает вместе с ним (CASCADE) и нормой быть не может по
				// chk_tcm_run_not_norm — то есть штамп на неё это ссылка на источник, которого не
				// бывает: строка рецепта утверждала бы, что её расход снят с раскладки, которую
				// карточка никогда не признавала нормой и которая исчезнет с прогоном.
				//
				// Отдельная причина, а не marker_not_on_card: карточку раскладка как раз называет
				// правильно (tech_card_id совпадает — потому запрос выше её и находит), неверна её
				// ПРИНАДЛЕЖНОСТЬ. Клиент такого не шлёт (markersOfColorway отсеивает прогонные до
				// того, как их можно выбрать), так что это доводка провода, а не поломка экрана.
				if row.RunId.Valid {
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d].norm_marker_id", i),
						"marker_belongs_to_run",
						fmt.Sprintf("раскладка %d принадлежит прогону %d", id, row.RunId.Int64),
						"источником нормы может быть только карточная раскладка: раскройная умирает "+
							"вместе со своим прогоном и нормой быть не может — примените норму с "+
							"карточной раскладки этой ткани или пришлите 0, чтобы снять штамп")
				}
				// И ТОТ ЖЕ ЗАПРОС ОТКАЗЫВАЕТ ЧЕРНОВИКУ (0299). Штамп утверждает «расход в этой строке
				// взят вот с той раскладки», а черновик расхода не называет ВООБЩЕ: на проводе у него
				// нет ни скалярного числа, ни пер-размерных (TechCardMarkerSummaryToPb withholds both).
				// Значит штамп на черновик — ссылка на источник, которого не было, и отчёт о
				// расхождении карточки печатал бы её как настоящую. Отдельная причина, а не
				// marker_not_on_card: та отправила бы искать удалённую строку, а искать надо бюджет.
				if row.IsDraft {
					return entity.NewFieldViolation(fmt.Sprintf("usages[%d].norm_marker_id", i),
						"marker_is_draft", fmt.Sprintf("раскладка %d", id),
						entity.MarkerDraftNormRefusal(row.Name, row.PlacedCount, row.TotalCount))
				}
			}
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
					(colorway_id, bom_item_id, bom_item_index, placement, color, pantone, consumption, consumption_source, waste_selvedge_pct, waste_cut_pct, norm_marker_id, norm_applied_at, quantity, piece_id, piece_index, material_id, display_order)
				VALUES (:colorway_id, :bom_item_id, :bom_item_index, :placement, :color, :pantone, :consumption, :consumption_source, :waste_selvedge_pct, :waste_cut_pct, :norm_marker_id, :norm_applied_at, :quantity, :piece_id, :piece_index, :material_id, :display_order)`,
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
					"norm_marker_id":     r.provenance.markerID,
					"norm_applied_at":    r.provenance.appliedAt,
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
		SELECT id, bom_item_id, piece_id, material_id, bom_item_index, placement, color, pantone, consumption, consumption_source, waste_selvedge_pct, waste_cut_pct, norm_marker_id, norm_applied_at, quantity, piece_index
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
