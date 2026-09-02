package design_test

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ «ТКАНИ КОЛОРВЕЯ» (0357, волна B, G-15).
//
// Модель владельца: «паттерн — это бесшовная плитка, а бесшовная плитка это ткань». Колорвей
// носит цвет ИЛИ паттерн; цветной случай не хранится вовсе (строка product уже несёт свой цвет), и
// весь предмет этих проб — вторая половина: «колорвей N носит ассет X».
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там.

// ─────────────────────── назначение, кража, снятие ───────────────────────

// ОДНА ТКАНЬ НА КОЛОРВЕЙ: НАЗНАЧЕНИЕ ПЕРЕЕЗЖАЕТ, А НЕ УДВАИВАЕТСЯ.
//
// Единственность держит Go в транзакции глагола, а не схема: UNIQUE по NULLable колонке не
// ограничил бы ничьи ассеты (MySQL считает NULL != NULL — а ничьих большинство) и превратил бы
// обычный клик по соседнему чипу в 1062, тогда как этот клик И ЕСТЬ намерение «теперь ткань N —
// вот эта».
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать UPDATE-кражу из SetAssetColorway — колорвей окажется на ДВУХ
// ассетах разом, и «ткань колорвея» перестанет быть единственным числом.
func TestDesignDBAssetColorwayIsSingleSelect(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	first := probeAsset(t, rep, card, "main jersey")
	second := probeAsset(t, rep, card, "contrast rib")

	assign := func(assetID, colorway int) (*entity.DesignAsset, error) {
		return rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
			TechCardId: card, AssetId: assetID, ColorwayId: colorway, Actor: "probe",
		})
	}
	wearer := func() []int {
		rows := []int{}
		q, err := raw.Query(`SELECT id FROM design_asset WHERE tech_card_id = ? AND colorway_id = ? ORDER BY id`, card, cw)
		require.NoError(t, err)
		defer q.Close()
		for q.Next() {
			var id int
			require.NoError(t, q.Scan(&id))
			rows = append(rows, id)
		}
		require.NoError(t, q.Err())
		return rows
	}

	got, err := assign(first.Id, cw)
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(got.ColorwayId))
	require.Equal(t, []int{first.Id}, wearer())

	// ПЕРЕНАЗНАЧЕНИЕ КРАДЁТ, И КРАЖА В ТОЙ ЖЕ ТРАНЗАКЦИИ: ни в один момент карточка не утверждает
	// про один колорвей две ткани.
	got, err = assign(second.Id, cw)
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(got.ColorwayId))
	require.Equal(t, []int{second.Id}, wearer(),
		"колорвей носит РОВНО ОДНУ ткань — прежняя обязана освободиться")

	// ПОВТОРНОЕ НАЗНАЧЕНИЕ ТОГО ЖЕ — идемпотентно, а не «снял и вернул».
	got, err = assign(second.Id, cw)
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(got.ColorwayId))
	require.Equal(t, []int{second.Id}, wearer())

	// СНЯТИЕ — НАСТОЯЩИЙ ОТВЕТ («пусть носит свой собственный цвет»), а не отсутствие ответа.
	got, err = assign(second.Id, 0)
	require.NoError(t, err)
	require.Zero(t, entity.DesignColorwayOrNone(got.ColorwayId))
	require.Empty(t, wearer())
}

// ФУРНИТУРА ТКАНЬЮ НЕ БЫВАЕТ, А ЧУЖОЙ КОЛОРВЕЙ — НЕ ЭТОЙ КАРТОЧКИ.
//
// Оба отказа названы, а не молчаливы: молния — не то, из чего сделан цвет, и назначить её значит
// попросить невыразимого; чужой колорвей — та же граница, что у всех дверей волны A.
//
// МУТАЦИЯ: снять kind-сторож — hardware начнёт носиться, и «ткань колорвея» перестанет что-либо
// значить.
func TestDesignDBAssetColorwayRefusesHardwareAndForeignColorways(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	other, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	foreign := probeColorway(t, raw, other, "BLK")
	ctx := context.Background()

	hardware, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindHardware, Name: "zip", Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: hardware.Id, ColorwayId: cw, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"молния — не то, из чего сделан цвет")

	// А СНЯТИЕ С ФУРНИТУРЫ НЕ ОТКАЗЫВАЕТСЯ: сторож бьёт по просьбе назначить, а не по нулю.
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: hardware.Id, ColorwayId: 0, Actor: "probe",
	})
	require.NoError(t, err)

	fabric := probeAsset(t, rep, card, "main jersey")
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: fabric.Id, ColorwayId: foreign, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignColorway)

	// И ЧУЖОЙ АССЕТ — NotFound, ровно как у DeleteDesignAsset: карточка в запросе это УБЕЖДЕНИЕ
	// вызывающего о том, на какую полку он смотрит, и расхождение с фактом стоит отказа.
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: other, AssetId: fabric.Id, ColorwayId: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignNotFound)
}

