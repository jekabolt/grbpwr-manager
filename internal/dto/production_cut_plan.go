package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// cutPlanRollSections — «рулонные» секции BOM, то есть те, из которых деталь физически ВЫКРАИВАЮТ.
// Именно на этом множестве работает вывод слота (правило «ровно один рулонный слот»): нитка, молния
// и этикетка не кроятся, и слот из них, попав сюда, сделал бы вывод неоднозначным на каждой второй
// карточке — то есть превратил бы наряд в список блокеров ровно там, где он должен был помочь.
//
// Множество СОЗНАТЕЛЬНО уже, чем planSlotSections материального плана: тот отвечает «сколько всего
// закупить» и обязан считать и фурнитуру, и нитки; этот отвечает «что положить на настил».
var cutPlanRollSections = map[entity.TechCardBomSection]bool{
	entity.BomSectionFabric:      true,
	entity.BomSectionLining:      true,
	entity.BomSectionInterlining: true,
	entity.BomSectionInsulation:  true,
}

// cutPlanCutArticleSection — может ли строка BOM, НАЗВАННАЯ рецептом на детали, быть артикулом её
// кроя. Производная от cutPlanRollSections (одно множество, не второй список секций — две выборки
// разъехались бы при первой правке одной из них) с одним вычетом: клеевой.
//
// РУЛОННОСТЬ ОБЯЗАТЕЛЬНА. Рецепт привязывает к детали ЛЮБОЙ расход — размерную этикетку, нитку
// обмётки, — и «usage называет деталь → его слот и есть ткань» без этого фильтра печатает цеху
// «кроить полочку из размерной этикетки» ровно тогда, когда основная ткань задана на весь колорвей
// (без piece_id) — то есть в самой частой авторской форме. Нерулонной привязки к детали для кроя не
// существует: деталь кроят из полотна.
//
// КЛЕЕВАЯ ВЫЧТЕНА. Дублерин рулонный и его кроят, но на строке наряда у него СВОИ два поля
// (fusing_bom_item_id / fusing_material_name), которые заполняет отдельное правило по секции
// INTERLINING. Считая его ещё и кандидатом в основной артикул, рецепт, честно назвавший на детали и
// ткань, и дублерин, давал бы блокер «для этой детали 2 разных слота» — остановку цеха на
// спецификации, в которой всё сказано. Обратная сторона размена: деталь, на которой рецепт назвал
// ОДИН лишь дублерин, теперь уходит в правило (б) и на карточке «ткань + клеевая» станет блокером —
// это осознанно, потому что ошибка в сторону «остановись и спроси» дешевле напечатанного наугад
// настила.
func cutPlanCutArticleSection(b *entity.TechCardBomItem) bool {
	return b != nil && cutPlanRollSections[b.Section] && b.Section != entity.BomSectionInterlining
}

// cutPlanLegacyOutputName — имя колонки наряда для линии, которая не несёт ни продукта, ни
// aux-цвета: единственный выход карточки в легаси-режиме (см. ProductionRunLine.OutputVariantId).
// Пустое имя здесь недопустимо: колонка попадает и в строки, и в блокеры, а блокер «деталь X, цвет
// “”» неисполним — по нему нельзя понять, какую партию остановили.
const cutPlanLegacyOutputName = "no colour (the card's only output)"

