package design_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ГРАНИЦА КАРТОЧКИ НА ПОЛКАХ И ИХ МЕТКАХ (0354).
//
// ⚠ ПОЧЕМУ ЭТОТ ФАЙЛ ВООБЩЕ ПОЯВИЛСЯ. Замер: сломать скоуп в listAssetPlacements — заменить
// `WHERE a.tech_card_id = :card` всегда истинным условием — и НИ ОДИН тест репозитория не
// краснеет. Полка мерится своей колонкой tech_card_id и потому защищена обычным WHERE, а у
// design_asset_placement своей колонки карточки НЕТ ВОВСЕ (0354 объясняет: второй дом одного факта
// расходится с первым при первом же переносе). «Метки этой карточки» достижимы ТОЛЬКО через JOIN
// на ассет — то есть JOIN здесь не украшение запроса, а САМА ГРАНИЦА. Снимите его, и полоса каждой
// карточки начнёт отдавать метки всех остальных, и ни одна проба этого не заметит.
//
// ТРИ ГРАНИЦЫ, ТРИ ПРОБЫ. Чтение (полоса карточки), удаление (оба удаляющих глагола, названные
// ЧУЖОЙ карточкой) и род кадра под меткой. Первые две проверяют скоуп, третья — что метка
// физически не может лечь на рендер или на кадр поворотного стола: координаты метки суть ДОЛИ
// ЭТОГО кадра, а флэт и рендер одного вида кадрированы по-разному, поэтому одна доля показывает у
// них на РАЗНЫЕ места изделия.
//
// ОБВЯЗКА ОБЩАЯ с wave2_db_test.go (probeRepository / probeCard / probeMedia): без CI=1 всё
// пропускается ДО открытия соединения, а имя базы, похожее на продовое, отвергается отдельно.

// probeAnnotation — минимальная законная геометрия метки. Стор её не разбирает (колонка JSON
// NOT NULL, СОДЕРЖАНИЕ — контракт, а не схема), поэтому важно ровно одно: это валидный непустой
// JSON, а не пустота и не литеральный `null`, которые SetAssetPlacement отвергает отдельно.
func probeAnnotation() json.RawMessage {
	return json.RawMessage(`{"view":"front","anchors":[{"x":0.5,"y":0.5}]}`)
}

// probeAsset кладёт на полку карточки одну ткань БЕЗ файла: медиа у ассета держится RESTRICT'ом, а
// границу, которую судят эти пробы, файл не трогает вовсе.
func probeAsset(t *testing.T, rep dependency.Repository, card int, name string) *entity.DesignAsset {
	t.Helper()
	a, err := rep.Design().UpsertAsset(context.Background(), entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindFabric, Name: name, Actor: "probe",
	})
	require.NoError(t, err)
	require.NotNil(t, a)
	return a
}

// probePicture заводит ОДИН кадр названного рода через обычную загрузку — тем же глаголом, каким
// его заводит человек. Пачка на кадр своя, чтобы не зависеть от порядка, в котором RegisterBatch
// возвращает картинки одной пачки.
func probePicture(t *testing.T, rep dependency.Repository, raw *sql.DB, card int, kind string) entity.DesignPicture {
	t.Helper()
	media := probeMedia(t, raw)
	batch, err := rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: media, Kind: kind}},
	})
	require.NoError(t, err)
	require.Len(t, batch.Pictures, 1)
	require.Equal(t, kind, entity.DesignKindOrFlat(batch.Pictures[0].Kind))
	return batch.Pictures[0]
}

// countRows — прямой счёт строк мимо стора. Удаление, которое ОТКАЗАЛО, обязано быть доказано не
// вторым вызовом того же стора (он мог бы отказать по той же причине и скрыть пропажу), а взглядом
// в таблицу.
func countRows(t *testing.T, raw *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(query, args...).Scan(&n))
	return n
}

// ─────────────────────── 1. ЧТЕНИЕ: полоса карточки не показывает чужие метки ───────────────────────