// ─────────────────────── UPSERT НЕ ТРЁТ НАЗНАЧЕНИЕ ───────────────────────

// ЭТО ГЛАВНАЯ ПРОБА ВОЛНЫ B, И ОНА ПРО ТУ ЛОВУШКУ, ЗА КОТОРУЮ УЖЕ ПЛАТИЛИ ДВАЖДЫ.
//
// UpsertAsset — ПОЛНАЯ ЗАМЕНА полей. Положи назначение туда полем — и всякая правка имени, цвета
// или «keep as cloth» приезжала бы с proto3-нулём и МОЛЧА снимала ткань с колорвея; ни один отказ
// при этом не прозвучал бы, потому что ноль в скаляре неотличим от «не заполнено». Ровно
// material_id/norm_marker_id из рецепта колорвея. Поэтому колонка не названа в SET-списке Upsert'а
// вовсе, а пишет её ровно один глагол.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: добавить `colorway_id = :cw` в SET-список UpsertAsset — переименование
// снимет ткань, и проба покраснеет на строке после Upsert'а.
func TestDesignDBUpsertAssetDoesNotClearTheColorwayAssignment(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "main jersey")
	assigned, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: asset.Id, ColorwayId: cw, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(assigned.ColorwayId))

	// Обычное переименование — тот жест, которым назначение и стиралось бы.
	renamed, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, AssetId: asset.Id,
		Kind: entity.DesignAssetKindFabric, Name: "main jersey 220gsm", Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, "main jersey 220gsm", renamed.Name)
	require.Equal(t, cw, entity.DesignColorwayOrNone(renamed.ColorwayId),
		"назначение обязано пережить любую правку ассета — иначе оно живёт до первого сохранения")
}

// ─────────────────────── удаление колорвея: SET NULL и ВЕРДИКТ ───────────────────────

// ТКАНЬ ПЕРЕЖИВАЕТ СВОЕГО НОСИТЕЛЯ, И ОПЕРАТОР ПРЕДУПРЕЖДЁН ЗАРАНЕЕ.
//
// FK — SET NULL по тому же доводу, что у кадра: ассет это АРТЕФАКТ КАРТОЧКИ, а не собственность
// колорвея. И ровно поэтому он обязан быть в вердикте удаления (урок F1, применённый вместе с
// самой колонкой, а не следующим ревью): потеря необратима, а сетка безопасности по MySQL 1451 её
// не видит — она ловит только RESTRICT.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать подсчёт design_asset из readColorwayDeletionFacts.
func TestDesignDBDeletingAColorwayUnassignsItsFabricAndSaysSo(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "main jersey")
	_, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: asset.Id, ColorwayId: cw, Actor: "probe",
	})
	require.NoError(t, err)

	// ВЕРДИКТ НАЗЫВАЕТ ПОТЕРЮ ДО ТОГО, как оператор её подпишет.
	verdict, err := rep.Products().EvaluateColorwayDeletion(ctx, cw)
	require.NoError(t, err)
	require.True(t, verdict.Deletable)
	var named int
	for _, e := range verdict.Orphans {
		if e.Reason == entity.ColorwayOrphanDesignAsset {
			named = e.Count
		}
	}
	require.Equal(t, 1, named,
		"ткань колорвея обязана быть названа в вердикте: 1451 её не поймает, FK — SET NULL")

	// И САМА ПОТЕРЯ ЧЕСТНА: плитка остаётся тканью карточки, просто ничьей.
	_, err = raw.Exec(`DELETE FROM product WHERE id = ?`, cw)
	require.NoError(t, err)
	var alive, colorway any
	require.NoError(t, raw.QueryRow(`SELECT id, colorway_id FROM design_asset WHERE id = ?`, asset.Id).
		Scan(&alive, &colorway))
	require.Nil(t, colorway, "носителя нет — назначение снято")
	require.NotNil(t, alive, "но ткань карточки осталась тканью карточки")
}
