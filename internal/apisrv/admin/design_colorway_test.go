package admin

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРОБЫ ОСИ КОЛОРВЕЯ (0356, L-2/L-3).
//
// Модель владельца дословно: «флеты одна разметка у фабрик рендера должно быть так 1 колорвей там
// должно быть мультивью которое мы генерим + из его нарезаем сплитом стороны размеченные и на
// каждый колорвей так и потом мы в 3д рендере уже выбираем колорвей который будем рендерить».
//
// Класс дефекта, который стерегут пробы этого файла, тот же, что у второй оси (kind): поле
// существует на проводе и в сторе, а какой-то ярус посередине его молча выбрасывает — и рендеры
// всех колорвеев ложатся в один верстак, а 3D собирается из смеси цветов, за которую заплачено.

// benchCw — слот рендер-верстака колорвея cw (0 = неатрибутированный легаси) с плитой pic.
func benchCw(id int, view string, cw int, pic int) entity.DesignBenchSlot {
	slot := entity.DesignBenchSlot{
		Id: id, TechCardId: designRunCardID, ViewKey: view,
		Kind:         entity.DesignPictureKindRender,
		ExclusiveKey: entity.DesignBenchExclusiveKey(view, cw),
		PictureId:    sql.NullInt32{Int32: int32(pic), Valid: true},
		Picture: &entity.DesignPicture{
			Id: pic, TechCardId: designRunCardID, MediaId: pic + 1000,
			Kind: entity.DesignPictureKindRender,
		},
	}
	if cw > 0 {
		slot.ColorwayId = sql.NullInt32{Int32: int32(cw), Valid: true}
		slot.Picture.ColorwayId = sql.NullInt32{Int32: int32(cw), Valid: true}
	}
	return slot
}

// designColorwayBench — верстак, на котором отбору ЕСТЬ ЧЕМ ошибиться: рендеры двух колорвеев,
// неатрибутированный легаси-рендер и флэт — все четыре занимают вид front одновременно, что до
// этой оси было невозможно выразить вовсе.
func designColorwayBench() []entity.DesignBenchSlot {
	flat := entity.DesignBenchSlot{
		Id: 1, TechCardId: designRunCardID, ViewKey: entity.DesignViewFront,
		Kind: entity.DesignPictureKindFlat, ExclusiveKey: entity.DesignViewFront,
		PictureId: sql.NullInt32{Int32: 10, Valid: true},
		Picture: &entity.DesignPicture{
			Id: 10, TechCardId: designRunCardID, MediaId: 1010,
			Kind: entity.DesignPictureKindFlat,
		},
	}
	return []entity.DesignBenchSlot{
		flat,
		benchCw(2, entity.DesignViewFront, 5, 11),
		benchCw(3, entity.DesignViewBack, 5, 12),
		benchCw(4, entity.DesignViewFront, 6, 13),
		benchCw(5, entity.DesignViewFront, 0, 14), // легаси, до оси
	}
}

// ─────────────────────── L-3: отбор плит 3D ───────────────────────

// 3D КОЛОРВЕЯ A ВИДИТ ТОЛЬКО ПЛИТЫ КОЛОРВЕЯ A.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать `matchColorway`-фильтр из designSelectBench — ровно состояние до
// правки, когда `want = render` брал рендеры ВСЕХ колорвеев карточки разом. Прогон собирался бы
// из смеси цветов, платилось бы за смесь, и в записи не оставалось бы ничего, чем это разобрать.
func TestA3DRunForOneColorwaySeesOnlyThatColorwaysPlates(t *testing.T) {
	slots, plates := designSelectBench(designInputSources{
		Kind:   entity.DesignRunKindThreed,
		Bench:  designColorwayBench(),
		Params: &pb_common.DesignRunParams{ColorwayId: 5},
	})
	require.Len(t, slots, 2, "колорвей 5 держит front и back — ровно два слота")
	require.ElementsMatch(t, []int32{11, 12}, plates,
		"плиты чужого колорвея (13) и неатрибутированная (14) не должны попасть в оплаченный прогон")
}

// «КОЛОРВЕЙ НЕ НАЗВАН» — ТОЖЕ ЗНАЧЕНИЕ, А НЕ ОТСУТСТВИЕ ФИЛЬТРА: безколорвейное 3D видит РОВНО
// неатрибутированный верстак — тот единственный, что существовал до оси. Старая карточка ведёт
// себя байт в байт как раньше, и НИЧЕГО колорвейного к ней не подмешивается.
//
// МУТАЦИЯ: трактовать 0 как «фильтра нет» — тогда сюда приехали бы все четыре рендера.
func TestA3DRunWithNoColorwaySeesOnlyUnattributedPlates(t *testing.T) {
	_, plates := designSelectBench(designInputSources{
		Kind:   entity.DesignRunKindThreed,
		Bench:  designColorwayBench(),
		Params: &pb_common.DesignRunParams{},
	})
	require.Equal(t, []int32{14}, plates,
		"безколорвейное 3D читает только легаси-верстак; именованные колорвеи — чужие")
}

