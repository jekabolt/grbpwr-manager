package design_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ «ТКАНИ ПАРЫ (КОЛОРВЕЙ, СЛОТ)» (0368, STEP 3).
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там:
// без CI=1 всё здесь пропускается ДО открытия соединения.
//
// ⚠ ПРЕДМЕТ ЭТИХ ПРОБ — ТО, ЧЕГО GO НЕ ВИДИТ: ключ пары (uq_design_asset_binding), четыре каскада и
// принадлежность трёх id одной карточке, которую схема выразить не может.

// probeBomLine — строка BOM карточки. Уходит каскадом вместе с карточкой (FK tech_card CASCADE),
// поэтому своей чистки ей не нужно.
func probeBomLine(t *testing.T, raw *sql.DB, card int, section, name string) int {
	t.Helper()
	key := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:26]
	res, err := raw.Exec(`INSERT INTO tech_card_bom_item (tech_card_id, section, name, line_key)
		VALUES (?, ?, ?, ?)`, card, section, name, key)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

// patternRunWith — patternRun с заявленным цветом: свотч строится из params.colour, и посадка
// читает оттуда код и hex.
func patternRunWith(t *testing.T, rep dependency.Repository, card, colorway int, pattern, colour map[string]any) *entity.DesignRunStarted {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"pattern": pattern, "colour": colour, "colorway_id": colorway})
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

func bindingsOf(t *testing.T, raw *sql.DB, card int) map[[2]int]int {
	t.Helper()
	rows, err := raw.Query(`SELECT colorway_id, bom_item_id, asset_id FROM design_asset_binding
		WHERE tech_card_id = ?`, card)
	require.NoError(t, err)
	defer rows.Close()
	out := map[[2]int]int{}
	for rows.Next() {
		var cw, bom, asset int
		require.NoError(t, rows.Scan(&cw, &bom, &asset))
		out[[2]int{cw, bom}] = asset
	}
	require.NoError(t, rows.Err())
	return out
}

// ОДНА ТКАНЬ НА ПАРУ, ОДНА ТКАНЬ НА МНОГО ПАР, СНЯТИЕ — ОТВЕТ, А НЕ ОШИБКА.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: заменить upsert на голый INSERT (второй выбор той же пары — 1062);
// скоупить ключ ассетом, а не парой (одна плитка перестанет служить двум слотам); сделать снятие
// пустой пары ошибкой.
func TestDesignDBAssetBindingIsSingleSelectPerPair(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	outer := probeBomLine(t, raw, card, "fabric", "outer")
	lining := probeBomLine(t, raw, card, "lining", "lining")
	ctx := context.Background()

	first := probeAsset(t, rep, card, "twill")
	second := probeAsset(t, rep, card, "poplin")
	bind := func(bom, asset int) (*entity.DesignAssetBinding, error) {
		return rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
			TechCardId: card, ColorwayId: cw, BomItemId: bom, AssetId: asset, SetBy: "probe",
		})
	}

	got, err := bind(outer, first.Id)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, card, got.TechCardId)
	require.Equal(t, cw, got.ColorwayId)
	require.Equal(t, outer, got.BomItemId)
	require.Equal(t, first.Id, got.AssetId)
	require.Equal(t, "probe", got.SetBy)

	// ВТОРОЙ ВЫБОР ТОЙ ЖЕ ПАРЫ — ЗАМЕНА, а не вторая строка и не 1062.
	got, err = bind(outer, second.Id)
	require.NoError(t, err)
	require.Equal(t, second.Id, got.AssetId)
	// ТА ЖЕ ПЛИТКА — ТКАНЬ ВТОРОГО СЛОТА: пара в ключе, плитка нет.
	_, err = bind(lining, second.Id)
	require.NoError(t, err)
	require.Equal(t, map[[2]int]int{{cw, outer}: second.Id, {cw, lining}: second.Id}, bindingsOf(t, raw, card))

	// ПОЛОСА ВЕЗЁТ ВСЕ ПАРЫ ЭТИМ ЖЕ ЧТЕНИЕМ.
	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.Len(t, band.AssetBindings, 2)

	// СНЯТИЕ — НАСТОЯЩИЙ ОТВЕТ; снятие пустой пары — тоже OK и ничего не меняет.
	got, err = bind(outer, 0)
	require.NoError(t, err)
	require.Nil(t, got, "после снятия у пары нет ткани, и ответ это говорит")
	got, err = bind(outer, 0)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, map[[2]int]int{{cw, lining}: second.Id}, bindingsOf(t, raw, card))

	// ПЛИТКА УДАЛЕНА — ПАРА ОСТАЁТСЯ БЕЗ ТКАНИ (FK CASCADE), а не с висящим id.
	_, err = rep.Design().DeleteAsset(ctx, card, second.Id)
	require.NoError(t, err)
	require.Empty(t, bindingsOf(t, raw, card))
	band, err = rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.NotNil(t, band.AssetBindings, "пустая карточка отдаёт [], а не nil — пустое ≠ отсутствующее")
	require.Empty(t, band.AssetBindings)
}

