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

// GetTechCardDerivedCostInputsDigest fingerprints the cost inputs a card's own write does not carry:
// measured piece areas and the recipe's piece→fabric assignments (Ф-П).
//
// BOTH SIDES CALL THIS ONE FUNCTION — the card read (which puts the token on the entity) and the
// write path that stamps a fresh COSTING approval (which has only the payload, and the payload
// cannot contain either half). A second implementation on the read side is what the comment inside
// warns against: it would produce a different token about the same set, i.e. a signature stale from
// birth that nothing can clear.
//
// Empty means «neither exists», which the projection turns into «append nothing» — see
// entity.DerivedCostInputsDigest for why that is load-bearing rather than an optimisation.
func (s *Store) GetTechCardDerivedCostInputsDigest(ctx context.Context, techCardID int) (string, error) {
	areas, err := s.GetTechCardPieceAreas(ctx, techCardID)
	if err != nil {
		return "", err
	}
	// ОДНА РЕАЛИЗАЦИЯ НА ОБЕ СТОРОНЫ, И ЭТО НЕ АККУРАТНОСТЬ, А УСЛОВИЕ РАБОТОСПОСОБНОСТИ.
	//
	// Токен обязан совпасть у записи и у чтения, иначе подпись КОСТИНГ становится устаревшей сразу
	// после проставления и погасить её нечем. Соблазн посчитать его на чтении из уже загруженных
	// структур ловушечен: чтение рецепта НЕ выбирает piece_line_key/bom_line_key (это поля провода,
	// db:"-"), так что Go-версия увидела бы «#id», а SQL-версия — настоящие line_key. Два разных
	// токена об одном и том же множестве.
	//
	// Отбор пер-детальных строк повторяет entity.IsPieceMaterialAssignment: piece_id ЛИБО легаси
	// piece_index. Взять только piece_id значило бы, что на легаси-строке предикат денег и предикат
	// подписи расходятся — деньги её пропускают, подпись не видит.
	//
	// АРХИВНЫЕ КОЛОРВЕИ ИСКЛЮЧЕНЫ ТЕМ ЖЕ ФИЛЬТРОМ, ЧТО И В ЧТЕНИИ КАРТОЧКИ (lifecycle_status <> 4):
	// расчёт их не видит, и токен, который бы их видел, объявлял бы подпись изменившейся из-за
	// колорвея, не влияющего ни на одно число.
	//
	// ПИН АРТИКУЛА ВХОДИТ В ТОКЕН, потому что оценка берёт у пришпиленного артикула И ЦЕНУ, И ШИРИНУ:
	// перешпилить деталь на другой рулон значит переоценить изделие, и подпись обязана это увидеть.
	//
	// ГЕОМЕТРИЯ АРТИКУЛА (ширина, кромка, единица) ВХОДИТ ПО ТОЙ ЖЕ ПРИЧИНЕ, хотя живёт в каталоге, а
	// не на карточке: норма — это площадь, делённая на РАСКРОЙНУЮ ширину, так что правка ширины или
	// кромки в справочнике переоценивает изделие, ничего не трогая в карточке. Подпись, не увидевшая
	// этого, осталась бы зелёной над другим числом.
	//
	// ЦЕНА артикула сюда НЕ входит — сознательно и с известной дырой. Каталожная цена пина не
	// заморожена и сегодня (см. слепок релиза), а хешировать её здесь значило бы устаревание подписи
	// КОСТИНГ у всех карточек, где этот артикул, при каждой переоценке справочника. Это решение
	// владельца, а не разработчика; до него дыра остаётся ровно там, где была.
	//
	// pieces_per_garment ВХОДИТ В ТОКЕН, потому что Ф1 умножает на него площадь одного контура:
	// правка «этой детали идёт две» меняет себестоимость, и подпись обязана это увидеть. Он уже
	// хешируется в CONSTRUCTION, но подпись КОСТИНГ — про другое утверждение и своей зависимости
	// делегировать не может.
	rows, err := storeutil.QueryListNamed[struct {
		ColorwayId       int    `db:"colorway_id"`
		PieceKey         string `db:"piece_key"`
		BomKey           string `db:"bom_key"`
		PiecesPerGarment int    `db:"pieces_per_garment"`
		PinnedMaterialId int64  `db:"pinned_material_id"`
		ArticleGeometry  string `db:"article_geometry"`
	}](ctx, s.DB, `
		SELECT u.colorway_id,
		       COALESCE(p.line_key, CONCAT('#', COALESCE(u.piece_id, 0)), '') AS piece_key,
		       COALESCE(b.line_key, CONCAT('#', COALESCE(u.bom_item_id, 0)), '') AS bom_key,
		       COALESCE(p.pieces_per_garment, 0) AS pieces_per_garment,
		       COALESCE(u.material_id, 0) AS pinned_material_id,
		       CONCAT_WS('|',
		           COALESCE(NULLIF(fa.width_cm, 0), m.fabric_width, ''),
		           COALESCE(fa.selvedge_cm, ''),
		           COALESCE(m.unit, '')
		       ) AS article_geometry
		FROM tech_card_colorway_usage u
		JOIN product c ON c.id = u.colorway_id AND c.style_id = :id AND c.lifecycle_status <> 4
		LEFT JOIN tech_card_piece p ON p.id = u.piece_id
		LEFT JOIN tech_card_bom_item b ON b.id = u.bom_item_id
		LEFT JOIN material m ON m.id = COALESCE(NULLIF(u.material_id, 0), b.material_id)
		LEFT JOIN material_fabric_attr fa ON fa.material_id = m.id
		WHERE u.piece_id IS NOT NULL OR u.piece_index IS NOT NULL`, map[string]any{"id": techCardID})
	if err != nil {
		return "", fmt.Errorf("load piece→fabric assignments of tech card %d: %w", techCardID, err)
	}
	assignments := make([]entity.PieceFabricAssignment, 0, len(rows))
	for _, r := range rows {
		assignments = append(assignments, entity.PieceFabricAssignment{
			ColorwayKey:      r.ColorwayId,
			PieceKey:         r.PieceKey,
			BomKey:           r.BomKey,
			PiecesPerGarment: r.PiecesPerGarment,
			PinnedMaterialId: r.PinnedMaterialId,
			ArticleGeometry:  r.ArticleGeometry,
		})
	}
	return entity.DerivedCostInputsDigest(areas, assignments), nil
}

