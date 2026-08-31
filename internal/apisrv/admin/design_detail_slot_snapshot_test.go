package admin

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ПУСТАЯ ДЕТАЛЬ — ЭТО ГЛАВНЫЙ СЛУЧАЙ ФИЧИ, А НЕ КРАЕВОЙ.
//
// Галку «нарисуй воротник» ставят ровно тогда, когда воротника ещё НЕТ: слот заведён, назван и
// пуст. Отбор плит верстака (designSelectBench) такой слот выбрасывал — у него нет картинки, —
// и потому в снимок он не попадал. А единственный читатель имени (designgen: `detail_slot_ids` →
// `slots[*].slot_id` → `detail_name`) кроме снимка имени взять неоткуда. Промпт говорил
// «draw these details: detail».
//
// ⚠ ПРОБА ИДЁТ ЧЕРЕЗ НАСТОЯЩИЙ ХЕНДЛЕР, А НЕ ЧЕРЕЗ designAssembleInputs. Ровно этой болезнью
// болели пробы волны: они собирали `runInputs` руками, проставляя slot_id, которого сборка не
// проставляла НИКОГДА, — и были зелены при полностью нерабочей фиче.

func TestStartDesignRunFreezesTheNameOfAnEmptyDetailSlot(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithEmptyDetails())
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail, entity.DesignViewDetail}
	req.Params.Layout = designLayoutOne
	req.Params.DetailSlotIds = []int32{17, 18}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))

	bySlot := map[int32]*pb_common.DesignInputSlot{}
	for _, s := range snap.GetSlots() {
		if s.GetSlotId() > 0 {
			bySlot[s.GetSlotId()] = s
		}
	}
	require.Contains(t, bySlot, int32(17), "слот, который ПРОСЯТ нарисовать, обязан быть в снимке")
	require.Contains(t, bySlot, int32(18))
	require.Equal(t, "collar", bySlot[17].GetDetailName())
	require.Equal(t, "patch pocket", bySlot[18].GetDetailName())
	require.Zero(t, bySlot[17].GetMediaId(),
		"в слоте нет картинки — её как раз и заказывают; нулевой media_id здесь правда, а не пропуск")
	require.Zero(t, bySlot[18].GetMediaId())

	// ── положительный контроль: плита с картинкой на месте и не потеряна ──
	plates := 0
	for _, s := range snap.GetSlots() {
		if s.GetMediaId() > 0 {
			plates++
		}
	}
	require.Equal(t, 1, plates,
		"запись-имя добавляется К плитам, а не вместо них: иначе проба зеленела бы на пустом снимке")
}

// ПОДПИСИ КАРТИНОК НЕ СДВИГАЮТСЯ ОТ ЗАПИСИ БЕЗ КАРТИНКИ.
//
// Это второе утверждение фикса и оно умеет быть ложным независимо от первого: подписи референсов
// нумерованы («- image 3: …»), и лишняя строка в списке картинок увела бы каждую следующую подпись
// на соседний снимок. Проверяется не словами, а составом: медиа-половина снимка обязана остаться
// той же, что и у прогона БЕЗ пустых деталей.
func TestDesignEmptyDetailRecordIsNotAPicture(t *testing.T) {
	withDetails := func(ids []int32, views []string) []int32 {
		rig := newDesignRunRig(t, designMoodCard(), designBandWithEmptyDetails())
		req := designStartRequest(entity.DesignRunKindFlat)
		req.Params.Views = views
		req.Params.Layout = designLayoutOne
		req.Params.DetailSlotIds = ids
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		require.NoError(t, err)
		snap := &pb_common.DesignInputSnapshot{}
		require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
		out := []int32{}
		for _, s := range snap.GetSlots() {
			if id := s.GetMediaId(); id > 0 {
				out = append(out, id)
			}
		}
		for _, r := range snap.GetRefs() {
			out = append(out, r.GetMediaId())
		}
		return out
	}
	bare := withDetails(nil, []string{entity.DesignViewFront})
	asked := withDetails([]int32{17, 18},
		[]string{entity.DesignViewDetail, entity.DesignViewDetail})
	require.Equal(t, bare, asked,
		"картинки прогона обязаны совпасть до элемента: запись без media_id не картинка")
}

// ─────────────────── РЕРАН ПОСЛЕ УДАЛЕНИЯ СЛОТА ───────────────────