// КАЖДЫЙ ИЗ ТРЁХ ID ПРОВЕРЯЕТСЯ ПРОТИВ КАРТОЧКИ; ФУРНИТУРА (fabrics and hardware bench) БИНДИТСЯ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать любой из трёх сторожей SetAssetBinding — FK примет строку ЧУЖОЙ
// карточки молча, и пара одной карточки назовёт ткань, колорвей либо слот другой.
func TestDesignDBAssetBindingRefusesForeignEnds(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	other, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	foreignCw := probeColorway(t, raw, other, "WHT")
	line := probeBomLine(t, raw, card, "fabric", "outer")
	foreignLine := probeBomLine(t, raw, other, "fabric", "outer")
	ctx := context.Background()

	mine := probeAsset(t, rep, card, "twill")
	theirs := probeAsset(t, rep, other, "twill")
	zip, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindHardware, Name: "zip", Actor: "probe",
	})
	require.NoError(t, err)

	set := func(cwID, bom, asset int) error {
		_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
			TechCardId: card, ColorwayId: cwID, BomItemId: bom, AssetId: asset, SetBy: "probe",
		})
		return err
	}
	require.ErrorIs(t, set(cw, line, theirs.Id), entity.ErrDesignNotFound)
	require.ErrorIs(t, set(foreignCw, line, mine.Id), entity.ErrDesignForeignColorway)
	require.ErrorIs(t, set(cw, foreignLine, mine.Id), entity.ErrDesignForeignBomLine)
	require.ErrorIs(t, set(cw, foreignLine, 0), entity.ErrDesignForeignBomLine,
		"снятие тоже называет карточку: каждый id проверяется против неё")
	require.ErrorIs(t, set(0, line, mine.Id), entity.ErrDesignInvalidArgument)
	require.ErrorIs(t, set(cw, 0, mine.Id), entity.ErrDesignInvalidArgument)
	require.ErrorIs(t, set(cw, line, -1), entity.ErrDesignInvalidArgument)
	require.Empty(t, bindingsOf(t, raw, card), "ни один отказ не оставил строки")

	// Фурнитура — картинка слота фурнитуры: связка принимается.
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	require.NoError(t, set(cw, hwLine, zip.Id))

	// РОД ПРОТИВ СЕМЬИ СТРОКИ: фурнитура не носится слотом ткани, ткань — слотом фурнитуры.
	require.ErrorIs(t, set(cw, line, zip.Id), entity.ErrDesignHardwareOnClothLine)
	require.ErrorIs(t, set(cw, hwLine, mine.Id), entity.ErrDesignClothOnTrimLine)
	threadLine := probeBomLine(t, raw, card, "thread", "topstitch")
	require.ErrorIs(t, set(cw, threadLine, mine.Id), entity.ErrDesignClothOnTrimLine)
	require.NoError(t, set(cw, line, 0), "снятие род не судит")
	require.Equal(t, map[[2]int]int{{cw, hwLine}: zip.Id}, bindingsOf(t, raw, card),
		"ни один отказ семьи не оставил строки")
}

