package techcard

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

// PR6 R1: the colourway write-path (insertTechCardColorways / insertTechCardColorwayUsages) was
// removed with the tech_card_colorway→product merge. Colourways are now first-class products created
// via CreateColorway, and a colourway's material recipe (usages, keyed by colorway_id = product.id)
// moves to the colourway write (ColorwayDevelopmentInsert.usages) in the T-B contract slice. The
// tech-card save no longer creates or full-replaces colourways; it only reads them for costing.

// insertTechCardPieces inserts the structural cut-pieces and, for each, its per-colourway fabric
// mapping (NF-05). It runs AFTER insertTechCardColorways in the child flow, so it re-queries the
// freshly-inserted colourways (in display order, same tx) to resolve each material's positional
// colorway_index into a real colorway_id — colourways are full-replace, so ids are recreated.
// clearTechCardPieceMaterials removes every piece_material of the card's pieces. It runs BEFORE the
// BOM upsert (Phase A of the reorder, §D5) so a BOM line the client is deleting is not falsely
// blocked by an old piece_material → bom_item RESTRICT ref: the fresh mapping is re-inserted by
// upsertTechCardPieces after the BOM ids resolve. A no-op on create (the card has no pieces yet).
func clearTechCardPieceMaterials(ctx context.Context, db dependency.DB, tcID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE FROM tech_card_piece_material
		WHERE piece_id IN (SELECT id FROM tech_card_piece WHERE tech_card_id = :id)`,
		map[string]any{"id": tcID}); err != nil {
		return fmt.Errorf("failed to clear tech card piece materials: %w", err)
	}
	return nil
}

type pieceExistingRow struct {
	Id      int    `db:"id"`
	LineKey string `db:"line_key"`
	// Name rides only the recipe path's SELECT (T4): the «две основные на одной детали» refusal
	// names the piece. The pieces upsert's own query leaves it empty.
	Name string `db:"name"`
	// Ungraded rides only the pieces upsert's own SELECT (0302): включение флага проверяется против
	// уже измеренных площадей, а для этого нужно ХРАНИМОЕ значение — по присланному переход не
	// отличить от повторного сохранения того же. Прочие пути этот столбец не выбирают, и false у них
	// ничего не значит.
	Ungraded bool `db:"ungraded"`
}

// calloutRef is a callout's canonical part name and the sketch it is pinned to, from the payload.
type calloutRef struct {
	part    string
	mediaID int
	pinned  bool // media_id > 0 (anchored on some sketch)
}

// calloutSync resolves a piece's callout_number against the card's payload (S6/S7/S8): the canonical
// part name (the single place a detail name is entered, §2.5) and whether the callout is anchored on a
// TECHNICAL sketch. A moodboard/unanchored callout carries no piece semantics (S7) — a piece pointing
// at one is marked detached, not name-synced.
type calloutSync struct {
	technicalMedia map[int]bool // raw media_id → is a technical sketch of this card
	byNumber       map[int]calloutRef
}

// buildCalloutSync indexes the payload's technical sketch media and its callouts. Both live in the same
// UpdateTechCard payload as the pieces, so no DB read is needed.
func buildCalloutSync(tc *entity.TechCardInsert) calloutSync {
	cs := calloutSync{technicalMedia: make(map[int]bool), byNumber: make(map[int]calloutRef, len(tc.Callouts))}
	for _, m := range tc.Media {
		if m.Category == entity.TechCardMediaCategoryTechnical {
			cs.technicalMedia[m.MediaId] = true
		}
	}
	for i := range tc.Callouts {
		c := &tc.Callouts[i]
		mediaID := 0
		if c.MediaId.Valid {
			mediaID = int(c.MediaId.Int32)
		}
		cs.byNumber[c.Number] = calloutRef{part: strings.TrimSpace(c.Part.String), mediaID: mediaID, pinned: mediaID > 0}
	}
	return cs
}

// apply syncs a piece's derived name from its technical-sketch callout and sets its detached flag
// (S7/S8): a piece linked to a technical callout takes that callout's part as its canonical name; a
// piece whose callout was removed or is a moodboard/unanchored callout keeps its own name but is
// marked detached (it has no live technical-sketch source — orphan-control keeps it, does not drop it).
func (cs calloutSync) apply(p *entity.TechCardPiece) {
	if !p.CalloutNumber.Valid {
		p.Detached = false
		return
	}
	ref, ok := cs.byNumber[int(p.CalloutNumber.Int32)]
	if ok && ref.pinned && cs.technicalMedia[ref.mediaID] {
		if ref.part != "" {
			p.Name = ref.part // S8: the detail name lives once, on callout.part; the piece derives it
		}
		p.Detached = false
		return
	}
	p.Detached = true
}

// The two halves of the piece upsert, named so a bind-without-a-database test can hold the REAL text
// rather than a copy of it (precedent: fabricDirectionLinesQuery). sqlx reads ':' anywhere in a named
// query — including inside a `--` comment — as a parameter, so a rationale note must live in Go
// comments, never in the SQL.
//
// cut_symmetry is guarded by :cut_symmetry_omitted on the UPDATE: a tab holding an older bundle does
// not send the field, and that must carry the stored marking forward rather than clear it — the
// marking cannot be reconstructed without a human holding the patterns. The digest half of the same
// contract is carryOmittedPieceCutSymmetryFrom in the API layer; both are required, and the second is
// the one that stops a sign-off from being born stale.
//
// The INSERT is deliberately NOT guarded: a brand-new row has no stored value to carry, so "omitted"
// and "explicitly unknown" are the same NULL — «не размечено».
//
// ungraded (0302) охраняется ТЕМ ЖЕ механизмом и по той же причине: у голого bool ноль неотличим от
// молчания, поэтому сохранение из вкладки, которая про градацию не спрашивает, сняло бы пометку со
// всех деталей карточки — незаметно, потому что снятая пометка выглядит как обычная карточка. Три
// ноги контракта те же самые: optional в прото, IF(:ungraded_omitted, …) здесь и
// carryOmittedPieceUngradedFrom перед пересчётом дайджеста CONSTRUCTION.
//
// РАЗМЕТКА ДУБЛИРОВАНИЯ (0304) идёт ТЕМ ЖЕ механизмом, но охраняется ОДНИМ флагом на ДВЕ колонки —
// :fusing_omitted закрывает и режим, и ширину. Раздельные флаги были бы не строже, а опаснее: пара
// «режим strip + ширина» осмысленна только целиком, и клиент, приславший режим без ширины, заявляет
// «полоса без числа» (это ошибка, её ловит entity.ValidatePieceFusing), а не «ширину не трогать». С
// двумя флагами такая правка молча донесла бы ширину от прошлой правки — чужую и правдоподобную.
//
// И ОДНО ОТЛИЧИЕ ОТ ОБОИХ СОСЕДЕЙ, БЕЗ КОТОРОГО ОХРАНА САМА СЕБЯ РОНЯЕТ: перенос вложен в
// IF(:fused, …, NULL). У cut_symmetry и ungraded хранимое значение можно нести дальше безусловно —
// они ни от чего на строке не зависят. Разметка дублирования зависит: chk_tcp_fusing_mode
// двухколоночный и требует, чтобы режим стоял ТОЛЬКО у fused-детали. Вкладка со старым бандлом
// поля не шлёт, но галку «дублируется» снять умеет — и голый перенос сохранил бы 'strip' рядом с
// fused=0, то есть уронил бы ВСЁ сохранение карточки в 3819 с именем колонки, которой эта вкладка
// не знает. Снятая галка обязана гасить разметку здесь же, в том же операторе, который её снимает:
// это не заявление оператора, а остаток прошлой правки, и означать он может ровно одно.
//
// Тот же довод — причина, по которой ЭТА ветка контракта не может обойтись переносом в API-слое
// (carryOmittedPieceFusingFrom): тот приводит в порядок ДАЙДЖЕСТ, а инвариант колонки обязан
// держаться и на путях, которые через парсер RPC не идут вовсе.
const (
	pieceUpdateQuery = `
				UPDATE tech_card_piece SET
					name=:name, pieces_per_garment=:pieces_per_garment, mirrored=:mirrored, grainline=:grainline,
					cut_symmetry=IF(:cut_symmetry_omitted, cut_symmetry, :cut_symmetry),
					ungraded=IF(:ungraded_omitted, ungraded, :ungraded),
					fusing_mode=IF(:fused, IF(:fusing_omitted, fusing_mode, :fusing_mode), NULL),
					fusing_width_mm=IF(:fused, IF(:fusing_omitted, fusing_width_mm, :fusing_width_mm), NULL),
					fused=:fused, callout_number=:callout_number, detached=:detached, note=:note, display_order=:display_order
				WHERE id=:id`

	pieceInsertQuery = `
				INSERT INTO tech_card_piece
					(tech_card_id, name, line_key, pieces_per_garment, mirrored, cut_symmetry, ungraded, grainline, fused, fusing_mode, fusing_width_mm, callout_number, detached, note, display_order)
				VALUES (:tech_card_id, :name, :line_key, :pieces_per_garment, :mirrored, :cut_symmetry, :ungraded, :grainline, :fused, :fusing_mode, :fusing_width_mm, :callout_number, :detached, :note, :display_order)`

	// The read side of the same row. It has to SELECT every column the write writes and the digest
	// hashes: a field the write stores and the read never loads makes the two projections permanently
	// disagree, and the sign-off it feeds can never match its own stored digest again — the failure the
	// piece-material read's line_key note records having already paid for once.
	pieceReadQuery = `
		SELECT id, tech_card_id, name, line_key, pieces_per_garment, mirrored, cut_symmetry, ungraded, grainline, fused, fusing_mode, fusing_width_mm, callout_number, detached, note
		FROM tech_card_piece
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order, id`
)

// upsertTechCardPieces reconciles a card's cut-pieces by line_key (S8), the same keyed upsert-diff the
// BOM uses (§2.3): a line_key already in the DB is UPDATEd in place (id stable — which is what lets a
// colourway recipe usage hold a real piece_id FK), a new line_key is INSERTed, and a line_key that
// vanished from the payload is DELETEd. A DELETE the FK RESTRICT blocks (piece still used by a
// colourway recipe usage) surfaces as a field-tagged error, not a raw 500 or a silent dangle — the
// deferred-from-0159 cross-aggregate guard. piece_material was cleared in Phase A, so each kept/new
// piece just re-inserts its per-colourway mapping with the BOM ids resolved by bomRes.
func upsertTechCardPieces(ctx context.Context, db dependency.DB, tcID int, pieces []entity.TechCardPiece, bomRes bomResolver, cs calloutSync) error {
	// PR6 R1/§14.3: a piece material addresses its colourway by explicit colorway_id = product.id.
	// Validate membership — the colourway must be one of this style's products (product.style_id = card).
	cwRows, err := storeutil.QueryListNamed[techCardPieceColorwayIDRow](ctx, db, `
		SELECT id FROM product WHERE style_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load colourway ids for pieces: %w", err)
	}
	validColorway := make(map[int]bool, len(cwRows))
	for _, r := range cwRows {
		validColorway[r.Id] = true
	}

	existingRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, db,
		`SELECT id, line_key, ungraded FROM tech_card_piece WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load existing pieces: %w", err)
	}
	existingByKey := make(map[string]int, len(existingRows))
	storedUngraded := make(map[string]bool, len(existingRows))
	for _, r := range existingRows {
		existingByKey[r.LineKey] = r.Id
		storedUngraded[r.LineKey] = r.Ungraded
	}

	// ИМЕНА С ВЫНОСОК — ДО ГАРДА, А НЕ ВНУТРИ ЦИКЛА ЗАПИСИ. cs.apply выводит имя детали из её
	// технической выноски (S8/S7) и помечает detached оторвавшиеся; работа независимая для каждой
	// детали, поэтому её порядок не меняет ничего — кроме одного: отказ ниже НАЗЫВАЕТ деталь, и
	// назвать её он обязан так, как она подписана на карточке, а не ULID'ом с провода.
	for i := range pieces {
		cs.apply(&pieces[i])
	}

	// ПОМЕТИТЬ ДЕТАЛЬ UNI МОЖНО, ТОЛЬКО ЕСЛИ ЕЁ ПЛОЩАДИ НЕ ИЗМЕРЕНЫ ПО РАЗМЕРАМ. Правило целиком, с
	// доводами, живёт в piece_area.go рядом со вторым его концом — отказом на записи площадей; здесь
	// только место, где переход происходит.
	//
	// ЧТО ИМЕННО ГАРАНТИРУЕТ ЭТО ЧТЕНИЕ. Само по себе — ничего против гонки: обычный snapshot-SELECT
	// не видит незакоммиченный замер, и одной «той же транзакции» здесь не хватает. Пару
	// сериализует строка tech_card: этот путь уже держит на ней X-замок (UPDATE tech_card SET
	// lock_version = … выполняется ДО записи детей), а путь замера берёт её FOR UPDATE первым же
	// действием своей транзакции. Поэтому замер либо целиком до нас (и мы видим его строки), либо
	// целиком после (и его собственная проверка видит нашу пометку); середины нет. Убрать любой из
	// двух замков — вернуть тихое противоречие, которое дальше не поймает уже ничто, потому что для
	// следующих сохранений флаг «уже true», то есть не переход.
	//
	// Запрос к площадям делается ТОЛЬКО когда что-то реально включается: на всей сегодняшней базе
	// помеченных деталей нет, значит обычное сохранение карточки не платит за это правило ничем.
	if flips := ungradedFlipKeys(pieces, storedUngraded); len(flips) > 0 {
		sizedAreas, err := sizedAreaPieceKeys(ctx, db, tcID)
		if err != nil {
			return err
		}
		if ve := ungradedFlipOnSizedAreas(pieces, flips, sizedAreas); ve != nil {
			return ve
		}
	}

	seen := make(map[string]bool, len(pieces))
	for i := range pieces {
		p := &pieces[i]
		key := strings.TrimSpace(p.LineKey)
		if key == "" {
			key = newLineKey() // legacy/keyless payload: server assigns a stable key
		}
		if seen[key] {
			return entity.NewFieldViolation(fmt.Sprintf("pieces[%d].line_key", i),
				"duplicate line_key within the payload", "", "each cut-piece needs a unique line_key")
		}
		// КАК КРОИТСЯ (0275) — refuse the unresolvable pair with words before MySQL refuses it with a
		// number. chk_tcp_mirrored_needs_even_count spans two columns, so its 3819 names
		// pieces_per_garment even when the save only changed the dropdown. This also covers the write
		// paths that do not come through the admin RPC parser at all.
		if err := entity.ValidatePieceCutSymmetry(
			fmt.Sprintf("pieces[%d].cut_symmetry", i), p.CutSymmetry, p.PiecesPerGarment); err != nil {
			return err
		}
		// КАК ДУБЛИРУЕТСЯ (0304) — тот же порядок и та же причина: сперва СВЕСТИ к форме, которую
		// CHECK'и принимают, потом судить то, что осталось.
		//
		// Нормализация здесь, а не только в парсере RPC, потому что этот стор — не единственная
		// дорога к колонке (сидер беты, будущие импорты), а chk_tcp_fusing_mode двухколоночный: он
		// сработает и на правке, которая тронула ОДНУ галку «дублируется», выдав 3819 с именем
		// колонки, которую оператор не трогал. Снятая галка обязана гасить разметку молча — это не
		// заявление оператора, а остаток прошлой правки, и означать он может ровно одно.
		p.NormalizeFusing()
		if err := entity.ValidatePieceFusing(
			fmt.Sprintf("pieces[%d].fusing_mode", i), p.Fused, p.FusingMode, p.FusingWidthMm); err != nil {
			return err
		}
		params := map[string]any{
			"tech_card_id":         tcID,
			"name":                 p.Name,
			"line_key":             key,
			"pieces_per_garment":   p.PiecesPerGarment,
			"mirrored":             p.Mirrored,
			"cut_symmetry":         p.CutSymmetry,
			"cut_symmetry_omitted": p.CutSymmetryOmitted,
			"ungraded":             p.Ungraded,
			"ungraded_omitted":     p.UngradedOmitted,
			"grainline":            p.Grainline,
			"fused":                p.Fused,
			"fusing_mode":          p.FusingMode,
			"fusing_width_mm":      p.FusingWidthMm,
			"fusing_omitted":       p.FusingOmitted,
			"callout_number":       p.CalloutNumber,
			"detached":             p.Detached,
			"note":                 p.Note,
			"display_order":        i,
		}
		var pieceID int
		if id, ok := existingByKey[key]; ok {
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, pieceUpdateQuery, params); err != nil {
				return fmt.Errorf("failed to update tech card piece: %w", err)
			}
			pieceID = id
		} else {
			newID, err := storeutil.ExecNamedLastId(ctx, db, pieceInsertQuery, params)
			if err != nil {
				return fmt.Errorf("failed to insert tech card piece: %w", err)
			}
			pieceID = newID
		}
		seen[key] = true

		for j := range p.Materials {
			m := &p.Materials[j]
			if !validColorway[m.ColorwayID] {
				return fmt.Errorf("tech card piece %q: colorway_id %d is not a colourway of this style", p.Name, m.ColorwayID)
			}
			// Resolve the refs to real bom_item ids (S2/S3). The legacy *_index columns are
			// still written for the transition (dropped in M3).
			fabricBomID, err := resolveBomRef(bomRes, m.BomLineKey, m.BomItemIndex,
				fmt.Sprintf("pieces[%d].materials[%d].bom_line_key", i, j))
			if err != nil {
				return err
			}
			fusingBomID, err := resolveBomRef(bomRes, m.FusingBomLineKey, m.FusingBomItemIndex,
				fmt.Sprintf("pieces[%d].materials[%d].fusing_bom_line_key", i, j))
			if err != nil {
				return err
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_piece_material
					(piece_id, colorway_id, bom_item_id, fusing_bom_item_id, bom_item_index, fusing_bom_item_index, note, display_order)
				VALUES (:piece_id, :colorway_id, :bom_item_id, :fusing_bom_item_id, :bom_item_index, :fusing_bom_item_index, :note, :display_order)`,
				map[string]any{
					"piece_id":              pieceID,
					"colorway_id":           m.ColorwayID,
					"bom_item_id":           fabricBomID,
					"fusing_bom_item_id":    fusingBomID,
					"bom_item_index":        m.BomItemIndex,
					"fusing_bom_item_index": m.FusingBomItemIndex,
					"note":                  m.Note,
					"display_order":         j,
				}); err != nil {
				return fmt.Errorf("failed to insert tech card piece material: %w", err)
			}
		}
	}

	// УДАЛЕНИЕ ДЕТАЛИ УНОСИТ ТО, ЧТО ВИСЕЛО НА НЕЙ. Иначе карточка запирается насмерть.
	//
	// Строка рецепта, привязанная к детали, держит её внешним ключом ON DELETE RESTRICT
	// (fk_usage_piece), а рецепт правится ДРУГИМ RPC — сохранение карточки его не редактирует. Пока
	// удаление просто упиралось в этот ключ, выход был один: вручную снять строку в каждом колорвее
	// и только потом удалять деталь. А строка эта появляется от самого обычного действия —
	// «назначить детали ткань», — так что на разобранной карточке держатся ВСЕ детали разом:
	// заменил чертёж на файл с другими именами блоков (новые детали заводятся сами) — и получай по
	// отказу на каждое сохранение, по одной детали за раз.
	//
	// ЧТО ИМЕННО УНОСИТСЯ И ПОЧЕМУ ЭТО НЕ ПОТЕРЯ ДАННЫХ СВЕРХ САМОГО УДАЛЕНИЯ. Строка рецепта — это
	// утверждение «ЭТА деталь кроится из этой ткани» (и, если вписана, её норма). Без детали у него
	// не остаётся подлежащего: ни один расчёт такую строку не читает (оценка идёт по НАЗНАЧЕННЫМ
	// деталям), а UNIQUE рецепта по ней не считает. Оставить её значило бы хранить факт о том, чего
	// нет. Замеренные площади — то же самое, но они висят на line_key без внешнего ключа, поэтому
	// их надо убирать явно: иначе от детали остаётся строка с площадью, которую никто не
	// перезапишет до следующего замера этой ткани.
	//
	// ЧЕЛОВЕК ОБ ЭТОМ ПРЕДУПРЕЖДЁН НА КЛИЕНТЕ (панель детали называет колорвеи-держатели и просит
	// подтверждение), а здесь стоит исполнение: удаление обязано быть атомарным с удалением самой
	// детали, а рецепт и деталь лежат в одной транзакции только тут.
	//
	// ЧУЖОЙ РЕДАКТОР РЕЦЕПТА НЕ ПОТЕРЯЕТСЯ МОЛЧА: оптимистическая версия колорвея — это общий
	// tech_card.lock_version стиля (см. UpdateColorwayRecipe), который это же сохранение и двигает,
	// так что параллельная правка рецепта получит честный конфликт, а не тихую перезапись.
	//
	// FK-ветка ниже остаётся backstop'ом: она ловит ссылки, о которых этот код не знает.
	doomedIDs := make([]int, 0, len(existingByKey))
	doomedKeys := make([]string, 0, len(existingByKey))
	for key, id := range existingByKey {
		if !seen[key] {
			doomedIDs = append(doomedIDs, id)
			doomedKeys = append(doomedKeys, key)
		}
	}
	if len(doomedIDs) > 0 {
		// УДАЛЯЕМ ТОЛЬКО СТРОКИ РЕЦЕПТА ЭТОГО ЖЕ СТИЛЯ, и это не перестраховка.
		//
		// Схема связывает строку рецепта с колорвеем и с деталью ДВУМЯ независимыми ключами и нигде
		// не требует, чтобы колорвей принадлежал стилю детали. Такой строки не создаёт ни один
		// живой путь записи, но `piece_id IN (...)` доверяет этому на слово — а плата за ошибку тут
		// не «отказ», а молча вычищенный рецепт ЧУЖОГО стиля. Соединение с product возвращает
		// проверку в сам запрос.
		//
		// Если такая строка всё же есть, она переживёт этот DELETE и упрётся в FK ниже: чужой
		// рецепт останется цел, а сохранение честно откажет вместо тихой порчи.
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE u FROM tech_card_colorway_usage u
			JOIN product p ON p.id = u.colorway_id
			WHERE u.piece_id IN (:ids) AND p.style_id = :card`,
			map[string]any{"ids": doomedIDs, "card": tcID}); err != nil {
			return fmt.Errorf("drop recipe lines of deleted cut-pieces: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE FROM tech_card_piece_area
			WHERE tech_card_id = :card AND piece_line_key IN (:keys)`,
			map[string]any{"card": tcID, "keys": doomedKeys}); err != nil {
			return fmt.Errorf("drop measured areas of deleted cut-pieces: %w", err)
		}
	}

	// Delete pieces that vanished from the payload.
	//
	// 1451 ЗДЕСЬ БОЛЬШЕ НЕ ЗНАЧИТ «СТРОКА РЕЦЕПТА» — их только что унесли выше, а операции карточки
	// стираются в начале этой же транзакции (см. список таблиц в UpdateTechCard), так что их связи
	// с деталями до сюда не доживают. Значит сюда доходит ссылка, о которой этот код не знает, и
	// назвать её конкретно нечем: врать про рецепт хуже, чем признать незнание и назвать деталь.
	for key, id := range existingByKey {
		if seen[key] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_piece WHERE id = :id`, map[string]any{"id": id}); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				return entity.NewFieldViolation("pieces",
					fmt.Sprintf("cut-piece %q is still referenced", key), "another record",
					"its recipe lines and measured areas are removed with it, so something else holds this piece — report it: the card cannot resolve this on its own")
			}
			return fmt.Errorf("failed to delete tech card piece: %w", err)
		}
	}
	return nil
}

// bomResolver maps a submitted BOM line to its persisted, stable id after an upsert-diff: by its
// stable line_key (the durable reference) and by its 0-based position in submission/display order
// (the legacy positional reference that operations/pieces still carry on the wire during the
// transition). It is how referrers resolve a real bom_item_id (S2/S3).
type bomResolver struct {
	byLineKey map[string]int
	ordered   []int // bom_item.id in submission order (== display_order)
}

// idForIndex resolves a legacy 0-based bom_item_index to a real id; ok=false when out of range (a
// dangling ref — the caller leaves the FK NULL rather than pointing at the wrong line).
func (r bomResolver) idForIndex(idx int) (int, bool) {
	if idx < 0 || idx >= len(r.ordered) {
		return 0, false
	}
	return r.ordered[idx], true
}

// containsID reports whether id belongs to the BOM reconciled for this card. ordered includes both
// retained rows and rows created by the current save, so referrers can be validated before insert.
func (r bomResolver) containsID(id int) bool {
	for _, candidate := range r.ordered {
		if candidate == id {
			return true
		}
	}
	return false
}

// resolveBomID turns a legacy positional bom_item_index (NULL-able) into a real bom_item id for a
// referrer's FK column, or SQL NULL when unset or out of range (a dangling ref).
func resolveBomID(res bomResolver, idx sql.NullInt32) any {
	if !idx.Valid {
		return nil
	}
	if id, ok := res.idForIndex(int(idx.Int32)); ok {
		return id
	}
	return nil
}

// resolveBomRef turns a referrer's BOM reference into a real bom_item id for its FK column: by stable
// line_key (preferred — positionality off the wire, WS3 follow-up), else the legacy positional index,
// else SQL NULL. Like resolveBomID, an unknown key / out-of-range index resolves to NULL (the ref was
// already broken) rather than pointing at the wrong line.
func resolveBomRef(res bomResolver, lineKey string, idx sql.NullInt32, keyField string) (any, error) {
	if key := strings.TrimSpace(lineKey); key != "" {
		if id, ok := res.byLineKey[key]; ok {
			return id, nil
		}
		// An unknown non-empty line_key is a field-tagged validation error, never a silent NULL —
		// the no-silent-no-op norm, same as the colourway recipe's resolveUsageBom (found live by
		// the beta A–L acceptance run: C.10's negative probe was accepted with 200). The legacy
		// positional index below keeps its transition tolerance (unresolvable -> NULL).
		return nil, entity.NewFieldViolation(keyField,
			fmt.Sprintf("no BOM line %q in this tech card", key), "", "reference an existing BOM line by its line_key")
	}
	return resolveBomID(res, idx), nil
}

type bomExistingRow struct {
	Id      int    `db:"id"`
	LineKey string `db:"line_key"`
	// Section rides only the recipe path's SELECT (roll-goods guard for marker provenance);
	// the BOM upsert's own query leaves it zero.
	Section sql.NullString `db:"section"`
	// Purpose and Name ride the same recipe-path SELECT only (T4): purpose feeds the derived layer
	// role (entity.DerivePieceLayerRole) behind the «две основные на одной детали» refusal, and the
	// name is what that refusal prints. Zero on the BOM upsert's own query.
	Purpose sql.NullString `db:"purpose"`
	Name    string         `db:"name"`
	// Price + provenance as stored, so the upsert can tell an edited price (restamp 'manual') from a
	// round-tripped one (keep the existing provenance, including the reprice action's 'catalog').
	UnitPrice       decimal.NullDecimal `db:"unit_price"`
	Currency        sql.NullString      `db:"currency"`
	PriceSource     sql.NullString      `db:"price_source"`
	PriceSnapshotAt sql.NullTime        `db:"price_snapshot_at"`
	// Wastage percent + provenance as stored (0296), for the same two distinctions one field over:
	// an edited percent resets to 'manual' whatever the wire claims (unless the handler VERIFIED the
	// claim against the live suggestion), an unchanged echo keeps its stored provenance verbatim.
	// Decided by entity.ResolveBomWastageProvenance — one pure rule for both statements.
	// applied_percent is the badge's self-check copy (MAJOR 4) — read so a desynced badge cannot be
	// carried forward as 'lays'.
	WastagePercent        decimal.NullDecimal `db:"wastage_percent"`
	WastageSource         sql.NullString      `db:"wastage_source"`
	WastageLayCount       sql.NullInt64       `db:"wastage_lay_count"`
	WastageAppliedAt      sql.NullTime        `db:"wastage_applied_at"`
	WastageAppliedPercent decimal.NullDecimal `db:"wastage_applied_percent"`
}

// bomPriceProvenance decides what price_source/price_snapshot_at a line carries after this save.
// The save path is a human act: a NEW priced line or a CHANGED price/currency is 'manual' as of now;
// an unchanged price keeps whatever provenance it had ('catalog' from a reprice, 'manual' from an
// earlier edit, or NULL on a pre-provenance row — an unchanged value's history must not be
// rewritten by a save that merely round-tripped it). A cleared price clears the provenance with it.
func bomPriceProvenance(existing *bomExistingRow, b *entity.TechCardBomItem, now time.Time) (sql.NullString, sql.NullTime) {
	if !b.UnitPrice.Valid {
		return sql.NullString{}, sql.NullTime{}
	}
	if existing != nil && existing.UnitPrice.Valid &&
		existing.UnitPrice.Decimal.Equal(b.UnitPrice.Decimal) &&
		strings.EqualFold(strings.TrimSpace(existing.Currency.String), strings.TrimSpace(b.Currency.String)) {
		// EqualFold to match the reprice action's own Changed test: a payload echoing 'eur' for a
		// stored 'EUR' is a round-trip, not an edit.
		return existing.PriceSource, existing.PriceSnapshotAt
	}
	return sql.NullString{String: entity.BomPriceSourceManual, Valid: true}, sql.NullTime{Time: now, Valid: true}
}

// bomWastageProvenance decides the wastage provenance a line carries after this save. A thin
// adapter over entity.ResolveBomWastageProvenance — the rule itself is pure and lives (and is
// tested) in entity; this function only translates the stored row into the rule's vocabulary.
// b.WastageClaimVerified is the HANDLER's verdict (verifyBomWastageClaims): the only door through
// which a fresh 'lays' claim becomes a badge — a direct store caller cannot open it, fail-closed.
func bomWastageProvenance(existing *bomExistingRow, b *entity.TechCardBomItem, now time.Time) entity.BomWastageProvenance {
	stored := entity.BomWastageProvenance{}
	storedPercent := decimal.NullDecimal{}
	if existing != nil {
		stored = entity.BomWastageProvenance{
			Source:         existing.WastageSource.String,
			LayCount:       existing.WastageLayCount,
			AppliedAt:      existing.WastageAppliedAt,
			AppliedPercent: existing.WastageAppliedPercent,
		}
		storedPercent = existing.WastagePercent
	}
	return entity.ResolveBomWastageProvenance(stored, storedPercent, b.WastagePercent,
		entity.BomWastageProvenance{Source: b.WastageSource, LayCount: b.WastageLayCount},
		!b.WastageProvenanceOmitted, b.WastageClaimVerified, now)
}

type colorwayBomUsageRefRow struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
	SKU  string `db:"sku"`
}

// The BOM upsert's two statements live as package-level constants so TestBomItemUpsertQueriesBind can
// bind them without a database. sqlx parses ':' ANYWHERE in a named query — including inside a `--`
// SQL comment — as a parameter, and a name the args map does not carry fails at BIND time, i.e. at
// request time on the card-save path. Every guarded column added here (purpose 0265, kind 0278) adds
// two params that are easy to add to one side only; the precedent is pieceUpdateQuery/pieceInsertQuery.
//
// The `*_omitted` guards are what stop a tab holding an older bundle from wiping a column it does not
// know about: absent on the wire means «keep what is stored», not «clear it». `kind` and `kind_note`
// share ONE decision (see parseTechCardBomItems) but keep two params, so the SQL reads as what it is —
// two columns, each guarded — rather than making the reader trust an off-screen coupling.
const bomItemUpdateQuery = `
	UPDATE tech_card_bom_item SET
		material_id=:material_id, section=:section,
		purpose=IF(:purpose_omitted, purpose, :purpose),
		purpose_note=IF(:purpose_omitted, purpose_note, :purpose_note),
		kind=IF(:kind_omitted, kind, :kind),
		kind_note=IF(:kind_note_omitted, kind_note, :kind_note),
		is_sample=IF(:is_sample_omitted, is_sample, :is_sample),
		name=:name, supplier=:supplier, supplier_ref=:supplier_ref,
		color=:color, composition=:composition, spec=:spec, unit=:unit, unit_price=:unit_price, currency=:currency,
		comment=:comment, display_order=:display_order, fabric_width=:fabric_width, fabric_weight_gsm=:fabric_weight_gsm,
		fabric_direction=IF(:fabric_direction_omitted, fabric_direction, :fabric_direction),
		wastage_percent=:wastage_percent,
		wastage_source=:wastage_source, wastage_lay_count=:wastage_lay_count, wastage_applied_at=:wastage_applied_at,
		wastage_applied_percent=:wastage_applied_percent,
		qty_per_garment=IF(:countable_omitted, qty_per_garment, :qty_per_garment),
		spare_qty=IF(:countable_omitted, spare_qty, :spare_qty),
		price_source=:price_source, price_snapshot_at=:price_snapshot_at
	WHERE id=:id`

const bomItemInsertQuery = `
	INSERT INTO tech_card_bom_item
		(tech_card_id, material_id, section, purpose, purpose_note, kind, kind_note, is_sample, name, supplier, supplier_ref,
		 color, composition, spec, unit, unit_price, currency, comment, display_order, fabric_width,
		 fabric_weight_gsm, fabric_direction, wastage_percent, wastage_source, wastage_lay_count, wastage_applied_at,
		 wastage_applied_percent, qty_per_garment, spare_qty, line_key, price_source, price_snapshot_at)
	VALUES (:tech_card_id, :material_id, :section, :purpose, :purpose_note, :kind, :kind_note, :is_sample, :name, :supplier, :supplier_ref,
		 :color, :composition, :spec, :unit, :unit_price, :currency, :comment, :display_order, :fabric_width,
		 :fabric_weight_gsm, :fabric_direction, :wastage_percent, :wastage_source, :wastage_lay_count, :wastage_applied_at,
		 :wastage_applied_percent, :qty_per_garment, :spare_qty, :line_key, :price_source, :price_snapshot_at)`

// validateBomKindSection enforces the kind↔section pairing (0278) the DB CHECK deliberately does not:
// a kind is legal only on a kind-eligible section, and within that, only in its own HOME section —
// except BomKindOther, the one section-agnostic value, which is the escape hatch of every eligible
// family at once (and the ONLY kind section='other' can carry).
//
// Both arms name the section that actually fired and how to get out of it. An unknown value is
// REFUSED rather than degraded: the read path may degrade a value it does not recognise (a row must
// never vanish), but a write that silently dropped a classification because the client spoke a newer
// vocabulary would be data loss the operator never asked for and could not see.
func validateBomKindSection(b *entity.TechCardBomItem, i int) error {
	if !b.Kind.Valid {
		return nil
	}
	field := fmt.Sprintf("bom_items[%d].kind", i)
	kind := entity.TechCardBomKind(b.Kind.String)
	home, known := entity.BomKindHomeSection(kind)
	if !known {
		return entity.NewFieldViolation(field, "unknown kind", b.Kind.String, "pick a kind from the list")
	}
	if !kindEligibleSections[b.Section] {
		return entity.NewFieldViolation(field,
			"kind applies only to "+kindEligibleSectionNames(),
			fmt.Sprintf("this line is section %q", b.Section),
			"clear the kind — roll goods are classified by purpose, and a label's type lives on the label spec")
	}
	if home != entity.BomKindAnySection && home != b.Section {
		return entity.NewFieldViolation(field,
			fmt.Sprintf("kind %q belongs to section %q", kind, home),
			fmt.Sprintf("this line is section %q", b.Section),
			fmt.Sprintf("move the line to section %q, or pick a kind of section %q", home, b.Section))
	}
	return nil
}

// validateBomCountableSection enforces the section boundary of the СЧЁТНОЙ НОРМЫ (0333): штуки на
// изделие и запас законны только на НЕмерной секции. Проверяется здесь, рядом с проверками
// назначения и вида, и по той же причине: двухколоночный CHECK (section ↔ qty_per_garment)
// выстрелил бы сырым MySQL 3819 на UPDATE'е, правящем ОДНУ секцию, и назвал бы оператору колонку,
// которой тот не касался.
//
// Отказ называет секцию, которая ФАКТИЧЕСКИ сработала, и способ выйти — образец
// validateBomKindSection. Предикат один на весь проект (entity.IsCountableSection), второй копии
// списка мерных семей быть не должно: клиентский MEASURED_SECTIONS — его зеркало.
func validateBomCountableSection(b *entity.TechCardBomItem, i int) error {
	if !b.QtyPerGarment.Valid && !b.SpareQty.Valid {
		return nil
	}
	if entity.IsCountableSection(b.Section) {
		return nil
	}
	field := fmt.Sprintf("bom_items[%d].qty_per_garment", i)
	if !b.QtyPerGarment.Valid {
		field = fmt.Sprintf("bom_items[%d].spare_qty", i)
	}
	return entity.NewFieldViolation(field,
		"a measured material is counted by its norm, not by the piece",
		fmt.Sprintf("this line is section %q", b.Section),
		"clear the per-garment count, and put the consumption on the colourway recipe row instead")
}

// upsertTechCardBom reconciles a card's BOM lines by line_key in one transaction instead of the old
// delete-all + reinsert (S2/S3 root): a line_key already in the DB is UPDATEd in place (its id is
// stable, which is what lets referrers hold a real FK), a new line_key is INSERTed, and a line_key
// that vanished from the payload is DELETEd — a DELETE the FK RESTRICT blocks (line still used by an
// operation/piece/colourway recipe) surfaces as a field-tagged error, not a raw 500. Returns a
// resolver so pieces/operations can turn their reference into a real bom_item_id.
func upsertTechCardBom(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardBomItem) (bomResolver, error) {
	res := bomResolver{byLineKey: make(map[string]int, len(items)), ordered: make([]int, 0, len(items))}

	existingRows, err := storeutil.QueryListNamed[bomExistingRow](ctx, db,
		`SELECT id, line_key, unit_price, currency, price_source, price_snapshot_at,
		        wastage_percent, wastage_source, wastage_lay_count, wastage_applied_at, wastage_applied_percent
		 FROM tech_card_bom_item WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return res, fmt.Errorf("failed to load existing bom lines: %w", err)
	}
	existingByKey := make(map[string]int, len(existingRows))
	existingRowByKey := make(map[string]*bomExistingRow, len(existingRows))
	for i := range existingRows {
		existingByKey[existingRows[i].LineKey] = existingRows[i].Id
		existingRowByKey[existingRows[i].LineKey] = &existingRows[i]
	}
	now := time.Now().UTC()

	seen := make(map[string]bool, len(items))
	for i := range items {
		b := &items[i]
		key := strings.TrimSpace(b.LineKey)
		if key == "" {
			key = newLineKey() // legacy/keyless payload: server assigns a stable key
		}
		if seen[key] {
			return res, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].line_key", i),
				"duplicate line_key within the payload", "", "each BOM line needs a unique line_key")
		}
		// НАЗНАЧЕНИЕ belongs to cloth (0265). It is checked HERE, against rollGoodsSections, rather
		// than at parse time with a second hand-written family list: the four families a marker may
		// lay out and the four a purpose may describe are the same four by construction, and the
		// comment above rollGoodsSectionList is explicit that proximity is not coupling — a fifth
		// family added there must not leave a stale copy behind in the dto.
		if b.Purpose.Valid && !rollGoodsSections[string(b.Section)] {
			return res, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].purpose", i),
				"purpose applies only to roll goods (fabric, lining, interlining, insulation)",
				fmt.Sprintf("this line is section %q", b.Section),
				"clear the purpose, or move the line to a roll-goods section")
		}
		// ЧТО ЭТО ЗА ПОЗИЦИЯ is the mirror of назначение and is checked in the same place, for the
		// same reason: eligibility is the roll-goods list's complement (kindEligibleSections), and a
		// second hand-written copy in the dto would go stale the moment a family moved.
		//
		// The DB CHECK closes the VOCABULARY only. The PAIRING lives here on purpose — a two-column
		// CHECK would fire as a raw 3819 on an UPDATE that touched nothing but `section` and name a
		// column the operator never edited (the argument 0275 makes for its even-count invariant,
		// pointing the other way: there the schema must carry it, here the schema must not).
		if err := validateBomKindSection(b, i); err != nil {
			return res, err
		}
		// СЧЁТНАЯ НОРМА (0333) — третья проверка того же семейства и в том же месте: секция решает,
		// считается ли строка штуками или нормой, и ответ обязан быть один на обе колонки пары.
		if err := validateBomCountableSection(b, i); err != nil {
			return res, err
		}
		params := bomItemParams(tcID, b, i, key)
		src, at := bomPriceProvenance(existingRowByKey[key], b, now)
		params["price_source"], params["price_snapshot_at"] = src, at
		// Провенанс процента раскроя (0296): финальную тройку решает чистое правило (verbatim при
		// молчании пары, сброс в 'manual' при правке значения через старый бандл, серверный штамп
		// applied_at на смене сути) — SQL пишет уже решённое, поэтому IF-гардов здесь нет.
		wp := bomWastageProvenance(existingRowByKey[key], b, now)
		params["wastage_source"] = wp.Source
		params["wastage_lay_count"] = wp.LayCount
		params["wastage_applied_at"] = wp.AppliedAt
		params["wastage_applied_percent"] = wp.AppliedPercent
		if id, ok := existingByKey[key]; ok {
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, bomItemUpdateQuery, params); err != nil {
				return res, fmt.Errorf("failed to update bom line: %w", err)
			}
			res.ordered = append(res.ordered, id)
			res.byLineKey[key] = id
		} else {
			newID, err := storeutil.ExecNamedLastId(ctx, db, bomItemInsertQuery, params)
			if err != nil {
				return res, fmt.Errorf("failed to insert bom line: %w", err)
			}
			res.ordered = append(res.ordered, newID)
			res.byLineKey[key] = newID
		}
		seen[key] = true
	}

	// Delete lines that vanished from the payload. FK RESTRICT blocks a line still referenced by an
	// operation/piece/colourway recipe — surface that as an actionable field-tagged error.
	for key, id := range existingByKey {
		if seen[key] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_bom_item WHERE id = :id`, map[string]any{"id": id}); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				conflicting := "an operation, piece or colourway recipe"
				howToFix := "remove that reference before deleting the BOM line"
				if refs, refErr := colorwayRecipeRefsForBom(ctx, db, id); refErr == nil && len(refs) > 0 {
					conflicting = "an operation or piece, and/or colourway recipes: " + strings.Join(refs, ", ")
					howToFix = "remove or move the listed colourway usages (and any operation/piece reference) before deleting the BOM line"
				}
				return res, entity.NewFieldViolation("bom_items",
					fmt.Sprintf("BOM line %q is still referenced", key), conflicting, howToFix)
			}
			return res, fmt.Errorf("failed to delete bom line: %w", err)
		}
	}
	return res, nil
}

// colorwayRecipeRefsForBom names the colourways that keep a BOM slot alive through
// tech_card_colorway_usage.bom_item_id. It is called only after the RESTRICT error, so the operator
// sees which recipes must be adjusted instead of a generic foreign-key failure.
func colorwayRecipeRefsForBom(ctx context.Context, db dependency.DB, bomItemID int) ([]string, error) {
	rows, err := storeutil.QueryListNamed[colorwayBomUsageRefRow](ctx, db, `
		SELECT DISTINCT p.id, COALESCE(NULLIF(p.dev_name, ''), '') AS name, COALESCE(p.sku, '') AS sku
		FROM tech_card_colorway_usage u
		JOIN product p ON p.id = u.colorway_id
		WHERE u.bom_item_id = :id
		ORDER BY name, sku, p.id`, map[string]any{"id": bomItemID})
	if err != nil {
		return nil, fmt.Errorf("load colourway recipe references for BOM item %d: %w", bomItemID, err)
	}
	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		sku := strings.TrimSpace(row.SKU)
		switch {
		case name != "" && sku != "":
			refs = append(refs, fmt.Sprintf("colourway %q (SKU %s)", name, sku))
		case name != "":
			refs = append(refs, fmt.Sprintf("colourway %q", name))
		case sku != "":
			refs = append(refs, fmt.Sprintf("colourway SKU %s", sku))
		default:
			refs = append(refs, fmt.Sprintf("colourway #%d", row.Id))
		}
	}
	return refs, nil
}

// bomItemParams maps a BOM line to named params for the upsert.
func bomItemParams(tcID int, b *entity.TechCardBomItem, displayOrder int, lineKey string) map[string]any {
	// СЧЁТНАЯ НОРМА НА МЕРНОЙ СЕКЦИИ СТИРАЕТСЯ, А НЕ СОХРАНЯЕТСЯ, и это не дубль
	// validateBomCountableSection: та проверяет ПРИСЛАННОЕ, а здесь закрывается дыра между
	// присланным и СОХРАНЁННЫМ.
	//
	// Сценарий целиком: в базе счётный слот с qty_per_garment=6 и spare_qty=1; вкладка со старым
	// бандлом меняет его секцию на trim и обеих колонок не шлёт. Проверка молчит (проверять
	// нечего), флаг присутствия говорит «не трогай», и IF в UPDATE сохраняет шестёрку — на строке,
	// где счётная норма запрещена по определению. Дальше резолвер её игнорирует из-за мерной
	// секции, и деньги строки исчезают ПОСЛЕ УСПЕШНОГО сохранения, которое оператор считает
	// безобидным.
	//
	// Поэтому «не трогай» здесь перекрывается: секция и счётность — одно утверждение, и сменивший
	// секцию сменил ответ. Состояние «мерная строка со счётной нормой» становится невыразимым, а не
	// «проверяемым на входе» — граница, которую нельзя обойти, забыв про неё.
	qtyPerGarment, spareQty, countableOmitted := b.QtyPerGarment, b.SpareQty, b.CountableOmitted
	if !entity.IsCountableSection(b.Section) {
		qtyPerGarment, spareQty, countableOmitted = decimal.NullDecimal{}, decimal.NullDecimal{}, false
	}
	return map[string]any{
		"tech_card_id": tcID,
		"material_id":  b.MaterialId,
		"section":      string(b.Section),
		// Присутствие, а не значение (0265). Отсутствующее поле означает «не трогай»: карточка
		// сохраняется целиком, а вкладка со старым бандлом этих полей не шлёт вовсе — без этого её
		// сейв стирал бы назначение у ВСЕХ строк карточки, и притом бесследно, потому что полей нет
		// в дайджесте подписи, а NULL неотличим от «ещё не разложили». Примечание ходит вместе с
		// назначением: они меняются одной парой, иначе примечание пережило бы смену назначения и
		// стало бы теневой ролью.
		"purpose":         b.Purpose,
		"purpose_omitted": b.PurposeOmitted,
		"purpose_note":    b.PurposeNote,
		// Та же пара для другой половины спецификации (0278). kind и kind_note СВЯЗАНЫ в схеме
		// (chk_bom_item_kind_note), поэтому их флаги ставятся вместе в parseTechCardBomItems: запись
		// одной половины поверх сохранённой второй — это строка, которую MySQL обязан отвергнуть.
		"kind":                     b.Kind,
		"kind_omitted":             b.KindOmitted,
		"kind_note":                b.KindNote,
		"kind_note_omitted":        b.KindNoteOmitted,
		"is_sample":                b.IsSample,
		"is_sample_omitted":        b.IsSampleOmitted,
		"name":                     b.Name,
		"supplier":                 b.Supplier,
		"supplier_ref":             b.SupplierRef,
		"color":                    b.Color,
		"composition":              b.Composition,
		"spec":                     b.Spec,
		"unit":                     b.Unit,
		"unit_price":               b.UnitPrice,
		"currency":                 b.Currency,
		"comment":                  b.Comment,
		"display_order":            displayOrder,
		"fabric_width":             b.FabricWidth,
		"fabric_weight_gsm":        b.FabricWeightGsm,
		"fabric_direction":         b.FabricDirection,
		"fabric_direction_omitted": b.FabricDirectionOmitted,
		"wastage_percent":          b.WastagePercent,
		// СЧЁТНАЯ НОРМА СЛОТА (0333) — та же пара под ОДНИМ флагом присутствия, что kind/kind_note:
		// количество и запас это две половины одного утверждения о слоте, и записать одну поверх
		// сохранённой второй значило бы сказать «пуговиц не задано, а запасных к ним одна».
		// Два параметра, один флаг — SQL читается как то, что он есть: две колонки, каждая под
		// своей защитой (см. bomItemUpdateQuery).
		"qty_per_garment":   qtyPerGarment,
		"spare_qty":         spareQty,
		"countable_omitted": countableOmitted,
		"line_key":          lineKey,
	}
}

// newLineKey mints a stable 26-char CHAR(26) key (ULID-shaped: base32 of 128 random bits) for a
// legacy/keyless BOM line so the upsert-diff has a durable handle even before clients send one.
func newLineKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

// insertTechCardDetails inserts the construction-description aspects and, for each, its
// reference media.
func insertTechCardDetails(ctx context.Context, db dependency.DB, tcID int, details []entity.TechCardDetail) error {
	for i := range details {
		d := &details[i]
		detailID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_detail (tech_card_id, detail_key, detail_text, display_order)
			VALUES (:tech_card_id, :detail_key, :detail_text, :display_order)`,
			map[string]any{
				"tech_card_id":  tcID,
				"detail_key":    d.Key,
				"detail_text":   d.Text,
				"display_order": i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card detail: %w", err)
		}
		if len(d.MediaIds) > 0 {
			rows := make([]map[string]any, 0, len(d.MediaIds))
			for j, mid := range d.MediaIds {
				rows = append(rows, map[string]any{
					"detail_id":     detailID,
					"media_id":      mid,
					"display_order": j,
				})
			}
			if err := storeutil.BulkInsert(ctx, db, "tech_card_detail_media", rows); err != nil {
				return fmt.Errorf("failed to insert tech card detail media: %w", err)
			}
		}
	}
	return nil
}

