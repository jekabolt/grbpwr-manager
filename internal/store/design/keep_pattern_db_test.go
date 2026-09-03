package design_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПОСАДКА ПЛИТКИ НА ПОЛКУ ПРИ ЗАКРЫТИИ ПРОГОНА (круг 15, J-12).
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там:
// без CI=1 всё здесь пропускается ДО открытия соединения.
//
// ⚠ ЭТИ ПРОБЫ НЕЛЬЗЯ БЫЛО НАПИСАТЬ БЕЗ БАЗЫ, И ЭТО НЕ ПРЕДПОЧТЕНИЕ. Три из четырёх утверждений —
// про то, чего Go не видит: UNIQUE-ключ uq_design_asset_colorway (кража ДО вставки), потолок полки,
// посчитанный ВНУТРИ той же транзакции, и внешние ключи на медиа и на родителя. Тест с моком
// проверял бы, что мы вызвали функцию, которую сами и написали.

// patternRun открывает прогон паттерна с замороженными params — теми самыми, из которых посадка
// читает имя и родителя.
func patternRun(t *testing.T, rep dependency.Repository, card, colorway int, params map[string]any) *entity.DesignRunStarted {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"pattern": params, "colorway_id": colorway})
	require.NoError(t, err)
	started, err := rep.Design().StartRun(context.Background(), entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindPattern, RequestedOutputs: 1, Author: "probe",
		Params:        raw,
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    colorway, ColorwayStated: true,
	})
	require.NoError(t, err)
	return started
}

func landPatternRun(t *testing.T, rep dependency.Repository, runID, media int) (*entity.DesignRun, error) {
	t.Helper()
	ctx := context.Background()
	claimed, err := rep.Design().ClaimRuns(ctx, 1, time.Minute, uuid.NewString())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, runID, claimed[0].Id)
	return rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		Outputs: []entity.DesignPictureInsert{{MediaId: media, Ordinal: 0}},
	})
}

func shelfOf(t *testing.T, raw *sql.DB, card int) []entity.DesignAsset {
	t.Helper()
	rows, err := raw.Query(`SELECT id, kind, name, media_id, repeat_mm, derived_from_asset_id, colorway_id,
		created_by FROM design_asset WHERE tech_card_id = ? ORDER BY id`, card)
	require.NoError(t, err)
	defer rows.Close()
	var out []entity.DesignAsset
	for rows.Next() {
		var a entity.DesignAsset
		require.NoError(t, rows.Scan(&a.Id, &a.Kind, &a.Name, &a.MediaId, &a.RepeatMm,
			&a.DerivedFromAssetId, &a.ColorwayId, &a.CreatedBy))
		out = append(out, a)
	}
	require.NoError(t, rows.Err())
	return out
}

// ПЛИТКА САДИТСЯ САМА, С ИМЕНЕМ, КАДРОМ, РОДОСЛОВНОЙ И КОЛОРВЕЕМ — И КРАДЁТ КОЛОРВЕЙ У ПРЕЖНЕЙ
// ТКАНИ, потому что колорвей носит РОВНО ОДНУ ткань (uq_design_asset_colorway).
//
// ⚠ КРАЖА ДО ВСТАВКИ — ЭТО И ЕСТЬ ПРЕДМЕТ ПРОБЫ. Ключ настоящий UNIQUE: вставь нового носителя,
// пока прежний ещё носит, и MySQL отдаст 1062 на прогоне, за который уже заплачено, откатив вместе
// с собой ВСЮ выдачу. Мутация «поменять местами stealColorwayTx и insertAssetTx» краснеет здесь
// ошибкой прилёта, а не расхождением чисел.
func TestDesignDBAPatternRunFILES_ITS_TILE_AND_TAKES_THE_COLOURWAY(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw)
	ctx := context.Background()

	// Прежний носитель колорвея: ткань, у которой плитка его отберёт.
	before, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindFabric, Name: "old jersey", Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: before.Id, ColorwayId: cw,
	})
	require.NoError(t, err)

	started := patternRun(t, rep, card, cw, map[string]any{
		"name": "chevron", "repeat_mm": 0, "source_asset_id": before.Id,
	})
	tile := probeMedia(t, raw)
	done, err := landPatternRun(t, rep, started.Run.Id, tile)
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunDone, done.Status)
	require.Len(t, done.Pictures, 1, "кадр остаётся кадром: посадка его не подменяет")
	require.Equal(t, entity.DesignPictureKindPattern, done.Pictures[0].Kind)
	require.Equal(t, cw, entity.DesignColorwayOrNone(done.Pictures[0].ColorwayId),
		"кадр паттерна наследует колорвей своей строки — сторож D6 обязан его пропустить")
	require.False(t, done.ErrorCode.Valid, "полка не была полна: причины отказа нет")

	shelf := shelfOf(t, raw, card)
	require.Len(t, shelf, 2, "ровно один новый ассет, а не два и не ноль")
	kept := shelf[1]
	require.Equal(t, entity.DesignAssetKindPattern, kept.Kind)
	require.Equal(t, "chevron", kept.Name)
	require.Equal(t, tile, int(kept.MediaId.Int32))
	require.Equal(t, before.Id, int(kept.DerivedFromAssetId.Int32), "родословная V-7")
	require.Equal(t, cw, entity.DesignColorwayOrNone(kept.ColorwayId))
	require.Equal(t, "probe", kept.CreatedBy, "автор строки — автор прогона")

	// КРАЖА: прежняя ткань осталась на полке и осталась тканью, но носителем быть перестала.
	require.Equal(t, before.Id, shelf[0].Id)
	require.Zero(t, entity.DesignColorwayOrNone(shelf[0].ColorwayId),
		"колорвей носит ровно одну ткань: прежняя обязана его лишиться")

	// ИДЕМПОТЕНТНОСТЬ: повторный прилёт того же результата не заводит второго ассета.
	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: "any-late-token",
		Outputs: []entity.DesignPictureInsert{{MediaId: tile, Ordinal: 0}},
	})
	require.NoError(t, err)
	require.Len(t, shelfOf(t, raw, card), 2, "закрытая строка отвечает составом, а не сажает второй раз")
}