// SetAssetBinding НЕ СОБИРАЕТ МУСОР НИ НА ЗАМЕНЕ, НИ НА СНЯТИИ: клиент предлагает UNDO, перепривязывая
// прежний id, и прежний ассет обязан пережить и то, и другое.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть dropSupersededHardwareTx в SetAssetBinding — UNDO после замены
// или снятия снимка фурнитуры получит NotFound на ассете, которого больше нет.
func TestDesignDBAssetBindingNeverDropsAnAsset(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	outer := probeBomLine(t, raw, card, "fabric", "outer")
	ctx := context.Background()

	hw := func(name string) int {
		a, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
			TechCardId: card, Kind: entity.DesignAssetKindHardware, Name: name, Actor: "probe",
		})
		require.NoError(t, err)
		return a.Id
	}
	set := func(cwID, bom, asset int) {
		t.Helper()
		_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
			TechCardId: card, ColorwayId: cwID, BomItemId: bom, AssetId: asset, SetBy: "probe",
		})
		require.NoError(t, err)
	}
	alive := func(id int) bool {
		t.Helper()
		var n int
		require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_asset WHERE id = ?`, id).Scan(&n))
		return n == 1
	}

	first, second := hw("zip 1"), hw("zip 2")
	set(cw, hwLine, first)
	set(cw, hwLine, second)
	require.True(t, alive(first), "заменённый снимок остаётся на полке — UNDO вернёт его")
	set(cw, hwLine, first) // UNDO замены
	require.Equal(t, map[[2]int]int{{cw, hwLine}: first}, bindingsOf(t, raw, card))

	set(cw, hwLine, 0)
	require.True(t, alive(first), "снятый снимок остаётся на полке — UNDO вернёт его")
	require.True(t, alive(second))
	set(cw, hwLine, first) // UNDO снятия

	// ТКАНЬ — ТОЖЕ.
	cloth := probeAsset(t, rep, card, "twill")
	set(cw, outer, cloth.Id)
	set(cw, outer, 0)
	require.True(t, alive(cloth.Id))
	require.Equal(t, map[[2]int]int{{cw, hwLine}: first}, bindingsOf(t, raw, card))
}

// ПОСАДКА ПРОГОНА ФУРНИТУРЫ ЗАМЕНЯЕТ СНИМОК ПАРЫ И УДАЛЯЕТ ПРЕЖНИЙ, ЕСЛИ ОН СИРОТА.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать dropSupersededHardwareTx из посадки (единственное место GC).
func TestDesignDBAHardwareRunLandingDropsTheSupersededPicture(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	resetBudget(t, raw)

	land := func(name string) int {
		started := patternRunWith(t, rep, card, cw, map[string]any{
			"name": name, "mode": entity.DesignPatternModeHardware, "bom_item_id": hwLine,
		}, map[string]any{"words": "horn button"})
		done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
		require.NoError(t, err)
		require.Equal(t, entity.DesignRunDone, done.Status)
		var id int
		require.NoError(t, raw.QueryRow(`SELECT MAX(id) FROM design_asset WHERE tech_card_id = ?`, card).Scan(&id))
		return id
	}
	first := land("button 1")
	require.Equal(t, map[[2]int]int{{cw, hwLine}: first}, bindingsOf(t, raw, card))
	second := land("button 2")
	require.Equal(t, map[[2]int]int{{cw, hwLine}: second}, bindingsOf(t, raw, card))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_asset WHERE id = ?`, first).Scan(&n))
	require.Zero(t, n, "прежний снимок фурнитуры пары удалён посадкой")
}