// ComputeProductionRunCutPlan projects a run's lines onto the style's cut pieces: деталь × колорвей
// × размер → сколько панелей выкроить и из какого артикула. Сестра ComputeProductionRunMaterialPlan
// и её антипод по вопросу: тот отвечает «сколько ткани заказать и хватает ли её», этот — «что из
// этой ткани выкроить». Обе проекции ЧИТАЮТ прогон и карту и ничего не хранят.
//
// `card` — это спецификация, по которой печатается наряд: снапшот РЕЛИЗА, если прогон к нему
// привязан, иначе живая карта. Решение принимает вызывающий (у него репозиторий), а `release`
// нужен только чтобы ответ мог назвать ревизию, по которой кроят.
//
// ИНВАРИАНТ, который нельзя нарушить: pieces_to_cut = pieces_per_garment × garments. cut_symmetry
// НЕ множитель (0266 свернул зеркальное удвоение в само количество, 0275 вернул одну лишь
// классификацию) — она едет в ответ словами для закройщика.
//
// ВТОРОЙ ИНВАРИАНТ, менее заметный и потому более хрупкий: КОЛИЧЕСТВА ЖИВУТ ТОЛЬКО НА ПРОГОНЕ.
// card.SizeQuantities (типовой тираж стиля) здесь не читается ни в каком виде и не должен читаться
// даже как «запасной вариант, когда линий нет»: партия из 40 изделий, напечатанная тиражом стиля в
// 200, — это 200 раскроенных полочек и вопрос, откуда взялись 160 лишних. Пустой прогон обязан дать
// пустой наряд.
//
// Денег в ответе нет по построению: он уходит на бумагу в цех и в публичный манифест наряда, где
// RBAC-стрипа костинга не существует.
func ComputeProductionRunCutPlan(
	run *entity.ProductionRun,
	card *entity.TechCard,
	release *entity.TechCardReleaseMeta,
) *pb_admin.GetProductionRunCutPlanResponse {
	out := &pb_admin.GetProductionRunCutPlanResponse{GeneratedAt: timestamppb.New(time.Now())}
	// Ревизия — поле ОТВЕТА, а не вывод клиента из наличия release_id: цех обязан видеть, по какой
	// спецификации кроит, а вывести это из запроса он не может — release_id живёт на прогоне.
	if release != nil {
		out.ReleaseId = int32(release.Id)
		out.ReleaseNumber = int32(release.ReleaseNumber)
	}
	if run == nil || card == nil {
		return out
	}
	out.RunLockVersion = int32(run.LockVersion)

	sizes := newCutPlanSizeIndex(card.SizeIds)
	groups, planned, caveats := cutPlanGroups(run, card, sizes)
	// Σ planned_qty ПО ЛИНИЯМ ПРОГОНА — по ВСЕМ, включая те, что наряд не смог привязать. Шапка
	// называет размер ПАРТИИ, а не сумму того, что удалось разложить: ужать её до посчитанного
	// значило бы спрятать «девять изделий не в наряде» в число, которое выглядит правдоподобно и ни
	// с чем не сходится. Разницу объясняют оговорки, а блокеры несут её поштучно — тот же приём, что
	// у CutPlanBlocker.garments.
	out.GarmentsTotal = int32(planned)

	// Пустая секция деталей — это не «нечего сказать», это наряд, по которому НЕЛЬЗЯ кроить, и
	// молчание здесь читалось бы как «деталей не нужно». Говорим только когда партия непуста:
	// у прогона без линий и так нет ни одной строки, и вторая жалоба ничего не добавляет.
	if len(card.Pieces) == 0 && len(groups) > 0 {
		caveats = append(caveats, "the card has no cut pieces — the run pack is empty even though the batch is planned")
	}

	for i := range card.Pieces {
		p := &card.Pieces[i]
		for _, g := range groups {
			art, reason := g.resolve(p, card.BomItems)
			if reason != "" {
				// Блокер, а не строка с прочерком: строка с пустой тканью читается как «кроить не из
				// чего» и теряется среди сорока таких же, а это единственное место наряда, где цех
				// обязан остановиться и спросить.
				//
				// ColorwayName у aux-линии несёт имя ЦВЕТА: у CutPlanBlocker нет поля варианта, а
				// безымянный блокер («деталь X, цвет “”») неисполним — по нему нельзя даже понять,
				// какую партию остановили. colorway_id при этом честно остаётся нулём.
				out.Blockers = append(out.Blockers, &pb_admin.CutPlanBlocker{
					PieceId:      int32(p.Id),
					PieceName:    p.Name,
					ColorwayId:   int32(g.key.colorwayID),
					ColorwayName: g.label(),
					Garments:     int32(g.garments),
					Reason:       reason,
				})
				continue
			}
			row := &pb_admin.CutPlanRow{
				PieceId:      int32(p.Id),
				PieceLineKey: p.LineKey,
				PieceName:    p.Name,
				ColorwayId:   int32(g.key.colorwayID),
				ColorwayName: g.colorwayName,
				// Aux-линия несёт цвет вместо продукта (0252/0253): пара полей ниже — это её
				// единственная идентичность, поэтому она едет рядом с колорвеем, а не вместо него.
				OutputVariantId:   int32(g.key.variantID),
				OutputVariantName: g.variantName,
				// Спецификация детали дублируется в КАЖДОЙ строке колорвея намеренно: наряд режут по
				// строкам, и деталь без долевой в своей строке раскроят неправильно, даже если та же
				// долевая написана строкой выше у другого цвета.
				PiecesPerGarment: int32(p.PiecesPerGarment),
				CutSymmetry:      PieceCutSymmetryToPb(p.CutSymmetry),
				Grainline:        p.Grainline,
				Fused:            p.Fused,
				BomItemId:        int64(art.bom.Id),
				SlotName:         art.bom.Name,
				Section:          pbBomSection(art.bom.Section),
				MaterialId:       int32(art.materialID),
				MaterialName:     cutPlanMaterialName(card, art.materialID, art.bom),
				Pinned:           art.pinned,
				SlotInferred:     art.inferred,
			}
			// Клеевая: ОДНА и только одна — «несколько» и «ни одной» одинаково нечего печатать, и ни
			// то ни другое не блокер. Деталь всё равно кроится; недублированная деталь — брак пошива,
			// а не остановка раскроя, и превращать это в блокер значило бы останавливать цех на
			// вопросе, который решается у стола дублирования.
			if p.Fused && len(g.fusingSlots) == 1 {
				fs := g.fusingSlots[0]
				row.FusingBomItemId = int64(fs.bom.Id)
				row.FusingMaterialName = cutPlanMaterialName(card, fs.articleID(), fs.bom)
			}
			for _, sizeID := range g.sizeOrder() {
				garments := g.bySize[sizeID]
				row.BySize = append(row.BySize, &pb_admin.CutPlanSizeQty{
					SizeId:   int32(sizeID),
					SizeName: cutPlanSizeName(sizeID),
					Garments: int32(garments),
					// ЕДИНСТВЕННОЕ умножение во всей проекции. cut_symmetry сюда не входит — см.
					// заголовок функции и CutPlanRow в контракте.
					PiecesToCut: int32(garments * p.PiecesPerGarment),
				})
			}
			row.GarmentsTotal = int32(g.garments)
			row.PiecesToCutTotal = int32(g.garments * p.PiecesPerGarment)
			out.PiecesToCutTotal += row.PiecesToCutTotal
			out.Rows = append(out.Rows, row)
		}
	}

	// Размеры вне градации сказаны ПОСЛЕ строк — их находит обход линий, а список выравнивается
	// только когда обход закончен.
	out.Caveats = append(caveats, sizes.strayNotes()...)
	return out
}