// ПРОГОН НЕ СТАНОВИТСЯ НЕПЕРЕЗАПУСКАЕМЫМ ОТ ТОГО, ЧТО СЛОТ УДАЛИЛИ.
//
// Пустой detail-слот законно удаляется (DeleteDesignDetailSlot). Карточная проверка адреса стояла
// БЕЗУСЛОВНО и била по УНАСЛЕДОВАННОМУ списку — тому, которого клиент не присылал. Ответ был
// «params.detail_slot_ids.0 17 is not a detail slot of tech card 41» на запрос, где нет ни поля
// `params`, и он был ВЕЧНЫМ: снимок задним числом не чинят.
func TestDesignRerunSurvivesTheDeletionOfTheDetailSlot(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true)) // ВЕРСТАК БЕЗ СЛОТА 17
	rig.design.EXPECT().GetRun(mock.Anything, 7).Return(&entity.DesignRun{
		Id: 7, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["detail"],"layout":"one","detail_slot_ids":[17]}`),
		Inputs: entity.RawJSON(`{"garment_note":"a shirt","slots":[` +
			`{"view_key":"detail","slot_id":17,"detail_name":"collar"}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = nil // КЛИЕНТ МОЛЧИТ: «повтори то же самое»
	req.RerunOfRunId = 7

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err,
		"молчащему клиенту нельзя отказывать полем, которого он не присылал")
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []int32{17}, stored.GetDetailSlotIds(),
		"унаследованная просьба едет как есть: вычистить из неё «уже не существующий» адрес "+
			"значило бы сдвинуть позиционное соответствие с views")

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Len(t, snap.GetSlots(), 1)
	require.Equal(t, "collar", snap.GetSlots()[0].GetDetailName(),
		"деградация ЧЕСТНАЯ: имя удалённой детали живо в снимке родителя, и промпт назовёт её им")
}

// ...И ТОТ ЖЕ АДРЕС ОТ ГОВОРЯЩЕГО КЛИЕНТА ПО-ПРЕЖНЕМУ ОТВЕРГАЕТСЯ.
//
// Без этой половины предыдущая проба зеленела бы и на полностью снесённой проверке. Правило
// связывает того, кто ГОВОРИТ: назвал адрес — отвечай за него.
func TestDesignRerunRestatingAForeignDetailSlotIsRefused(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 7).Return(&entity.DesignRun{
		Id: 7, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["detail"],"layout":"one","detail_slot_ids":[17]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail}
	req.Params.DetailSlotIds = []int32{17} // КЛИЕНТ ПОВТОРИЛ АДРЕС САМ
	req.RerunOfRunId = 7

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids.0 17")
	require.Nil(t, rig.sent)
}