// БЕЗ КОЛОРВЕЯ ПЛИТКА ВСЁ РАВНО САДИТСЯ — карточка без колорвеев это обычное состояние беты, и
// отказывать ей было бы отказом всей фиче.
func TestDesignDBAPatternRunWithoutAColourwaySTILL_FILES_ITS_TILE(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	resetBudget(t, raw)

	started := patternRun(t, rep, card, 0, map[string]any{"name": "plain weave"})
	done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
	require.NoError(t, err)
	require.False(t, done.ErrorCode.Valid)

	shelf := shelfOf(t, raw, card)
	require.Len(t, shelf, 1)
	require.Equal(t, "plain weave", shelf[0].Name)
	require.Zero(t, entity.DesignColorwayOrNone(shelf[0].ColorwayId), "плитка ничья, и это законно")

	// ⚠ ПРОГОН, ЗАМОРОЖЕННЫЙ ДО КРУГА 15, НЕ САЖАЕТ НИЧЕГО. Имени взять негде, а выдуманное
	// приехало бы в следующий промпт словом «pattern». Такие плитки кладёт человек, как и раньше.
	legacy := patternRun(t, rep, card, 0, nil)
	doneLegacy, err := landPatternRun(t, rep, legacy.Run.Id, probeMedia(t, raw))
	require.NoError(t, err)
	require.Len(t, doneLegacy.Pictures, 1, "кадр всё равно filed: за него заплачено")
	require.Len(t, shelfOf(t, raw, card), 1, "безымянный прогон полку не трогает")
}

// ПОЛКА, ПЕРЕПОЛНИВШАЯСЯ ПОКА ПРОГОН ШЁЛ: КАДР FILED, АССЕТА НЕТ, ПРИЧИНА НАЗВАНА.
//
// ⚠ ЭТО НЕ ОТКАЗ, И РАЗЛИЧИЕ ДЕНЕЖНОЕ. Картинка куплена; провалить прилёт значило бы выбросить
// оплаченный результат и оставить байты в бакете ничьими. Дверь спрашивает то же самое ДО денег
// (apisrv: `library_full`), и обычным путём эта ветка не достижима — она про гонку.
func TestDesignDBAFullShelfLEAVES_THE_PICTURE_AND_NAMES_THE_REASON(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	resetBudget(t, raw)
	ctx := context.Background()

	started := patternRun(t, rep, card, 0, map[string]any{"name": "chevron"})

	// Полка забивается ПОСЛЕ старта — ровно та гонка, ради которой вторая проверка существует.
	for i := 0; i < entity.MaxDesignAssetsPerCard; i++ {
		_, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
			TechCardId: card, Kind: entity.DesignAssetKindFabric,
			Name: fmt.Sprintf("cloth %d", i), Actor: "probe",
		})
		require.NoError(t, err)
	}

	done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
	require.NoError(t, err, "оплаченный результат обязан прилететь")
	require.Equal(t, entity.DesignRunDone, done.Status)
	require.Len(t, done.Pictures, 1)
	require.True(t, done.ErrorCode.Valid)
	require.Equal(t, entity.DesignErrorCodeLibraryFull, done.ErrorCode.String)
	require.Len(t, shelfOf(t, raw, card), entity.MaxDesignAssetsPerCard,
		"сорок первой строки не появилось — потолок держит и вторая проверка тоже")
}