// cutPlanGroupKey — колонка наряда: продаваемый колорвей ИЛИ aux-цвет. Линия прогона несёт ЛИБО
// product_id (+размер), ЛИБО output_variant_id (chk_prl_variant_xor), поэтому больше одного поля
// ключа ненулевым не бывает, а вместе они дают устойчивую идентичность колонки в обоих мирах.
//
// ОБА НУЛЯ — ТОЖЕ КОЛОНКА, а не мусор: это единственный выход aux-карточки в легаси-режиме (см.
// ProductionRunLine.OutputVariantId — xor выполняется и на двух NULL). Такой ключ обязан давать
// нормальную группу: линия чехла на 200 штук, выпавшая из группировки, оставляет наряд с шапкой
// «200 изделий» и без единой строки кроя на них.
type cutPlanGroupKey struct {
	colorwayID int // product id колорвея; 0 у aux-линии
	variantID  int // tech_card_output_variant.id; 0 у продаваемой линии
}

// cutPlanSlot — слот BOM вместе с той строкой рецепта, которая его использует. Пара, а не один
// слот: пин артикула живёт на usage, а роль и секция — на строке BOM, и разрешать артикул без
// второй половины значит потерять ровно ту разницу между колорвеями, ради которой закройщик и
// смотрит в наряд. usage == nil у вспомогательной карточки: пину там взяться неоткуда.
type cutPlanSlot struct {
	bom   *entity.TechCardBomItem
	usage *entity.TechCardColorwayUsage
}

// articleID резолвит артикул слота теми же двумя шагами, что и материальный план: пин рецепта,
// иначе умолчание слота. 0 = артикула нет вовсе.
func (s cutPlanSlot) articleID() int {
	if s.usage != nil {
		id, _ := s.usage.EffectiveMaterialId(s.bom)
		return id
	}
	if s.bom != nil && s.bom.MaterialId.Valid && s.bom.MaterialId.Int64 > 0 {
		return int(s.bom.MaterialId.Int64)
	}
	return 0
}

// cutPlanArticle — разрешённый ответ на вопрос «из чего кроить эту деталь в этом цвете».
type cutPlanArticle struct {
	bom        *entity.TechCardBomItem
	materialID int
	pinned     bool
	// inferred = «рецепт эту деталь не называет, взят единственный рулонный слот». Флаг едет на
	// провод (slot_inferred), потому что подставить молча — значит напечатать догадку шрифтом факта.
	inferred bool
}

// cutPlanGroup — одна колонка наряда со всем, что нужно, чтобы разрешить любую деталь в ней.
// Разрешение слотов считается ОДИН раз на группу, а не на каждую пару (деталь, колорвей): обход
// рецепта на каждой детали дал бы то же самое N раз и разъехался бы при первой же правке.
type cutPlanGroup struct {
	key          cutPlanGroupKey
	order        int // позиция колонки в карточке — порядок печати
	colorwayName string
	variantName  string
	garments     int
	bySize       map[int]int
	sizes        *cutPlanSizeIndex
	orderedSizes []int // мемо sizeOrder; заполняется после того, как все линии разложены

	// specLabel — как назвать источник спецификации в человеческой фразе блокера. У вспомогательной
	// карточки колорвеев нет вовсе, и «рецепт колорвея не называет ткань» было бы для неё
	// бессмысленным обвинением в адрес несуществующей сущности.
	specLabel string
	// byPieceID / byPieceKey — строки рецепта, которые НАЗЫВАЮТ деталь. ДВЕ карты на одну связь, и
	// это не перестраховка: у детали ДВЕ идентичности, и в замороженном релизе выживает только
	// вторая. Снапшот карточки не несёт tech_card_piece.id вовсе (в контракте детали нет поля id,
	// есть line_key), поэтому наряд, печатаемый по релизу, сопоставлял бы usage.piece_id с нулями и
	// «рецепт эту деталь не называет» стало бы истиной для КАЖДОЙ детали каждого релиза — то есть
	// ровно там, где спецификация заморожена и надёжнее всего. Пусто у aux-карточки.
	byPieceID  map[int][]*entity.TechCardColorwayUsage
	byPieceKey map[string][]*entity.TechCardColorwayUsage
	// rollSlots — рулонные слоты, ДОСТУПНЫЕ этой колонке, в порядке рецепта (у aux — в порядке BOM).
	rollSlots []cutPlanSlot
	// fusingSlots — подмножество rollSlots секции INTERLINING, для дублированных деталей.
	fusingSlots []cutPlanSlot
}