// ⚠ ЭТО ТА САМАЯ НЕПОКРЫТАЯ ГРАНИЦА. Обе карточки настоящие и обе непустые — и в этом весь смысл:
// сломанный скоуп на карточке-одиночке выглядит идеально, потому что «все метки» и «метки этой
// карточки» там одно и то же множество. Разойтись они могут только когда рядом живёт ВТОРАЯ
// карточка со своей меткой.
func TestDesignDBAssetPlacementsAreScopedToTheirCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	mine, other := probeCard(t, raw), probeCard(t, raw)
	myAsset, otherAsset := probeAsset(t, rep, mine, "cloth of mine"), probeAsset(t, rep, other, "cloth of theirs")
	myFlat, otherFlat := probePicture(t, rep, raw, mine, entity.DesignPictureKindFlat),
		probePicture(t, rep, raw, other, entity.DesignPictureKindFlat)

	myMark, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: mine, AssetId: myAsset.Id, PictureId: myFlat.Id,
		Annotation: probeAnnotation(), Note: "mine", Actor: "probe",
	})
	require.NoError(t, err)
	otherMark, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: other, AssetId: otherAsset.Id, PictureId: otherFlat.Id,
		Annotation: probeAnnotation(), Note: "theirs", Actor: "probe",
	})
	require.NoError(t, err)
	require.NotEqual(t, myMark.Id, otherMark.Id)

	// Обе метки лежат в таблице — иначе «моя полоса показала одну» доказывало бы не скоуп, а то,
	// что второй строки просто нет.
	require.Equal(t, 2, countRows(t, raw,
		`SELECT COUNT(*) FROM design_asset_placement WHERE id IN (?, ?)`, myMark.Id, otherMark.Id))

	band, err := rep.Design().GetBand(ctx, mine, 1)
	require.NoError(t, err)
	require.Len(t, band.AssetPlacements, 1,
		"полоса карточки обязана нести ТОЛЬКО свои метки: у design_asset_placement нет своей колонки карточки, и JOIN на ассет — единственная граница")
	require.Equal(t, myMark.Id, band.AssetPlacements[0].Id)
	for _, p := range band.AssetPlacements {
		require.NotEqual(t, otherMark.Id, p.Id, "метка ЧУЖОЙ карточки не имеет права попасть в полосу")
	}
	// Полка мерится своей колонкой, но утверждать её здесь всё равно нужно: без этого «полоса
	// чистая» могло бы означать «полоса пустая по другой причине».
	require.Len(t, band.Assets, 1)
	require.Equal(t, myAsset.Id, band.Assets[0].Id)

	// Симметрия. Односторонняя проба зеленеет и на запросе, который просто ничего не возвращает.
	otherBand, err := rep.Design().GetBand(ctx, other, 1)
	require.NoError(t, err)
	require.Len(t, otherBand.AssetPlacements, 1)
	require.Equal(t, otherMark.Id, otherBand.AssetPlacements[0].Id)
	require.Len(t, otherBand.Assets, 1)
	require.Equal(t, otherAsset.Id, otherBand.Assets[0].Id)

	// ЗАПИСЬ ЧЕРЕЗ ГРАНИЦУ — оба конца. Ни своим ассетом на чужом флэте, ни чужим ассетом на своём:
	// схема не выражает ни того, ни другого, и обе половины держит стор.
	_, err = rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: mine, AssetId: myAsset.Id, PictureId: otherFlat.Id,
		Annotation: probeAnnotation(), Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignCardPlate,
		"свой ассет на ЧУЖОМ флэте — отказ: координаты метки суть доли того кадра, на котором её поставили")
	_, err = rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: mine, AssetId: otherAsset.Id, PictureId: myFlat.Id,
		Annotation: probeAnnotation(), Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignNotFound,
		"ЧУЖОЙ ассет на своём флэте — отказ: полка принадлежит другой карточке")
	require.Equal(t, 2, countRows(t, raw,
		`SELECT COUNT(*) FROM design_asset_placement WHERE id IN (?, ?)`, myMark.Id, otherMark.Id))
	require.Equal(t, 0, countRows(t, raw,
		`SELECT COUNT(*) FROM design_asset_placement WHERE id NOT IN (?, ?) AND asset_id IN (?, ?)`,
		myMark.Id, otherMark.Id, myAsset.Id, otherAsset.Id),
		"отказавшая постановка не имеет права оставить строку")
}