// scopeFingerprints computes TODAY's source fingerprint for every fabric scope of a card — sheets
// AND блок→деталь links, because both are what the measurement was derived from.
//
// A scope with no sheets simply does not appear: its stored areas then compare against "" and read
// as stale, which is the honest answer — the files they were measured from are gone.
func (s *Store) scopeFingerprints(ctx context.Context, db dependency.DB, techCardID int) (map[string]string, error) {
	lines, err := loadRollGoodsLines(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	byScope, err := scopeSheetRefs(ctx, db, techCardID, lines)
	if err != nil {
		return nil, err
	}
	blocks, err := scopeBlockRefs(ctx, db, techCardID, lines)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(byScope))
	for k, refs := range byScope {
		out[k] = entity.PieceAreaSourceFingerprint(refs, blocks[k])
	}
	return out, nil
}

// scopeSheetRefs loads a card's pattern sheets bucketed by the ТКАНЬ they address.
//
// СШИВАЮТСЯ ОНИ С ЛИЧНОСТЬЮ ТКАНИ, А НЕ С ВЕДРОМ ЗАПИСИ (entity.FabricScopeIdentity). Ключ, под
// которым площади ХРАНЯТСЯ, приходит от клиента, а клиент группирует по сегодняшнему BOM
// (bom-purpose.ts scopeKeyOfBinding); сверять с ним то, что назвала сама запись, значит сравнивать
// разные вопросы — и на разобранной по назначениям карточке они расходятся молча.
func scopeSheetRefs(ctx context.Context, db dependency.DB, techCardID int, lines []entity.RollGoodsLine) (map[string][]entity.PatternSheetRef, error) {
	sheets, err := storeutil.QueryListNamed[patternSheetRow](ctx, db, patternSheetsQuery,
		map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load pattern sheets of tech card %d: %w", techCardID, err)
	}
	out := map[string][]entity.PatternSheetRef{}
	for _, sh := range sheets {
		k := entity.FabricScopeIdentity(sh.FabricPurpose, sh.BomLineKey, lines)
		out[k] = append(out[k], entity.PatternSheetRef{
			LineKey: sh.LineKey, URL: sh.URL, Version: sh.Version,
		})
	}
	return out, nil
}