// label — как назвать колонку человеку: колорвей, а у aux-линии — её цвет.
func (g *cutPlanGroup) label() string {
	if g.colorwayName != "" {
		return g.colorwayName
	}
	return g.variantName
}

// sizeOrder — размеры колонки В ПОРЯДКЕ ГРАДАЦИИ КАРТЫ, а не в порядке линий прогона. Порядок
// линий — это порядок, в котором их набрали в модалке; печатать по нему значит выдать в цех
// «L, XS, M», и закройщик будет искать свой размер глазами в каждой строке.
//
// Считается один раз на колонку: он одинаков для всех её деталей, а карточка на сорок деталей
// пересортировывала бы его сорок раз.
func (g *cutPlanGroup) sizeOrder() []int {
	if g.orderedSizes != nil {
		return g.orderedSizes
	}
	ids := make([]int, 0, len(g.bySize))
	for id := range g.bySize {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ri, rj := g.sizes.rankOf(ids[i]), g.sizes.rankOf(ids[j])
		if ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
	g.orderedSizes = ids
	return ids
}

// usagesFor — строки рецепта, называющие эту деталь: по FK, а если карточка пришла из релиза (где
// у детали есть только ULID) — по line_key.
func (g *cutPlanGroup) usagesFor(piece *entity.TechCardPiece) []*entity.TechCardColorwayUsage {
	if piece.Id > 0 {
		if us := g.byPieceID[piece.Id]; len(us) > 0 {
			return us
		}
	}
	if piece.LineKey != "" {
		return g.byPieceKey[piece.LineKey]
	}
	return nil
}

// resolve — ТА САМАЯ резолюция «деталь → артикул», три правила по порядку и ни одного шага сверх.
//
// Пустой reason = строка печатается; непустой = вместо строки блокер с этой фразой.
//
// tech_card_piece_material (piece.Materials) здесь НЕ ИСПОЛЬЗУЕТСЯ, и это не забывчивость: админка
// в эту таблицу ничего не пишет, на живых карточках она пуста — именно поэтому колонка «ткань по
// колорвеям» на старом стилевом cut list всегда была пустой. Читать её значит повторить тот же
// пустой экран, только в цехе.
func (g *cutPlanGroup) resolve(piece *entity.TechCardPiece, items []entity.TechCardBomItem) (cutPlanArticle, string) {
	// (а) РЕЦЕПТ НАЗЫВАЕТ ДЕТАЛЬ ПОЛОТНОМ. Единственный случай, где наряд ничего не выводит: строка
	// рецепта сама сказала, какой слот идёт на эту деталь. FK сначала, ULID вторым — тот же порядок
	// приоритетов, что у planBomLine со строкой BOM, и по той же причине: id точнее, а ключ
	// переживает заморозку релиза.
	//
	// В кандидаты берутся ТОЛЬКО секции cutPlanCutArticleSection: привязка к детали сама по себе
	// ничего не говорит о крое (к детали привязывают и этикетку, и нитку), а «называет → значит
	// ткань» печатало цеху артикул этикетки. Названные, но нерулонные строки не блокер и не строка —
	// они просто молчат о крое, и ответ на «из чего кроить» ищется дальше, в правиле (б).
	if usages := g.usagesFor(piece); len(usages) > 0 {
		slots := make([]cutPlanSlot, 0, len(usages))
		// resolved — сколько названных на детали строк рецепта вообще нашли свою строку BOM. Считается
		// ДО фильтра по секции, потому что различает два разных «кандидатов нет»: сломанную ссылку
		// (рецепт указывает на строку, которой в карточке нет — это блокер) и честную привязку
		// нерулонного расхода (этикетка, нитка, клеевая — про крой они не говорят ничего, и наряд
		// обязан спуститься к правилу (б), а не остановить цех фразой про ненайденную строку BOM).
		resolved := 0
		// Дедуп по УКАЗАТЕЛЮ на строку BOM, а не по её id, и это не стиль: снапшоты релизов, снятые
		// до появления bom_item.id на проводе, несут id = 0 у ВСЕХ строк, и дедуп по id схлопнул бы
		// основную ткань с подкладкой в одну — то есть напечатал бы строку ровно там, где контракт
		// требует блокер. Указатель в один и тот же слайс уникален всегда.
		seen := make(map[*entity.TechCardBomItem]bool, len(usages))
		for _, u := range usages {
			bom := planBomLine(u, items)
			if bom == nil || seen[bom] {
				continue
			}
			seen[bom] = true
			resolved++
			if !cutPlanCutArticleSection(bom) {
				continue
			}
			slots = append(slots, cutPlanSlot{bom: bom, usage: u})
		}
		switch {
		case len(slots) == 1:
			return cutPlanArticleOf(slots[0], false)
		case len(slots) > 1:
			// Две разные ткани на одну деталь — это не «выбери любую»: у детали один крой, и какой
			// из слотов настилать, знает только человек.
			return cutPlanArticle{}, fmt.Sprintf(
				"%s names %d different slots for this piece — which one to cut it from is not stated", g.specLabel, len(slots))
		case resolved == 0:
			return cutPlanArticle{}, fmt.Sprintf(
				"%s names this piece, but the BOM line it references is not found in the card", g.specLabel)
		}
		// Рецепт назвал деталь, но ни одним полотном: про крой он не сказал ничего — падаем в (б).
	}
	// (б) ВЫВОД, КОГДА ВЫВОДИТЬ БЕЗОПАСНО. usage.piece_id необязателен и на живых карточках чаще
	// пуст, чем полон; требовать его — выдать цеху наряд из одних блокеров. Но подставлять можно
	// только когда подставлять НЕЧЕГО: ровно один рулонный слот — это не выбор, это единственный
	// вариант, и строка честно помечается slot_inferred.
	switch len(g.rollSlots) {
	case 1:
		return cutPlanArticleOf(g.rollSlots[0], true)
	case 0:
		return cutPlanArticle{}, fmt.Sprintf(
			"%s does not name a fabric for this piece, and it has no roll slots", g.specLabel)
	default:
		// (в) ДВУСМЫСЛЕННОСТЬ УХОДИТ В БЛОКЕРЫ ЦЕЛИКОМ. Подкладка вместо основной ткани — это
		// испорченный настил, а не опечатка в бумаге.
		return cutPlanArticle{}, fmt.Sprintf(
			"%s does not name a fabric for this piece, and it has %d roll slots — the run pack does not guess",
			g.specLabel, len(g.rollSlots))
	}
}

// cutPlanArticleOf достраивает слот до артикула. Слот без артикула — это блокер, а не строка с
// нулём: «кроить из роли, у которой нет ткани» невыполнимо, а строка об этом молчала бы.
func cutPlanArticleOf(s cutPlanSlot, inferred bool) (cutPlanArticle, string) {
	mid, pinned := 0, false
	if s.usage != nil {
		mid, pinned = s.usage.EffectiveMaterialId(s.bom)
	} else {
		mid = s.articleID()
	}
	if mid == 0 {
		return cutPlanArticle{}, fmt.Sprintf("slot %q has no article: neither a recipe pin nor a slot default", s.bom.Name)
	}
	return cutPlanArticle{bom: s.bom, materialID: mid, pinned: pinned, inferred: inferred}, ""
}

// cutPlanGroups раскладывает линии прогона по колонкам наряда и готовит резолюцию каждой из них.
// Возвращает колонки в порядке печати, Σ planned_qty ПО ВСЕМ линиям (размер партии, а не размер
// того, что удалось привязать) и оговорки о том, что реально произошло с линиями.
func cutPlanGroups(run *entity.ProductionRun, card *entity.TechCard, sizes *cutPlanSizeIndex) ([]*cutPlanGroup, int, []string) {
	colorwayByProduct := make(map[int]*entity.TechCardColorway, len(card.Colorways))
	colorwayOrder := make(map[int]int, len(card.Colorways))
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		if cw.ProductId.Valid {
			pid := int(cw.ProductId.Int32)
			colorwayByProduct[pid] = cw
			colorwayOrder[pid] = i
		}
	}
	variants := make(map[int]*entity.TechCardOutputVariant, len(card.OutputVariants))
	variantOrder := make(map[int]int, len(card.OutputVariants))
	for i := range card.OutputVariants {
		v := &card.OutputVariants[i]
		variants[v.Id] = v
		variantOrder[v.Id] = i
	}

	var caveats []string
	groups := make(map[cutPlanGroupKey]*cutPlanGroup, len(run.Lines))
	ordered := make([]*cutPlanGroup, 0, len(run.Lines))
	// Пропущенные линии складываются в ОДНУ фразу с количеством: наряд, где шапка говорит 100, а
	// строки покрывают 60, обязан сам объяснить, куда делись сорок.
	noProduct, noColorway := 0, 0
	unknownProducts := make(map[int]bool)
	// Есть ли у карточки колорвеи, которые линия ВООБЩЕ могла бы назвать. Ноль означает, что назвать
	// продукт на этой карточке нельзя ни одной линией — то есть её единственный выход и есть
	// легаси-форма; см. ветку default ниже.
	sellable := len(colorwayByProduct) > 0

	planned := 0
	for i := range run.Lines {
		ln := &run.Lines[i]
		if ln.PlannedQty <= 0 {
			continue
		}
		planned += ln.PlannedQty
		var key cutPlanGroupKey
		var cw *entity.TechCardColorway
		switch {
		case ln.ProductId.Valid:
			key.colorwayID = int(ln.ProductId.Int32)
			cw = colorwayByProduct[key.colorwayID]
			if cw == nil {
				// Прогон спланирован на продукт, которого в этой карточке нет: спецификации для него
				// не существует, и напечатать по нему крой значило бы выдумать её.
				noColorway += ln.PlannedQty
				unknownProducts[key.colorwayID] = true
				continue
			}
		case ln.OutputVariantId.Valid:
			key.variantID = int(ln.OutputVariantId.Int32)
		default:
			// НИ ПРОДУКТА, НИ ЦВЕТА. На карточке БЕЗ продаваемых колорвеев это легаси-форма её
			// единственного выхода (см. ProductionRunLine.OutputVariantId), и ключ {0,0} — такая же
			// полноценная колонка, как aux-цвет: раньше эти линии уходили в оговорку, и наряд на 200
			// чехлов состоял из шапки «200 изделий» и фразы о том, что кроить их не с чем
			// сопоставить, — то есть из бумаги без инструкции ровно на те изделия, которые в ней же
			// и заявлены. Резолюция та же, что у aux-цвета (BOM карточки): рецептов у такой карточки
			// нет и взяться им неоткуда.
			//
			// А ВОТ НА ПРОДАВАЕМОЙ КАРТОЧКЕ такая линия — дыра в плане, а не легаси-выход: колорвеи у
			// неё есть, они расходятся пинами, и колонка, собранная по голому BOM, напечатала бы цеху
			// умолчание слота вместо пришпиленного артикула — тихо и с видом обычной строки. Здесь
			// честнее прежняя оговорка: изделия посчитаны в шапке, а их крою не с чем сопоставиться,
			// пока цвет не назван.
			if sellable {
				noProduct += ln.PlannedQty
				continue
			}
		}
		g := groups[key]
		if g == nil {
			g = newCutPlanGroup(key, cw, variants[key.variantID], card, sizes)
			switch {
			case key.colorwayID != 0:
				g.order = colorwayOrder[key.colorwayID]
			case key.variantID != 0:
				g.order = variantOrder[key.variantID]
			default:
				// Безымянный выход печатается ПОСЛЕ всех объявленных колонок карточки: своей позиции
				// в ней у него нет, а нулевой order поставил бы его перед первым колорвеем.
				g.order = len(card.Colorways) + len(card.OutputVariants)
			}
			groups[key] = g
			ordered = append(ordered, g)
		}
		g.garments += ln.PlannedQty
		g.bySize[ln.SizeId] += ln.PlannedQty
		sizes.rankOf(ln.SizeId) // регистрирует размер вне градации, чтобы сказать о нём один раз
	}

	if noProduct > 0 {
		caveats = append(caveats, fmt.Sprintf(
			"lines with no product and no aux colour (%d units) did not make it into the run pack — there is nothing to match their cutting against", noProduct))
	}
	if noColorway > 0 {
		pids := make([]int, 0, len(unknownProducts))
		for pid := range unknownProducts {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		caveats = append(caveats, fmt.Sprintf(
			"products %v are not found among the card's colourways — %d units did not make it into the run pack", pids, noColorway))
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].order != ordered[j].order {
			return ordered[i].order < ordered[j].order
		}
		if ordered[i].key.colorwayID != ordered[j].key.colorwayID {
			return ordered[i].key.colorwayID < ordered[j].key.colorwayID
		}
		return ordered[i].key.variantID < ordered[j].key.variantID
	})
	return ordered, planned, caveats
}

