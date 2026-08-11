package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// СЛОИ ДЕТАЛИ КРОЯ (T4) — проекция связи «деталь ↔ материалы» и её ЕДИНСТВЕННЫЙ носитель.
//
// У связи ДВА источника, и объединяются они ЗДЕСЬ, при построении индекса (newPieceUsageIndex):
//
//  1. детальные строки рецепта (tech_card_colorway_usage.piece_id) — то, что пишет админка сегодня
//     («+ добавить материал к детали» на вкладке колорвеев); на бете связь живёт почти целиком тут;
//  2. tech_card_piece_material (piece.Materials) — замороженная таблица старой вкладки деталей.
//     Новых записей в неё нет, но на ПРОДЕ она — единственный носитель связи живой карточки: пять
//     деталей колорвея названы ТОЛЬКО ею, а рецепт того же колорвея весь «на изделие». Читатель
//     одного лишь рецепта терял бы ткань у каждой из этих деталей.
//
// Деталь привязана к ткани, если её называет ЛЮБОЙ из источников — то же решение и по той же
// причине, что у админки в use-fabric-dxf-pieces.ts (там второй источник — рецепт, здесь — старая
// таблица; пустым бывает то один, то другой). Пересечение безопасно: обе строки одного слота
// схлопывает дедуп по строке BOM в collectPieceLayers, и строка РЕЦЕПТА замешивается первой, так
// что слот достаётся ей — вместе с её пином артикула.
//
// Роль каждого слоя — вывод из строки BOM (entity.DerivePieceLayerRole), нигде не хранится; для
// строки старой таблицы она выводится тем же правилом по её слоту. Этот файл держит общий разбор
// для всех серверных читателей связи: кат-плана прогона, кат-листа стиля, покрытия настилов и
// гейта готовности. Объединять источники в любом ИЗ них (а не здесь, в общем индексе) значило бы
// завести ВТОРОЕ определение «какими слоями кроится деталь»: кат-лист показывал бы ткань, которой
// наряд не видит, и обе стороны выглядели бы правыми. Запись в старую таблицу не возвращается:
// round-trip и COLOUR-дайджест не тронуты, снос — отдельной фазой, после переноса данных прода.

// pieceUsageIndex — детальные строки ОДНОГО колорвея (обоих источников, см. шапку файла),
// разложенные по идентичностям детали. Формы
// привязки — ТЕ ЖЕ ТРИ, что узнаёт предикат entity.IsPieceMaterialAssignment: FK piece_id, ULID
// piece_line_key (в замороженном релизе выживает только он — снапшот не несёт tech_card_piece.id)
// и легаси-позиционный piece_index (у совсем старого релиза — единственная выжившая форма, которую
// CutSpecCardFromReleaseSnapshot переносит именно ради этого). Наборы форм здесь и в предикате
// обязаны совпадать: строка, которую предикат признал назначением детали — и тем самым вычел из
// рецепта нормы, — но которую не видит разбор слоёв, была бы деталью, «привязанной в никуда»:
// наряд по старому релизу отказывал бы ложной неоднозначностью, а покрытие звало бы деталь
// непривязанной.
//
// Позиционная форма резолвится в деталь ПРИ ПОСТРОЕНИИ индекса — тем же правилом, что у стора на
// записи (resolveUsagePiece: индекс = позиция в списке деталей карты; снапшот сохраняет порядок
// деталей) — и дальше живёт под идентичностями самой детали. Четвёртого правила матчинга нет.
type pieceUsageIndex struct {
	byID  map[int][]*entity.TechCardColorwayUsage
	byKey map[string][]*entity.TechCardColorwayUsage
	// byPiece — строки, чей легаси-индекс разрешился в деталь БЕЗ обеих идентичностей (старый
	// снапшот: id нет по контракту, line_key ещё не существовал). Ключ — указатель в тот самый
	// срез pieces, что пришёл в конструктор; все читатели зовут forPiece указателями из него же
	// (&card.Pieces[i]). nil, пока не встретилась хотя бы одна такая строка.
	byPiece map[*entity.TechCardPiece][]*entity.TechCardColorwayUsage
}