// --- enrich (load materials for read paths) ---

type techCardColorwayRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardColorway
}

type techCardBomItemRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardBomItem
}

type techCardColorwayUsageRow struct {
	ColorwayID int `db:"colorway_id"`
	entity.TechCardColorwayUsage
}

type techCardUsageConsumptionRow struct {
	UsageID int `db:"usage_id"`
	entity.TechCardBomSizeConsumption
}

type techCardDetailRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardDetail
}

type techCardDetailMediaRow struct {
	DetailID int `db:"detail_id"`
	MediaID  int `db:"media_id"`
}

type techCardPieceRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPiece
}

type techCardPieceMaterialRow struct {
	PieceID int `db:"piece_id"`
	// The line_keys behind bom_item_id / fusing_bom_item_id, joined in on read. They live here
	// rather than on the entity because the entity's own BomLineKey/FusingBomLineKey are db:"-" —
	// wire-only INPUT the store resolves to the FKs — and giving them column tags would make every
	// other SELECT that scans a TechCardPieceMaterial demand columns it does not have.
	BomLineKeyJoined       sql.NullString `db:"bom_line_key"`
	FusingBomLineKeyJoined sql.NullString `db:"fusing_bom_line_key"`
	entity.TechCardPieceMaterial
}

// techCardColorwayPriceRow is one product_price row, keyed back to its colourway.
type techCardColorwayPriceRow struct {
	ProductID int             `db:"product_id"`
	Currency  string          `db:"currency"`
	Price     decimal.Decimal `db:"price"`
}