// newCutPlanGroup готовит колонку: имена, и — главное — те два множества, на которых стоит вся
// резолюция (какие строки рецепта называют детали и какие рулонные слоты колонке вообще доступны).
func newCutPlanGroup(
	key cutPlanGroupKey,
	cw *entity.TechCardColorway,
	variant *entity.TechCardOutputVariant,
	card *entity.TechCard,
	sizes *cutPlanSizeIndex,
) *cutPlanGroup {
	g := &cutPlanGroup{
		key:        key,
		bySize:     map[int]int{},
		sizes:      sizes,
		byPieceID:  map[int][]*entity.TechCardColorwayUsage{},
		byPieceKey: map[string][]*entity.TechCardColorwayUsage{},
		specLabel:  "the colourway recipe",
	}
	switch {
	case variant != nil:
		g.variantName = variant.ColorName
	case key.colorwayID == 0 && key.variantID == 0:
		// Легаси-выход: цвета у него нет не по недосмотру, а потому что карточка их не заводит.
		// Колонка всё равно обязана называться — см. cutPlanLegacyOutputName.
		g.variantName = cutPlanLegacyOutputName
	}
	if cw == nil {
		// ВСПОМОГАТЕЛЬНАЯ КАРТОЧКА: рецептов у неё нет вовсе, и вывод слота идёт по её BOM.
		//
		// Это НЕ расширение правила «ровно один рулонный слот», а оно же на карточке, у которой
		// колорвеев не бывает. Aux-цвет — это корзина СКЛАДА для готового изделия (0252: «one
		// warehouse bucket per colour»), а не выбор входной ткани: он красит выход, а не вход,
		// поэтому выбирать между вариантами тут нечего, и «ровно один рулонный слот» остаётся ровно
		// одним слотом. Догадки, которую можно было бы напечатать фактом, здесь не существует —
		// неоднозначность (двух рулонных слотов) точно так же уходит в блокеры ниже.
		//
		// Обратное решение — блокировать любую aux-деталь — сделало бы крой пыльника списком
		// блокеров с фразой про несуществующий колорвей, а поля output_variant_* и «size_id = 0 =
		// безразмерная линия (aux-прогон)» самого контракта — полями, которые не может заполнить
		// никто.
		g.specLabel = "the card BOM"
		for i := range card.BomItems {
			if b := &card.BomItems[i]; cutPlanRollSections[b.Section] {
				g.rollSlots = append(g.rollSlots, cutPlanSlot{bom: b})
			}
		}
		g.fusingSlots = cutPlanFusingSlots(g.rollSlots)
		return g
	}

	g.colorwayName = cw.Name
	if g.colorwayName == "" {
		// Код колорвея — не выдумка, а вторая его подпись в карточке; пустая колонка в цехе хуже.
		g.colorwayName = cw.Code.String
	}
	// По указателю, а не по id — по той же причине, что и в resolve: у строк BOM старого снапшота
	// релиза id нулевые, и дедуп по ним оставил бы один «рулонный слот» там, где их два.
	seenSlot := make(map[*entity.TechCardBomItem]bool, len(cw.Usages))
	for i := range cw.Usages {
		u := &cw.Usages[i]
		if u.PieceId.Valid && u.PieceId.Int64 > 0 {
			pid := int(u.PieceId.Int64)
			g.byPieceID[pid] = append(g.byPieceID[pid], u)
		}
		if u.PieceLineKey != "" {
			g.byPieceKey[u.PieceLineKey] = append(g.byPieceKey[u.PieceLineKey], u)
		}
		bom := planBomLine(u, card.BomItems)
		if bom == nil || !cutPlanRollSections[bom.Section] || seenSlot[bom] {
			continue
		}
		seenSlot[bom] = true
		g.rollSlots = append(g.rollSlots, cutPlanSlot{bom: bom, usage: u})
	}
	g.fusingSlots = cutPlanFusingSlots(g.rollSlots)
	return g
}

