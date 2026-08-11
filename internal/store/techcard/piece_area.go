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

// ПЛОЩАДИ ДЕТАЛЕЙ КРОЯ (Ф0, 0297) — the store half.
//
// The safety property is the one 0280 established and this table inherits verbatim: THE CLIENT NEVER
// SUPPLIES THE FINGERPRINT. It sends the areas it measured and the list of sheets it measured them
// from; the server looks those sheets up in its own tech_card_size_pattern rows, refuses a list that
// does not match the scope's current membership, and computes the fingerprint from what IT found.
//
// The write is a FULL REPLACE OF ONE SCOPE, never of the card. A card's fabrics are parsed
// independently (the pack downloads what it can and reports the rest as warnings), so a card-wide
// replace would let a failed lining download delete a perfectly good main-fabric measurement — and
// delete it silently, on an action the operator thinks of as «refresh».

// GetTechCardPieceAreas returns a card's stored areas grouped by fabric scope, with staleness
// already resolved against today's sheets.
//
// A card with no rows is not an error and not an empty measurement: the reader must turn a missing
// scope into «nobody has measured this fabric», which is a different sentence — and a different
// next action — from «this fabric needs no cloth».
func (s *Store) GetTechCardPieceAreas(ctx context.Context, techCardID int) (map[string]entity.PieceAreaScope, error) {
	rows, err := storeutil.QueryListNamed[entity.PieceAreaRow](ctx, s.DB, `
		SELECT id, tech_card_id, scope_key, piece_line_key, size_id, area_cm2,
		       contour_layer, seam_allowance_mm, hulled, ambiguous_pick,
		       sheet_fingerprint, parsed_by, parsed_at
		FROM tech_card_piece_area
		WHERE tech_card_id = :id
		ORDER BY scope_key, piece_line_key, size_key`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load piece areas of tech card %d: %w", techCardID, err)
	}
	if len(rows) == 0 {
		return map[string]entity.PieceAreaScope{}, nil
	}
	current, err := s.scopeFingerprints(ctx, s.DB, techCardID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]entity.PieceAreaScope, len(current))
	for _, r := range rows {
		sc := out[r.ScopeKey]
		sc.ScopeKey = r.ScopeKey
		sc.Rows = append(sc.Rows, r)
		sc.CurrentFingerprint = current[r.ScopeKey]
		// STALE АГРЕГИРУЕТСЯ ЧЕРЕЗ OR, А НЕ ПЕРЕЗАПИСЫВАЕТСЯ ПОСЛЕДНЕЙ СТРОКОЙ. Одна транзакция
		// пишет весь скоуп одним отпечатком, так что разнобой означать может только повреждение —
		// но если он всё же случился, вердикт обязан быть «устарело», а не «зависит от того, какая
		// строка легла последней». Осторожность здесь стоит одного «||».
		sc.Stale = sc.Stale || current[r.ScopeKey] != r.SheetFingerprint
		out[r.ScopeKey] = sc
	}
	return out, nil
}

// scopeFingerprints computes TODAY's source fingerprint for every fabric scope of a card — sheets
// AND блок→деталь links, because both are what the measurement was derived from.
//
// A scope with no sheets simply does not appear: its stored areas then compare against "" and read
// as stale, which is the honest answer — the files they were measured from are gone.
func (s *Store) scopeFingerprints(ctx context.Context, db dependency.DB, techCardID int) (map[string]string, error) {
	sheets, err := storeutil.QueryListNamed[patternSheetRow](ctx, db, patternSheetsQuery,
		map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load pattern sheets of tech card %d: %w", techCardID, err)
	}
	blocks, err := scopeBlockRefs(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	byScope := map[string][]entity.PatternSheetRef{}
	for _, sh := range sheets {
		k := entity.FabricScopeKey(sh.FabricPurpose, sh.BomLineKey)
		byScope[k] = append(byScope[k], entity.PatternSheetRef{
			LineKey: sh.LineKey, URL: sh.URL, Version: sh.Version,
		})
	}
	out := make(map[string]string, len(byScope))
	for k, refs := range byScope {
		out[k] = entity.PieceAreaSourceFingerprint(refs, blocks[k])
	}
	return out, nil
}

// scopeBlockRefs loads a card's блок→деталь links, bucketed by fabric scope. A link whose piece is
// gone does not appear (the FK cascades it away), which is exactly the event the fingerprint has to
// notice.
func scopeBlockRefs(ctx context.Context, db dependency.DB, techCardID int) (map[string][]entity.PieceAreaBlockRef, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ScopeKey     string `db:"scope_key"`
		BlockName    string `db:"block_name"`
		PieceLineKey string `db:"piece_line_key"`
	}](ctx, db, `
		SELECT COALESCE(NULLIF(a.fabric_purpose, ''), a.bom_line_key) AS scope_key,
		       a.block_name,
		       COALESCE(p.line_key, '') AS piece_line_key
		FROM tech_card_piece_dxf_block a
		JOIN tech_card_piece p ON p.id = a.piece_id
		WHERE a.tech_card_id = :id`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load dxf block links of tech card %d: %w", techCardID, err)
	}
	out := make(map[string][]entity.PieceAreaBlockRef, len(rows))
	for _, r := range rows {
		out[r.ScopeKey] = append(out[r.ScopeKey], entity.PieceAreaBlockRef{
			BlockName: r.BlockName, PieceLineKey: r.PieceLineKey,
		})
	}
	return out, nil
}

