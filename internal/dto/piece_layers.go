package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// СЛОИ ДЕТАЛИ КРОЯ (T4) — рецептная проекция связи «деталь ↔ материалы» и её ЕДИНСТВЕННЫЙ носитель.
//
// Связь живёт в детальных строках рецепта (tech_card_colorway_usage.piece_id); роль каждого слоя —
// вывод из строки BOM (entity.DerivePieceLayerRole), нигде не хранится. Этот файл держит общий
// разбор для всех серверных читателей связи: кат-плана прогона, кат-листа стиля, покрытия настилов
// и гейта готовности. Второй разбор в любом из них был бы вторым определением «какими слоями
// кроится деталь» — и разошёлся бы с первым молча, потому что обе стороны вернули бы какой-то
// список.
//
// tech_card_piece_material здесь СОЗНАТЕЛЬНО не читается: админка в неё не пишет (schema.ts прямо
// говорит «this admin no longer edits that map at all»), на живых карточках она пуста, и все её
// серверные читатели переведены на эту проекцию. Таблица законсервирована: запись, round-trip и
// COLOUR-дайджест не тронуты, снос — отдельной фазой.

// pieceUsageIndex — детальные строки ОДНОГО колорвея, разложенные по идентичностям детали. Формы
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
// резолву позиционной формы.
func newPieceUsageIndex(usages []entity.TechCardColorwayUsage, pieces []entity.TechCardPiece) *pieceUsageIndex {
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
		p := &pieces[idx]
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
	return x
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

// StyleCutFabricIndex — рецептная проекция для кат-листа стиля (GetStyleCutList): по колорвею и
// детали — из каких слоёв она кроится и какой клеевой дублируется. Заменяет чтение замороженной
// tech_card_piece_material, из-за которого колонка «ткань по колорвеям» на живых карточках была
// пуста (карта 38 жила одним рецептом).
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
		sc := styleCutColorway{cw: cw, idx: newPieceUsageIndex(cw.Usages, card.Pieces)}
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
		colorwayID := sc.cw.Id
		if sc.cw.ProductId.Valid && sc.cw.ProductId.Int32 > 0 {
			// Идентичность колонки — product id, как её несла piece_material.colorway_id и как её
			// называет линия прогона; на живой карточке они совпадают by construction.
			colorwayID = int(sc.cw.ProductId.Int32)
		}
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
