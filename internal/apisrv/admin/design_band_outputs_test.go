package admin

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// dcoNoCostingCtx — аккаунт, ради которого редакция существует: полосу читать можно, деньги —
// нельзя. Скоупный, а не super: super пропускает всё и не доказал бы ничего про предикат.
func dcoNoCostingCtx() context.Context {
	return auth.PutAdminUsername(auth.PutAdminAuthz(context.Background(), auth.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessRead},
	}), "designer")
}

// ВЫХОДЫ КАРТОЧКИ НА ПРОВОДЕ (H-9). Полоса везёт ОДНУ страницу ленты, а раздел «рендеры этой
// карточки» обещает КАРТОЧКУ; до этой волны он читал страницу и терял рендеры по одному, унося с
// ними кропы, нарезанные из их листов. Здесь проверяется вторая половина обещания — что штамп
// прогона доезжает до провода целым, потому что БЕЗ НЕГО список неотличим от мусора: прогон
// такого кадра в ответе отсутствует, и ни род, ни ревизия, ни колорвей из картинки не выводятся.

const (
	dcoCardID  = 4242
	dcoOffPage = 80 // прогон ВНЕ страницы истории — ради него всё и затевалось
)

func dcoOutputsBand() *entity.DesignBand {
	hidden := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	return &entity.DesignBand{
		// СТРАНИЦА ЛЕНТЫ НЕ СОДЕРЖИТ НИ ОДНОГО ИЗ ЭТИХ ПРОГОНОВ — ровно то состояние, в котором
		// прежний читатель показывал пустой раздел.
		Runs: []entity.DesignRun{{
			Id: 91, TechCardId: dcoCardID, Kind: entity.DesignRunKindFlat,
			Status: entity.DesignRunDone,
		}},
		// ⚠ КАРТОЧКА УСЕЧЕНА, И ИМЕННО ПОЭТОМУ ОДНОГО ЧИСЛА МАЛО. Всего у неё 142 выхода, в ответ
		// поместились пять. Раздел колорвея 5 показывает ВСЕ свои три кадра, раздел колорвея 6 —
		// ОДИН из ста тридцати восьми. Карточное «142» не различает эти два состояния вовсе, и
		// подписать усечение им нельзя: подпись раздела берётся из поколорвейной карты.
		OutputsTotal:           142,
		OutputsTotalByColorway: map[int]int{5: 3, 6: 138, 0: 1},
		Outputs: []entity.DesignCardOutput{
			// Кроп листа рендера: наследует run_id родителя, поэтому это такой же выход прогона.
			{
				Picture: entity.DesignPicture{
					Id: 46, TechCardId: dcoCardID, MediaId: 900,
					RunId:       sql.NullInt32{Int32: dcoOffPage, Valid: true},
					Kind:        entity.DesignPictureKindRender,
					DerivedFrom: sql.NullInt32{Int32: 22, Valid: true},
					ColorwayId:  sql.NullInt32{Int32: 5, Valid: true},
				},
				RunId: dcoOffPage, RunKind: entity.DesignRunKindRender,
				RunRrev: 7, RunColorwayId: 5,
			},
			// ПЕРЕКРАС: КАДР ГОВОРИТ `render`, ПРОГОН ГОВОРИТ `recolor`. Ловушка L-1 закрыта
			// именно здесь, и только штампом.
			{
				Picture: entity.DesignPicture{
					Id: 47, TechCardId: dcoCardID, MediaId: 901,
					RunId:      sql.NullInt32{Int32: 81, Valid: true},
					Kind:       entity.DesignPictureKindRender,
					ColorwayId: sql.NullInt32{Int32: 5, Valid: true},
				},
				RunId: 81, RunKind: entity.DesignRunKindRecolor, RunRrev: 3, RunColorwayId: 5,
			},
			// Спрятанный кадр едет СО СВОИМ ФЛАГОМ — фильтрует клиент, не сервер. Колорвея у него
			// нет: он из безколорвейного, легаси-раздела 0.
			{
				Picture: entity.DesignPicture{
					Id: 48, TechCardId: dcoCardID, MediaId: 902,
					RunId:    sql.NullInt32{Int32: dcoOffPage, Valid: true},
					Kind:     entity.DesignPictureKindRender,
					HiddenAt: sql.NullTime{Time: hidden, Valid: true},
					HiddenBy: sql.NullString{String: "designer", Valid: true},
				},
				RunId: dcoOffPage, RunKind: entity.DesignRunKindRender, RunRrev: 7,
			},
			// КАДР ИЗ ПАЧКИ: прогона нет вовсе, и штамп это говорит нулями, а не выдумкой. Зато
			// КОЛОРВЕЙ у него назван — и раздел он выбирает по нему, а не по нулевому штампу.
			{
				Picture: entity.DesignPicture{
					Id: 63, TechCardId: dcoCardID, MediaId: 903,
					BatchId:    sql.NullInt32{Int32: 20, Valid: true},
					Kind:       entity.DesignPictureKindRender,
					ColorwayId: sql.NullInt32{Int32: 6, Valid: true},
				},
			},
			// 3D-кадр той же карточки: раздел 3D читает тот же список.
			{
				Picture: entity.DesignPicture{
					Id: 64, TechCardId: dcoCardID, MediaId: 904,
					RunId:      sql.NullInt32{Int32: 92, Valid: true},
					Kind:       entity.DesignPictureKindThreed,
					ColorwayId: sql.NullInt32{Int32: 5, Valid: true},
				},
				RunId: 92, RunKind: entity.DesignRunKindThreed, RunRrev: 7, RunColorwayId: 5,
			},
		},
	}
}

