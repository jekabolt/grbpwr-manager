package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══════════════════════ ДВЕРЬ ПЛЕЙГРАУНДА (kind=freeform, kind=cutout) ═══════════════════════
//
// ПЛЕЙГРАУНД НЕ ЧИТАЕТ КАРТОЧКУ ВОВСЕ: ни верстака, ни референсов, ни описания изделия, ни
// рецепта. Его вход — ТОЛЬКО те картинки, которые человек положил в `params.freeform.items`, и
// только те слова, которые он написал. Поэтому у него собственный список входов, собственная
// проверка формы и собственная арифметика ссылок — всё в этом файле, чтобы «что уезжает в модель
// у плейграунда» отвечалось в одном месте, а не собиралось по родам в четырёх.
//
// ⚠ ВСЁ ЗДЕСЬ — ДО ДЕНЕГ. StartRun резервирует бюджет дня той же транзакцией, что вставляет
// строку; всякий отказ после неё — занятый резерв и `failed` в оплаченной истории за просьбу,
// которую можно было отклонить бесплатно.

// designFreeformPresets — ЧТО ЭТОТ СЕРВЕР УМЕЕТ В ПЛЕЙГРАУНДЕ, списком для экрана.
//
// ⚠ ЭТО НЕ КОПИЯ СЛОВАРЯ, А ТА ЖЕ ЛЕСТНИЦА, ЧТО У ДВЕРИ, ЗАДАННАЯ ЗАРАНЕЕ. Оба вопроса — «пустят
// ли сюда» — решают одни и те же две проверки: флаг генерации и ворота рода (Produces × Accepts у
// маршрута). Список имён, зашитый здесь отдельно, разошёлся бы с дверью молча и в обе стороны:
// экран рисовал бы кнопку, за которой отказ, или прятал бы работающую.
//
// ПУСТОЙ, НО НЕ nil, И ЭТО ПРО ПРОВОД. Ответ маршалится с EmitUnpopulated, поэтому поле уезжает
// всегда — `[]` значит «сервер про плейграунд знает и говорит, что сейчас нельзя», а отсутствие
// поля значило бы «сервер старый». Клиент читает ПРИСУТСТВИЕ поля, ровно как у has_fabric_render
// и colour_plan; пустой список гасит ячейку на рельсе, а не показывает её сломанной.
//
// ⚠ `cutout` СТОИТ В ЭТОМ ЖЕ СПИСКЕ, ХОТЯ ЭТО ДРУГОЙ РОД ПРОГОНА. Для человека это четвёртая
// кнопка в одном ряду («cut out the background»), и спрашивать её доступность отдельным полем
// значило бы завести на экране второе понятие там, где у него одно. Ворота у неё СВОИ — свой
// провайдер, свой ключ, — поэтому она появляется и исчезает независимо от трёх остальных.
func (s *Server) designFreeformPresets() []string {
	out := []string{}
	if s.designGenerationGate() != nil {
		return out
	}
	if s.designKindGateCheck(entity.DesignRunKindFreeform) == nil {
		out = append(out, entity.FreeformPresets()...)
	}
	if s.designKindGateCheck(entity.DesignRunKindCutout) == nil {
		out = append(out, entity.DesignRunKindCutout)
	}
	return out
}

