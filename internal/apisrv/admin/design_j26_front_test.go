package admin

import (
	"database/sql"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ J-26: 3D СТРОИТСЯ ИЗ СЛОТОВ ФАБРИК-РЕНДЕРА, И ПЕРЁД — ТА СТОРОНА, БЕЗ КОТОРОЙ НЕЛЬЗЯ ═══════
//
// ЧТО БЫЛО СЛОМАНО. Ворота 3D (`no_fabric_render`) спрашивают МНОЖЕСТВО КОЛОРВЕЕВ С ЗАНЯТЫМ
// РЕНДЕР-ВЕРСТАКОМ (`designRenderBenchColorways`: DISTINCT по колорвею занятых render-слотов). Это
// вопрос «занят ли верстак хоть чем-нибудь», и он не различает СТОРОН. Верстак, на котором стоит
// одна СПИНА, проходил их насквозь: строка заводилась, резерв дня занимался, и через тик воркер
// отказывал у самой двери провайдера — `threedPictures` без переда возвращает пустой список, а
// маршрут отвечает `meshy.ErrImageCount` / `fal.ErrNoFrontView`. Оба Retryable=false, то есть
// провайдеру не платят; платит за это ЧЕСТНОСТЬ ИСТОРИИ — `failed` в платной ленте и занятый
// резерв за просьбу, которую можно было отклонить бесплатно и словами.
//
// ЧЕМ ЭТО СТАЛО ВАЖНЕЕ ИМЕННО В КРУГЕ 15. J-26 сжимает вкладку 3D до ЧЕТЫРЁХ ЯЧЕЕК верстака и
// снимает с неё всё остальное; J-27 убирает оттуда и поле колорвея. Единственное, что человек там
// делает, — смотрит на четыре стороны. Значит отказ обязан говорить про СТОРОНУ, а не про
// «карточку без рендеров», и обязан приходить до денег.
//
// ⚠ ЧЕГО ЭТИ ПРОБЫ НЕ УТВЕРЖДАЮТ. Не «списание предотвращено»: провайдера без переда не зовут ни
// на одном из двух маршрутов, и списания не было и раньше. Утверждается ровно измеримое —
// `Design().StartRun` НЕ ВЫЗЫВАЕТСЯ, то есть нет ни строки, ни резерва (`moveBudgetDay` живёт
// внутри той же транзакции, что вставка).

// designBandWithRenderSide — полоса, у которой рендер-верстак занят РОВНО одной названной стороной.
//
// Множество RenderBenchColorways выводится тем же правилом, что в сторе
// (`designRenderBenchColorwaysOf`), поэтому стенд НЕ УМЕЕТ соврать в ту сторону, в которую соврал
// однажды: «занятый верстак» здесь всегда следствие занятого слота, а не приписанное число.
func designBandWithRenderSide(view string) *entity.DesignBand {
	band := designBandWith(false)
	band.Bench = append(band.Bench, entity.DesignBenchSlot{
		Id:         6,
		TechCardId: designRunCardID,
		ViewKey:    view,
		Kind:       entity.DesignPictureKindRender,
		DetailName: sql.NullString{String: "cuff", Valid: view == entity.DesignViewDetail},
		PictureId:  sql.NullInt32{Int32: 78, Valid: true},
		Picture: &entity.DesignPicture{
			Id: 78, TechCardId: designRunCardID, MediaId: designRenderPlateMediaID,
			Kind: entity.DesignPictureKindRender,
		},
	})
	band.HasFabricRender = true
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	return band
}

// designRunRigNoStartRun — стенд без заглушки StartRun И С НАЗВАННОЙ ПОЛОСОЙ.
//
// ⚠ ПОЧЕМУ НЕ newDesignRunRigWithoutStartRun С ДОБАВЛЕННЫМ EXPECT. Тот стенд уже вешает
// `GetBand … Maybe()` с полосой designBandWith(true) — то есть С ПЕРЕДОМ. Второе ожидание на тот же
// вызов не заменяет первое: mockery отдаёт ПЕРВОЕ подходящее, и проба «спина без переда» тихо
// исполнялась бы на верстаке с передом. Так и случилось при первом прогоне этого файла: три пробы
// упали неожиданным StartRun — то есть на самом деле они мерили не тот вход. Стенд обязан называть
// свою полосу ровно один раз.
func designRunRigNoStartRun(t *testing.T, band *entity.DesignBand) *designRunRig {
	t.Helper()
	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).
		Return(designMoodCard(), nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
		Return(band, nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true}
	return rig
}

// TestThreedWithOnlyABackRenderIsRefusedBeforeTheReserve — ГЛАВНАЯ ПРОБА ЭТОЙ ВОЛНЫ.
//
// Стенд БЕЗ заглушки StartRun: любое обращение к нему роняет пробу по имени mockery. Это и есть
// измерение «до денег» — не отсутствие проверки, а ДОКАЗАННОЕ ОТСУТСТВИЕ ВЫЗОВА.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСИТ:
//   - снять вызов `designRefuseThreedWithoutFront` из StartDesignRun;
//   - перенести его ПОСЛЕ `s.repo.Design().StartRun` (тогда падает неожиданный вызов стора —
//     ровно то, что проба и охраняет);
//   - заменить в нём `entity.DesignViewFront` на любую другую сторону.
func TestThreedWithOnlyABackRenderIsRefusedBeforeTheReserve(t *testing.T) {
	rig := designRunRigNoStartRun(t, designBandWithRenderSide(entity.DesignViewBack))

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err, "спина одна не строит поворотный стол: провайдер читает первую картинку как ПЕРЁД")
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code,
		"просьба правильная — верстак не готов; это не InvalidArgument")
	require.Equal(t, entity.DesignErrorCodeNoFrontRender, md["reason"],
		"экран обязан отличать «на верстаке пусто» от «есть, но не спереди»: это две разные следующие двери")
	require.Equal(t, entity.DesignViewFront, md["view_key"],
		"отказ обязан назвать сторону, которую от человека ждут")
	require.Contains(t, err.Error(), "Nothing was reserved")
	require.Nil(t, rig.sent, "строка прогона не заводится, значит и резерв дня не двигается")
}