// cutPlanFusingSlots отбирает клеевые из уже собранных рулонных слотов. Отдельная функция ровно
// затем, чтобы множество клеевых НЕ МОГЛО разойтись с множеством, на котором работает вывод слота:
// две независимые выборки по секции разъехались бы при первой правке одной из них.
func cutPlanFusingSlots(roll []cutPlanSlot) []cutPlanSlot {
	var out []cutPlanSlot
	for _, s := range roll {
		if s.bom != nil && s.bom.Section == entity.BomSectionInterlining {
			out = append(out, s)
		}
	}
	return out
}

// cutPlanSizeIndex упорядочивает размеры так, как их градуирует КАРТА, и помнит те, которых в
// градации нет.
type cutPlanSizeIndex struct {
	rank  map[int]int
	base  int
	stray []int
}

func newCutPlanSizeIndex(sizeIDs []int) *cutPlanSizeIndex {
	idx := &cutPlanSizeIndex{rank: make(map[int]int, len(sizeIDs)), base: len(sizeIDs)}
	for i, id := range sizeIDs {
		if _, dup := idx.rank[id]; dup {
			continue
		}
		idx.rank[id] = i
	}
	return idx
}

// rankOf — место размера в печати. Побочный эффект намеренный: первое обращение к размеру ВНЕ
// градации регистрирует его, и оговорка о нём говорится ровно один раз, а не по разу на линию.
//
// Безразмерная линия (size_id = 0, aux-прогон) идёт первой и оговоркой НЕ становится: у неё нет
// размера не потому, что кто-то ошибся, а потому что у пыльника размеров не бывает.
func (x *cutPlanSizeIndex) rankOf(sizeID int) int {
	if sizeID <= 0 {
		return -1
	}
	if r, ok := x.rank[sizeID]; ok {
		return r
	}
	r := x.base + len(x.stray)
	x.rank[sizeID] = r
	x.stray = append(x.stray, sizeID)
	return r
}