// newPieceUsageIndex строит индекс по строкам колорвея; pieces — детали ТОЙ ЖЕ карты, нужны
// резолву позиционной формы и второму источнику связи (piece.Materials).
func newPieceUsageIndex(cw *entity.TechCardColorway, pieces []entity.TechCardPiece) *pieceUsageIndex {
	var usages []entity.TechCardColorwayUsage
	if cw != nil {
		usages = cw.Usages
	}
	x := &pieceUsageIndex{
		byID:  make(map[int][]*entity.TechCardColorwayUsage, len(usages)),
		byKey: make(map[string][]*entity.TechCardColorwayUsage, len(usages)),
	}
	for i := range usages {
		u := &usages[i]
		named := false
		if u.PieceId.Valid && u.PieceId.Int64 > 0 {
			pid := int(u.PieceId.Int64)
			x.byID[pid] = append(x.byID[pid], u)
			named = true
		}
		if u.PieceLineKey != "" {
			x.byKey[u.PieceLineKey] = append(x.byKey[u.PieceLineKey], u)
			named = true
		}
		if named || !u.PieceIndex.Valid {
			// Приоритет форм — как у resolveUsagePiece: идентичность точнее позиции, и строка,
			// несущая обе, не должна попасть в индекс дважды.
			continue
		}
		idx := int(u.PieceIndex.Int32)
		if idx < 0 || idx >= len(pieces) {
			// Битая позиционная ссылка не называет ни одной детали — ровно как повисший piece_id:
			// для предиката строка остаётся назначением, но деталь не находит.
			continue
		}
		x.addForPiece(&pieces[idx], u)
	}
	if cw == nil {
		return x
	}

	// ВТОРОЙ ИСТОЧНИК (см. шапку файла): строки tech_card_piece_material этого колорвея входят в
	// индекс СИНТЕТИЧЕСКИМИ строками рецепта — та же деталь, тот же слот BOM, — и дальше их судит
	// тот же разбор: planBomLine, роль по слоту (DerivePieceLayerRole), дедуп по строке BOM.
	// Ссылка на клеевую — отдельная синтетическая строка: клеевость и у рецепта не хранится на
	// связи, а выводится из СЕКЦИИ строки BOM (collectPieceLayers), и старая таблица проходит через
	// то же правило. Синтетика замешивается ПОСЛЕ строк рецепта, поэтому при пересечении источников
	// слот выигрывает строка рецепта — вместе со своим пином артикула. В cw.Usages синтетика не
	// попадает: нормы, деньги и провод её не видят.
	cwID := pieceMaterialColorwayID(cw)
	for i := range pieces {
		p := &pieces[i]
		for _, pm := range p.Materials {
			if pm.ColorwayID != cwID {
				continue
			}
			for _, ref := range [2]struct {
				id  sql.NullInt64
				pos sql.NullInt32
			}{
				{pm.BomItemId, pm.BomItemIndex},
				{pm.FusingBomItemId, pm.FusingBomItemIndex},
			} {
				hasID := ref.id.Valid && ref.id.Int64 > 0
				hasPos := ref.pos.Valid && ref.pos.Int32 >= 0
				if !hasID && !hasPos {
					continue
				}
				u := &entity.TechCardColorwayUsage{PieceLineKey: p.LineKey}
				if hasID {
					u.BomItemId = ref.id
				}
				if hasPos {
					u.BomItemIndex = ref.pos
				}
				if p.Id > 0 {
					u.PieceId = sql.NullInt64{Int64: int64(p.Id), Valid: true}
				}
				x.addForPiece(p, u)
			}
		}
	}
	return x
}

// addForPiece кладёт строку под идентичности САМОЙ детали — единое правило регистрации для
// легаси-позиционной формы и для синтетики второго источника: приоритет идентичностей тот же,
// которым их читает forPiece, четвёртого правила матчинга не появляется.
func (x *pieceUsageIndex) addForPiece(p *entity.TechCardPiece, u *entity.TechCardColorwayUsage) {
	switch {
	case p.Id > 0:
		x.byID[p.Id] = append(x.byID[p.Id], u)
	case p.LineKey != "":
		x.byKey[p.LineKey] = append(x.byKey[p.LineKey], u)
	default:
		if x.byPiece == nil {
			x.byPiece = make(map[*entity.TechCardPiece][]*entity.TechCardColorwayUsage)
		}
		x.byPiece[p] = append(x.byPiece[p], u)
	}
}

// pieceMaterialColorwayID — идентичность, которой tech_card_piece_material называет колорвей:
// его product id (так писала старая вкладка), на живой карточке совпадающий с id колорвея by
// construction. ЕДИНОЕ правило с идентичностью колонки ответа FabricsFor.
func pieceMaterialColorwayID(cw *entity.TechCardColorway) int {
	if cw.ProductId.Valid && cw.ProductId.Int32 > 0 {
		return int(cw.ProductId.Int32)
	}
	return cw.Id
}