// ─────────────────────── 2. УДАЛЕНИЕ: чужая карточка не удаляет ничего ───────────────────────

// DeleteAsset КАСКАДИРУЕТ на метки, и именно поэтому его граница дороже прочих: ошибка здесь стоит
// не одной строки, а всей разметки флэтов вместе с ней.
//
// ⚠ ВЕТКА «КАРТА 0 — НЕ ПРОВЕРЯТЬ» УБРАНА ИЗ СТОРА, и проба обязана это удостоверить, а не принять
// на веру: пока такое значение cardID существовало, единственный каскадящий глагол файла был
// заодно единственным глаголом БЕЗ границы карточки вовсе. Неправильное состояние теперь не
// проверяется — оно не выразимо; ниже перечислены все способы его произнести.
func TestDesignDBDeleteAssetRefusesForeignCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	mine, other := probeCard(t, raw), probeCard(t, raw)
	asset := probeAsset(t, rep, mine, "cloth under test")
	flat := probePicture(t, rep, raw, mine, entity.DesignPictureKindFlat)
	mark, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: mine, AssetId: asset.Id, PictureId: flat.Id,
		Annotation: probeAnnotation(), Actor: "probe",
	})
	require.NoError(t, err)

	alive := func(where string) {
		t.Helper()
		require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, asset.Id), where)
		require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset_placement WHERE id = ?`, mark.Id), where)
	}

	n, err := rep.Design().DeleteAsset(ctx, other, asset.Id)
	require.ErrorIs(t, err, entity.ErrDesignNotFound,
		"удаление полки, названное ЧУЖОЙ карточкой, обязано отказать")
	require.Equal(t, 0, n, "отказавшее удаление не имеет права отчитаться о снятых метках")
	alive("отказ по чужой карточке не имеет права ничего удалить")

	// «Не проверять» произнести нечем: и ноль, и отрицательное — невыразимое состояние, а не
	// сквозной проход.
	for _, bad := range []int{0, -1} {
		n, err = rep.Design().DeleteAsset(ctx, bad, asset.Id)
		require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
			"tech card %d обязан быть отказом, а не пропуском проверки", bad)
		require.Equal(t, 0, n)
		alive("невыразимая карточка не имеет права ничего удалить")
	}

	// КОНТРОЛЬ УЗОСТИ: та же полка, названная СВОЕЙ карточкой, удаляется — и уносит ровно свою
	// метку. Без него «отказало» зеленело бы и на глаголе, сломанном насмерть.
	n, err = rep.Design().DeleteAsset(ctx, mine, asset.Id)
	require.NoError(t, err)
	require.Equal(t, 1, n, "удаление обязано отчитаться о метке, ушедшей каскадом")
	require.Equal(t, 0, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, asset.Id))
	require.Equal(t, 0, countRows(t, raw, `SELECT COUNT(*) FROM design_asset_placement WHERE id = ?`, mark.Id))
}

// Снятие метки — глагол без каскада, и граница у него ЖИВЁТ ТОЛЬКО В JOIN'е: у строки метки нет
// колонки карточки, поэтому «эта метка моя» неоткуда узнать, кроме как через её ассет.
func TestDesignDBDeleteAssetPlacementRefusesForeignCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	mine, other := probeCard(t, raw), probeCard(t, raw)
	asset := probeAsset(t, rep, mine, "cloth of mine")
	flat := probePicture(t, rep, raw, mine, entity.DesignPictureKindFlat)
	mark, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: mine, AssetId: asset.Id, PictureId: flat.Id,
		Annotation: probeAnnotation(), Actor: "probe",
	})
	require.NoError(t, err)

	// У ЧУЖОЙ КАРТОЧКИ ЕСТЬ СВОЯ ПОЛКА. Иначе JOIN мог бы отказать не потому, что метка чужая, а
	// потому, что у названной карточки нет ассетов вовсе, — и проба судила бы не то.
	probeAsset(t, rep, other, "cloth of theirs")

	require.ErrorIs(t, rep.Design().DeleteAssetPlacement(ctx, other, mark.Id), entity.ErrDesignNotFound,
		"снятие метки, названное ЧУЖОЙ карточкой, обязано отказать")
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset_placement WHERE id = ?`, mark.Id),
		"отказ не имеет права снять метку")

	for _, bad := range []int{0, -1} {
		require.ErrorIs(t, rep.Design().DeleteAssetPlacement(ctx, bad, mark.Id), entity.ErrDesignInvalidArgument,
			"tech card %d обязан быть отказом, а не пропуском проверки", bad)
		require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset_placement WHERE id = ?`, mark.Id))
	}

	require.NoError(t, rep.Design().DeleteAssetPlacement(ctx, mine, mark.Id))
	require.Equal(t, 0, countRows(t, raw, `SELECT COUNT(*) FROM design_asset_placement WHERE id = ?`, mark.Id))
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, asset.Id),
		"снятие метки — не удаление полки: ассет обязан пережить свою метку")
}

// ─────────────────────── 3. РОД КАДРА ПОД МЕТКОЙ ───────────────────────

// Метка стоит НА ФЛЭТЕ — так говорят все три записи факта: схема 0354, контракт SetAssetPlacement и
// клиент, предлагающий выбрать только флэты. Проверялось это НИГДЕ, и метка на рендере принималась
// и возвращалась полосой как метка на флэте — система утверждала о картинке род, которого у той
// нет. Здесь судится ЖИВАЯ проводка: что стор действительно читает строку кадра и спрашивает у неё
// род, а не что правило существует в entity (это проверено без базы, в design_asset_test.go).
func TestDesignDBAssetPlacementRefusesNonFlatPicture(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	card := probeCard(t, raw)
	asset := probeAsset(t, rep, card, "cloth on the shelf")
	flat := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	render := probePicture(t, rep, raw, card, entity.DesignPictureKindRender)
	threed := probePicture(t, rep, raw, card, entity.DesignPictureKindThreed)

	for _, pic := range []entity.DesignPicture{render, threed} {
		_, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
			TechCardId: card, AssetId: asset.Id, PictureId: pic.Id,
			Annotation: probeAnnotation(), Actor: "probe",
		})
		require.ErrorIsf(t, err, entity.ErrDesignWrongKind,
			"метка на кадре рода %q обязана получать отказ: её координаты — доли ИМЕННО ЭТОГО кадра", pic.Kind)
		require.Equal(t, 0, countRows(t, raw,
			`SELECT COUNT(*) FROM design_asset_placement WHERE picture_id = ?`, pic.Id),
			"отказавшая постановка не имеет права оставить строку")
	}

	// КОНТРОЛЬ УЗОСТИ: тот же ассет на ФЛЭТЕ той же карточки принимается. Без него «отказало»
	// зеленело бы и на глаголе, который отказывает всем подряд.
	mark, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
		TechCardId: card, AssetId: asset.Id, PictureId: flat.Id,
		Annotation: probeAnnotation(), Actor: "probe",
	})
	require.NoError(t, err, "метка на ФЛЭТЕ — законный жест, ради которого таблица и заведена")
	require.NotNil(t, mark)
	require.Equal(t, flat.Id, mark.PictureId)

	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.Len(t, band.AssetPlacements, 1, "в полосе обязана оказаться ровно одна метка — та, что на флэте")
	require.Equal(t, mark.Id, band.AssetPlacements[0].Id)
}