// sameMeasurement reports whether a submitted set is byte-for-byte what is already stored, under the
// same source fingerprint. Compared field by field rather than by a digest so that adding a column
// later cannot silently make two different measurements look equal.
func sameMeasurement(stored []entity.PieceAreaRow, submitted []entity.PieceAreaInput, fingerprint string) bool {
	if len(stored) != len(submitted) {
		return false
	}
	type key struct {
		piece string
		size  int64
	}
	have := make(map[key]entity.PieceAreaRow, len(stored))
	for _, r := range stored {
		if r.SheetFingerprint != fingerprint {
			return false
		}
		have[key{strings.ToUpper(strings.TrimSpace(r.PieceLineKey)), r.SizeId.Int64}] = r
	}
	for _, s := range submitted {
		r, ok := have[key{strings.ToUpper(strings.TrimSpace(s.PieceLineKey)), s.SizeId.Int64}]
		if !ok ||
			!r.AreaCm2.Equal(s.AreaCm2) ||
			r.ContourLayer != s.ContourLayer ||
			!r.SeamAllowanceMm.Equal(s.SeamAllowanceMm) ||
			r.Hulled != s.Hulled ||
			r.AmbiguousPick != s.AmbiguousPick {
			return false
		}
	}
	return true
}

// pieceSetDiff returns "" when the submitted pieces are exactly the scope's expected set, and a
// readable description otherwise.
func pieceSetDiff(expected, submitted map[string]bool) string {
	var missing, extra []string
	for k := range expected {
		if !submitted[k] {
			missing = append(missing, k)
		}
	}
	for k := range submitted {
		if !expected[k] {
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
		parts = append(parts, "not measured: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "not in this fabric scope: "+strings.Join(extra, ", "))
	}
	return strings.Join(parts, "; ")
}

// SaveTechCardPieceAreas replaces ONE fabric scope's measured areas.
//
// In a transaction because the sheet set it fingerprints and the rows it writes must be ONE
// snapshot: reading the sheets, then having somebody replace one, then writing a fingerprint of the
// OLD set would produce areas claiming to be current for files nobody measured — the forgery the
// fingerprint exists to prevent, reached by accident rather than malice.
func (s *Store) SaveTechCardPieceAreas(ctx context.Context, in entity.PieceAreaWrite) (entity.PieceAreaResult, error) {
	var out entity.PieceAreaResult
	scopeKey := strings.TrimSpace(in.ScopeKey)
	if scopeKey == "" {
		return out, entity.NewFieldViolation("scope_key", "required", "",
			"name the fabric scope: its назначение, or the BOM line's line_key when the card has not been sorted")
	}
	if len(in.Rows) == 0 {
		// An empty set is NOT «this fabric needs no pieces» — it is a parse that found nothing, and
		// storing it would turn a failed read into a confident zero area, i.e. a free garment.
		return out, entity.NewFieldViolation("areas", "empty", scopeKey,
			"the parse produced no areas for this fabric — nothing is stored; check the contour layer and the block↔piece links")
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// РЕЛИЗ ЗАМОРАЖИВАЕТ СОДЕРЖИМОЕ, А ПЛОЩАДИ — СОДЕРЖИМОЕ. С тех пор как они уезжают в
		// слепок релиза (dto.ConvertEntityTechCardToPb), запись поверх released-карточки развела бы
		// живую карточку со слепком, по которому уже режут, и сделала бы состав слепка зависящим от
		// того, кто закоммитился первым. Проверка стоит ПЕРВОЙ и внутри той же транзакции — иначе
		// релиз, случившийся между проверкой и вставкой, всё равно бы её обошёл.
		if err := storeutil.RequireMutableTechCard(ctx, db, in.TechCardId); err != nil {
			return err
		}
		sheets, err := storeutil.QueryListNamed[patternSheetRow](ctx, db, patternSheetsQuery,
			map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load pattern sheets of tech card %d: %w", in.TechCardId, err)
		}
		var mine []entity.PatternSheetRef
		for _, sh := range sheets {
			if entity.FabricScopeKey(sh.FabricPurpose, sh.BomLineKey) != scopeKey {
				continue
			}
			mine = append(mine, entity.PatternSheetRef{LineKey: sh.LineKey, URL: sh.URL, Version: sh.Version})
		}
		if len(mine) == 0 {
			return entity.NewFieldViolation("scope_key", "scope_has_no_sheets", scopeKey,
				"this fabric scope carries no pattern sheets on the server — upload them, or check that the scope key matches the card's назначение / line_key")
		}
		if diff := sheetSetDiff(mine, in.SheetLineKeys); diff != "" {
			return entity.NewFieldViolation("sheet_line_keys", "sheet_set_mismatch", diff,
				"re-run the measurement — the scope's sheets changed between the parse and this call, and areas built over a different set of files would answer for files nobody read")
		}
		// КОМПЛЕКТ ОБЯЗАН БЫТЬ ПОЛНЫМ, И ЭТО ПРОВЕРЯЕМОЕ УТВЕРЖДЕНИЕ, А НЕ ОБЕЩАНИЕ В КОММЕНТАРИИ.
		//
		// Ожидаемый состав скоупа — детали, у которых В ЭТОМ СКОУПЕ есть привязка блока чертежа
		// (tech_card_piece_dxf_block, 0262/0267). Именно из этих блоков клиент и меряет площадь, так
		// что любое расхождение означает ровно одно: измеряли не то, что карточка называет своим.
		//
		// Проверка нужна В ОБЕ СТОРОНЫ. Лишняя деталь — это чужой скоуп (у детали подкладки свой
		// контур и своя площадь, T4), и принять её значило бы приписать основной ткани площадь
		// подкладочной. Недостающая — это неполный комплект, а неполный комплект занижает площадь
		// изделия, заниженная площадь занижает норму, и обнаруживается это на складе, когда ткань
		// кончилась, а не на экране, где число придумали.
		aliasPieces, err := storeutil.QueryListNamed[struct {
			PieceLineKey string `db:"piece_line_key"`
		}](ctx, db, `
			SELECT COALESCE(p.line_key, '') AS piece_line_key
			FROM tech_card_piece_dxf_block a
			JOIN tech_card_piece p ON p.id = a.piece_id
			WHERE a.tech_card_id = :id
			  AND COALESCE(NULLIF(a.fabric_purpose, ''), a.bom_line_key) = :scope`,
			map[string]any{"id": in.TechCardId, "scope": scopeKey})
		if err != nil {
			return fmt.Errorf("load dxf block links of scope %q: %w", scopeKey, err)
		}
		expected := make(map[string]bool, len(aliasPieces))
		for _, a := range aliasPieces {
			if k := strings.ToUpper(strings.TrimSpace(a.PieceLineKey)); k != "" {
				expected[k] = true
			}
		}
		if len(expected) == 0 {
			// Без привязок блоков полноту доказать НЕЧЕМ, и «примем что дали» здесь — это молчаливое
			// согласие на любой недобор.
			return entity.NewFieldViolation("scope_key", "scope_has_no_block_links", scopeKey,
				"this fabric scope has no блок→деталь links, so a complete set cannot be proven — link the DXF blocks to the pieces first")
		}
		// Детали карточки — для отдельной, более понятной жалобы на ключ, которого вообще нет.
		pieces, err := storeutil.QueryListNamed[struct {
			LineKey string `db:"line_key"`
		}](ctx, db, `SELECT COALESCE(line_key, '') AS line_key FROM tech_card_piece WHERE tech_card_id = :id`,
			map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load pieces of tech card %d: %w", in.TechCardId, err)
		}
		known := make(map[string]bool, len(pieces))
		for _, p := range pieces {
			if k := strings.ToUpper(strings.TrimSpace(p.LineKey)); k != "" {
				known[k] = true
			}
		}
		sizeRows, err := storeutil.QueryListNamed[struct {
			SizeId int `db:"size_id"`
		}](ctx, db, `SELECT size_id FROM tech_card_size WHERE tech_card_id = :id`,
			map[string]any{"id": in.TechCardId})
		if err != nil {
			return fmt.Errorf("load size range of tech card %d: %w", in.TechCardId, err)
		}
		cardSizes := make(map[int]bool, len(sizeRows))
		for _, sr := range sizeRows {
			cardSizes[sr.SizeId] = true
		}

		var unknown []string
		submitted := map[string]bool{}
		seen := map[string]bool{}
		for _, r := range in.Rows {
			k := strings.ToUpper(strings.TrimSpace(r.PieceLineKey))
			if k == "" || !known[k] {
				unknown = append(unknown, r.PieceLineKey)
				continue
			}
			submitted[k] = true
			// РАЗМЕР ОБЯЗАН БЫТЬ РАЗМЕРОМ ЭТОЙ КАРТОЧКИ. Площадь, записанная на размер вне ряда,
			// не попадёт ни в один расчёт (все они идут по ряду) и будет молча висеть, делая
			// комплект «полным» для читателя, считающего строки.
			if r.SizeId.Valid && !cardSizes[int(r.SizeId.Int64)] {
				return entity.NewFieldViolation("areas", "size_not_in_range", r.PieceLineKey,
					"this size is not in the card's size range — measure the range the card declares")
			}
			// A duplicate (piece, size) in ONE payload is a client bug that the UNIQUE key would
			// report as a driver error with no field on it. Named here instead.
			dup := fmt.Sprintf("%s|%d", k, r.SizeId.Int64)
			if seen[dup] {
				return entity.NewFieldViolation("areas", "duplicate_piece_size", r.PieceLineKey,
					"one piece has two areas for the same size in this payload — measure once per (piece, size)")
			}
			seen[dup] = true
			if !r.AreaCm2.IsPositive() {
				return entity.NewFieldViolation("areas", "non_positive_area", r.PieceLineKey,
					"a piece with zero or negative area is a failed measurement, not a small piece")
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return entity.NewFieldViolation("areas", "unknown_piece", strings.Join(unknown, ", "),
				"these line keys are not pieces of this card — re-read the card before measuring")
		}
		if diff := pieceSetDiff(expected, submitted); diff != "" {
			return entity.NewFieldViolation("areas", "piece_set_mismatch", diff,
				"measure the WHOLE scope: a missing piece understates the garment's area (and the norm derived from it), an extra one belongs to another fabric")
		}

		blocks, err := scopeBlockRefs(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		fingerprint := entity.PieceAreaSourceFingerprint(mine, blocks[scopeKey])

		// ПОВТОР ТОГО ЖЕ ЗАМЕРА — НИЧЕГО НЕ ДЕЛАЕТ. Без этой проверки каждое нажатие «пересчитать»
		// на неизменившейся карточке двигало бы id и parsed_at, то есть переписывало бы провенанс
		// («измерено сегодня») там, где ничего не измеряли заново.
		existing, err := storeutil.QueryListNamed[entity.PieceAreaRow](ctx, db, `
			SELECT piece_line_key, size_id, area_cm2, contour_layer, seam_allowance_mm,
			       hulled, ambiguous_pick, sheet_fingerprint
			FROM tech_card_piece_area
			WHERE tech_card_id = :card AND scope_key = :scope
			ORDER BY piece_line_key, size_key`,
			map[string]any{"card": in.TechCardId, "scope": scopeKey})
		if err != nil {
			return fmt.Errorf("load stored piece areas of scope %q: %w", scopeKey, err)
		}
		if sameMeasurement(existing, in.Rows, fingerprint) {
			out = entity.PieceAreaResult{ScopeKey: scopeKey, SheetFingerprint: fingerprint, Stored: len(existing)}
			return nil
		}

		// FULL REPLACE OF THIS SCOPE ONLY. A piece that left the fabric must lose its area here, or
		// the garment keeps paying for cloth it no longer cuts.
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE FROM tech_card_piece_area WHERE tech_card_id = :card AND scope_key = :scope`,
			map[string]any{"card": in.TechCardId, "scope": scopeKey}); err != nil {
			return fmt.Errorf("clear piece areas of scope %q: %w", scopeKey, err)
		}
		for _, r := range in.Rows {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_piece_area
					(tech_card_id, scope_key, piece_line_key, size_id, area_cm2,
					 contour_layer, seam_allowance_mm, hulled, ambiguous_pick,
					 sheet_fingerprint, parsed_by)
				VALUES (:card, :scope, :piece, :size, :area,
				        :layer, :seam, :hulled, :ambiguous,
				        :fp, :by)`,
				map[string]any{
					"card":      in.TechCardId,
					"scope":     scopeKey,
					"piece":     strings.ToUpper(strings.TrimSpace(r.PieceLineKey)),
					"size":      r.SizeId,
					"area":      r.AreaCm2,
					"layer":     r.ContourLayer,
					"seam":      r.SeamAllowanceMm,
					"hulled":    r.Hulled,
					"ambiguous": r.AmbiguousPick,
					"fp":        fingerprint,
					"by":        in.ParsedBy,
				}); err != nil {
				return fmt.Errorf("write piece area %q: %w", r.PieceLineKey, err)
			}
		}
		out = entity.PieceAreaResult{
			ScopeKey:         scopeKey,
			SheetFingerprint: fingerprint,
			Stored:           len(in.Rows),
		}
		return nil
	})
	if err != nil {
		return entity.PieceAreaResult{}, err
	}
	return out, nil
}