// РЕНДЕР ЛЮБОГО КОЛОРВЕЯ ЧИТАЕТ ОДИН И ТОТ ЖЕ ФЛЭТОВЫЙ ВЕРСТАК — «флеты одна разметка» (L-4).
// Фильтр колорвея обязан НЕ ДЕЙСТВОВАТЬ на отбор флэтов: рендеры колорвеев различаются рецептом
// цвета, а не чертежом.
//
// МУТАЦИЯ: распространить colourway-фильтр на want=flat — рендер именованного колорвея потерял бы
// все флэты (у флэта колорвей всегда 0) и уехал бы к модели без единого чертежа.
func TestARenderRunReadsTheOneFlatBenchWhicheverColorwayItIsFor(t *testing.T) {
	for _, cw := range []int32{0, 5, 6} {
		_, plates := designSelectBench(designInputSources{
			Kind:   entity.DesignRunKindRender,
			Bench:  designColorwayBench(),
			Params: &pb_common.DesignRunParams{ColorwayId: cw},
		})
		require.Equalf(t, []int32{10}, plates,
			"рендер колорвея %d обязан прочитать флэтовый верстак целиком", cw)
	}
}

// designColorwayBenchWithout — тот же верстак, но БЕЗ слотов названного колорвея: так выглядит
// карточка, у которой этот цвет ещё не разложен (или уже разобран). Честный способ построить
// случай «колорвею нечего рендерить» — убрать его слоты, а не переписать множество поверх них.
func designColorwayBenchWithout(colorway int) []entity.DesignBenchSlot {
	in := designColorwayBench()
	out := make([]entity.DesignBenchSlot, 0, len(in))
	for _, s := range in {
		if entity.DesignColorwayOrNone(s.ColorwayId) == colorway &&
			entity.DesignPictureKindTakesColorway(s.Kind) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ─────────────────────── L-3: ворота 3D на колорвей ───────────────────────

// ЧЛЕНСТВО, А НЕ НЕПУСТОТА. Рендер чужого колорвея не открывает дверь: отбор плит вернул бы
// пустоту, и прогон был бы оплачен без входов.
func TestDesignHasRenderForColorwayIsMembershipNotNonEmptiness(t *testing.T) {
	set := []int{0, 6}
	require.True(t, designHasRenderForColorway(set, 0), "легаси-рендеры открывают безколорвейное 3D")
	require.True(t, designHasRenderForColorway(set, 6))
	require.False(t, designHasRenderForColorway(set, 5),
		"рендер колорвея 6 не открывает 3D колорвея 5")
	require.False(t, designHasRenderForColorway(nil, 0),
		"карточка без рендеров не открывает и безколорвейное 3D")
}

// ВОРОТА В САМОМ ХЕНДЛЕРЕ: threed с колорвеем вне множества — FailedPrecondition ДО стора (деньги
// не резервируются), с колорвеем из множества — доезжает до StartRun, и колорвей доезжает с ним.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ ВТОРАЯ ПОЛОВИНА: потерять `ColorwayId: int(params.GetColorwayId())` при
// сборке entity.DesignRunStart — прогон завёлся бы БЕЗ колорвея, его кадры родились бы
// неатрибутированными, и весь конвейер «мультивью колорвея → сплит → верстак колорвея» умер бы
// на первом же звене, молча.
func TestStartDesignRunGatesThreedPerColorwayAndPassesItThrough(t *testing.T) {
	// ⚠ МНОЖЕСТВО ВЫВОДИТСЯ ИЗ ВЕРСТАКА, А НЕ ЗАДАЁТСЯ ЧИСЛОМ (N7). Первая редакция этой пробы
	// ставила `{0, 6}` поверх верстака, где ЗАНЯТЫ слоты колорвеев 0, 5 И 6, — то есть отрицательный
	// контроль «колорвею 5 отказано» был ЛОЖНО-ЗЕЛЁНЫМ: настоящий GetBand отдал бы 5, и гейт
	// пропустил бы его. Проба доказывала отказ на состоянии, которого стор не производит, — тот же
	// вид дефекта, который эта волна только что чинила в designBandWith.
	//
	// Чтобы случай «чужой колорвей» стал НАСТОЯЩИМ, его надо построить честно: колорвей 5
	// РАЗБИРАЮТ с верстака, и множество перестаёт его содержать само.
	band := designBandWith(true)
	band.Bench = designColorwayBenchWithout(5)
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	require.Equal(t, []int{0, 6}, band.RenderBenchColorways,
		"верстак обязан САМ давать {0,6}; заданное числом множество — ложная зелень")

	// Чужой колорвей — отказ до стора.
	rig := newDesignRunRig(t, designMoodCard(), band)
	req := designStartRequest(entity.DesignRunKindThreed)
	req.Params.ColorwayId = 5
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "no_fabric_render", md["reason"])
	require.Equal(t, "5", md["colorway_id"], "отказ называет колорвей, которому нечего рендерить")
	require.Nil(t, rig.sent, "отказ ворот не должен доходить до стора и резервировать деньги")

	// Свой колорвей — проходит, и колорвей доезжает до стора.
	rig2 := newDesignRunRig(t, designMoodCard(), band)
	req2 := designStartRequest(entity.DesignRunKindThreed)
	req2.Params.ColorwayId = 6
	_, err = rig2.srv.StartDesignRun(designRunCtx(), req2)
	require.NoError(t, err)
	require.NotNil(t, rig2.sent)
	require.Equal(t, 6, rig2.sent.ColorwayId, "колорвей прогона — из действующих params")
}

// РЕНДЕР-ПРОГОН НЕСЁТ КОЛОРВЕЙ В СТОР, а ворота 3D его не трогают (они только про threed).
func TestStartDesignRunCarriesTheRenderColorwayToTheStore(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ColorwayId = 7
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Equal(t, 7, rig.sent.ColorwayId)
}

// РЕРАН НАСЛЕДУЕТ КОЛОРВЕЙ ИЗ ЗАМОРОЖЕННЫХ params РОДИТЕЛЯ: клиент, не приславший params,
// повторяет рендер колорвея 6 КАК рендер колорвея 6 — не восстанавливая его руками.
func TestDesignRerunInheritsTheColorwayFromTheParentsFrozenParams(t *testing.T) {
	parent := &entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindRender,
		Params: entity.RawJSON(`{"colorway_id":6}`),
	}
	params, err := designEffectiveParams(nil, parent)
	require.NoError(t, err)
	require.Equal(t, int32(6), params.GetColorwayId(),
		"снимок params родителя — единственный источник колорвея рерана без параметров")
}