// TestThreedWithOnlyADetailRenderIsRefusedToo — ДЕТАЛЬ НЕ СТОРОНА.
//
// Рендер манжеты — законная плита верстака: он занимает колорвей в множестве ворот
// (`no_fabric_render` пропускает такую карточку) и едет в снимок как слот со своим slot_id. Тем не
// менее вид изделия он не образует, и 3D на таком верстаке обязано быть отвергнуто.
//
// ⚠ ЧЕСТНО ПРО ИГЛУ. Отдельного силуэтного сторожа в правиле НЕТ — замер показал, что он инертен
// (см. комментарий у designRefuseThreedWithoutFront). Эта проба стережёт ПОВЕДЕНИЕ, а не строку, и
// краснеет от тех же мутаций, что проба выше: снятия вызова и подмены стороны. Держится она
// потому, что «верстак занят ДЕТАЛЬЮ» — отдельный достижимый вход, а не пересказ «верстак занят
// спиной»: у него другой путь в designSelectBench (слот детали несёт slot_id и имя).
func TestThreedWithOnlyADetailRenderIsRefusedToo(t *testing.T) {
	rig := designRunRigNoStartRun(t, designBandWithRenderSide(entity.DesignViewDetail))

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, entity.DesignErrorCodeNoFrontRender, md["reason"])
	require.Nil(t, rig.sent)
}

// TestThreedWithAFrontRenderStillReachesTheStore — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ.
//
// Без него обе пробы выше зеленели бы и на «отказывать всякому 3D», то есть на починке, которая
// убивает функцию. Проверяется не только отсутствие ошибки, но и что снимок ВЕЗЁТ передний кадр:
// иначе дверь могла бы пропускать прогон, чьи входы пусты.
func TestThreedWithAFrontRenderStillReachesTheStore(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithRenderSide(entity.DesignViewFront))

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "верстак с передом обязан дойти до стора")

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Len(t, snap.GetSlots(), 1)
	require.Equal(t, entity.DesignViewFront, snap.GetSlots()[0].GetViewKey())
	require.Equal(t, int32(designRenderPlateMediaID), snap.GetSlots()[0].GetMediaId())
}

// TestTheEmptyRenderBenchStillAnswersWithItsOwnRefusal — ДВА ОТКАЗА, А НЕ ОДИН.
//
// Ворота `no_fabric_render` остаются на месте и отвечают ПЕРВЫМИ на пустом верстаке: у них другой
// смысл («рендерить нечего») и другой следующий жест человека («сделай рендер»), чем у нового
// («положи кадр во ФРОНТ»). Проба стережёт ИМЕННО РАЗЛИЧЕНИЕ: сведение двух причин к одной
// зеленело бы у соседей и молча стирало бы половину смысла экрана.
func TestTheEmptyRenderBenchStillAnswersWithItsOwnRefusal(t *testing.T) {
	rig := designRunRigNoStartRun(t, designBandWith(false))

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, "no_fabric_render", md["reason"],
		"пустой верстак обязан остаться пустым верстаком, а не превратиться в «нет переда»")
	require.Nil(t, rig.sent)
}

// TestANonThreedRunIsUntouchedByTheFrontRule — ГРАНИЦА ПРАВИЛА.
//
// Рендер собирается из ФЛЭТОВ и переда среди них не требует вовсе; правило, расползшееся на
// соседний род, запрещало бы законный прогон и стоило бы владельцу ночи. Стенд отдаёт полосу, у
// которой рендер-верстак занят только спиной, — то есть ровно тот вход, на котором 3D отказано.
func TestANonThreedRunIsUntouchedByTheFrontRule(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithRenderSide(entity.DesignViewBack))

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))
	require.NoError(t, err, "рендеру перед в РЕНДЕР-верстаке не нужен: он строится из флэтов")
	require.NotNil(t, rig.sent)
}