// strayNotes — размеры, которых карта не градуирует. Их количества ПОСЧИТАНЫ (изделия существуют и
// их кто-то будет кроить), но выкройки на них в карточке может не быть, и промолчать об этом
// значило бы выдать наряд на размер, которого нет в спецификации.
func (x *cutPlanSizeIndex) strayNotes() []string {
	out := make([]string, 0, len(x.stray))
	for _, id := range x.stray {
		out = append(out, fmt.Sprintf(
			"size %s is not in the card's size range — its quantities are counted, but the card may have no pattern for it",
			sizeLabelOf(id)))
	}
	return out
}

// cutPlanSizeName — имя размера для бумаги. Пустое, когда словарь его не знает: выдуманное имя
// размера на наряде хуже пустой ячейки рядом с числом, которое и так на месте.
func cutPlanSizeName(sizeID int) string {
	if sizeID <= 0 {
		return ""
	}
	return sizeDictionaryName(sizeID)
}

// CutSpecCardFromReleaseSnapshot восстанавливает из замороженного блоба релиза РОВНО ТО, что читает
// кат-лист: градацию, детали, BOM, рецепты колорвеев и aux-цвета. Ни костинга, ни операций, ни
// медиа — и это не «пока не сделали»: частичный читатель, названный общим именем, рано или поздно
// будет вызван ради поля, которого он не заполняет, и отдаст ноль вместо ошибки. Имя говорит, что
// он читает.
//
// ЧИТАТЬ БЛОБ ЧЕРЕЗ ConvertPbTechCardInsertToEntity НЕЛЬЗЯ: тот ВАЛИДИРУЕТ payload на запись, а
// снапшот — это история. Релиз, снятый до того, как правило появилось, отказался бы читаться, и
// «эта карточка старая» превратилось бы в «по этой карточке нельзя выдать наряд» — прецедент
// дословно назван в MarkerLayoutFactsFromBlob.
//
// ДВЕ ИДЕНТИЧНОСТИ, КОТОРЫХ В БЛОБЕ НЕТ, и обе стоит знать в лицо:
//   - у детали в контракте НЕТ id, только line_key — поэтому кат-лист сопоставляет рецепт с деталью
//     и по ULID тоже (см. usagesFor), а piece_id в строках наряда по релизу будет нулём;
//   - у колорвея нет отдельного product_id: колорвей карточки И ЕСТЬ продукт (product.style_id =
//     card, materials.go: `c.id AS product_id`), поэтому colorway_id снапшота кладётся в оба поля.
func CutSpecCardFromReleaseSnapshot(snap *pb_common.TechCard) *entity.TechCard {
	if snap == nil || snap.GetTechCard() == nil {
		return nil
	}
	ins := snap.GetTechCard()
	card := &entity.TechCard{Id: int(snap.GetId())}
	for _, id := range ins.GetSizeIds() {
		card.SizeIds = append(card.SizeIds, int(id))
	}
	for _, b := range ins.GetBomItems() {
		item := entity.TechCardBomItem{
			Id:      int(b.GetId()),
			LineKey: b.GetLineKey(),
			Name:    b.GetName(),
			Section: techCardBomSectionPbToEntity[b.GetSection()],
		}
		if mid := b.GetMaterialId(); mid > 0 {
			item.MaterialId = sql.NullInt64{Int64: mid, Valid: true}
		}
		card.BomItems = append(card.BomItems, item)
	}
	for _, p := range ins.GetPieces() {
		// proto3 zero = «поля не было», как и на записи (parseTechCardPieces): деталь кроят хотя бы
		// раз, иначе она не деталь.
		perGarment := int(p.GetPiecesPerGarment())
		if perGarment <= 0 {
			perGarment = 1
		}
		piece := entity.TechCardPiece{
			LineKey:          p.GetLineKey(),
			Name:             p.GetName(),
			PiecesPerGarment: perGarment,
			Grainline:        p.GetGrainline(),
			Fused:            p.GetFused(),
		}
		if v, ok := techCardPieceCutSymmetryPbToEntity[p.GetCutSymmetry()]; ok {
			piece.CutSymmetry = sql.NullString{String: string(v), Valid: true}
		}
		card.Pieces = append(card.Pieces, piece)
	}
	for _, c := range snap.GetColorways() {
		cw := entity.TechCardColorway{
			Id:   int(c.GetColorwayId()),
			Name: c.GetDevName(),
			Code: sql.NullString{String: c.GetDevCode(), Valid: c.GetDevCode() != ""},
		}
		if id := c.GetColorwayId(); id > 0 {
			cw.ProductId = sql.NullInt32{Int32: id, Valid: true}
		}
		for _, u := range c.GetUsages() {
			// Читаем ТОЛЬКО связи: слот, деталь и пин артикула. Нормы расхода — вопрос материального
			// плана, а не наряда, и лишнее поле здесь пришлось бы держать в синхроне без единого
			// читателя.
			use := entity.TechCardColorwayUsage{PieceLineKey: u.GetPieceLineKey(), BomLineKey: u.GetBomLineKey()}
			if id := u.GetBomItemId(); id > 0 {
				use.BomItemId = sql.NullInt64{Int64: id, Valid: true}
			}
			if u.BomItemIndex != nil {
				use.BomItemIndex = sql.NullInt32{Int32: *u.BomItemIndex, Valid: true}
			}
			if id := u.GetPieceId(); id > 0 {
				use.PieceId = sql.NullInt64{Int64: id, Valid: true}
			}
			if u.MaterialId != nil && *u.MaterialId > 0 {
				use.MaterialId = sql.NullInt64{Int64: *u.MaterialId, Valid: true}
			}
			cw.Usages = append(cw.Usages, use)
		}
		card.Colorways = append(card.Colorways, cw)
	}
	for _, v := range snap.GetOutputVariants() {
		card.OutputVariants = append(card.OutputVariants, entity.TechCardOutputVariant{
			TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{
				Id:         int(v.GetId()),
				ColorCode:  v.GetColorCode(),
				MaterialId: int(v.GetMaterialId()),
				Active:     v.GetActive(),
			},
			ColorName: v.GetColorName(),
		})
	}
	return card
}

// cutPlanMaterialName называет артикул: каталожным именем, когда карточка его резолвила, иначе
// собственным снимком строки BOM. Под слотами имя строки — это РОЛЬ («основная ткань»), поэтому
// каталог обязан выигрывать всякий раз, когда он есть; но пустая ячейка вместо роли была бы хуже —
// пин при этом всё равно виден по material_id и pinned.
func cutPlanMaterialName(card *entity.TechCard, materialID int, bom *entity.TechCardBomItem) string {
	if m, ok := card.LinkedMaterials[materialID]; ok && m.Name != "" {
		return m.Name
	}
	if bom != nil {
		return bom.Name
	}
	return ""
}