// designRefuseMalformedFreeform — ФОРМА ПРОСЬБЫ ПЛЕЙГРАУНДА, и спрашивается она С ГОВОРЯЩЕГО.
//
// ⚠ ГРАНИЦА ТА ЖЕ, ЧТО У designRefuseMalformedColourMaps, И ДОВОД ТОТ ЖЕ. Словари (пресеты, роли)
// законно растут и законно сужаются, а параметры родителя заморожены: проверка УНАСЛЕДОВАННОГО
// значения сделала бы старый прогон неперезапускаемым навсегда — в тот день, когда пресет
// переименуют. Молчащий реран противоречить не может, поэтому `spoken == nil` — это ноль проверок
// и ноль отказов.
//
// ⚠ ЧТО ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ, И ЭТО НАМЕРЕННО: «есть ли хоть одна картинка» и «влезает ли всё это
// в один вызов». Первое — вопрос о РАБОТОСПОСОБНОСТИ прогона (designRefuseUnworkableSources),
// второе — о ДЕНЬГАХ (designRefuseFreeformOverflow), и оба задаются ДЕЙСТВУЮЩИМ параметрам, а не
// сообщению: неработоспособная форма остаётся неработоспособной и на реране.
func designRefuseMalformedFreeform(kind string, spoken *pb_common.DesignRunParams) error {
	ff := spoken.GetFreeform()
	if kind != entity.DesignRunKindFreeform {
		// ⚠ ЧУЖОЙ РОД С ЗАПОЛНЕННЫМ `freeform` — ОТКАЗ, А НЕ МОЛЧАЛИВОЕ ИГНОРИРОВАНИЕ. Поле не
		// читает никто, кроме плейграунда, поэтому «принято» здесь означало бы «размеченные вами
		// области и ваши слова не поедут никуда, и мы вам об этом не скажем» — за деньги.
		if ff != nil && (strings.TrimSpace(ff.GetPreset()) != "" || len(ff.GetItems()) > 0) {
			return designRefusal(codes.InvalidArgument, "freeform_forbidden",
				fmt.Sprintf("params.freeform is the playground's own list of pictures and only a "+
					"freeform run reads it; this is a %s run. Nothing was reserved and nothing was "+
					"charged", kind),
				map[string]string{"kind": kind})
		}
		return nil
	}
	if ff == nil {
		// Молчание проверять не на чем: «плейграунд без единой картинки» — вопрос
		// designRefuseUnworkableSources, и отвечает он там же, где реран.
		return nil
	}
	if !entity.IsFreeformPreset(ff.GetPreset()) {
		return designRefusal(codes.InvalidArgument, "unknown_preset",
			fmt.Sprintf("params.freeform.preset %q is not %s — the preset picks the craft paragraph, "+
				"and the server has no paragraph for that word. Nothing was reserved and nothing was "+
				"charged", ff.GetPreset(), strings.Join(entity.FreeformPresets(), " | ")),
			map[string]string{"preset": ff.GetPreset()})
	}
	if n := len(ff.GetItems()); n > entity.MaxDesignFreeformItems {
		return status.Errorf(codes.InvalidArgument,
			"params.freeform.items names %d pictures; the ceiling is %d",
			n, entity.MaxDesignFreeformItems)
	}
	// ⚠ ОДИН СПИСОК НА ОДИН ФАКТ. `extra_input_media_ids` и `freeform.items` — два написания
	// «картинки этого прогона», и прогон, назвавший картинку в обоих, отправил бы её дважды: один
	// раз подписью плейграунда, второй — «additional reference image». Сборка ссылок читает у
	// плейграунда ТОЛЬКО items (designgen/snapshot.go), так что второй список либо уехал бы
	// молчаливым дублем, либо не уехал вовсе — и оба исхода человек узнал бы по счёту.
	if len(ff.GetItems()) > 0 && len(spoken.GetExtraInputMediaIds()) > 0 {
		return designRefusal(codes.InvalidArgument, "one_list_per_fact",
			fmt.Sprintf("a freeform run states its pictures in params.freeform.items; this one also "+
				"names %d in params.extra_input_media_ids. One list per fact — the playground reads "+
				"items and nothing else. Nothing was reserved and nothing was charged",
				len(spoken.GetExtraInputMediaIds())),
			map[string]string{"extra": strconv.Itoa(len(spoken.GetExtraInputMediaIds()))})
	}
	// ⚠ ОДНА КАРТИНКА — ОДНА ЗАПИСЬ, И ЭТО НЕ АККУРАТНОСТЬ, А ПОТОЛОК ОБЛАСТЕЙ. Потолок в четыре
	// области объявлен НА КАРТИНКУ; та же картинка, названная дважды по две области, обошла бы его
	// молча — а обходится он в производные картинки, то есть в размер платного запроса. Плюс
	// снимок дедуплицирует ссылки, а сборка задания — нет: прогон получил бы ДВЕ обведённые копии
	// с подписями «image 1 with area A…», обе про один и тот же номер.
	seen := make(map[int32]int, len(ff.GetItems()))
	for i, it := range ff.GetItems() {
		where := "params.freeform.items." + strconv.Itoa(i)
		if it.GetMediaId() <= 0 {
			return status.Errorf(codes.InvalidArgument, "%s.media_id must be a media id", where)
		}
		if first, dup := seen[it.GetMediaId()]; dup {
			return designRefusal(codes.InvalidArgument, "duplicate_picture",
				fmt.Sprintf("%s names picture %d, which params.freeform.items.%d already names: one "+
					"picture is one entry, with up to %d marked areas on it. Nothing was reserved and "+
					"nothing was charged", where, it.GetMediaId(), first,
					entity.MaxDesignFreeformRegionsPerItem),
				map[string]string{"media_id": strconv.Itoa(int(it.GetMediaId()))})
		}
		seen[it.GetMediaId()] = i
		if !entity.IsFreeformRole(it.GetRole()) {
			return designRefusal(codes.InvalidArgument, "unknown_role",
				fmt.Sprintf("%s.role %q is not subject | hardware | cloth (empty is «just a picture»). "+
					"Nothing was reserved and nothing was charged", where, it.GetRole()),
				map[string]string{"role": it.GetRole()})
		}
		if n := len(it.GetRegions()); n > entity.MaxDesignFreeformRegionsPerItem {
			return status.Errorf(codes.InvalidArgument,
				"%s.regions marks %d places; the ceiling is %d per picture",
				where, n, entity.MaxDesignFreeformRegionsPerItem)
		}
		for r, region := range it.GetRegions() {
			if err := designRefuseMalformedRegion(where+".regions."+strconv.Itoa(r), region); err != nil {
				return err
			}
		}
		// ⚠ ПОДПИСЕЙ НЕ БОЛЬШЕ, ЧЕМ ОБЛАСТЕЙ ПЛЮС ОДНА, И ЛИШНЯЯ — ЭТО ПОДПИСЬ КАРТИНКИ ЦЕЛИКОМ.
		// Пара «область i ↔ подпись i» позиционная, и она же строит вложения; список подписей
		// длиннее пары молча потерял бы хвост — то есть слова человека не доехали бы до модели, а
		// экран показывал бы их отправленными.
		if n, max := len(it.GetTexts()), len(it.GetRegions())+1; n > max {
			return status.Errorf(codes.InvalidArgument,
				"%s.texts has %d entries but the picture has %d marked areas: a text belongs to the "+
					"area of the same index, and at most one text describes the whole picture",
				where, n, len(it.GetRegions()))
		}
		for t, text := range it.GetTexts() {
			if n := len([]rune(text)); n > entity.MaxDesignFreeformTextRunes {
				return status.Errorf(codes.InvalidArgument,
					"%s.texts.%d is %d characters; the ceiling is %d",
					where, t, n, entity.MaxDesignFreeformTextRunes)
			}
		}
	}
	return nil
}