// СНЯТИЕ С ПРОПАВШЕЙ СТРОКИ BOM — OK: СОСТОЯНИЕ, О КОТОРОМ ПРОСЯТ, УЖЕ НАСТУПИЛО (ревью STEP 3).
//
// Строку удалили, пока экран был открыт, пара ушла каскадом, и человек жмёт «снять» на устаревшем
// экране. foreign_bom_line сказал бы «чужая строка» о строке, которой нет ни у кого.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: вернуть отказ пропавшей строке на снятии; ослабить правило до «снятие не
// спрашивает строку вовсе» (строка ДРУГОЙ карточки обязана отказывать и на снятии); пустить
// привязку на пропавшую строку (FK уронил бы её 1452 — отказ обязан прийти раньше, словом).
func TestDesignDBAssetBindingUnbindOfAVanishedLineIsOK(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	other, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	line := probeBomLine(t, raw, card, "fabric", "outer")
	foreignLine := probeBomLine(t, raw, other, "fabric", "outer")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "twill")
	set := func(bom, assetID int) (*entity.DesignAssetBinding, error) {
		return rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
			TechCardId: card, ColorwayId: cw, BomItemId: bom, AssetId: assetID, SetBy: "probe",
		})
	}
	_, err := set(line, asset.Id)
	require.NoError(t, err)
	require.Equal(t, map[[2]int]int{{cw, line}: asset.Id}, bindingsOf(t, raw, card))

	_, err = raw.Exec(`DELETE FROM tech_card_bom_item WHERE id = ?`, line)
	require.NoError(t, err)
	require.Empty(t, bindingsOf(t, raw, card), "пара ушла со строкой каскадом")

	got, err := set(line, 0)
	require.NoError(t, err, "снятие с пропавшей строки — то состояние, о котором просят")
	require.Nil(t, got)
	got, err = set(line, 0)
	require.NoError(t, err, "и повтор тоже OK")
	require.Nil(t, got)

	require.ErrorIs(t, func() error { _, err := set(line, asset.Id); return err }(),
		entity.ErrDesignForeignBomLine, "ПРИВЯЗАТЬ к пропавшей строке нельзя — это не снятие")
	require.ErrorIs(t, func() error { _, err := set(foreignLine, 0); return err }(),
		entity.ErrDesignForeignBomLine, "строка, которая есть у ДРУГОЙ карточки, отказывает и на снятии")
	require.Empty(t, bindingsOf(t, raw, card))
}

// КАРТОЧКА БЕЗ ПРИВЯЗОК ОТДАЁТ [], А НЕ nil — И НЕ ЧУЖИЕ ПРИВЯЗКИ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: убрать нормализацию nil → [] в listAssetBindings (провод скажет «этот
// бинарь не знает привязок» вместо «ещё ничего не привязано»); потерять WHERE tech_card_id (полоса
// свежей карточки понесёт пары соседа).
func TestDesignDBAFreshCardBandCarriesAnEmptyBindingList(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	neighbour, _, _ := designProbeCard(t, rep, raw)
	ctx := context.Background()

	// У СОСЕДА ПРИВЯЗКА ЕСТЬ: пустота свежей карточки — это её пустота, а не пустота таблицы.
	ncw := probeColorway(t, raw, neighbour, "BLK")
	nline := probeBomLine(t, raw, neighbour, "fabric", "outer")
	nasset := probeAsset(t, rep, neighbour, "twill")
	_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
		TechCardId: neighbour, ColorwayId: ncw, BomItemId: nline, AssetId: nasset.Id, SetBy: "probe",
	})
	require.NoError(t, err)

	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.NotNil(t, band.AssetBindings, "пустое ≠ отсутствующее: свежая карточка отдаёт []")
	require.Empty(t, band.AssetBindings)

	band, err = rep.Design().GetBand(ctx, neighbour, 1)
	require.NoError(t, err)
	require.Len(t, band.AssetBindings, 1, "положительный контроль: у соседа пара читается")
}

// УДАЛЕНИЕ ПЛИТКИ, НОСИМОЙ ПАРАМИ: ВЫЗОВ УДАЁТСЯ, ЕЁ ПАРЫ УХОДЯТ, ЧУЖИЕ ОСТАЮТСЯ — И ЧИСЛО
// СНЯТЫХ ПАР СОСЧИТАНО ДО УДАЛЕНИЯ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: считать пары ПОСЛЕ оператора (каскад их уже унёс — в журнале 0 и записи
// нет вовсе); считать по карточке, а не по ассету (в числе окажется пара соседней плитки); снимать
// лишнее (пара другой плитки той же карточки обязана пережить удаление).
func TestDesignDBDeletingABoundAssetUnbindsItsPairsCountedFirst(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	outer := probeBomLine(t, raw, card, "fabric", "outer")
	lining := probeBomLine(t, raw, card, "lining", "lining")
	pocket := probeBomLine(t, raw, card, "fabric", "pocket")
	ctx := context.Background()

	doomed := probeAsset(t, rep, card, "twill")
	kept := probeAsset(t, rep, card, "poplin")
	for bom, asset := range map[int]int{outer: doomed.Id, lining: doomed.Id, pocket: kept.Id} {
		_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
			TechCardId: card, ColorwayId: cw, BomItemId: bom, AssetId: asset, SetBy: "probe",
		})
		require.NoError(t, err)
	}

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	removed, err := rep.Design().DeleteAsset(ctx, card, doomed.Id)
	require.NoError(t, err, "плитка, носимая парами, удаляется: пары уходят каскадом")
	require.Zero(t, removed, "меток на флетах у плитки не было")
	require.Equal(t, map[[2]int]int{{cw, pocket}: kept.Id}, bindingsOf(t, raw, card),
		"ушли ровно пары удалённой плитки")

	type deleteLog struct {
		Msg     string `json:"msg"`
		AssetID int    `json:"asset_id"`
		Removed int    `json:"removed_bindings"`
	}
	var said *deleteLog
	for _, line := range bytes.Split(bytes.TrimSpace(logged.Bytes()), []byte("\n")) {
		var e deleteLog
		if json.Unmarshal(line, &e) == nil && e.AssetID == doomed.Id && strings.Contains(e.Msg, "slot fabrics") {
			said = &e
			break
		}
	}
	require.NotNil(t, said, "удаление, снявшее ткань со слотов, обязано это сказать: %s", logged.String())
	require.Equal(t, 2, said.Removed, "обе пары сосчитаны ДО оператора, после которого считать нечего")
}