func dcoRead(t *testing.T, ctx context.Context, band *entity.DesignBand) *pb_admin.GetDesignBandResponse {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design).Maybe()
	design.EXPECT().GetBand(mock.Anything, dcoCardID, mock.Anything).Return(band, nil).Once()
	resp, err := (&Server{repo: repo}).GetDesignBand(ctx,
		&pb_admin.GetDesignBandRequest{TechCardId: dcoCardID})
	require.NoError(t, err)
	return resp
}

// TestGetDesignBandServesWholeCardOutputsWithTheirRunStamp — список едет целиком и со штампом.
//
// МУТАЦИЯ, КОТОРАЯ ЭТО КРАСНИТ: снять строку `Outputs:` из ответа GetDesignBand (полоса
// по-прежнему отвечает 200, раздел просто пустеет) либо собрать штамп из картинки, а не из
// прогона (`RunKind: o.Picture.Kind`) — тогда перекрас перестанет отличаться от рендера.
func TestGetDesignBandServesWholeCardOutputsWithTheirRunStamp(t *testing.T) {
	resp := dcoRead(t, designRunCtx(), dcoOutputsBand())

	require.Len(t, resp.GetOutputs(), 5,
		"раздел обещает КАРТОЧКУ: выходы не режутся страницей истории")
	require.Equal(t, int32(142), resp.GetOutputsTotal(),
		"счётчик существует затем, чтобы усечение на потолке было измеримо")

	// ⚠ ПОДПИСЬ УСЕЧЕНИЯ ДОЕЗЖАЕТ ДО ТОГО, КТО СУЖАЕТ. Без этой карты раздел колорвея 6 не может
	// отличить «это всё» от «это первый кадр из ста тридцати восьми»: карточное 142 не отвечает
	// на вопрос раздела вовсе — это тот же класс дефекта, что и постраничное чтение, которое
	// волна закрывает, просто на горизонте потолка.
	//
	// МУТАЦИЯ: снять строку OutputsTotalByColorway из ответа GetDesignBand — полоса по-прежнему
	// ответит 200, а раздел молча потеряет способность подписать собственное усечение.
	require.Equal(t, map[int32]int32{5: 3, 6: 138, 0: 1}, resp.GetOutputsTotalByColorway(),
		"поколорвейный итог — единственное, чем раздел может подписать своё усечение")
	// И ключ этой карты — колорвей КАДРА: у загруженной плиты 63 штамп прогона нулевой, а лежит
	// она в разделе 6. Ключ по прогону свалил бы её в «неатрибутированный».
	require.Equal(t, int32(6), resp.GetOutputs()[3].GetPicture().GetColorwayId())
	require.Zero(t, resp.GetOutputs()[3].GetRunColorwayId())

	// Прогон кропа в ответе ОТСУТСТВУЕТ — то есть штамп и есть единственный источник этих фактов.
	for _, r := range resp.GetRuns() {
		require.NotEqual(t, int32(dcoOffPage), r.GetId(),
			"стенд обязан держать прогон выхода ВНЕ страницы, иначе проба ничего не проверяет")
	}

	byPicture := map[int32]*struct {
		runID, rrev, cw, batch int32
		runKind, picKind       string
		hidden                 bool
	}{}
	for _, o := range resp.GetOutputs() {
		byPicture[o.GetPicture().GetId()] = &struct {
			runID, rrev, cw, batch int32
			runKind, picKind       string
			hidden                 bool
		}{
			runID: o.GetRunId(), rrev: o.GetRunRrev(), cw: o.GetRunColorwayId(),
			batch: o.GetBatchId(), runKind: o.GetRunKind(),
			picKind: o.GetPicture().GetKind(), hidden: o.GetPicture().GetHiddenAt() != nil,
		}
	}

	crop := byPicture[46]
	require.NotNil(t, crop, "кроп листа рендера — выход прогона, как и сам лист")
	require.Equal(t, int32(dcoOffPage), crop.runID)
	require.Equal(t, "render", crop.runKind)
	require.Equal(t, int32(7), crop.rrev)
	require.Equal(t, int32(5), crop.cw, "сужение по колорвею читает колорвей ПРОГОНА")

	recolor := byPicture[47]
	require.NotNil(t, recolor)
	require.Equal(t, "render", recolor.picKind,
		"перекрас правда рождает кадр рода render — на выходе фотография изделия")
	require.Equal(t, "recolor", recolor.runKind,
		"…и только род ПРОГОНА разводит ON MODEL и RENDERS: без штампа они слиплись бы")

	hidden := byPicture[48]
	require.NotNil(t, hidden, "спрятанный кадр не исчезает из ответа")
	require.True(t, hidden.hidden, "он едет СО СВОИМ ФЛАГОМ; фильтрует клиент")

	upload := byPicture[63]
	require.NotNil(t, upload, "загруженный рендер — рендер этой карточки, и его можно выбрать")
	require.Zero(t, upload.runID, "прогона нет — штамп говорит это нулём, а не выдумкой")
	require.Empty(t, upload.runKind)
	require.Equal(t, int32(20), upload.batch, "зато сказано, с какой полки он пришёл")

	require.Equal(t, "threed", byPicture[64].runKind)
}