// forPiece — строки рецепта, называющие эту деталь: по FK; по line_key, если карточка пришла из
// релиза (где у детали есть только ULID); и под указателем на саму деталь, если у неё нет ни того,
// ни другого (легаси-позиционные строки старого релиза резолвятся туда при построении индекса).
// Порядок первых двух — дословно правило usagesFor кат-плана.
func (x *pieceUsageIndex) forPiece(p *entity.TechCardPiece) []*entity.TechCardColorwayUsage {
	if p == nil || x == nil {
		return nil
	}
	if p.Id > 0 {
		if us := x.byID[p.Id]; len(us) > 0 {
			return us
		}
	}
	if p.LineKey != "" {
		if us := x.byKey[p.LineKey]; len(us) > 0 {
			return us
		}
	}
	// Достижимо только для детали без id и line_key: у неё других идентичностей нет, поэтому
	// строки её слоёв не могли лечь ни в одну из карт выше.
	return x.byPiece[p]
}

// pieceLayers — разобранные слои одной детали в одном колорвее.
type pieceLayers struct {
	// layers — КРОИМЫЕ слои (рулонные секции минус клеевая, cutPlanCutArticleSection), в порядке
	// рецепта, дедуп по УКАЗАТЕЛЮ на строку BOM (старые снапшоты несут id=0 у всех строк — дедуп
	// по id схлопнул бы основную с подкладкой).
	layers []cutPlanSlot
	// roles[i] — производная роль layers[i] (entity.PieceLayerRoleUnsorted = «не разложено»).
	roles []entity.TechCardBomPurpose
	// fusing — привязанные к детали слоты клеевой (секция INTERLINING): в строку наряда они идут
	// парой fusing_*, не отдельной строкой кроя.
	fusing []cutPlanSlot
	// mains / unsorted — индексы слоёв роли «основная» и «не разложено» (только fabric без
	// назначения). Двое основных — ошибка данных (П1); основная+неразложенная или две
	// неразложенные — недоказуемость той же ошибки.
	mains    []int
	unsorted []int
	// resolved — сколько названных строк вообще нашли свою строку BOM (до фильтра по секции):
	// различает сломанную ссылку и честную привязку нерулонного расхода.
	resolved int
}

// layerNames — имена строк BOM выбранных слоёв, для человеческих фраз.
func (pl *pieceLayers) layerNames(idxs []int) []string {
	out := make([]string, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, pl.layers[i].bom.Name)
	}
	return out
}

// mainConflict — П1 и её недоказуемый близнец, одним предикатом: две «основные» — ошибка данных;
// основная рядом с неразложенной fabric-строкой (или две неразложенные) — нельзя доказать, что это
// не вторая основная. Оба случая останавливают наряд; различаются только словами (см. resolve).
func (pl *pieceLayers) mainConflict() bool {
	return len(pl.mains) >= 2 || (len(pl.unsorted) >= 1 && len(pl.mains)+len(pl.unsorted) >= 2)
}

// collectPieceLayers разбирает детальные строки одной детали на слои. usages — ответ forPiece.
func collectPieceLayers(usages []*entity.TechCardColorwayUsage, items []entity.TechCardBomItem) pieceLayers {
	pl := pieceLayers{}
	seen := make(map[*entity.TechCardBomItem]bool, len(usages))
	for _, u := range usages {
		bom := planBomLine(u, items)
		if bom == nil || seen[bom] {
			continue
		}
		seen[bom] = true
		pl.resolved++
		role, roll := bom.PieceLayerRole()
		if roll && bom.Section == entity.BomSectionInterlining {
			pl.fusing = append(pl.fusing, cutPlanSlot{bom: bom, usage: u})
			continue
		}
		if !cutPlanCutArticleSection(bom) {
			continue
		}
		idx := len(pl.layers)
		pl.layers = append(pl.layers, cutPlanSlot{bom: bom, usage: u})
		pl.roles = append(pl.roles, role)
		switch role {
		case entity.BomPurposeMain:
			pl.mains = append(pl.mains, idx)
		case entity.PieceLayerRoleUnsorted:
			pl.unsorted = append(pl.unsorted, idx)
		}
	}
	return pl
}

// StyleCutFabricIndex — проекция связи для кат-листа стиля (GetStyleCutList): по колорвею и
// детали — из каких слоёв она кроится и какой клеевой дублируется. Источники и их объединение —
// pieceUsageIndex (см. шапку файла): рецепт (носитель связи на бете; чтение одной лишь замороженной
// tech_card_piece_material оставляло колонку «ткань по колорвеям» пустой — карта 38 жила одним
// рецептом) ПЛЮС сама tech_card_piece_material (единственный носитель связи живой карточки прода).
type StyleCutFabricIndex struct {
	card      *entity.TechCard
	colorways []styleCutColorway
}