// ─────────────────────── адрес слота и загрузка ───────────────────────

// КОЛОРВЕЙ АДРЕСА ДОЕЗЖАЕТ ДО СТОРА. МУТАЦИЯ: убрать `ColorwayId: int(ref.GetColorwayId())` из
// ветки view_key — всякий адрес приезжал бы в безколорвейный верстак, и рендеры всех колорвеев
// ложились бы в один, ровно как до оси. Это дословно класс дефекта L-1 (поле есть, ярус его
// выбрасывает), пойманный на соседнем поле.
func TestDesignSlotRefCarriesTheColorway(t *testing.T) {
	ref, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot:       &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
		Kind:       entity.DesignPictureKindRender,
		ColorwayId: 5,
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignColorwayRef(5), ref.ColorwayId,
		"front колорвея A и front колорвея B — два разных слота; без колорвея адрес называет один")

	// ⚠ ПО id КОЛОРВЕЙ ТЕПЕРЬ ТОЖЕ ДОЕЗЖАЕТ (D2), И ЭТО ОТЛИЧИЕ ОТ РОДА. Выброшенный здесь, он
	// давал OK на просьбу, которой никто не исполнил: «положи в слот 12, он колорвея 5», где слот
	// 12 флэтовый либо стоит на другом колорвее. Рассудить противоречие есть чем — строка слота
	// лежит в базе, — и рассуживает его сторож стора; дело парсера в том, чтобы не потерять
	// сказанное по дороге.
	//
	// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть `entity.DesignSlotRef{SlotId: int(v.SlotId)}` без ColorwayId.
	byID, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot:       &pb_admin.DesignBenchSlotRef_SlotId{SlotId: 12},
		ColorwayId: 5,
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignColorwayRef(5), byID.ColorwayId,
		"названный колорвей обязан доехать до того, кто может его рассудить")
	require.True(t, byID.ColorwayId.Stated())

	// Молчание по-прежнему значит «не назвал» — сегодняшний клиент по id колорвея не шлёт.
	silent, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot: &pb_admin.DesignBenchSlotRef_SlotId{SlotId: 12},
	})
	require.NoError(t, err)
	require.False(t, silent.ColorwayId.Stated())

	// -1 — НАЗВАННЫЙ БЕЗКОЛОРВЕЙНЫЙ ВЕРСТАК, а не мусор: он адресует 0 и при этом СКАЗАН.
	unattributed, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot:       &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
		Kind:       entity.DesignPictureKindRender,
		ColorwayId: entity.DesignColorwayUnattributed,
	})
	require.NoError(t, err)
	require.True(t, unattributed.ColorwayId.Stated())
	require.Zero(t, unattributed.ColorwayId.Id(), "сентинел адресует ровно безколорвейный верстак")

	// А всё, что ниже сентинела, — отказ у двери.
	_, err = designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot:       &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
		ColorwayId: -2,
	})
	require.Error(t, err)
}