// TestDesignCardOutputsCarryNoMoneyAndSurviveRedaction — проверено, а не предположено.
//
// План волны требовал ПОДТВЕРДИТЬ, что stripDesignCosting нечего снимать со штампа. Утверждение
// проверяется с двух сторон сразу: у аккаунта без costing:read деньги прогона исчезают (значит
// редакция вообще исполнилась — положительный контроль), а выходы и их штампы остаются целыми.
//
// ⚠ ЧЕГО ЭТА ПРОБА НЕ ДЕЛАЕТ, И РАНЬШЕ ЗДЕСЬ СТОЯЛО ОБЕЩАНИЕ, ЧТО ДЕЛАЕТ. Было написано:
// «МУТАЦИЯ: дописать в штамп денежное поле и не расширить stripDesignCosting — эта проба покажет
// цену прогона там, где её быть не должно, как только поле появится». НЕ ПОКАЖЕТ. Проба читает
// цену СУЩЕСТВУЮЩЕГО прогона и целость выходов; появись завтра на DesignCardOutput поле
// price_actual, оно доехало бы до аккаунта без costing:read, а здесь всё осталось бы зелёным —
// то есть сторож обещал стеречь дверь, которой у него нет.
//
// Сторожит эту дверь СОСЕДНЯЯ проба, TestDesignCardOutputCarriesNoMoneyShapedFieldAtAll, и не
// перечислением сегодняшних полей, а по ФОРМЕ: денежного поля не должно быть на сообщении вовсе.
// Перечисление протухло бы ровно так же, как протух этот абзац.
//
// ЗДЕСЬ ЖЕ ПРОВЕРЯЕТСЯ ДРУГОЕ И ТОЛЬКО ОНО: редакция костинга НА ЭТОМ ПУТИ вообще исполняется
// (иначе всё, что про выходы, ничего не значило бы), и она не выкашивает ни выходы, ни их штампы,
// ни подпись усечения.
//
// ⚠ ОБЕ ПОЛОВИНЫ ЧИТАЮТ ПОЛОСУ С ЦЕНОЙ, И ЭТО НЕ ОПРЯТНОСТЬ. Пока редактируемое чтение получало
// СВЕЖИЙ dcoOutputsBand() — то есть полосу БЕЗ цены, — строка `require.Nil(...PriceEstimate)`
// проходила и с редакцией, и без неё: снимать было нечего. Так называемый «положительный
// контроль» не мог упасть ни при какой поломке, и редакция костинга на полосе не была защищена
// НИЧЕМ. Проверено мутацией: заменить `s.stripDesignCosting(...)` в GetDesignBand на пустую
// строку — до этой правки пакет оставался 1164 PASS / 9 FAIL, буква в букву.
func TestDesignCardOutputsCarryNoMoneyAndSurviveRedaction(t *testing.T) {
	priced := func() *entity.DesignBand {
		b := dcoOutputsBand()
		b.Runs[0].PriceEstimate = decimal.NullDecimal{
			Decimal: decimal.RequireFromString("1.25"), Valid: true,
		}
		return b
	}

	full := dcoRead(t, designRunCtx(), priced())
	require.NotNil(t, full.GetRuns()[0].GetPriceEstimate(),
		"положительный контроль: с costing:read цена видна")

	redacted := dcoRead(t, dcoNoCostingCtx(), priced())
	require.Nil(t, redacted.GetRuns()[0].GetPriceEstimate(),
		"без costing:read редакция обязана исполниться — иначе проба ниже ничего не значит")
	require.Len(t, redacted.GetOutputs(), 5,
		"редакция денег не должна выкашивать выходы карточки")
	require.Equal(t, int32(142), redacted.GetOutputsTotal())
	require.Equal(t, map[int32]int32{5: 3, 6: 138, 0: 1}, redacted.GetOutputsTotalByColorway(),
		"подпись усечения — не деньги: редакция костинга её не касается")
	require.Equal(t, "recolor", redacted.GetOutputs()[1].GetRunKind(),
		"штамп не несёт денег, поэтому редакция его не касается")
}