type styleCutColorway struct {
	cw  *entity.TechCardColorway
	idx *pieceUsageIndex
	// fusingSlots — клеевые слоты колорвея (фолбэк пары для fused-деталей без своей привязки), из
	// тех же строк рецепта, что и слои, — правило кат-плана, не вторая выборка.
	fusingSlots []cutPlanSlot
}

func NewStyleCutFabricIndex(card *entity.TechCard) *StyleCutFabricIndex {
	x := &StyleCutFabricIndex{card: card}
	if card == nil {
		return x
	}
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		sc := styleCutColorway{cw: cw, idx: newPieceUsageIndex(cw, card.Pieces)}
		seen := make(map[*entity.TechCardBomItem]bool, len(cw.Usages))
		for j := range cw.Usages {
			u := &cw.Usages[j]
			bom := planBomLine(u, card.BomItems)
			if bom == nil || seen[bom] {
				continue
			}
			seen[bom] = true
			if bom.Section == entity.BomSectionInterlining {
				sc.fusingSlots = append(sc.fusingSlots, cutPlanSlot{bom: bom, usage: u})
			}
		}
		x.colorways = append(x.colorways, sc)
	}
	return x
}

// FabricsFor — строки «из чего кроится деталь» по всем колорвеям: по записи НА КАЖДЫЙ КРОИМЫЙ СЛОЙ
// (шелл и подклад одной детали — две записи одного колорвея). Клеевая едет парой fusing_* на
// записи ОСНОВНОГО слоя (роль main; без основного — на первой), тем же правилом, что у наряда:
// сначала привязанный к детали interlining-слот, иначе единственный клеевой слот колорвея у
// fused-детали. Конфликт «двух основных» кат-лист не прячет и не решает — он показывает все слои,
// а останавливает наряд и называет гейт.
func (x *StyleCutFabricIndex) FabricsFor(piece *entity.TechCardPiece) []*pb_admin.StyleCutListFabric {
	if x == nil || x.card == nil || piece == nil {
		return nil
	}
	var out []*pb_admin.StyleCutListFabric
	for i := range x.colorways {
		sc := &x.colorways[i]
		// Идентичность колонки — product id, как её несёт piece_material.colorway_id и как её
		// называет линия прогона; правило одно с матчингом второго источника в индексе
		// (pieceMaterialColorwayID), на живой карточке совпадает с id by construction.
		colorwayID := pieceMaterialColorwayID(sc.cw)
		pl := collectPieceLayers(sc.idx.forPiece(piece), x.card.BomItems)
		if len(pl.layers) == 0 {
			continue
		}
		fusing := pieceFusingSlot(piece, pl.fusing, sc.fusingSlots)
		fusingAt := 0
		if len(pl.mains) > 0 {
			fusingAt = pl.mains[0]
		}
		for j, slot := range pl.layers {
			f := &pb_admin.StyleCutListFabric{ColorwayId: int64(colorwayID)}
			if slot.bom.Id > 0 {
				f.BomItemId = int64(slot.bom.Id)
			}
			f.FabricName = slot.bom.Name
			if fusing != nil && j == fusingAt {
				if fusing.bom.Id > 0 {
					f.FusingBomItemId = int64(fusing.bom.Id)
				}
				f.FusingName = fusing.bom.Name
			}
			out = append(out, f)
		}
	}
	return out
}

// pieceFusingSlot — ЕДИНОЕ правило разрешения клеевой детали (T4, усиление старого): СНАЧАЛА
// interlining-строка, привязанная к самой детали (ровно одна — две привязанные это та же
// двусмысленность, что и раньше), ИНАЧЕ прежнее правило «единственный клеевой слот колорвея при
// fused=TRUE». Ноль или неоднозначность — пары нет, и это НЕ блокер: деталь всё равно кроится,
// недублированная деталь — брак пошива, а не остановка раскроя.
func pieceFusingSlot(piece *entity.TechCardPiece, pieceBound, colorwayWide []cutPlanSlot) *cutPlanSlot {
	if piece == nil || !piece.Fused {
		return nil
	}
	if len(pieceBound) == 1 {
		return &pieceBound[0]
	}
	if len(pieceBound) == 0 && len(colorwayWide) == 1 {
		return &colorwayWide[0]
	}
	return nil
}