// КОЛОРВЕЙ ЗАГРУЖАЕМОГО ФАЙЛА ДОЕЗЖАЕТ ДО СТОРА — утверждение загружающего, как и род: из
// пикселей его не восстановить, а сторожа (ось рода, граница карточки) стоят в сторе.
func TestRegisterDesignUploadCarriesTheColorwayToTheStore(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "44444444-4444-4444-4444-444444444444",
		Items: []*pb_admin.DesignUploadItem{
			{MediaId: 501, Kind: entity.DesignPictureKindRender, ColorwayId: 5},
			{MediaId: 502, Kind: entity.DesignPictureKindRender},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Len(t, rig.sent.Items, 2)
	require.Equal(t, 5, rig.sent.Items[0].ColorwayId)
	require.Zero(t, rig.sent.Items[1].ColorwayId,
		"неназванный колорвей остаётся нулём: рендер файлится неатрибутированным, как до оси")
}

// ─────────────────────── чтение: верстак одного колорвея ───────────────────────

// ВЕРСТАК ОДНОГО КОЛОРВЕЯ = ФЛЭТЫ (они у всех колорвеев одни, L-4) ПЛЮС слоты именно этого
// колорвея. Неатрибутированный легаси-верстак в чужой колорвей НЕ входит: атрибуцию фильтром не
// выдумывают. 0 — не фильтр: полоса целиком, как её читает старый клиент.
func TestDesignBenchForColorwayServesOneColorwaysBench(t *testing.T) {
	bench := designColorwayBench()

	whole := designBenchForColorway(bench, 0)
	require.Len(t, whole, len(bench), "0 = не назван, полоса целиком")

	cw5 := designBenchForColorway(bench, 5)
	ids := make([]int, 0, len(cw5))
	for _, s := range cw5 {
		ids = append(ids, s.Id)
	}
	require.ElementsMatch(t, []int{1, 2, 3}, ids,
		"флэт (1) + два слота колорвея 5 (2,3); чужой колорвей (4) и легаси (5) не входят")
}

// ─────────────────────── DTO: колонка доезжает до провода ───────────────────────

// СЛОТ, КАРТИНКА И ПРОГОН НЕСУТ КОЛОРВЕЙ НА ПРОВОД; NULL читается нулём одним правилом на всех
// ярусах. МУТАЦИЯ: потерять присвоение ColorwayId в любом из трёх конвертеров — клиент не смог бы
// ни сгруппировать верстаки, ни нарисовать историю колорвея, и ни один round trip не покраснел бы.
func TestDesignDTOsCarryTheColorway(t *testing.T) {
	slot := designSlotToPb(benchCw(2, entity.DesignViewFront, 5, 11))
	require.Equal(t, int32(5), slot.GetColorwayId())
	require.Equal(t, int32(5), slot.GetPicture().GetColorwayId())

	legacy := designSlotToPb(benchCw(5, entity.DesignViewFront, 0, 14))
	require.Zero(t, legacy.GetColorwayId(), "NULL колонки — это 0 на проводе, не потеря поля")

	run := designRunToPb(designRunCtx(), entity.DesignRun{
		Id: 3, Kind: entity.DesignRunKindRender,
		ColorwayId: sql.NullInt32{Int32: 6, Valid: true},
	})
	require.Equal(t, int32(6), run.GetColorwayId())
}

// ─────────────────────── D3: безколорвейный верстак СЕЛЕКТИРУЕТСЯ ───────────────────────

// БЕЗКОЛОРВЕЙНЫЙ ВЕРСТАК — ЭТО ВЫБОР, А НЕ ОШИБКА И НЕ «ВСЁ».
//
// ЧТО БЫЛО СЛОМАНО: селектор считал 0 «фильтра нет», и назвать неатрибутированный верстак было
// НЕЧЕМ. Между тем он законный, вечный и выбираемый — 3D без колорвея читает ровно его, — так что
// пикер колорвеев на экране 3D не мог показать свою первую строку: «все» и «этот» отвечали одним
// и тем же множеством.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть `if colorwayID <= 0 { return in }` вместо `if !sel.Stated()`.
func TestDesignBenchForColorwayCanNameTheUnattributedBench(t *testing.T) {
	bench := designColorwayBench()

	unattributed := designBenchForColorway(bench, entity.DesignColorwayUnattributed)
	ids := make([]int, 0, len(unattributed))
	for _, s := range unattributed {
		ids = append(ids, s.Id)
	}
	require.ElementsMatch(t, []int{1, 5}, ids,
		"флэт (1) + неатрибутированный рендер-верстак (5); колорвеи 5 и 6 в него не входят")
	require.NotEqual(t, len(bench), len(unattributed),
		"назвать безколорвейный верстак обязано ОТЛИЧАТЬСЯ от «отдай всё» — иначе выбора нет")

	// И ноль по-прежнему значит «не назвал»: старый клиент получает полосу целиком.
	require.Len(t, designBenchForColorway(bench, 0), len(bench))
}

// ХЕНДЛЕР ПРИНИМАЕТ СЕНТИНЕЛ И ОТКАЗЫВАЕТ ВСЕМУ, ЧТО НИЖЕ НЕГО.
func TestGetDesignBandAcceptsTheUnattributedBenchSelector(t *testing.T) {
	band := designBandWith(true)
	band.Bench = designColorwayBench()
	// ВОСЬМОЙ СТЕНД ТОЙ ЖЕ ПОРОДЫ (T7): designBandWith считает множество по СВОЕМУ верстаку, а
	// строкой выше верстак заменён целиком — и полоса утверждала бы [0] над верстаком, из которого
	// стор посчитал бы [0 5 6]. Сегодня это инертно (проба смотрит только на id слотов и на отказ
	// −2), но поле уезжает на провод и его читают денежные ворота, поэтому любое будущее
	// утверждение здесь было бы зелено на непроизводимом состоянии. Оба соседних стенда
	// пересчитывают; этот был исключением.
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	// И ЭТО УТВЕРЖДАЕТСЯ, А НЕ ПРОСТО ДЕЛАЕТСЯ: без строки ниже пересчёт был бы косметикой,
	// которую следующая правка снимет молча, — а стенд снова начал бы служить на провод
	// множество, которого стор из этого верстака не получил бы.
	require.Equal(t, []int{0, 5, 6}, band.RenderBenchColorways,
		"множество обязано следовать за верстаком стенда, а не за тем, с чего стенд начинали")

	newSrv := func() *Server {
		repo := mocks.NewMockRepository(t)
		design := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(design).Maybe()
		design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
			Return(band, nil).Maybe()
		return &Server{repo: repo}
	}

	resp, err := newSrv().GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{
		TechCardId: designRunCardID, BenchColorwayId: entity.DesignColorwayUnattributed,
	})
	require.NoError(t, err, "-1 обязан быть законным селектором, а не отказом")
	got := make([]int32, 0, len(resp.GetBench()))
	for _, s := range resp.GetBench() {
		got = append(got, s.GetId())
	}
	require.ElementsMatch(t, []int32{1, 5}, got)

	_, err = newSrv().GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{
		TechCardId: designRunCardID, BenchColorwayId: -2,
	})
	require.Error(t, err, "ниже сентинела смысла нет — отказ у двери")
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
}