// ─────────────── денежное поле на выходе: сторож по ФОРМЕ, а не по списку ───────────────

// dcoMoneyType — тип, которым на этом проводе выражены ВСЕ деньги. Проверено по проводу, а не по
// памяти: price_estimate, price_actual, DesignRunAttempt.price, budget.spent и budget.reserved —
// пять полей, которые снимает stripDesignCosting, — и все пять google.type.Decimal. То есть это
// не эвристика «похоже на деньги», а полная характеристика сегодняшней поверхности редакции.
const dcoMoneyType = "google.type.Decimal"

// dcoMoneyNameTokens ловит вторую форму денег — сумму в МЕЛКОЙ ЕДИНИЦЕ или строкой, которая мимо
// google.type.Decimal проехала бы молча (`int64 price_minor`, `string cost`). Проверено против
// всего графа, достижимого из DesignCardOutput: ни одно сегодняшнее имя сюда не попадает.
var dcoMoneyNameTokens = []string{
	"price", "cost", "amount", "money", "fee", "charge", "spend", "spent", "payable",
	"currency", "budget",
}

// dcoMoneyShapedFields возвращает пути ВСЕХ достижимых из md полей, которые выглядят деньгами.
//
// ⚠ ОБХОД РЕКУРСИВНЫЙ, И ЭТО НЕ ПЕДАНТИЗМ. Деньги, появившиеся на DesignPicture, утекли бы точно
// так же, как деньги на самом DesignCardOutput: раздел везёт картинку целиком. Поэтому граница
// сторожа — не сообщение, а ВСЁ, что уезжает вместе с ним.
//
// В google.type.Decimal и в google.protobuf.* обход не заходит: первый и есть искомый лист,
// второй — служебные типы, чьи поля (seconds/nanos) к деньгам отношения не имеют.
func dcoMoneyShapedFields(md protoreflect.MessageDescriptor) []string {
	var found []string
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor, string)
	walk = func(m protoreflect.MessageDescriptor, path string) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		fields := m.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			at := path + "." + string(f.Name())
			for _, token := range dcoMoneyNameTokens {
				if strings.Contains(string(f.Name()), token) {
					found = append(found, at+" (name says money)")
					break
				}
			}
			if f.Kind() != protoreflect.MessageKind && f.Kind() != protoreflect.GroupKind {
				continue
			}
			name := string(f.Message().FullName())
			if name == dcoMoneyType {
				found = append(found, at+" ("+dcoMoneyType+")")
				continue
			}
			if strings.HasPrefix(name, "google.protobuf.") {
				continue
			}
			walk(f.Message(), at)
		}
	}
	walk(md, string(md.Name()))
	sort.Strings(found)
	return found
}