// scopeBlockRefs loads a card's блок→деталь links, bucketed by the ТКАНЬ they address. A link whose
// piece is gone does not appear (the FK cascades it away), which is exactly the event the
// fingerprint has to notice.
//
// ГРУППИРОВКА ИДЁТ НЕ ПО ХРАНИМОМУ scope_key, И ЭТО ТА САМАЯ ПОЧИНКА. Генерируемая колонка держит
// ВЕДРО УНИКАЛЬНОСТИ — то, что назвала сама связь, — а связь, заведённая до разбора карточки,
// называет СТРОКУ и называет её навсегда (чтение отдаёт обе половины, клиент возвращает их как
// есть). Карточка, у которой строку потом разложили в назначение, держала девять привязанных блоков
// под ключом строки, тогда как площади и деньги спрашивали про «main»: доказательство полноты
// находило пустоту и отвечало «блоки не привязаны» — на карточке, где привязано всё.
func scopeBlockRefs(ctx context.Context, db dependency.DB, techCardID int, lines []entity.RollGoodsLine) (map[string][]entity.PieceAreaBlockRef, error) {
	rows, err := storeutil.QueryListNamed[struct {
		BomLineKey    string `db:"bom_line_key"`
		FabricPurpose string `db:"fabric_purpose"`
		BlockName     string `db:"block_name"`
		PieceLineKey  string `db:"piece_line_key"`
	}](ctx, db, `
		SELECT COALESCE(a.bom_line_key, '') AS bom_line_key,
		       COALESCE(a.fabric_purpose, '') AS fabric_purpose,
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
		k := entity.FabricScopeIdentity(r.FabricPurpose, r.BomLineKey, lines)
		out[k] = append(out[k], entity.PieceAreaBlockRef{
			BlockName: r.BlockName, PieceLineKey: r.PieceLineKey,
		})
	}
	return out, nil
}

// sizeCoverageDiff returns "" when every measured piece covers the range in one of the two legal
// forms, and a readable description of the first violations otherwise.
//
// Legal forms, and nothing between them:
//   - EXACTLY ONE row with no size — the piece does not grade and enters every size whole;
//   - one row per size of the declared range — the piece grades.
//
// A mixture (a sizeless row AND sized rows for the same piece) is not a partial answer: it is two
// contradicting statements about the piece, and picking either one would be inventing the operator's
// intent.
func sizeCoverageDiff(sizesByPiece map[string]map[int]bool, cardSizes []int) string {
	var problems []string
	pieces := make([]string, 0, len(sizesByPiece))
	for p := range sizesByPiece {
		pieces = append(pieces, p)
	}
	sort.Strings(pieces)
	for _, p := range pieces {
		got := sizesByPiece[p]
		ungraded := got[0]
		sized := len(got) - boolToInt(ungraded)
		switch {
		case ungraded && sized > 0:
			problems = append(problems, p+": measured both with and without a size")
		case ungraded:
			// One sizeless row is a complete answer by itself.
		case len(cardSizes) == 0:
			problems = append(problems, p+": the card declares no size range, so a sized measurement cannot be complete")
		default:
			var missing []string
			for _, s := range cardSizes {
				if !got[s] {
					missing = append(missing, fmt.Sprintf("%d", s))
				}
			}
			if len(missing) > 0 {
				problems = append(problems, p+": missing sizes "+strings.Join(missing, ", "))
			}
		}
	}
	return strings.Join(problems, "; ")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
		// ОДИН НАБОР СТРОК ТКАНИ НА ВСЮ ТРАНЗАКЦИЮ. Листы и связи блоков разрешаются в ОДНУ И ТУ ЖЕ
		// личность ткани (entity.FabricScopeIdentity), и если бы каждая половина читала BOM сама,
		// правка назначения между двумя чтениями развела бы их по разным ключам внутри одной проверки.
		lines, err := loadRollGoodsLines(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		sheetsByScope, err := scopeSheetRefs(ctx, db, in.TechCardId, lines)
		if err != nil {
			return err
		}
		mine := sheetsByScope[scopeKey]
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
		//
		// СОСТАВ БЕРЁТСЯ ИЗ ТОГО ЖЕ scopeBlockRefs, ЧТО И ОТПЕЧАТОК НИЖЕ, а не отдельным запросом с
		// `scope_key = :scope`. Тот запрос сравнивал ВЕДРО записи с ЛИЧНОСТЬЮ ткани и на разобранной
		// карточке не находил ничего — «блоки не привязаны» при девяти привязанных блоках. Заодно
		// исчезает третье место, где скоуп разрешался бы в SQL: правило целиком осталось в Go.
		blocks, err := scopeBlockRefs(ctx, db, in.TechCardId, lines)
		if err != nil {
			return err
		}
		expected := make(map[string]bool, len(blocks[scopeKey]))
		for _, a := range blocks[scopeKey] {
			if k := strings.ToUpper(strings.TrimSpace(a.PieceLineKey)); k != "" {
				expected[k] = true
			}
		}
		if len(expected) == 0 {
			// Без привязок блоков полноту доказать НЕЧЕМ, и «примем что дали» здесь — это молчаливое
			// согласие на любой недобор.
			//
			// ТЕКСТ НАЗЫВАЕТ МЕСТО И ДЕЙСТВИЕ, А НЕ ЗАДАЧУ. Прежний («link the DXF blocks to the
			// pieces first») отправлял связывать блоки — и попадал в оператора, который их уже
			// связал: сопоставление живёт в форме карточки, пишется через setValue и до сохранения
			// карточки на сервер не приезжает вовсе. Человек смотрел в модалку, где связано всё, и
			// читал отказ как поломку. После починки скоупа (личность ткани вместо ведра записи,
			// см. scopeBlockRefs) это остаётся ЕДИНСТВЕННОЙ живой причиной пустоты здесь — значит
			// именно её и надо называть первой.
			return entity.NewFieldViolation("scope_key", "scope_has_no_block_links", scopeKey,
				"this fabric scope has no SAVED блок→деталь links, so a complete set cannot be proven. Open «↔ детали кроя» for this fabric on the PATTERNS tab, save the mapping, then save the card — a mapping that exists only in the form is invisible here")
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
		cardSizeOrder := make([]int, 0, len(sizeRows))
		for _, sr := range sizeRows {
			if !cardSizes[sr.SizeId] {
				cardSizeOrder = append(cardSizeOrder, sr.SizeId)
			}
			cardSizes[sr.SizeId] = true
		}

		var unknown []string
		submitted := map[string]bool{}
		seen := map[string]bool{}
		// Какие размеры измерены у каждой детали — вход проверки полноты по паре (деталь, размер).
		sizesByPiece := map[string]map[int]bool{}
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
			if sizesByPiece[k] == nil {
				sizesByPiece[k] = map[int]bool{}
			}
			sizesByPiece[k][int(r.SizeId.Int64)] = true // 0 = ungraded
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
		// ПОЛНОТА ПРОВЕРЯЕТСЯ ПО ПАРЕ (ДЕТАЛЬ, РАЗМЕР), А НЕ ПО ДЕТАЛИ.
		//
		// Проверка только по деталям пропускает ровно ту же беду, от которой защищает проверка по
		// деталям, просто на другой оси: набор, где деталь измерена на S и не измерена на L,
		// проходит целиком, и площадь изделия размера L молча теряет эту деталь. Норма L выходит
		// заниженной, и обнаруживается это там же, где всегда, — когда ткань кончилась.
		//
		// Деталь либо НЕ ГРАДУИРУЕТСЯ (ровно одна строка с пустым размером — она входит в каждый
		// размер целиком), либо градуируется и обязана иметь строку на КАЖДЫЙ размер ряда. Смесь
		// этих двух форм у одной детали — не «частичный ответ», а два разных утверждения о ней.
		if diff := sizeCoverageDiff(sizesByPiece, cardSizeOrder); diff != "" {
			return entity.NewFieldViolation("areas", "size_set_incomplete", diff,
				"measure every size of the card's range for each piece (or exactly once with no size when the piece does not grade)")
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
		// ЗАМЕР — МУТАЦИЯ АГРЕГАТА СТИЛЯ, ПОТОМУ ЧТО С Ф-П ОН ВХОДИТ В ПОДПИСЬ КОСТИНГ.
		//
		// Без этого бампа возможна такая последовательность: путь сохранения карточки читает токен
		// входов себестоимости, кто-то в этот момент перемеряет площади, карточка коммитит подпись
		// со СТАРЫМ токеном — и подпись рождается устаревшей, а оптимистический замок карточки этого
		// не замечает, потому что версия не двигалась. Рецепт колорвея бампает по той же причине.
		//
		// Бампается ТОЛЬКО при реальном изменении: повтор того же замера выше уходит в no-op и версию
		// не двигает, иначе «пересчитать» на неизменившейся карточке 409-ил бы чужую открытую форму
		// ни за что.
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id`,
			map[string]any{"id": in.TechCardId}); err != nil {
			return fmt.Errorf("bump lock for piece areas: %w", err)
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