// ─────────────────────── D8: пустые детали тоже скоупятся колорвеем ───────────────────────

// ПУСТАЯ ДЕТАЛЬ — ТОЖЕ ВХОД, И ЕЙ ПОЛАГАЕТСЯ ТОТ ЖЕ КОЛОРВЕЙНЫЙ СКОУП, ЧТО ПЛИТАМ.
//
// ЧТО БЫЛО СЛОМАНО: designNamedEmptyDetailSlots строил свою карту по ВСЕМ detail-слотам карточки
// без единой проверки колорвея, поэтому 3D колорвея A могло вписать в свой ЗАМОРОЖЕННЫЙ снимок
// пустую деталь колорвея B — вход чужого верстака в оплаченном прогоне, и разобрать это потом
// нечем: снимок задним числом не чинят.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать проверку matchColorway из цикла designNamedEmptyDetailSlots.
func TestNamedEmptyDetailSlotsAreScopedToTheRunsColorway(t *testing.T) {
	detail := func(id, cw int, name string) entity.DesignBenchSlot {
		s := entity.DesignBenchSlot{
			Id: id, TechCardId: designRunCardID, ViewKey: entity.DesignViewDetail,
			Kind:       entity.DesignPictureKindRender,
			DetailName: sql.NullString{String: name, Valid: true},
		}
		if cw > 0 {
			s.ColorwayId = sql.NullInt32{Int32: int32(cw), Valid: true}
		}
		return s
	}
	bench := []entity.DesignBenchSlot{
		detail(31, 5, "collar A"),
		detail(32, 6, "collar B"),
		detail(33, 0, "collar legacy"),
	}

	named := func(kind string, cw int32) []int32 {
		out := designNamedEmptyDetailSlots(designInputSources{
			Kind:  kind,
			Bench: bench,
			Params: &pb_common.DesignRunParams{
				ColorwayId: cw, DetailSlotIds: []int32{31, 32, 33},
			},
		}, nil)
		ids := make([]int32, 0, len(out))
		for _, s := range out {
			ids = append(ids, s.GetSlotId())
		}
		return ids
	}

	require.Equal(t, []int32{31}, named(entity.DesignRunKindThreed, 5),
		"3D колорвея 5 не называет в своём снимке чужие и неатрибутированные детали")
	require.Equal(t, []int32{33}, named(entity.DesignRunKindThreed, 0),
		"безколорвейное 3D видит ровно неатрибутированный верстак")

	// А У РЕНДЕРА СКОУПА НЕТ И БЫТЬ НЕ ДОЛЖНО: он строится из ОДНОГО флэтового верстака, и
	// сузить его колорвеем значило бы отнять у него детали вовсе. Тот же довод, что у плит.
	require.Equal(t, []int32{31, 32, 33}, named(entity.DesignRunKindRender, 5),
		"рендер читает верстак без колорвейного сужения — правило одно на оба прохода")
}