// TestDesignCardOutputCarriesNoMoneyShapedFieldAtAll — обещание, которое НЕ ПРОТУХАЕТ.
//
// ЧТО ЭТО СТЕРЕЖЁТ. GetDesignBand зовёт stripDesignCosting только по Runs и Budget: выходы
// карточки редакцию НЕ ПРОХОДЯТ вовсе. Сегодня это правильно — на DesignCardOutput денег нет, —
// но правильно ЛИШЬ ПОКА их там нет. Появись завтра price_actual, и он молча доехал бы до
// аккаунта без costing:read: ни одна проба соседнего файла этого не заметила бы, потому что все
// они проверяют цену ПРОГОНА, а прогонная половина как раз редактируется.
//
// ПОЧЕМУ ПО ФОРМЕ, А НЕ СПИСКОМ СЕГОДНЯШНИХ ПОЛЕЙ. Список — это тот же протухающий комментарий,
// только на языке Go: он верен ровно до первого нового поля и молчит именно тогда, когда нужен.
// Здесь сторож спрашивает «есть ли на сообщении хоть что-то денежной ФОРМЫ», и потому срабатывает
// на поле, которого ещё никто не написал.
//
// ЧТО ДЕЛАТЬ, ЕСЛИ ОН УПАЛ: это не повод править пробу. Это значит, что stripDesignCosting обязан
// снимать новое поле, а GetDesignBand — передавать ему выходы; проба зазеленеет сама, когда
// редакция догонит провод, и красна ровно в промежутке между «поле есть» и «оно снимается».
func TestDesignCardOutputCarriesNoMoneyShapedFieldAtAll(t *testing.T) {
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, БЕЗ КОТОРОГО НИЖНЯЯ СТРОКА НЕ ЗНАЧИТ НИЧЕГО. Тот же сторож,
	// натравленный на DesignRun, ОБЯЗАН найти деньги: там их пять полей и все они снимаются
	// редакцией. Пустой ответ здесь означал бы, что сторож сломан, а не что денег нет, — и
	// зелёная нижняя строка была бы сторожем у мёртвого кода.
	onRun := dcoMoneyShapedFields((&pb_common.DesignRun{}).ProtoReflect().Descriptor())
	require.NotEmpty(t, onRun,
		"сторож обязан находить деньги там, где они ЕСТЬ, иначе его молчание ничего не стоит")
	require.Contains(t, onRun, "DesignRun.price_estimate ("+dcoMoneyType+")")
	require.Contains(t, onRun, "DesignRun.price_actual ("+dcoMoneyType+")")
	require.Contains(t, onRun, "DesignRun.attempts.price ("+dcoMoneyType+")",
		"обход обязан заходить ВГЛУБЬ: цена попытки живёт на вложенном сообщении")
	require.Contains(t, onRun, "DesignRun.currency (name says money)",
		"вторая рука сторожа — ИМЯ: она и ловит сумму, приехавшую строкой или мелкой единицей "+
			"мимо google.type.Decimal")

	// И РОВНО ТОТ ЖЕ СТОРОЖ — НА ВЫХОДЕ КАРТОЧКИ.
	require.Empty(t, dcoMoneyShapedFields((&pb_common.DesignCardOutput{}).ProtoReflect().Descriptor()),
		"на выходе карточки появилось денежное поле, а stripDesignCosting его не снимает: "+
			"GetDesignBand редактирует только Runs и Budget, значит это поле уедет аккаунту "+
			"без costing:read. Чинить редакцию, а не пробу")
}