// ИСТОЧНИК, УДАЛЁННЫЙ ПОКА ПРОГОН ШЁЛ, ОСТАВЛЯЕТ ПЛИТКУ БЕЗ РОДОСЛОВНОЙ, А НЕ РОНЯЕТ ПРИЛЁТ.
//
// Вставка с висящим id упала бы внешним ключом на уже оплаченном результате; «паттерн без
// родословной» — законное состояние, ровно в него его переводит ON DELETE SET NULL.
func TestDesignDBADeletedSourceLEAVES_THE_TILE_WITHOUT_PARENTAGE(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	resetBudget(t, raw)
	ctx := context.Background()

	src, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindFabric, Name: "swatch", Actor: "probe",
	})
	require.NoError(t, err)
	started := patternRun(t, rep, card, 0, map[string]any{"name": "chevron", "source_asset_id": src.Id})

	_, err = rep.Design().DeleteAsset(ctx, card, src.Id)
	require.NoError(t, err)

	done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
	require.NoError(t, err)
	require.False(t, done.ErrorCode.Valid)
	shelf := shelfOf(t, raw, card)
	require.Len(t, shelf, 1)
	require.False(t, shelf[0].DerivedFromAssetId.Valid, "родословной нет, и это не отказ")
	require.Equal(t, "chevron", shelf[0].Name)
}

// РАППОРТ ИЗ ЗАМОРОЖЕННОГО ПРОГОНА, НЕ ВЛЕЗАЮЩИЙ В КОЛОНКУ, ПОДРЕЗАЕТСЯ, А НЕ РОНЯЕТ ПРИЛЁТ.
//
// ⚠ ЭТО ПРО ПРОГОНЫ, ЗАМОРОЖЕННЫЕ РАНЬШЕ ДВЕРИ. До круга 15 `params.pattern.repeat_mm` ехало
// только в промпт и границы не имело вовсе; экран предлагал и свободное поле мм. Теперь это число
// ландит в `design_asset.repeat_mm` — SMALLINT UNSIGNED (0354), — и 70000 отдало бы сырую 1264 на
// прилёте УЖЕ ОПЛАЧЕННОЙ картинки. Дверь держит ту же границу до денег; здесь пояс.
func TestDesignDBAnOutOfRangeRepeatIS_CLAMPED_NOT_FATAL(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	resetBudget(t, raw)

	started := patternRun(t, rep, card, 0, map[string]any{"name": "huge", "repeat_mm": 70000})
	done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
	require.NoError(t, err, "оплаченный кадр обязан прилететь даже с невозможным числом")
	require.Len(t, done.Pictures, 1)

	shelf := shelfOf(t, raw, card)
	require.Len(t, shelf, 1)
	require.Equal(t, entity.MaxDesignAssetRepeatMm, shelf[0].RepeatMm,
		"число подрезано до той же границы, которую держит UpsertDesignAsset")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: законное число доезжает как есть.
	ok := patternRun(t, rep, card, 0, map[string]any{"name": "ordinary", "repeat_mm": 120})
	_, err = landPatternRun(t, rep, ok.Run.Id, probeMedia(t, raw))
	require.NoError(t, err)
	after := shelfOf(t, raw, card)
	require.Len(t, after, 2)
	require.Equal(t, 120, after[1].RepeatMm)
}

// ФУРНИТУРА НЕ СТАНОВИТСЯ РОДИТЕЛЕМ ПРИНТА (N1).
//
// Контракт `source_asset_id` называет `fabric|pattern`, и запись едет в `derived_from_asset_id`,
// чей собственный контракт говорит «паттерн, сделанный из ткани». FK знает только «какая-то строка
// design_asset» и принял бы молнию молча — а строка «этот принт сделан из молнии» пережила бы
// прогон навсегда. Дверь отказывает такому источнику ДО денег; здесь родословная просто не
// пишется, потому что провалить прилёт УЖЕ ОПЛАЧЕННОЙ плитки из-за поля, которое законно
// пустует, было бы дороже правды, которую оно несёт.
func TestDesignDBHardwareIsNOT_THE_PARENT_OF_A_PATTERN(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	resetBudget(t, raw)
	ctx := context.Background()

	zip, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindHardware, Name: "zip", Actor: "probe",
	})
	require.NoError(t, err)
	cloth, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindFabric, Name: "jersey", Actor: "probe",
	})
	require.NoError(t, err)

	fromZip := patternRun(t, rep, card, 0, map[string]any{"name": "from zip", "source_asset_id": zip.Id})
	_, err = landPatternRun(t, rep, fromZip.Run.Id, probeMedia(t, raw))
	require.NoError(t, err, "оплаченная плитка обязана прилететь")

	fromCloth := patternRun(t, rep, card, 0, map[string]any{"name": "from cloth", "source_asset_id": cloth.Id})
	_, err = landPatternRun(t, rep, fromCloth.Run.Id, probeMedia(t, raw))
	require.NoError(t, err)

	shelf := shelfOf(t, raw, card)
	require.Len(t, shelf, 4, "молния, ткань и две плитки")
	byName := map[string]entity.DesignAsset{}
	for _, a := range shelf {
		byName[a.Name] = a
	}
	require.False(t, byName["from zip"].DerivedFromAssetId.Valid,
		"«этот принт сделан из молнии» — предложение без смысла, и строки с ним быть не должно")
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: законный родитель записывается, иначе проба доказывала бы только то,
	// что родословная не пишется никогда.
	require.Equal(t, cloth.Id, int(byName["from cloth"].DerivedFromAssetId.Int32))
}