// ПЛИТКА, СДЕЛАННАЯ ДЛЯ ПАРЫ, САДИТСЯ ТКАНЬЮ ЭТОЙ ПАРЫ — И ПОМНИТ СВОЙ ЦВЕТ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: не писать связку в keepPatternTx (свотч садится на полку, но слот его не
// носит); писать связку «только если пусто» (второй свотч не перепривязывает пару); не писать
// colour_code/colour_hex (свотч на полке — безымянный квадрат цвета); вернуть слотовому прогону
// легаси-кражу и запись design_asset.colorway_id (колонка прыгала бы на самый свежий свотч слота и
// отнимала колорвей у ткани, назначенной руками).
func TestDesignDBAPatternRunMadeForASlotBINDS_THE_PAIR(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	outer := probeBomLine(t, raw, card, "fabric", "outer")
	resetBudget(t, raw)
	ctx := context.Background()

	// ТКАНЬ ВСЕГО КОЛОРВЕЯ, НАЗНАЧЕННАЯ РУКАМИ (legacy-колонка). Свотч слота её не отнимает.
	legacy := probeAsset(t, rep, card, "old jersey")
	_, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: legacy.Id, ColorwayId: cw,
	})
	require.NoError(t, err)
	colorwayOf := func(asset int) sql.NullInt64 {
		t.Helper()
		var v sql.NullInt64
		require.NoError(t, raw.QueryRow(`SELECT colorway_id FROM design_asset WHERE id = ?`, asset).Scan(&v))
		return v
	}

	land := func(name string) int {
		started := patternRunWith(t, rep, card, cw, map[string]any{
			"name": name, "mode": entity.DesignPatternModeSwatch, "bom_item_id": outer,
		}, map[string]any{"code": "18-1664 TCX", "hex": " #C8102E ", "words": "Pantone Fiery Red · outer"})
		done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
		require.NoError(t, err)
		require.Equal(t, entity.DesignRunDone, done.Status)
		var id int
		require.NoError(t, raw.QueryRow(`SELECT MAX(id) FROM design_asset WHERE tech_card_id = ?`, card).Scan(&id))
		return id
	}

	first := land("black · outer")
	require.Equal(t, map[[2]int]int{{cw, outer}: first}, bindingsOf(t, raw, card))
	// ⚠ LEGACY-КОЛОНКА НЕ ПРЫГАЕТ (ревью STEP 3): свотч слота — ткань ПАРЫ, а не колорвея целиком,
	// поэтому ни кражи, ни записи design_asset.colorway_id у слотового прогона нет.
	require.False(t, colorwayOf(first).Valid, "свотч слота не пишет себя тканью всего колорвея")
	require.Equal(t, int64(cw), colorwayOf(legacy.Id).Int64, "и не отнимает колорвей у ткани, назначенной руками")
	var code, hex sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT colour_code, colour_hex FROM design_asset WHERE id = ?`, first).
		Scan(&code, &hex))
	require.Equal(t, "18-1664 TCX", code.String)
	require.Equal(t, "#C8102E", hex.String, "обрезано по краям, как у UpsertAsset")

	// САМЫЙ СВЕЖИЙ СВОТЧ — ТО, О ЧЁМ ТОЛЬКО ЧТО ПОПРОСИЛИ: пара перепривязывается, прежний остаётся
	// на полке кандидатом.
	second := land("black · outer 2")
	require.Equal(t, map[[2]int]int{{cw, outer}: second}, bindingsOf(t, raw, card))
	var alive int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_asset WHERE id = ?`, first).Scan(&alive))
	require.Equal(t, 1, alive)
	require.False(t, colorwayOf(second).Valid, "второй свотч слота — тоже только ткань пары")
	require.Equal(t, int64(cw), colorwayOf(legacy.Id).Int64,
		"legacy-колонка не перескакивает на «самый свежий свотч слота»")
}