// ─────────────────────── F3: контроль, которого не было ───────────────────────

// КАРТОЧКА С РЕНДЕРАМИ-КАДРАМИ, НО ПУСТЫМ РЕНДЕР-ВЕРСТАКОМ — ОТКАЗ.
//
// ЭТО ГЛАВНЫЙ НОВЫЙ СЛУЧАЙ D5, и до этой пробы у него не было контроля НИ ОДНОГО: старый гейт
// считал картинки, новый — занятые слоты, и расходятся они ровно здесь. Загрузили фабрик-рендер,
// не поставили ни на одну сторону — has_fabric_render говорит «да», а собирать прогону нечего.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть гейту `band.HasFabricRender` вместо членства в
// RenderBenchColorways — проба позеленеет на состоянии, за которое платят пустым прогоном.
func TestStartDesignRunRefusesThreedWhenRendersExistButTheBenchIsEmpty(t *testing.T) {
	band := designBandWith(false)
	// Кадры на карточке ЕСТЬ — старый флаг это и утверждает.
	band.HasFabricRender = true
	// А верстак пуст: ни одного занятого render-слота, значит и множество пусто.
	require.Empty(t, band.RenderBenchColorways,
		"стенд обязан выводить множество из верстака, иначе он снова начнёт врать")

	rig := newDesignRunRig(t, designMoodCard(), band)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "no_fabric_render", md["reason"])
	require.Nil(t, rig.sent,
		"прогон без единого входа не должен доходить до стора и занимать деньги дня")
}

// И ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ К НЕМУ: поставленная плита открывает дверь И ДОЕЗЖАЕТ ДО СНИМКА.
// Без второй половины «дверь открылась» доказывало бы только то, что гейт не вечен.
func TestStartDesignRunAllowsThreedWhenTheRenderBenchIsOccupied(t *testing.T) {
	band := designBandWith(true)
	require.Equal(t, []int{0}, band.RenderBenchColorways)

	rig := newDesignRunRig(t, designMoodCard(), band)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Contains(t, string(rig.sent.Inputs), `"media_id":210`,
		"дверь открыл занятый слот — он же обязан оказаться входом прогона")
}

// ─────────────────────── F2: реран переживает удаление колорвея ───────────────────────

// УНАСЛЕДОВАННЫЙ КОЛОРВЕЙ ПРОВЕРЯЕТСЯ НЕ ТАК, КАК НАЗВАННЫЙ.
//
// ЧТО БЫЛО СЛОМАНО: реран без параметров наследует колорвей из ЗАМОРОЖЕННЫХ params родителя, и
// стор проверял его строго. Колорвей законно удаляют — FK гасит колонку родителя, а params
// по-прежнему называют id, — и такой прогон становился НЕПОВТОРИМЫМ НАВСЕГДА: клиент не присылал
// ни params, ни колорвея, а получал `foreign_colorway`, и написать иначе было нечего. Соседние
// два сторожа (детали и полки) отказались от ровно этого дословно теми же словами.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: захардкодить `ColorwayStated: true` — стор снова начал бы проверять
// унаследованное строго.
func TestRerunWithoutParamsDoesNotVouchForTheInheritedColorway(t *testing.T) {
	parent := &entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindRender,
		Params: entity.RawJSON(`{"colorway_id":7}`),
	}

	// Реран БЕЗ параметров: колорвей унаследован, и за него никто не ручается.
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(parent, nil).Maybe()
	req := designStartRequest(entity.DesignRunKindRender)
	req.Params = nil
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Equal(t, 7, rig.sent.ColorwayId, "колорвей всё равно наследуется — теряется не он, а порука")
	require.False(t, rig.sent.ColorwayStated,
		"за унаследованный колорвей вызывающий не ручается: он его не называл")

	// А НАЗВАННЫЙ КЛИЕНТОМ — ручается, и стор проверит его строго.
	rig2 := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	spoken := designStartRequest(entity.DesignRunKindRender)
	spoken.Params.ColorwayId = 7
	_, err = rig2.srv.StartDesignRun(designRunCtx(), spoken)
	require.NoError(t, err)
	require.True(t, rig2.sent.ColorwayStated)
}

// ─────────────────────── D1 на API-ЯРУСЕ: отказ доезжает до клиента ───────────────────────