// designRefuseMalformedRegion — ОДНА РАЗМЕЧЕННАЯ ОБЛАСТЬ.
//
// ⚠ ТОЛЬКО POLYGON, И ЭТО НЕ УЖЕСТОЧЕНИЕ РАДИ ПОРЯДКА. Область плейграунда работает ровно двумя
// способами: контуром на размеченной копии и bbox'ом кропа (designgen/freeform_derive.go). PIN —
// это точка, у неё нет площади; DIM — размерная линия; ARC — дуга. Ни у одного из них нет того,
// что оба потребителя читают, поэтому «принято» означало бы кроп нулевой площади в углу кадра —
// за деньги и молча.
//
// Координаты проверяются designUnitInterval, тем же выражением, что и кроп кадра: доли 0..1,
// обычная запись, потолок знаков после запятой. Второе правописание «доли картинки» разошлось бы
// с первым в первый же день.
func designRefuseMalformedRegion(where string, region *pb_common.TechCardAnnotation) error {
	if region == nil {
		return status.Errorf(codes.InvalidArgument, "%s is empty", where)
	}
	if region.GetKind() != pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON {
		return designRefusal(codes.InvalidArgument, "region_not_a_polygon",
			fmt.Sprintf("%s is %s; a marked area is a POLYGON — the server outlines it on a copy of "+
				"the picture and crops it, and neither can be done with a point or a line. Nothing "+
				"was reserved and nothing was charged", where, region.GetKind().String()),
			map[string]string{"kind": region.GetKind().String()})
	}
	points := region.GetPoints()
	if len(points) < designFreeformRegionMinPoints || len(points) > designFreeformRegionMaxPoints {
		return status.Errorf(codes.InvalidArgument,
			"%s.points has %d points; a marked area is %d..%d points",
			where, len(points), designFreeformRegionMinPoints, designFreeformRegionMaxPoints)
	}
	for i, p := range points {
		if _, err := designUnitInterval(fmt.Sprintf("%s.points.%d.x", where, i), p.GetX()); err != nil {
			return err
		}
		if _, err := designUnitInterval(fmt.Sprintf("%s.points.%d.y", where, i), p.GetY()); err != nil {
			return err
		}
	}
	return nil
}

// Сколько точек в размеченной области. Три — это треугольник, меньшего многоугольника не бывает;
// двенадцать — потолок того, что человек рисует пальцем и что имеет смысл обводить контуром.
// Прямоугольник, который тянут мышью, приезжает сюда четырьмя точками.
const (
	designFreeformRegionMinPoints = 3
	designFreeformRegionMaxPoints = 12
)