// РЕРАН, ПОСТАВИВШИЙ ГАЛКУ НА ДРУГУЮ ДЕТАЛЬ, ТОЖЕ ПОЛУЧАЕТ ИМЯ.
//
// Контракт разрешает менять просьбу на реране («`ask` and `params` still apply ON TOP»), а снимок
// родителя про новую деталь не знает ничего. Без дополнения снимка починка MAJOR-1 работала бы
// ровно на половине входов: свежий прогон называет деталь, повторение с новой галкой — нет.
//
// И ВТОРАЯ ПОЛОВИНА УТВЕРЖДЕНИЯ: имя, УЖЕ замороженное родителем, не переписывается сегодняшним.
func TestDesignRerunThatTicksANewDetailGetsItsNameToo(t *testing.T) {
	band := designBandWithEmptyDetails()
	// Деталь 17 ПЕРЕИМЕНОВАНА после родителя: сегодня она «notched collar».
	band.Bench[1].DetailName = sql.NullString{String: "notched collar", Valid: true}
	rig := newDesignRunRig(t, designMoodCard(), band)
	rig.design.EXPECT().GetRun(mock.Anything, 7).Return(&entity.DesignRun{
		Id: 7, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["detail"],"layout":"one","detail_slot_ids":[17]}`),
		Inputs: entity.RawJSON(`{"slots":[{"view_key":"detail","slot_id":17,"detail_name":"collar"}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail, entity.DesignViewDetail}
	req.Params.Layout = designLayoutOne
	req.Params.DetailSlotIds = []int32{17, 18} // 18 — НОВАЯ галка
	req.RerunOfRunId = 7

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	bySlot := map[int32]string{}
	for _, s := range snap.GetSlots() {
		bySlot[s.GetSlotId()] = s.GetDetailName()
	}
	require.Equal(t, "patch pocket", bySlot[18],
		"деталь, которую попросили ВПЕРВЫЕ, обязана получить своё имя — иначе промпт снова скажет «detail»")
	require.Equal(t, "collar", bySlot[17],
		"имя, замороженное родителем, сегодняшним переименованием не переписывается: прогон "+
			"обязан читаться тем именем, с которым он был отправлен")
}

// ─────────────────── ШОВ МЕЖДУ ПАКЕТАМИ ───────────────────

// designSnapshotGolden — путь к ЗАФИКСИРОВАННОМУ выходу настоящей сборки.
//
// ⚠ ЗАЧЕМ ФАЙЛ, А НЕ ЛИТЕРАЛ В КАЖДОМ ПАКЕТЕ. Сборка снимка живёт здесь, а промпт собирается в
// internal/designgen, и импортировать друг друга они не могут. Пока обе половины держали СВОИ
// литералы, ничто не мешало им разойтись — и они разошлись: пробы designgen задавали слоту
// `slot_id`, которого эта сборка не проставляла ни при каком входе, и были зелены при мёртвой
// фиче. Файл делает шов ПРОВЕРЯЕМЫМ: здесь доказывается, что сервер пишет ИМЕННО ЭТО, там —
// что из ИМЕННО ЭТОГО получается нужный промпт.
//
// Обновляется командой DESIGN_GOLDEN_UPDATE=1 go test ./internal/apisrv/admin/ -run Golden.
const designSnapshotGolden = "../../designgen/testdata/server_frozen_two_details.json"

func TestDesignFrozenSnapshotGoldenMatchesTheServer(t *testing.T) {
	type frozen struct {
		Params json.RawMessage `json:"params"`
		Inputs json.RawMessage `json:"inputs"`
	}
	run := func(layout string, views []string, ids []int32) frozen {
		rig := newDesignRunRig(t, designMoodCard(), designBandWithEmptyDetails())
		req := designStartRequest(entity.DesignRunKindFlat)
		req.Params.Views = views
		req.Params.Layout = layout
		req.Params.DetailSlotIds = ids
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		require.NoError(t, err)
		require.NotNil(t, rig.sent)
		return frozen{Params: json.RawMessage(rig.sent.Params), Inputs: json.RawMessage(rig.sent.Inputs)}
	}
	two := []string{entity.DesignViewDetail, entity.DesignViewDetail}
	got := map[string]frozen{
		designLayoutOne:     run(designLayoutOne, two, []int32{17, 18}),
		designLayoutPerView: run(designLayoutPerView, two, []int32{17, 18}),
		// КОНТРОЛЬ ФОРМЫ «РОВНО ОДНА ДЕТАЛЬ»: ей принадлежит дословный абзац Эталона 2, и проба
		// соседнего пакета обязана уметь отличить её от прогона на две.
		"one_single_detail": run(designLayoutOne, []string{entity.DesignViewDetail}, []int32{17}),
	}
	// ⚠ ОТСТУПЫ protojson НЕДЕТЕРМИНИРОВАНЫ (detrand), поэтому сравнение СМЫСЛОВОЕ: JSONEq.
	// Побайтовое здесь краснело бы через раз и научило бы всех игнорировать красноту.
	encoded, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)

	if os.Getenv("DESIGN_GOLDEN_UPDATE") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(designSnapshotGolden), 0o755))
		require.NoError(t, os.WriteFile(designSnapshotGolden, append(encoded, '\n'), 0o644))
		t.Log("golden rewritten:", designSnapshotGolden)
		return
	}
	want, err := os.ReadFile(designSnapshotGolden)
	require.NoError(t, err, "эталон снимка обязан лежать в дереве: он вход проб соседнего пакета")
	require.JSONEq(t, string(want), string(encoded),
		"сборка снимка разошлась с тем, из чего собирают промпт пробы internal/designgen")
}

// designBandWithEmptyDetails — верстак с ОДНОЙ занятой стороной и ДВУМЯ пустыми деталями.
//
// Пустота здесь не небрежность стенда, а предмет пробы: слот заведён и назван, картинки в нём нет.
func designBandWithEmptyDetails() *entity.DesignBand {
	band := designBandWith(true)
	for _, d := range []struct {
		id   int
		name string
	}{{17, "collar"}, {18, "patch pocket"}} {
		band.Bench = append(band.Bench, entity.DesignBenchSlot{
			Id:         d.id,
			TechCardId: designRunCardID,
			ViewKey:    entity.DesignViewDetail,
			Kind:       entity.DesignPictureKindFlat,
			DetailName: sql.NullString{String: d.name, Valid: true},
		})
	}
	return band
}