// СТОРОЖ ПОЛОСЫ ВИДЕН СНАРУЖИ КАК FailedPrecondition, А НЕ КАК Internal 500.
//
// Проба стора доказывает, что отказ РОЖДАЕТСЯ; эта — что он ДОЕЗЖАЕТ. Без ветки в switch'е
// хендлера новый сентинел упал бы в `default` и вышел бы к оператору пятисоткой без единой
// подсказки, что именно держит колорвей, — то есть сторож работал бы и был бы бесполезен.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать case entity.ErrColorwayHasDesignRows из RelinkDraftColorway.
func TestRelinkDraftColorwaySurfacesTheDesignBandRefusal(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	repo.EXPECT().Products().Return(products).Maybe()
	products.EXPECT().RelinkDraftColorway(mock.Anything, 7, 42, 1, 1).
		Return(fmt.Errorf("%w: 4 design bench slot row(s) of the source style name colourway 7; "+
			"clear the design band of that colourway first", entity.ErrColorwayHasDesignRows)).Once()

	srv := &Server{repo: repo}
	_, err := srv.RelinkDraftColorway(designRunCtx(), &pb_admin.RelinkDraftColorwayRequest{
		ColorwayId: 7, TargetStyleId: 42,
		ExpectedColorwayVersion: 1, ExpectedTargetStyleVersion: 1,
	})
	require.Error(t, err)
	st := status.Convert(err)
	require.Equal(t, codes.FailedPrecondition, st.Code(),
		"это поправимое состояние, а не сбой сервера")
	require.Contains(t, st.Message(), "design bench slot",
		"сообщение обязано назвать, ЧТО держит колорвей: без этого оператору нечего открыть")
}

// ─────────────────────── N5: названный адрес отвергается ТАМ, ГДЕ СКАЗАЛИ ───────────────────────

// ЧУЖАЯ ДЕТАЛЬ И ЧУЖАЯ ПЛИТА — ОТКАЗ У ДВЕРИ, А НЕ ТИХИЙ ВЫБРОС ПРИ ОТБОРЕ.
//
// ЧТО БЫЛО СЛОМАНО: дверь проверяла только карточку и `view_key=detail`, а отбор сужал верстак ещё
// и колорвеем — значит адрес чужого колорвея ПРИНИМАЛСЯ и молча выпадал из снимка. Клиент получал
// OK на просьбу, которую никто не исполнил, и платил за прогон без названной им детали.
//
// `fix_slot_ids` был хуже: проверки принадлежности не было вовсе, а он СУЖАЕТ отбор. Денежные
// ворота смотрят на верстак колорвея целиком, поэтому одного занятого слота хватало, чтобы дверь
// открылась, — и выборочный прогон, назвавший только чужой адрес, уезжал оплаченным и пустым.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять колорвейный предикат из designRefuseForeignDetailSlots (первая
// половина); убрать вызов designRefuseForeignFixSlots (вторая).
func TestStartDesignRunRefusesSlotsItWouldNotHonour(t *testing.T) {
	band := designBandWith(true)
	band.Bench = designColorwayBench()
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	// Деталь колорвея 6 — законный слот карточки, но НЕ читаемый прогоном колорвея 5.
	band.Bench = append(band.Bench, entity.DesignBenchSlot{
		Id: 40, TechCardId: designRunCardID, ViewKey: entity.DesignViewDetail,
		Kind:       entity.DesignPictureKindRender,
		ColorwayId: sql.NullInt32{Int32: 6, Valid: true},
		DetailName: sql.NullString{String: "collar", Valid: true},
	})

	start := func(mutate func(*pb_admin.StartDesignRunRequest)) (*designRunRig, error) {
		rig := newDesignRunRig(t, designMoodCard(), band)
		req := designStartRequest(entity.DesignRunKindThreed)
		req.Params.ColorwayId = 5
		mutate(req)
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		return rig, err
	}

	// (1) ДЕТАЛЬ ЧУЖОГО КОЛОРВЕЯ.
	//
	// ⚠ `views` ОБЯЗАН НАЗВАТЬ ДЕТАЛЬ. Первая редакция этой пробы его не называла, и запрос падал
	// РАНЬШЕ — на правиле «сколько detail-видов, столько и адресов», то есть проба была зелена по
	// чужой причине и мутация двери её пережила. Мутационный прогон это и показал; предикат
	// доказывает только та проба, которая доходит до него.
	rig, err := start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.Views = []string{entity.DesignViewDetail}
		r.Params.DetailSlotIds = []int32{40}
	})
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Nil(t, rig.sent, "отказ обязан прийти ДО стора: иначе деньги уже зарезервированы")

	// (2) fix_slot_ids, НАЗЫВАЮЩИЙ ТО, ЧЕГО ПРОГОН НЕ ПРОЧТЁТ. Слот 4 — рендер колорвея 6.
	rig, err = start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.FixSlotIds = []int32{4}
	})
	require.Error(t, err)
	code, _ = errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Nil(t, rig.sent, "прогон, сузившийся до нечитаемого адреса, был бы оплачен и пуст")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: СВОЙ адрес проходит. Без него оба отказа доказывали бы только то,
	// что дверь закрыта всегда.
	rig, err = start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.FixSlotIds = []int32{2} // render/front колорвея 5
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}

// ─────────────────────── T1: сужение ПО ВИДУ судится той же дверью ───────────────────────