// designFreeformItemMediaIDs — картинки плейграунда, в порядке их номеров. Один читатель у
// границы карточки, второй — у сужения ссылок на реране.
func designFreeformItemMediaIDs(params *pb_common.DesignRunParams) []int {
	items := params.GetFreeform().GetItems()
	out := make([]int, 0, len(items))
	for _, it := range items {
		if id := int(it.GetMediaId()); id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// designRefuseFreeformOverflow — ВЛЕЗАЕТ ЛИ ЭТОТ ПРОГОН В ОДИН ВЫЗОВ, посчитано ДО денег.
//
// ⚠ КАРТИНОК УЕЗЖАЕТ БОЛЬШЕ, ЧЕМ ЧЕЛОВЕК ПОЛОЖИЛ, И В ЭТОМ ВСЯ ПРОВЕРКА. Каждая размеченная
// область рождает КРОП (отдельная картинка), а каждая картинка, на которой есть хоть одна область,
// рождает ещё и РАЗМЕЧЕННУЮ КОПИЮ с контурами. Восемь картинок с четырьмя областями каждая — это
// 8 + 32 + 8 = 48 ссылок при потолке провайдера 16, и без этой арифметики отказ пришёл бы от
// поставщика — после резерва, а на асинхронном маршруте и после списания.
//
// ЧИСЛО БЕРЁТСЯ У ПОСТАВЩИКА (orimages.MaxInputReferences), а не переписывается сюда: копия
// разошлась бы молча и в ту сторону, в которую дороже. Формула ОБЯЗАНА совпадать с тем, что
// действительно собирает designgen (buildJob → deriveFreeform); проба держит обе половины рядом.
func designRefuseFreeformOverflow(kind string, params *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindFreeform {
		return nil
	}
	items := params.GetFreeform().GetItems()
	pictures, regions, marked := 0, 0, 0
	for _, it := range items {
		if it.GetMediaId() <= 0 {
			continue
		}
		pictures++
		if n := len(it.GetRegions()); n > 0 {
			regions += n
			marked++
		}
	}
	total := pictures + regions + marked
	if total <= orimages.MaxInputReferences {
		return nil
	}
	return designRefusal(codes.InvalidArgument, "too_many_pictures",
		fmt.Sprintf("this run would send %d images in one call — %d pictures, %d close crops of the "+
			"marked areas and %d copies with the outlines drawn on them — and the provider takes at "+
			"most %d. Remove a picture or a marked area. Nothing was reserved and nothing was charged",
			total, pictures, regions, marked, orimages.MaxInputReferences),
		map[string]string{
			"images":   strconv.Itoa(total),
			"pictures": strconv.Itoa(pictures),
			"crops":    strconv.Itoa(regions),
			"outlined": strconv.Itoa(marked),
			"ceiling":  strconv.Itoa(orimages.MaxInputReferences),
		})
}

// designFreeformRefs — ВХОДЫ ПЛЕЙГРАУНДА В СНИМКЕ: одна запись на картинку, с областями,
// замороженными готовым сообщением.
//
// ⚠ ОБЛАСТЬ ЗАМЕРЗАЕТ ВЫНОСКОЙ (DesignMoodCallout), А НЕ ПАРОЙ СПИСКОВ. Снимок — это то, что
// ушло в модель, и «область A ↔ слова про область A» обязано читаться из него ОДНИМ элементом:
// два параллельных списка в замороженной строке разъезжаются при первом же чтении вполглаза, и
// история начинает утверждать про картинку то, чего про неё не говорили.
//
// ПОДПИСЬ БЕЗ ОБЛАСТИ СТАНОВИТСЯ ЗАПИСКОЙ (`Note`) — тем же полем, которым говорит записка
// референса: это слова ПРО КАРТИНКУ ЦЕЛИКОМ, и другого места для них в снимке нет.
func designFreeformRefs(params *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
	items := params.GetFreeform().GetItems()
	out := make([]*pb_common.DesignInputRef, 0, len(items))
	seen := make(map[int32]struct{}, len(items))
	for _, it := range items {
		id := it.GetMediaId()
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			// Одна и та же картинка, названная дважды, — одна запись снимка. Сборка ссылок
			// дедуплицирует так же (referenceList.add), значит снимок и вложения совпадают.
			continue
		}
		seen[id] = struct{}{}
		ref := &pb_common.DesignInputRef{MediaId: id, Role: it.GetRole()}
		texts := it.GetTexts()
		for i, region := range it.GetRegions() {
			text := ""
			if i < len(texts) {
				text = texts[i]
			}
			ref.Callouts = append(ref.Callouts, &pb_common.DesignMoodCallout{
				MediaId:    id,
				Text:       text,
				Annotation: region,
			})
		}
		// Хвостовая подпись — та, которой не досталось области.
		if len(texts) > len(it.GetRegions()) {
			ref.Note = texts[len(it.GetRegions())]
		}
		out = append(out, ref)
	}
	return out
}