// TestARerunIsJudgedByTheFROZEN_SNAPSHOT_NOT_TODAYS_BENCH — ГДЕ ИМЕННО СПРАШИВАЕТСЯ ПЕРЁД.
//
// Реран переписывает входы со строки родителя ЦЕЛИКОМ и из сегодняшнего верстака не берёт ни поля
// (`designRunInputs`). Проверка, заданная верстаку, а не снимку, сделала бы такой повтор
// НЕВОЗМОЖНЫМ НАВСЕГДА, стоило человеку снять кадр со стороны, — ровно тот класс вечного отказа,
// от которого дверь уже отказывалась у деталей и у полок.
//
// Здесь сегодняшний рендер-верстак занят СПИНОЙ, а снимок родителя несёт ПЕРЁД: повтор законен.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: перенести проверку выше `designRunInputs` и задать её `band.Bench`.
func TestARerunIsJudgedByTheFROZEN_SNAPSHOT_NOT_TODAYS_BENCH(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithRenderSide(entity.DesignViewBack))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindThreed,
		Inputs: entity.RawJSON(`{"slots":[{"view_key":"front","media_id":601}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindThreed)
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err, "повтор шлёт модели ТО ЖЕ САМОЕ; сегодняшний верстак ему не судья")
	require.NotNil(t, rig.sent)
}

// TestARerunWhoseFrozenFrontHasNoMediaIsRefused — ЗАЧЕМ В ПРАВИЛЕ СТОИТ `media_id > 0`.
//
// На свежем прогоне этот сторож недостижим: `designSelectBench` не кладёт в снимок слот без
// картинки, а единственный писатель безкартиночных слотов (`designNamedEmptyDetailSlots`) пишет
// только детали, которых силуэтный сторож и так не пускает. Достижим он ровно здесь — снимок
// РОДИТЕЛЯ заморожен, его никто задним числом не чинит, и строка `{"view_key":"front"}` без медиа
// в нём законна. Воркер такую сторону не считает передом (`threedPictures` требует MediaID > 0), и
// дверь обязана считать так же — иначе они расходятся на прогоне, который дверь пропустила, а
// провайдер отверг.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: снять `sl.GetMediaId() <= 0` из designRefuseThreedWithoutFront.
// ИГЛА УНИКАЛЬНА: все остальные пробы этого файла зелены без неё.
func TestARerunWhoseFrozenFrontHasNoMediaIsRefused(t *testing.T) {
	rig := designRunRigNoStartRun(t, designBandWithRenderSide(entity.DesignViewBack))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindThreed,
		Inputs: entity.RawJSON(`{"slots":[{"view_key":"front"}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindThreed)
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err, "сторона без файла — не сторона: провайдеру нечего показать")
	_, md := errorReason(t, err)
	require.Equal(t, entity.DesignErrorCodeNoFrontRender, md["reason"])
	require.Nil(t, rig.sent)
}

// ═══ J-25/J-26: ШТАМП ПРОГОНА У ПЛИТЫ СЛОТА ══════════════════════════════════════════════════
//
// Проба ЯРУСА ПЕРЕВОДА. Полный путь (строка прогона → слот → провод) стережёт
// TestDesignDBBenchSlotCarriesTheRunStampOfItsPlate в store/design; здесь — то, что перевод не
// теряет и не выдумывает.

// TestABenchSlotCarriesTheRunStampOnlyWithItsPlate.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСИТ: снять `out.RunRrev` или `out.RunKind` из designSlotToPb; вынести их
// из-под `if s.Picture != nil` (тогда пустая сторона начинает утверждать ревизию, которой у неё
// нет, и вторая половина пробы краснеет).
func TestABenchSlotCarriesTheRunStampOnlyWithItsPlate(t *testing.T) {
	filled := designSlotToPb(entity.DesignBenchSlot{
		Id: 6, TechCardId: designRunCardID, ViewKey: entity.DesignViewFront,
		Kind:      entity.DesignPictureKindRender,
		PictureId: sql.NullInt32{Int32: 78, Valid: true},
		Picture: &entity.DesignPicture{
			Id: 78, TechCardId: designRunCardID, MediaId: designRenderPlateMediaID,
			Kind: entity.DesignPictureKindRender,
		},
		RunKind: entity.DesignRunKindRender,
		RunRrev: 7,
	})
	require.Equal(t, entity.DesignRunKindRender, filled.GetRunKind(),
		"без рода прогона перекрашенный кадр на рендер-верстаке неотличим от фабрик-рендера")
	require.Equal(t, int32(7), filled.GetRunRrev(),
		"без ревизии отказ «four sides of ONE revision» нечем сосчитать")

	empty := designSlotToPb(entity.DesignBenchSlot{
		Id: 7, TechCardId: designRunCardID, ViewKey: entity.DesignViewBack,
		Kind:    entity.DesignPictureKindRender,
		RunKind: entity.DesignRunKindRender,
		RunRrev: 7,
	})
	require.Empty(t, empty.GetRunKind(), "у пустой стороны нет прогона ни в каком смысле")
	require.Zero(t, empty.GetRunRrev())
}