// СЛОТ, ПРОПАВШИЙ МЕЖДУ ДВЕРЬЮ И ПРИЛЁТОМ, НЕ РОНЯЕТ ОПЛАЧЕННУЮ ПОСАДКУ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать перечитывание строки BOM в bindKeptPatternTx — вставка связки
// упадёт внешним ключом и откатит ВСЮ выдачу, за которую уже заплачено.
func TestDesignDBAPatternRunWhoseSlotVanishedSTILL_LANDS(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	other, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	gone := probeBomLine(t, raw, card, "fabric", "outer")
	foreign := probeBomLine(t, raw, other, "fabric", "outer")
	resetBudget(t, raw)

	for _, bom := range []int{gone, foreign} {
		started := patternRunWith(t, rep, card, cw, map[string]any{
			"name": "tile", "mode": entity.DesignPatternModeSwatch, "bom_item_id": bom,
		}, map[string]any{"hex": "#000000"})
		if bom == gone {
			_, err := raw.Exec(`DELETE FROM tech_card_bom_item WHERE id = ?`, gone)
			require.NoError(t, err)
		}
		done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
		require.NoError(t, err, "слот — не повод выбрасывать оплаченную плитку")
		require.Equal(t, entity.DesignRunDone, done.Status)
	}
	require.Len(t, shelfOf(t, raw, card), 2, "обе плитки на полке")
	require.Empty(t, bindingsOf(t, raw, card), "ни одна пара не перепривязана на чужой либо пропавший слот")
}