// НАЗВАТЬ ВИД — ТО ЖЕ, ЧТО НАЗВАТЬ АДРЕС, И ДВЕРЬ ОБЯЗАНА СУДИТЬ ОБА.
//
// ЧТО БЫЛО СЛОМАНО: дверь выходила первой строкой, если `fix_slot_ids` пуст, а `fix_targets`
// проверялся только на форму. После колорвейного сужения это стало ДОСТИЖИМО: до волны `side_l`
// совпадал с side_l любого колорвея, теперь — только своего.
//
// ЧЕМ ПЛАТИЛОСЬ: у поставщика turntable нижний порог в ОДИН кадр, поэтому прогон не падал, а молча
// строился из меньшего числа сторон, чем выбрал человек. Это хуже пустого прогона: пустой заметен.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть ранний выход `if len(ids) == 0 { return nil }`.
func TestStartDesignRunRefusesFixTargetsItWouldNotHonour(t *testing.T) {
	// Верстак колорвея 5 держит front и back; side_l есть ТОЛЬКО у колорвея 6.
	bench := designColorwayBench()
	bench = append(bench, benchCw(7, entity.DesignViewSideL, 6, 15))
	band := designBandWith(true)
	band.Bench = bench
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)

	start := func(mutate func(*pb_admin.StartDesignRunRequest)) (*designRunRig, error) {
		rig := newDesignRunRig(t, designMoodCard(), band)
		req := designStartRequest(entity.DesignRunKindThreed)
		req.Params.ColorwayId = 5
		mutate(req)
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		return rig, err
	}

	// ВЫБОР front + side_l ДЛЯ КОЛОРВЕЯ 5: side_l принадлежит чужому цвету.
	rig, err := start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.FixTargets = []string{entity.DesignViewFront, entity.DesignViewSideL}
	})
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, status.Convert(err).Message(), "colourway 5",
		"сообщение обязано назвать ЦВЕТ: «нет рендера на side_l» на карточке, где side_l виден, "+
			"не подсказывает ни одного следующего шага")
	require.Nil(t, rig.sent, "молча урезанный прогон не должен доходить до стора и до денег")

	// СКАЛЯР `fix_target` СУДИТСЯ ТЕМ ЖЕ ПРАВИЛОМ — отбор читает его по тому же падению.
	rig, err = start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.FixTarget = entity.DesignViewSideL
	})
	require.Error(t, err)
	require.Nil(t, rig.sent)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: СВОИ стороны проходят, и прогон уезжает.
	rig, err = start(func(r *pb_admin.StartDesignRunRequest) {
		r.Params.FixTargets = []string{entity.DesignViewFront, entity.DesignViewBack}
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}

// ─────────────────────── T2: реран с параметрами не теряет колорвей ───────────────────────

// ЛОВУШКА PROTO3-СКАЛЯРА, ПРИМЕНЁННАЯ К ТОМУ ЖЕ ПОЛЮ В ДРУГОМ СООБЩЕНИИ.
//
// ЧТО БЫЛО СЛОМАНО: наследование родительских params — ОПТОМ ИЛИ НИКАК. Клиент, приславший params
// по НЕСВЯЗАННОЙ причине (правка `ask`), доставлял colorway_id = 0, и «реран наследует колорвей»
// переставало быть правдой. Ворота при этом проверяли членство НУЛЯ — на карточке с легаси-
// рендерами он есть, значит открывались, — входы копировались из снимка родителя (плиты цвета 5),
// строка писалась NULL, а кадры рождались неатрибутированными и в threed-верстак цвета 5 их уже не
// поставить. Деньги потрачены, результат некуда положить.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать поштучное наследование colorway_id из designEffectiveParams.
func TestRerunWithUnrelatedParamsKeepsTheParentsColorway(t *testing.T) {
	parent := &entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Kind: entity.DesignRunKindThreed,
		Params: entity.RawJSON(`{"colorway_id":5,"views":["front"],"layout":"per_view"}`),
	}

	// Клиент прислал params ради `ask` и колорвея не называл: наследуется родительский.
	spoken := &pb_common.DesignRunParams{Views: []string{entity.DesignViewFront}, Layout: designLayoutPerView}
	params, err := designEffectiveParams(spoken, parent)
	require.NoError(t, err)
	require.Equal(t, int32(5), params.GetColorwayId(),
		"proto3-ноль от клиента, который поля не знает, не должен читаться как «сними колорвей»")

	// НАЗВАННЫЙ КЛИЕНТОМ КОЛОРВЕЙ ПОБЕЖДАЕТ РОДИТЕЛЬСКИЙ — наследование не затирает просьбу.
	spoken.ColorwayId = 6
	params, err = designEffectiveParams(spoken, parent)
	require.NoError(t, err)
	require.Equal(t, int32(6), params.GetColorwayId())

	// БЕЗ РОДИТЕЛЯ НИЧЕГО НЕ ВЫДУМЫВАЕТСЯ.
	params, err = designEffectiveParams(&pb_common.DesignRunParams{Layout: designLayoutPerView}, nil)
	require.NoError(t, err)
	require.Zero(t, params.GetColorwayId())
}