// techCardPieceColorwayIDRow carries a colourway id when validating that a piece material's explicit
// colorway_id belongs to the owning style (product.style_id = card).
type techCardPieceColorwayIDRow struct {
	Id int `db:"id"`
}

// enrichMaterials loads colourways (+ their usages and per-size usage consumption), BOM
// lines (the article catalog) and construction-description details (+ media) for each card.
func (s *Store) enrichMaterials(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	// Colourways grouped per card (in display order). PR6 R1: tech_card_colorway was merged into
	// product, so a card's colourways are its products (product.style_id = card). PLM fields live on
	// product (dev_code/dev_name/dev_comment/dev_hex ← ex code/name/comment/hex). ARCHIVED(4) colourways
	// are EXCLUDED entirely (WHERE c.lifecycle_status <> 4): they previously surfaced as detached rows
	// with product_id = NULL — un-openable "ghost" colourways in the tech-card colourways tab — so they
	// are dropped from the enrichment rather than shown dead. Every returned row is therefore live, so
	// product_id is simply c.id.
	cwRows, err := storeutil.QueryListNamed[techCardColorwayRow](ctx, s.DB, `
		SELECT c.id, c.style_id AS tech_card_id, c.dev_code AS code, COALESCE(c.dev_name, '') AS name,
		       c.color_code, COALESCE(c.lab_dip_status, 'pending') AS lab_dip_status, c.id AS product_id,
		       COALESCE(c.sku, '') AS sku, c.lifecycle_status,
		       c.dev_comment AS comment, c.pantone, c.pantone_system, c.dev_hex AS hex, c.swatch_media_id,
		       c.lab_dip_round, c.lab_dip_submitted_at, c.lab_dip_decided_at, c.lab_dip_decided_by, c.lab_dip_reject_reason,
		       c.cost_price, c.cost_price_source, c.cost_price_updated_at
		FROM product c
		WHERE c.style_id IN (:ids) AND c.lifecycle_status <> 4
		ORDER BY c.style_id, c.display_order, c.id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card colourways: %w", err)
	}
	colorwaysByCard := make(map[int][]entity.TechCardColorway, len(ids))
	for _, r := range cwRows {
		colorwaysByCard[r.TechCardID] = append(colorwaysByCard[r.TechCardID], r.TechCardColorway)
	}
	// Index colourways by id to attach usages; collect ids for the usage query.
	colorwayByID := make(map[int]*entity.TechCardColorway, len(cwRows))
	colorwayIDs := make([]int, 0, len(cwRows))
	for card := range colorwaysByCard {
		cws := colorwaysByCard[card]
		for i := range cws {
			colorwayByID[cws[i].Id] = &cws[i]
			colorwayIDs = append(colorwayIDs, cws[i].Id)
		}
	}
	if len(colorwayIDs) > 0 {
		usageRows, err := storeutil.QueryListNamed[techCardColorwayUsageRow](ctx, s.DB, `
			SELECT id, colorway_id, bom_item_id, piece_id, material_id, bom_item_index, placement, color, pantone, consumption, consumption_source, waste_selvedge_pct, waste_cut_pct, norm_marker_id, norm_applied_at, quantity, piece_index
			FROM tech_card_colorway_usage
			WHERE colorway_id IN (:ids)
			ORDER BY colorway_id, display_order`, map[string]any{"ids": colorwayIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card colourway usages: %w", err)
		}
		usageByID := make(map[int]*entity.TechCardColorwayUsage, len(usageRows))
		usageIDs := make([]int, 0, len(usageRows))
		for _, r := range usageRows {
			cw, ok := colorwayByID[r.ColorwayID]
			if !ok {
				continue
			}
			cw.Usages = append(cw.Usages, r.TechCardColorwayUsage)
		}
		// Slices are final now; index usages by id to attach per-size consumption.
		for cwID := range colorwayByID {
			us := colorwayByID[cwID].Usages
			for i := range us {
				usageByID[us[i].Id] = &us[i]
				usageIDs = append(usageIDs, us[i].Id)
			}
		}
		if len(usageIDs) > 0 {
			consRows, err := storeutil.QueryListNamed[techCardUsageConsumptionRow](ctx, s.DB, `
				SELECT usage_id, size_id, consumption
				FROM tech_card_colorway_usage_consumption
				WHERE usage_id IN (:ids)
				ORDER BY usage_id, display_order`, map[string]any{"ids": usageIDs})
			if err != nil {
				return fmt.Errorf("can't load tech card usage consumptions: %w", err)
			}
			for _, c := range consRows {
				if u, ok := usageByID[c.UsageID]; ok {
					u.SizeConsumptions = append(u.SizeConsumptions, c.TechCardBomSizeConsumption)
				}
			}
		}

		// Retail prices per colourway. The costing tab needs a price to measure a margin against, and
		// the only alternative was fanning GetColorwayByID out over every colourway of the style and
		// hoping they agreed. One batched query for the whole page keeps that N+1 out of the client.
		priceRows, err := storeutil.QueryListNamed[techCardColorwayPriceRow](ctx, s.DB, `
			SELECT product_id, currency, price
			FROM product_price
			WHERE product_id IN (:ids)
			ORDER BY product_id, currency`, map[string]any{"ids": colorwayIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card colourway prices: %w", err)
		}
		for _, r := range priceRows {
			if cw, ok := colorwayByID[r.ProductID]; ok {
				cw.Prices = append(cw.Prices, entity.ColorwayPrice{Currency: r.Currency, Price: r.Price})
			}
		}

		// Lab-dip round journal per colourway (oldest first). The LabDip* scalars on the colourway are
		// the LATEST round; these are the rounds before it, which the scalars overwrote and which the
		// journal (0212) is there to keep.
		roundRows, err := storeutil.QueryListNamed[entity.ColorwayLabDipRound](ctx, s.DB, `
			SELECT product_id, round_number, status, submitted_at, decided_at, decided_by,
			       reject_reason, comment, swatch_media_id, created_at
			FROM product_lab_dip_round
			WHERE product_id IN (:ids)
			ORDER BY product_id, round_number`, map[string]any{"ids": colorwayIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card colourway lab dip rounds: %w", err)
		}
		for _, r := range roundRows {
			if cw, ok := colorwayByID[r.ProductId]; ok {
				cw.LabDipRounds = append(cw.LabDipRounds, r)
			}
		}
	}

	// BOM lines per card (the article catalog).
	//
	// NAME PRECEDENCE: bi.name is the SLOT ROLE (for example, "main zip") and must win even when the
	// slot has a default catalog article. The linked material's name describes that concrete article,
	// not the role; it is only a fallback for legacy/blank role names. Released cards remain frozen by
	// their whole proto-JSON snapshot (0096).
	//
	// Identity facts (supplier / supplier_ref / composition / spec / unit) resolve from the linked
	// catalog material: unlike the role name, these describe the concrete article. The stored column
	// is the fallback for a broken or absent link. unit matters most of the five — it is the denominator
	// of every consumption figure and multiplies into LineTotal, so a stale copy is a wrong NUMBER,
	// not a cosmetic mismatch. unit_price / currency are deliberately NOT resolved: a cost must stay
	// frozen at the point it was agreed, and resolvePlanUnitPrice already treats the stored value as
	// the snapshot with the catalog as an explicit fallback.
	//
	// NB: rationale lives HERE and not inside the query — a ':' inside a SQL '--' comment breaks
	// sqlx's named-parameter binding ("could not find name  in map"), which is exactly how this
	// query shipped broken and took every tech-card read down with it.
	bomRows, err := storeutil.QueryListNamed[techCardBomItemRow](ctx, s.DB, `
		SELECT bi.id, bi.tech_card_id, bi.material_id, bi.section,
		       bi.purpose, bi.purpose_note, bi.kind, bi.kind_note, bi.is_sample,
		       COALESCE(NULLIF(bi.name, ''), m.name) AS name,
		       COALESCE(NULLIF(m.supplier, ''), bi.supplier) AS supplier,
		       COALESCE(NULLIF(m.supplier_ref, ''), bi.supplier_ref) AS supplier_ref,
		       bi.color,
		       COALESCE(NULLIF(m.composition, ''), bi.composition) AS composition,
		       COALESCE(NULLIF(m.spec, ''), bi.spec) AS spec,
		       COALESCE(NULLIF(m.unit, ''), bi.unit) AS unit,
		       bi.unit_price, bi.currency, bi.comment,
		       bi.fabric_width, bi.fabric_weight_gsm, bi.fabric_direction, bi.wastage_percent,
		       bi.wastage_source, bi.wastage_lay_count, bi.wastage_applied_at, bi.wastage_applied_percent,
		       bi.qty_per_garment, bi.spare_qty,
		       COALESCE(bi.line_key, '') AS line_key, bi.price_source, bi.price_snapshot_at,
		       COALESCE(bi.fabric_width, NULLIF(mfa.width_cm, 0), m.fabric_width) AS effective_fabric_width_cm,
		       mfa.selvedge_cm AS selvedge_cm
		FROM tech_card_bom_item bi
		LEFT JOIN material m ON m.id = bi.material_id
		LEFT JOIN material_fabric_attr mfa ON mfa.material_id = bi.material_id
		WHERE bi.tech_card_id IN (:ids)
		ORDER BY bi.tech_card_id, bi.display_order, bi.id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card bom items: %w", err)
	}
	bomByCard := make(map[int][]entity.TechCardBomItem, len(ids))
	for _, r := range bomRows {
		bomByCard[r.TechCardID] = append(bomByCard[r.TechCardID], r.TechCardBomItem)
	}

	// Construction-description details per card, then media per detail.
	detailRows, err := storeutil.QueryListNamed[techCardDetailRow](ctx, s.DB, `
		SELECT id, tech_card_id, detail_key, detail_text
		FROM tech_card_detail
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card details: %w", err)
	}
	detailsByCard := make(map[int][]entity.TechCardDetail, len(ids))
	for _, r := range detailRows {
		detailsByCard[r.TechCardID] = append(detailsByCard[r.TechCardID], r.TechCardDetail)
	}
	detailByID := make(map[int]*entity.TechCardDetail, len(detailRows))
	detailIDs := make([]int, 0, len(detailRows))
	for card := range detailsByCard {
		ds := detailsByCard[card]
		for i := range ds {
			detailByID[ds[i].Id] = &ds[i]
			detailIDs = append(detailIDs, ds[i].Id)
		}
	}
	if len(detailIDs) > 0 {
		dmRows, err := storeutil.QueryListNamed[techCardDetailMediaRow](ctx, s.DB, `
			SELECT detail_id, media_id
			FROM tech_card_detail_media
			WHERE detail_id IN (:ids)
			ORDER BY detail_id, display_order`, map[string]any{"ids": detailIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card detail media: %w", err)
		}
		for _, m := range dmRows {
			if d, ok := detailByID[m.DetailID]; ok {
				d.MediaIds = append(d.MediaIds, m.MediaID)
			}
		}
	}

	// Cut-pieces per card (NF-05), then per-colourway fabric mapping per piece. The stored
	// colorway_id is surfaced directly (R1/§14.3 — no positional colorway_index anymore).
	pieceRows, err := storeutil.QueryListNamed[techCardPieceRow](ctx, s.DB, pieceReadQuery,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card pieces: %w", err)
	}
	piecesByCard := make(map[int][]entity.TechCardPiece, len(ids))
	for _, r := range pieceRows {
		piecesByCard[r.TechCardID] = append(piecesByCard[r.TechCardID], r.TechCardPiece)
	}
	pieceByID := make(map[int]*entity.TechCardPiece, len(pieceRows))
	pieceIDs := make([]int, 0, len(pieceRows))
	for card := range piecesByCard {
		ps := piecesByCard[card]
		for i := range ps {
			pieceByID[ps[i].Id] = &ps[i]
			pieceIDs = append(pieceIDs, ps[i].Id)
		}
	}
	if len(pieceIDs) > 0 {
		// The line_keys are joined in, not merely the FKs: the durable reference is what the wire
		// speaks and what the COLOUR section digest hashes. Emitting only the resolved id meant the
		// read hashed "" where the write had hashed real keys, so the digest could never match its
		// signed value and the COLOUR sign-off read "changed since approved" forever — re-approving
		// just re-stamped the same non-matching hash.
		pmRows, err := storeutil.QueryListNamed[techCardPieceMaterialRow](ctx, s.DB, `
			SELECT pm.id, pm.piece_id, pm.colorway_id, pm.bom_item_id, pm.fusing_bom_item_id,
			       pm.bom_item_index, pm.fusing_bom_item_index, pm.note,
			       b.line_key AS bom_line_key, fb.line_key AS fusing_bom_line_key
			FROM tech_card_piece_material pm
			LEFT JOIN tech_card_bom_item b ON b.id = pm.bom_item_id
			LEFT JOIN tech_card_bom_item fb ON fb.id = pm.fusing_bom_item_id
			WHERE pm.piece_id IN (:ids)
			ORDER BY pm.piece_id, pm.display_order, pm.id`, map[string]any{"ids": pieceIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card piece materials: %w", err)
		}
		for _, r := range pmRows {
			p, ok := pieceByID[r.PieceID]
			if !ok {
				continue
			}
			m := r.TechCardPieceMaterial
			m.BomLineKey = r.BomLineKeyJoined.String
			m.FusingBomLineKey = r.FusingBomLineKeyJoined.String
			p.Materials = append(p.Materials, m)
		}
	}

	// DXF block aliases (§2.2), with the piece's stable line_key joined in — the wire speaks keys,
	// not FKs. A piece deleted mid-flight cascades its aliases away, so the JOIN never dangles.
	aliasRows, err := storeutil.QueryListNamed[techCardPieceDxfAliasRow](ctx, s.DB, `
		SELECT a.tech_card_id, a.bom_line_key, COALESCE(a.fabric_purpose, '') AS fabric_purpose,
		       a.block_name, a.piece_id, COALESCE(p.line_key, '') AS piece_line_key
		FROM tech_card_piece_dxf_block a
		JOIN tech_card_piece p ON p.id = a.piece_id
		WHERE a.tech_card_id IN (:ids)
		ORDER BY a.tech_card_id, a.scope_key, a.block_name`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card piece dxf aliases: %w", err)
	}
	aliasesByCard := make(map[int][]entity.TechCardPieceDxfAlias, len(ids))
	for _, r := range aliasRows {
		aliasesByCard[r.TechCardID] = append(aliasesByCard[r.TechCardID], r.TechCardPieceDxfAlias)
	}

	for i := range cards {
		id := cards[i].Id
		cws := colorwaysByCard[id]
		for j := range cws {
			// The colourway's optimistic-lock token is its style's shared tech_card.lock_version (R2/R4);
			// surface it on each derived AdminColorwayRef so a per-colourway lab-dip edit can be
			// optimistically locked straight from the ref (UpdateColorway.expected_colorway_version).
			cws[j].LockVersion = cards[i].LockVersion
		}
		cards[i].Colorways = cws
		cards[i].BomItems = bomByCard[id]
		cards[i].Details = detailsByCard[id]
		cards[i].Pieces = piecesByCard[id]
		cards[i].PieceDxfAliases = aliasesByCard[id]
	}
	return nil
}

type techCardPieceDxfAliasRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPieceDxfAlias
}

// nullIfEmpty writes "" as SQL NULL. The alias table's fabric_purpose has to be NULL rather than ”
// for the empty case: scope_key is COALESCE(fabric_purpose, bom_line_key), and ” is not NULL to
// COALESCE — a line-scoped row storing ” would scope to the empty string and collide with every
// other such row of the card.
func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// upsertTechCardPieceDxfAliases reconciles the DXF block → piece aliases (§2.2), keyed by
// (SCOPE, block_name) with the DB's case-insensitive semantics mirrored in Go. The scope is
// entity.ResolveFabricScope's answer — назначение (0267) where the card has been sorted, the legacy
// bom_line_key where it has not — and it is computed through that ONE function so this diff, the dto
// dedupe and the generated column scope_key cannot drift apart.
//
// Runs AFTER upsertTechCardPieces (resolveUsagePiece precedent) so piece_line_key resolves against
// this save's pieces, and BEFORE nothing in particular — the BOM is already upserted by the time
// UpdateTechCard reaches here, so a назначение set on the BOM tab a moment ago resolves.
//
// Presence-gated: a payload that did not speak (set=false — a stale client) leaves stored aliases
// untouched; a present payload is the new full set. An EXISTING (scope, block) pair is grandfathered
// even when its scope went dead («слот удалён» / «в это назначение ещё ничего не разложено» are UI
// states, not reasons to fail a card save); a NEW pair requires a live scope.
//
// A card MID-SORT — some lines sorted, some not — is the normal state for the whole transition, and
// it needs no special case here: each alias resolves on its own, so a line-scoped row and a
// purpose-scoped row of the same card coexist and are diffed side by side.
func upsertTechCardPieceDxfAliases(ctx context.Context, db dependency.DB, tcID int, set bool, aliases []entity.TechCardPieceDxfAlias, username string) error {
	if !set {
		return nil
	}
	pieceRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, db,
		`SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load pieces for dxf aliases: %w", err)
	}
	// Lowercased on BOTH sides: alias keys are dto-uppercased while piece/BOM line keys are only
	// trimmed on their own entry paths — a lowercase-keyed but internally consistent payload must
	// not be refused by the server's own normalization asymmetry.
	pieceByKey := make(map[string]int, len(pieceRows))
	for _, r := range pieceRows {
		pieceByKey[strings.ToLower(r.LineKey)] = r.Id
	}
	type aliasExistingRow struct {
		Id            int    `db:"id"`
		BomLineKey    string `db:"bom_line_key"`
		FabricPurpose string `db:"fabric_purpose"`
		BlockName     string `db:"block_name"`
		PieceId       int    `db:"piece_id"`
	}
	existing, err := storeutil.QueryListNamed[aliasExistingRow](ctx, db,
		`SELECT id, bom_line_key, COALESCE(fabric_purpose, '') AS fabric_purpose, block_name, piece_id
		 FROM tech_card_piece_dxf_block WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load existing dxf aliases: %w", err)
	}
	ciKey := func(scope, block string) string { return strings.ToLower(scope) + "|" + strings.ToLower(block) }
	existingByKey := make(map[string]aliasExistingRow, len(existing))
	for _, r := range existing {
		// The stored row's scope needs no BOM: it is COALESCE of what the row itself holds, the same
		// value MySQL materialised into scope_key.
		existingByKey[ciKey(entity.FabricScopeKey(r.FabricPurpose, r.BomLineKey), r.BlockName)] = r
	}
	// The card's cloth lines WITH their назначения — resolving a purpose-scoped alias needs both
	// halves, and a purpose that no line carries is exactly the dangling case to refuse on a NEW pair.
	var rollGoods []entity.RollGoodsLine
	if len(aliases) > 0 {
		rollGoods, err = loadRollGoodsLines(ctx, db, tcID)
		if err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(aliases))
	for i, a := range aliases {
		pieceID, ok := pieceByKey[strings.ToLower(a.PieceLineKey)]
		if !ok {
			return entity.NewFieldViolation(fmt.Sprintf("piece_dxf_aliases[%d].piece_line_key", i),
				"not_found", a.PieceLineKey, "the referenced cut-piece is not on this card")
		}
		scope := a.Scope(rollGoods)
		key := ciKey(scope.Key, a.BlockName)
		if prev, exists := existingByKey[key]; exists {
			// Same scope, so the row keeps its identity. Its two halves are still rewritten: a card
			// that has just been sorted sends the same scope with the compatibility line now filled in
			// (or emptied, when the назначение turned out to own several lines), and a row that kept
			// the old halves would answer a pre-0267 reader with a line that is no longer the whole truth.
			if prev.PieceId != pieceID || prev.FabricPurpose != a.FabricPurpose || prev.BomLineKey != a.BomLineKey {
				if err := storeutil.ExecNamed(ctx, db, `
					UPDATE tech_card_piece_dxf_block
					SET piece_id = :piece_id, bom_line_key = :bom_line_key, fabric_purpose = :fabric_purpose,
					    updated_by = :username
					WHERE id = :id`, map[string]any{
					"id":             prev.Id,
					"piece_id":       pieceID,
					"bom_line_key":   a.BomLineKey,
					"fabric_purpose": nullIfEmpty(a.FabricPurpose),
					"username":       username,
				}); err != nil {
					return fmt.Errorf("failed to update dxf alias: %w", err)
				}
			}
		} else {
			if !scope.Live() {
				// Two different dead ends, two different controls to fix them in — so two messages.
				// Self-contained on purpose: the operator may see this without the form scrolling
				// anywhere, so it has to say what to do, not merely what is wrong.
				if scope.ByPurpose {
					return entity.NewFieldViolation(fmt.Sprintf("piece_dxf_aliases[%d].fabric_purpose", i),
						"not_found", a.FabricPurpose,
						"no fabric line of this card carries that purpose — set it on the line you need on the BOM tab (purpose) and save again")
				}
				return entity.NewFieldViolation(fmt.Sprintf("piece_dxf_aliases[%d].bom_line_key", i),
					"not_found", a.BomLineKey,
					"pick the fabric for this DXF on the PATTERNS tab — a BOM line of section fabric, lining, interlining or insulation")
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_piece_dxf_block
					(tech_card_id, bom_line_key, fabric_purpose, block_name, piece_id, created_by, updated_by)
				VALUES (:tech_card_id, :bom_line_key, :fabric_purpose, :block_name, :piece_id, :username, :username)`,
				map[string]any{
					"tech_card_id":   tcID,
					"bom_line_key":   a.BomLineKey,
					"fabric_purpose": nullIfEmpty(a.FabricPurpose),
					"block_name":     a.BlockName,
					"piece_id":       pieceID,
					"username":       username,
				}); err != nil {
				var me *mysql.MySQLError
				if errors.As(err, &me) && me.Number == 1062 { // ER_DUP_ENTRY
					// The DB collation folds more than Go's ToLower (accents, ё=е, ß=ss) — a pair
					// Go saw as distinct can still collide in MySQL. A readable refusal, not a 500.
					return entity.NewFieldViolation(fmt.Sprintf("piece_dxf_aliases[%d].block_name", i),
						fmt.Sprintf("block %q collides with a link that already exists in this same scope (the database folds case and diacritics)", a.BlockName),
						a.BlockName, "open “cut pieces” on the PATTERNS tab and leave one link for this block")
				}
				return fmt.Errorf("failed to insert dxf alias: %w", err)
			}
		}
		seen[key] = true
	}
	for key, r := range existingByKey {
		if seen[key] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_piece_dxf_block WHERE id = :id`, map[string]any{"id": r.Id}); err != nil {
			return fmt.Errorf("failed to delete dxf alias: %w", err)
		}
	}
	return nil
}