// СТРОКА, СМЕНИВШАЯ СЕМЬЮ МЕЖДУ ДВЕРЬЮ И ПРИЛЁТОМ: ОПЛАЧЕННЫЙ АССЕТ — НА ПОЛКУ НИЧЬИМ, ПРЕЖНЯЯ
// СВЯЗКА ПАРЫ НЕ ТРОНУТА И НЕ СОБРАНА.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: убрать пересуживание семьи в bindKeptPatternTx (снимок фурнитуры ляжет на
// рулонную строку, плитка — на строку фурнитуры, а прежний снимок пары будет удалён GC).
func TestDesignDBARunLandingOnALineThatChangedFamilyStaysUnbound(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	outer := probeBomLine(t, raw, card, "fabric", "outer")
	resetBudget(t, raw)

	lastAsset := func() int {
		t.Helper()
		var id int
		require.NoError(t, raw.QueryRow(`SELECT MAX(id) FROM design_asset WHERE tech_card_id = ?`, card).Scan(&id))
		return id
	}
	land := func(started *entity.DesignRunStarted, moveTo string, line int) int {
		t.Helper()
		if moveTo != "" {
			_, err := raw.Exec(`UPDATE tech_card_bom_item SET section = ? WHERE id = ?`, moveTo, line)
			require.NoError(t, err)
		}
		done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
		require.NoError(t, err, "смена семьи — не повод выбрасывать оплаченный ассет")
		require.Equal(t, entity.DesignRunDone, done.Status)
		return lastAsset()
	}
	hwRun := func() *entity.DesignRunStarted {
		return patternRunWith(t, rep, card, cw, map[string]any{
			"name": "button", "mode": entity.DesignPatternModeHardware, "bom_item_id": hwLine,
		}, map[string]any{"words": "horn button"})
	}
	swRun := func() *entity.DesignRunStarted {
		return patternRunWith(t, rep, card, cw, map[string]any{
			"name": "tile", "mode": entity.DesignPatternModeSwatch, "bom_item_id": outer,
		}, map[string]any{"hex": "#000000"})
	}

	firstHw := land(hwRun(), "", 0)
	firstSw := land(swRun(), "", 0)
	want := map[[2]int]int{{cw, hwLine}: firstHw, {cw, outer}: firstSw}
	require.Equal(t, want, bindingsOf(t, raw, card))

	// ФУРНИТУРА ПРИЛЕТАЕТ НА СТРОКУ, СТАВШУЮ РУЛОННОЙ.
	strayHw := land(hwRun(), "fabric", hwLine)
	// ПЛИТКА ПРИЛЕТАЕТ НА СТРОКУ, СТАВШУЮ ФУРНИТУРОЙ.
	straySw := land(swRun(), "hardware", outer)

	require.Equal(t, want, bindingsOf(t, raw, card), "прежние связки пар не тронуты")
	for _, id := range []int{firstHw, firstSw, strayHw, straySw} {
		var n int
		require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_asset WHERE id = ?`, id).Scan(&n))
		require.Equal(t, 1, n, "asset %d на полке", id)
	}
}

// КОЛОРВЕЙ, НОСЯЩИЙ ТКАНЬ СЛОТА: ВЕРДИКТ УДАЛЕНИЯ НАЗЫВАЕТ ЕЁ, ПЕРЕПРИВЯЗКА ОТКАЗЫВАЕТ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать design_asset_binding из readColorwayDeletionFacts (диалог удаления
// промолчит о тканях слотов — сетка 1451 CASCADE не видит) либо из designColorwayHolders (колорвей
// уедет на чужой стиль, оставив здесь пару, которая его называет).
func TestDesignDBColourwayWearingASlotFabricIsNamedAndHeld(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	target := probeCard(t, raw)
	cw := draftColorway(t, raw, card, "BLK")
	line := probeBomLine(t, raw, card, "fabric", "outer")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "twill")
	_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
		TechCardId: card, ColorwayId: cw, BomItemId: line, AssetId: asset.Id, SetBy: "probe",
	})
	require.NoError(t, err)

	verdict, err := rep.Products().EvaluateColorwayDeletion(ctx, cw)
	require.NoError(t, err)
	var named int
	for _, e := range verdict.Cascade {
		if e.Reason == entity.ColorwayCascadeDesignAssetBinding {
			named = e.Count
		}
	}
	require.Equal(t, 1, named, "ткань слота уходит с колорвеем, и оператор обязан это увидеть")

	err = rep.Products().RelinkDraftColorway(ctx, cw, target,
		styleLock(t, raw, card), styleLock(t, raw, target))
	require.ErrorIs(t, err, entity.ErrColorwayHasDesignRows,
		"пара (колорвей, слот) держит колорвей на этой карточке")
}

// ПЕРЕЗАКАЗ СНИМКА НА ТУ ЖЕ ПАРУ УВОЗИТ МЕТКИ ФЛЭТА НА НОВЫЙ СНИМОК (B-m2), А НЕ ТЕРЯЕТ ИХ КАСКАДОМ.
// Снимок, который носит ЕЩЁ И другая пара, своих меток не отдаёт и на полке остаётся.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: убрать UPDATE design_asset_placement из bindKeptPatternTx (метка уйдёт
// каскадом вместе с GC прежнего снимка); снять условие «связан только с этой парой» (метки снимка
// второй пары уедут на чужой ассет).
func TestDesignDBAHardwareRunLandingMovesTheFlatMarks(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	cw2 := probeColorway(t, raw, card, "WHT")
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	flat := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	resetBudget(t, raw)
	ctx := context.Background()

	land := func(name string) int {
		started := patternRunWith(t, rep, card, cw, map[string]any{
			"name": name, "mode": entity.DesignPatternModeArtwork, "bom_item_id": hwLine,
		}, map[string]any{"words": "crest"})
		done, err := landPatternRun(t, rep, started.Run.Id, probeMedia(t, raw))
		require.NoError(t, err)
		require.Equal(t, entity.DesignRunDone, done.Status)
		require.False(t, done.ErrorCode.Valid)
		var id int
		require.NoError(t, raw.QueryRow(`SELECT MAX(id) FROM design_asset WHERE tech_card_id = ?`, card).Scan(&id))
		return id
	}
	mark := func(asset int) int {
		m, err := rep.Design().SetAssetPlacement(ctx, entity.DesignAssetPlacementSet{
			TechCardId: card, AssetId: asset, PictureId: flat.Id,
			Annotation: probeAnnotation(), Actor: "probe",
		})
		require.NoError(t, err)
		return m.Id
	}
	ownerOf := func(markID int) int {
		var a int
		require.NoError(t, raw.QueryRow(`SELECT asset_id FROM design_asset_placement WHERE id = ?`, markID).Scan(&a))
		return a
	}

	first := land("crest 1")
	m1 := mark(first)
	second := land("crest 2")
	require.Equal(t, second, ownerOf(m1), "метка пары переехала на новый снимок")
	require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, first),
		"прежний снимок собран после переезда меток")

	// ВТОРАЯ ПАРА НОСИТ ТОТ ЖЕ СНИМОК: его метки — её, и они остаются.
	_, err := rep.Design().SetAssetBinding(ctx, entity.DesignAssetBindingSet{
		TechCardId: card, ColorwayId: cw2, BomItemId: hwLine, AssetId: second, SetBy: "probe",
	})
	require.NoError(t, err)
	third := land("crest 3")
	require.Equal(t, second, ownerOf(m1), "снимок другой пары меток не отдаёт")
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, second))
	require.Equal(t, map[[2]int]int{{cw, hwLine}: third, {cw2, hwLine}: second}, bindingsOf(t, raw, card))
}

// ПОЛКА НА 120 ПРИНИМАЕТ ПЕРЕЗАКАЗ СНИМКА, КОТОРЫЙ СОБЕРЁТ ПРЕЖНИЙ (B-m1): посадка меняет строку на
// строку, а не прибавляет. Посадка на пару, чей прежний снимок ОСТАЁТСЯ (его носит ещё колорвей
// другой пары), на 120 по-прежнему пишет library_full.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть refuseFullShelf к брутто-счёту (freeing = 0) в keepPatternTx.
func TestDesignDBAFullShelfTakesAReplacementThatFreesARow(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	cw2 := probeColorway(t, raw, card, "WHT")
	hwLine := probeBomLine(t, raw, card, "hardware", "closure")
	resetBudget(t, raw)
	ctx := context.Background()

	start := func(colorway int, name string) int {
		return patternRunWith(t, rep, card, colorway, map[string]any{
			"name": name, "mode": entity.DesignPatternModeHardware, "bom_item_id": hwLine,
		}, map[string]any{"words": "horn button"}).Run.Id
	}
	first := start(cw, "button 1")
	done, err := landPatternRun(t, rep, first, probeMedia(t, raw))
	require.NoError(t, err)
	require.False(t, done.ErrorCode.Valid)
	var prev int
	require.NoError(t, raw.QueryRow(`SELECT MAX(id) FROM design_asset WHERE tech_card_id = ?`, card).Scan(&prev))

	// Второй пары у cw2 нет: её посадка на полной полке ничего не освобождает.
	replace, fresh := start(cw, "button 2"), start(cw2, "button 3")
	for i := len(shelfOf(t, raw, card)); i < entity.MaxDesignAssetsPerCard; i++ {
		_, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
			TechCardId: card, Kind: entity.DesignAssetKindFabric,
			Name: "cloth " + uuid.NewString()[:8], Actor: "probe",
		})
		require.NoError(t, err)
	}
	require.Len(t, shelfOf(t, raw, card), entity.MaxDesignAssetsPerCard)

	done, err = landPatternRun(t, rep, fresh, probeMedia(t, raw))
	require.NoError(t, err)
	require.True(t, done.ErrorCode.Valid, "посадка без замены на полной полке — library_full")
	require.Equal(t, entity.DesignErrorCodeLibraryFull, done.ErrorCode.String)

	done, err = landPatternRun(t, rep, replace, probeMedia(t, raw))
	require.NoError(t, err)
	require.False(t, done.ErrorCode.Valid, "замена собираемого снимка полку не переполняет")
	require.Len(t, shelfOf(t, raw, card), entity.MaxDesignAssetsPerCard, "строка за строку")
	require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM design_asset WHERE id = ?`, prev))
}
