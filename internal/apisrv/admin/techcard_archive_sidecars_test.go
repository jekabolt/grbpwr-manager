package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// Ф1.3 — сайдкары архива тех-карты. Проверяется ровно то, ради чего этот код существует:
// отсутствие данных даёт ПУСТОЙ сайдкар, битая ссылка даёт ДЫРУ и сбор продолжается, а отказ
// инфраструктуры хоронит весь экспорт.

// archiveTestDictionary — словарь размеров и мерок, общий на все случаи ниже.
func archiveTestDictionary() *entity.DictionaryInfo {
	return &entity.DictionaryInfo{
		Sizes: []entity.Size{
			{Id: 3, Name: "s"},
			{Id: 4, Name: "m"},
			{Id: 5, Name: "l"},
		},
		Measurements: []entity.MeasurementName{
			{Id: 11, Name: "chest"},
			{Id: 12, Name: "length"},
		},
	}
}

// archiveTestObject отдаёт объект бакета как поток, чтобы мок FileStore вёл себя как настоящий.
func archiveTestObject(body string) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
}

// archiveHoleReasons — коды дыр в порядке появления; тесты сравнивают именно коды, потому что
// detail это свободный текст без контракта.
func archiveHoleReasons(holes []techcardarchive.ExportHole) []techcardarchive.Reason {
	out := make([]techcardarchive.Reason, 0, len(holes))
	for _, h := range holes {
		out = append(out, h.Reason)
	}
	return out
}

// Карточка, у которой ничего нет, обязана дать ПУСТЫЕ индексы — и ни одного запроса за тем, чего
// на ней не заведено: ни строки медиа, ни каталога материалов. Пустой индекс и «парсер молча
// ничего не собрал» различает manifest.contents, и он считается по этим спискам.
func TestArchiveSidecarsEmptyCardYieldsEmptyIndexes(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	cache := mocks.NewMockCache(t)
	repo.EXPECT().TechCards().Return(cards)
	repo.EXPECT().Cache().Return(cache)
	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveTestDictionary(), nil)
	cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{StyleID: 7}, nil)
	cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return(nil, nil)

	// Ни Media(), ни FileStore, ни ListMaterials не ожидаются вовсе: мок падает на незаявленном
	// вызове, поэтому «ничего лишнего не спросили» проверяется самим фактом зелёного теста.
	sc, err := (&Server{repo: repo}).collectArchiveSidecars(t.Context(), &entity.TechCard{Id: 7})
	require.NoError(t, err)
	defer sc.Close()

	require.Empty(t, sc.SizeChart.Cells)
	require.NotNil(t, sc.SizeChart.Cells, "пустая таблица едет как [], а не как null")
	require.Empty(t, sc.Assembly)
	require.Empty(t, sc.Colorways)
	require.Empty(t, sc.Materials)
	require.Empty(t, sc.Media)
	require.Empty(t, sc.Patterns)
	require.Empty(t, sc.Markers)
	require.Empty(t, sc.MarkerFiles)
	require.Empty(t, sc.Blobs)
	require.Empty(t, sc.Holes)
}

// Полная карточка: обе оси размерной таблицы именами, компонент сборки по номеру стиля, рецепт без
// единого денежного поля, дедуп одинаковых байтов, выкройка без размера с null и раскладка, из
// которой вычищена ссылка на наш CDN.
func TestArchiveSidecarsFullCardTravelsByNameAndWithoutMoney(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	cache := mocks.NewMockCache(t)
	media := mocks.NewMockMedia(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().TechCards().Return(cards)
	repo.EXPECT().Cache().Return(cache)
	repo.EXPECT().Media().Return(media)
	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveTestDictionary(), nil)

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, LineKey: "BOM-SHELL", Name: "основная ткань",
		Section:    entity.BomSectionFabric,
		MaterialId: sql.NullInt64{Int64: 8120, Valid: true},
		Unit:       sql.NullString{String: "m", Valid: true},
	}}
	card.Pieces = []entity.TechCardPiece{{
		Id: 900, LineKey: "PIECE-FRONT", Name: "полочка",
		Materials: []entity.TechCardPieceMaterial{{
			ColorwayID: 812, BomLineKey: "BOM-SHELL", Note: sql.NullString{String: "долевая", Valid: true},
		}},
	}}
	card.Colorways = []entity.TechCardColorway{{
		Id: 812, ColorCode: "BLK", BaseSku: sql.NullString{String: "GRB-014-BLK", Valid: true},
		// Деньги колорвея заполнены НАРОЧНО: они не должны найти дорогу в сайдкар.
		CostPrice: decimal.NullDecimal{Decimal: decimal.RequireFromString("41.20"), Valid: true},
		Prices:    []entity.ColorwayPrice{{Currency: "EUR", Price: decimal.RequireFromString("300")}},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:         sql.NullInt64{Int64: 501, Valid: true},
			MaterialId:        sql.NullInt64{Int64: 8120, Valid: true},
			Placement:         sql.NullString{String: "outer", Valid: true},
			Color:             sql.NullString{String: "black", Valid: true},
			Consumption:       decimal.NullDecimal{Decimal: decimal.RequireFromString("1.42"), Valid: true},
			ConsumptionSource: sql.NullString{String: "marker", Valid: true},
			WasteCutPct:       decimal.NullDecimal{Decimal: decimal.RequireFromString("12.4"), Valid: true},
			NormMarkerId:      sql.NullInt64{Int64: 77, Valid: true},
			SizeConsumptions: []entity.TechCardBomSizeConsumption{
				{SizeId: 3, Consumption: decimal.RequireFromString("1.38")},
				{SizeId: 4, Consumption: decimal.RequireFromString("1.42")},
			},
		}},
	}}
	card.Media = []entity.TechCardMediaItem{{
		MediaId: 4020, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
		Caption: sql.NullString{String: "front flat", Valid: true},
	}}
	card.Callouts = []entity.TechCardCallout{{Number: 1, MediaId: sql.NullInt32{Int32: 4021, Valid: true}}}
	card.Patterns = []entity.TechCardSizePattern{
		{
			LineKey: "PAT-FRONT", SizeId: 0, Version: 3,
			URL:           "https://cdn.grbpwr.com/tech-card-patterns/front_v3.dxf",
			Filename:      sql.NullString{String: "front_v3.dxf", Valid: true},
			Name:          sql.NullString{String: "перед", Valid: true},
			FabricPurpose: sql.NullString{String: string(entity.BomPurposeMain), Valid: true},
		},
		{
			LineKey: "PAT-BACK", SizeId: 4, Version: 1,
			URL: "https://cdn.grbpwr.com/tech-card-patterns/back_v1.dxf",
		},
	}
	card.Markers = []entity.TechCardMarkerSummary{{
		Id: 77, TechCardId: 7, Name: "shell 150 cm",
		SizeId:       sql.NullInt64{Int64: 4, Valid: true},
		Sets:         sql.NullInt64{Int64: 2, Valid: true},
		BomItemId:    sql.NullInt64{Int64: 501, Valid: true},
		BomLineKey:   sql.NullString{String: "BOM-SHELL", Valid: true},
		UsedLengthCm: decimal.RequireFromString("300"),
	}}

	cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{
		StyleID: 7,
		Cells: []entity.StyleSizeChartCell{
			{SizeID: 3, MeasurementNameID: 11, Value: decimal.RequireFromString("50")},
			{SizeID: 4, MeasurementNameID: 11, Value: decimal.RequireFromString("52")},
		},
		GradeBaseSizeID: 4,
		GradeSteps:      []entity.StyleSizeChartGradeStep{{MeasurementNameID: 11, Step: decimal.RequireFromString("2")}},
	}, nil)
	cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return([]entity.StyleAssembly{{
		Id: 1, StyleId: 7, ComponentTechCardId: 902, Qty: decimal.RequireFromString("1"),
		PrintNote: sql.NullString{String: "brand logo", Valid: true}, Active: true,
	}}, nil)
	component := &entity.TechCard{Id: 902}
	component.StyleNumber = sql.NullString{String: "GRB-AUX-0012", Valid: true}
	cards.EXPECT().GetTechCardById(mock.Anything, 902).Return(component, nil)
	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{{
		Material: entity.Material{Id: 8120, MaterialInsert: entity.MaterialInsert{
			Name: "wool melton 320", Code: sql.NullString{String: "F-WOOL-320", Valid: true},
			Unit: sql.NullString{String: "m", Valid: true}, MaterialClass: string(entity.MaterialClassFabric),
			CuttingCoefficient: decimal.NullDecimal{Decimal: decimal.RequireFromString("1.03"), Valid: true},
			FabricAttr:         &entity.MaterialFabricAttr{SelvedgeCm: decimal.RequireFromString("1.5")},
		}},
		// Цена заполнена НАРОЧНО: у паспорта нет поля, куда её положить, и это проверяется тем, что
		// сериализованный паспорт её не содержит.
		LatestPrice: &entity.MaterialPrice{MaterialId: 8120, Price: decimal.RequireFromString("18.40"), Currency: "EUR"},
	}}, nil)

	// Две карточки медиа с ОДИНАКОВЫМИ байтами: в архиве обязан оказаться один файл.
	media.EXPECT().GetMediaByIds(mock.Anything, []int{4020, 4021}).Return(map[int]entity.MediaFull{
		4020: {Id: 4020, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/a.jpg",
			FullSizeWidth:    2400, FullSizeHeight: 3200,
		}},
		4021: {Id: 4021, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/b.jpg",
			FullSizeWidth:    800, FullSizeHeight: 600,
		}},
	}, nil)
	files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/a.jpg").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("same-bytes") }).Maybe()
	files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/b.jpg").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("same-bytes") }).Maybe()
	files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/front_v3.dxf").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("DXF-FRONT") }).Maybe()
	files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/back_v1.dxf").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("DXF-BACK") }).Maybe()

	// В блобе раскладки лежит ссылка на CDN экспортирующего инстанса — она обязана погаснуть.
	layout, err := protojson.Marshal(&pb_common.TechCardMarkerLayout{
		SchemaVersion: 3,
		Pieces: []*pb_common.TechCardMarkerPiece{{
			PieceId: 1, Name: "FP_L", Quantity: 1, SizeId: 4,
			SourceUrl: "https://cdn.grbpwr.com/tech-card-patterns/front_v3.dxf",
		}},
	})
	require.NoError(t, err)
	cards.EXPECT().GetMarker(mock.Anything, 77).Return(&entity.TechCardMarker{
		TechCardMarkerSummary: card.Markers[0], Layout: string(layout),
	}, nil)

	sc, err := (&Server{repo: repo, bucket: files}).collectArchiveSidecars(t.Context(), card)
	require.NoError(t, err)
	defer sc.Close()
	require.Empty(t, sc.Holes, "на здоровой карточке дыр быть не должно")

	// sizechart: обе оси именами, ни style_id, ни lock_version.
	require.Equal(t, []techcardarchive.SizeChartCell{
		{SizeName: "s", Measurement: "chest", Value: "50"},
		{SizeName: "m", Measurement: "chest", Value: "52"},
	}, sc.SizeChart.Cells)
	require.Equal(t, "m", sc.SizeChart.GradeBaseSizeName)
	require.Equal(t, []techcardarchive.SizeChartGradeStep{{Measurement: "chest", Step: "2"}}, sc.SizeChart.GradeSteps)

	// assembly: компонент по номеру стиля, размер null = на все размеры.
	require.Len(t, sc.Assembly, 1)
	require.Equal(t, "GRB-AUX-0012", sc.Assembly[0].ComponentStyleNumber)
	require.Nil(t, sc.Assembly[0].SizeName)
	require.Equal(t, "1", sc.Assembly[0].Qty)

	// colorways: строка адресована line_key'ями, пер-размерный расход по ИМЕНАМ размеров.
	require.Len(t, sc.Colorways, 1)
	cw := sc.Colorways[0]
	require.Equal(t, "BLK", cw.ColorCode)
	require.Len(t, cw.Recipe, 1)
	require.Equal(t, "BOM-SHELL", cw.Recipe[0].BomLineKey)
	require.Equal(t, int64(8120), cw.Recipe[0].MaterialRef)
	require.Equal(t, map[string]string{"s": "1.38", "m": "1.42"}, cw.Recipe[0].SizeConsumptions)
	require.Equal(t, []techcardarchive.PieceMaterialLine{
		{PieceLineKey: "PIECE-FRONT", BomLineKey: "BOM-SHELL", Note: "долевая"},
	}, cw.PieceMaterials)

	// Ни денег, ни штампа чужой раскладки: сериализуем и ищем имена полей, а не значения.
	blob, err := json.Marshal(cw)
	require.NoError(t, err)
	for _, forbidden := range []string{"cost_price", "price", "line_total", "size_run_total", "norm_marker_id"} {
		require.NotContains(t, string(blob), forbidden, "денежное/инстансное поле уехало в colorways.json")
	}

	// materials: паспорт без цены.
	require.Len(t, sc.Materials, 1)
	require.Equal(t, int64(8120), sc.Materials[0].Ref)
	require.Equal(t, "MATERIAL_UNIT_M", sc.Materials[0].UnitCode)
	require.Equal(t, "MATERIAL_CLASS_FABRIC", sc.Materials[0].Class)
	require.Equal(t, "1.03", sc.Materials[0].CuttingCoefficient)
	require.NotNil(t, sc.Materials[0].Attributes)
	require.Equal(t, "1.5", sc.Materials[0].Attributes.Fabric.SelvedgeCm)
	passport, err := json.Marshal(sc.Materials[0])
	require.NoError(t, err)
	require.NotContains(t, string(passport), "18.40")
	require.NotContains(t, string(passport), "price")

	// media: две записи, ОДИН файл — дедуп по содержимому; вид и подпись только у эскизного слота.
	require.Len(t, sc.Media, 2)
	require.Equal(t, "TECH_CARD_MEDIA_KIND_FRONT", sc.Media[0].Kind)
	require.Equal(t, "front flat", sc.Media[0].Caption)
	require.Empty(t, sc.Media[1].Kind, "медиа, добытое через выноску, вида в индексе не несёт")
	require.Equal(t, sc.Media[0].File, sc.Media[1].File)
	require.True(t, strings.HasPrefix(sc.Media[0].File, "media/"), "имя записи считается от корня архива")
	require.True(t, strings.HasSuffix(sc.Media[0].File, ".jpg"))
	require.Equal(t, sc.Media[0].SHA256+".jpg", strings.TrimPrefix(sc.Media[0].File, "media/"))

	// patterns: лист без размера едет с null, назначение — именем значения перечисления.
	require.Len(t, sc.Patterns, 2)
	require.Nil(t, sc.Patterns[0].SizeName, "выкройка без размера законна (0281) и едет с null")
	require.Equal(t, "TECH_CARD_BOM_PURPOSE_MAIN", sc.Patterns[0].FabricPurpose)
	require.NotNil(t, sc.Patterns[1].SizeName)
	require.Equal(t, "m", *sc.Patterns[1].SizeName)

	// blobs: три файла (две выкройки + одна дедуплицированная картинка), каждый открывается.
	require.Len(t, sc.Blobs, 3)
	for _, b := range sc.Blobs {
		rc, err := b.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
		require.Equal(t, b.Size, int64(len(body)))
	}

	// markers: имя файла по размеру, ссылка нашего инстанса погашена, чужие id ОСТАЛИСЬ (их
	// разбирает импорт, §5.7).
	require.Len(t, sc.Markers, 1)
	require.Equal(t, "markers/m-1.json", sc.Markers[0].File)
	require.NotNil(t, sc.Markers[0].SizeName)
	require.Equal(t, "m", *sc.Markers[0].SizeName)
	require.Equal(t, "BOM-SHELL", sc.Markers[0].BomLineKey)
	require.Len(t, sc.MarkerFiles, 1)
	require.Equal(t, "markers/m-1.json", sc.MarkerFiles[0].Name)
	var written pb_common.TechCardMarker
	require.NoError(t, protojson.Unmarshal(sc.MarkerFiles[0].Data, &written))
	require.Len(t, written.GetLayout().GetPieces(), 1)
	require.Empty(t, written.GetLayout().GetPieces()[0].GetSourceUrl(), "ссылка на CDN нашего инстанса обязана погаснуть")
	require.Equal(t, int32(4), written.GetLayout().GetPieces()[0].GetSizeId())
	require.Equal(t, int32(77), written.GetSummary().GetId(), "чужие id внутри блоба едут вербатим")

	// id_maps.sizes: имена собраны по ВСЕМ сайдкарам, включая размер, названный только раскладкой.
	require.Equal(t, map[int]string{3: "s", 4: "m"}, sc.SizeNames)
}

// Битые ссылки: удалённый из каталога материал, недоступный объект картинки, нечитаемая выкройка и
// снесённая карточка компонента. Ни одна из четырёх не имеет права уронить экспорт — на выходе
// архив с честным списком дыр и всем остальным содержимым.
func TestArchiveSidecarsBrokenReferencesBecomeHoles(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	cache := mocks.NewMockCache(t)
	media := mocks.NewMockMedia(t)
	files := mocks.NewMockFileStore(t)
	repo.EXPECT().TechCards().Return(cards)
	repo.EXPECT().Cache().Return(cache)
	repo.EXPECT().Media().Return(media)
	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveTestDictionary(), nil)

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, LineKey: "BOM-SHELL", Name: "основная ткань",
		MaterialId: sql.NullInt64{Int64: 8121, Valid: true},
	}}
	card.Media = []entity.TechCardMediaItem{{MediaId: 4021, Kind: entity.TechCardMediaFront}}
	card.Patterns = []entity.TechCardSizePattern{{
		LineKey: "PAT-FRONT", URL: "https://cdn.grbpwr.com/tech-card-patterns/gone.dxf",
	}}

	cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{StyleID: 7}, nil)
	cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return([]entity.StyleAssembly{{
		Id: 1, StyleId: 7, ComponentTechCardId: 902, Qty: decimal.RequireFromString("1"), Active: true,
	}}, nil)
	// Компонент снесён: строка не едет, дыра называет её по id компонента.
	cards.EXPECT().GetTechCardById(mock.Anything, 902).Return(nil, sql.ErrNoRows)
	// Артикул удалён из каталога: строка BOM самодостаточна и уезжает, паспорта нет.
	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{}, nil)
	media.EXPECT().GetMediaByIds(mock.Anything, []int{4021}).Return(map[int]entity.MediaFull{
		4021: {Id: 4021, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/1a2b.jpg",
		}},
	}, nil)
	files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/1a2b.jpg").
		Return(nil, int64(0), errors.New("404 from bucket"))
	files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/gone.dxf").
		Return(nil, int64(0), errors.New("404 from bucket"))

	sc, err := (&Server{repo: repo, bucket: files}).collectArchiveSidecars(t.Context(), card)
	require.NoError(t, err, "битая ссылка это дыра, а не отказ")
	defer sc.Close()

	require.ElementsMatch(t, []techcardarchive.Reason{
		techcardarchive.ReasonAssemblyComponentNotFound,
		techcardarchive.ReasonMaterialNotFound,
		techcardarchive.ReasonMediaObjectMissing,
		techcardarchive.ReasonPatternInvalid,
	}, archiveHoleReasons(sc.Holes))

	// Дыра материала названа строкой BOM — тем, что оператор найдёт в card.json.
	for _, h := range sc.Holes {
		if h.Reason == techcardarchive.ReasonMaterialNotFound {
			require.Equal(t, "material", h.Entity)
			require.Equal(t, "bom_line_key=BOM-SHELL", h.Ref)
		}
		if h.Reason == techcardarchive.ReasonMediaObjectMissing {
			require.Equal(t, "media_id=4021", h.Ref)
		}
	}

	// Дыра — это ОТСУТСТВИЕ записи в индексе, а не пустая запись: иначе импорт пошёл бы искать
	// файл, которого в архиве нет.
	require.Empty(t, sc.Media)
	require.Empty(t, sc.Patterns)
	require.Empty(t, sc.Materials)
	require.Empty(t, sc.Assembly)
	require.Empty(t, sc.Blobs)
}

// Отказ инфраструктуры — это отказ ВСЕГО экспорта. Архив, у которого молча нет медиа, потому что
// база не ответила, неотличим от карточки, у которой их не было.
func TestArchiveSidecarsInfrastructureFailureRefusesTheExport(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	cache := mocks.NewMockCache(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().TechCards().Return(cards)
	repo.EXPECT().Cache().Return(cache)
	repo.EXPECT().Media().Return(media)
	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveTestDictionary(), nil)

	card := &entity.TechCard{Id: 7}
	card.Media = []entity.TechCardMediaItem{{MediaId: 4021, Kind: entity.TechCardMediaFront}}
	cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{StyleID: 7}, nil)
	cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return(nil, nil)
	media.EXPECT().GetMediaByIds(mock.Anything, []int{4021}).Return(nil, errors.New("connection refused"))

	sc, err := (&Server{repo: repo}).collectArchiveSidecars(t.Context(), card)
	require.Error(t, err)
	require.Nil(t, sc, "неудавшийся сбор не отдаёт половину архива")
	require.Contains(t, err.Error(), "connection refused")
}

// Потолок формата держится ЗДЕСЬ, а не только в zip-writer'е: байты материализует этот файл, и
// проверка «после сборки» означала бы гигабайты, уже уехавшие во временный каталог.
func TestArchiveSpoolRefusesContentBeyondTheFormatCeiling(t *testing.T) {
	sp := newArchiveSpool()
	defer sp.close()

	// Поток без конца: если бы потолка не было, add читал бы его вечно.
	endless := io.LimitReader(neverEndingByte{}, int64(techcardarchive.MaxUncompressedBytes)*2)
	_, err := sp.add(techcardarchive.DirMedia, ".bin", endless)
	require.ErrorIs(t, err, errArchiveContentTooLarge)
	require.True(t, archiveIsFatal(err), "перебор потолка обязан хоронить экспорт, а не становиться дырой")
}

// Выходной артикул вспомогательной карточки едет паспортом, как любой пин.
//
// Без паспорта импорту НЕЧЕГО сопоставлять, и поле, обязательное до первого прогона, теряется
// молча — в логе сервера, который отчётом не является. Паспорт кладётся под тем же `ref`
// (исходный material_id), что и у строки BOM, поэтому импортный матчинг для него уже написан.
func TestArchiveMaterialsCarryTheAuxOutputArticle(t *testing.T) {
	auxCard := func() *entity.TechCard {
		c := &entity.TechCard{Id: 7}
		c.Purpose = entity.TechCardPurposeAuxiliary
		c.OutputMaterialId = sql.NullInt64{Int64: 8300, Valid: true}
		return c
	}

	t.Run("паспорт в индексе", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)
		cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{
			tcimpCatalogRow(8300, "AUX-DUSTBAG", "pcs"),
		}, nil)

		mats, holes, err := (&Server{repo: repo}).collectArchiveMaterials(t.Context(), auxCard())
		require.NoError(t, err)
		require.Empty(t, holes)
		require.Len(t, mats, 1, "у карточки нет ни одной строки BOM — паспорт в индексе только один, и он выходного артикула")
		require.EqualValues(t, 8300, mats[0].Ref, "ref — исходный material_id: ключ, по которому импорт узнаёт поле карточки")
		require.Equal(t, "AUX-DUSTBAG", mats[0].Code)
	})

	t.Run("артикула нет в каталоге — дыра под своим ref", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		cards := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(cards)
		cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(nil, nil)

		mats, holes, err := (&Server{repo: repo}).collectArchiveMaterials(t.Context(), auxCard())
		require.NoError(t, err)
		require.Empty(t, mats)
		require.Len(t, holes, 1)
		require.Equal(t, archiveRefOutputMaterial, holes[0].Ref,
			"строка BOM зовётся своим line_key, а поле карточки — своим именем")
		require.Equal(t, techcardarchive.ReasonMaterialNotFound, holes[0].Reason)
	})
}

// ИНВАРИАНТ ДВУХ ПОТОЛКОВ, а не сторож одной константы.
//
// Файл маркера в архиве — это protojson(summary + layout), а layout приходит с ЖИВОГО пути
// сохранения, где его режет maxMarkerLayoutBytes. Значит потолок файла обязан быть СТРОГО больше
// потолка раскладки: при равенстве легально сохранённый маркер в 2 МиБ даёт запись за потолком, и
// OpenArchive отвергает ВЕСЬ архив на проходе по директории — ни дыры, ни кода причины, ни слова
// оператору. Это ровно дефект R1-4, и он воскреснет при правке ЛЮБОЙ из двух констант.
//
// Тест живёт в пакете admin, потому что это единственное место, откуда видны обе: одна принадлежит
// формату, вторая — обработчику сохранения, и связь между ними не выражена ничем, кроме этой
// строки.
func TestMarkerFileCeilingLeavesRoomForALegallySavedLayout(t *testing.T) {
	require.Greater(t, techcardarchive.MaxMarkerFileBytes, maxMarkerLayoutBytes,
		"потолок файла маркера обязан быть строго больше потолка раскладки: файл = summary + layout, "+
			"и равенство означает архив, который наша же читалка отвергает целиком")
}

// Имя записи в zip строится из ключа объекта, а ключ — из url, который уже раскодирован: символы,
// которые FORMAT.md §1.1 в имени запрещает, обязаны отсеиваться здесь, а не в zip-writer'е.
func TestArchiveObjectExtNeverCarriesAPathIntoTheEntryName(t *testing.T) {
	for raw, want := range map[string]string{
		"grbpwr-com/2026/08/a.jpg":     ".jpg",
		"grbpwr-com/2026/08/a.JPEG":    ".jpeg",
		"tech-card-patterns/front.dxf": ".dxf",
		"grbpwr-com/2026/08/no-ext":    "",
		`grbpwr-com/2026/08/a.jp\g`:    ".jpg",
		// path.Ext смотрит только за последней косой чертой, поэтому «..» из середины ключа в
		// расширение не попадает вовсе: остаётся безобидное ".pg".
		"grbpwr-com/2026/08/a.j/../..pg": ".pg",
		"grbpwr-com/2026/08/a.":          "",
	} {
		require.Equal(t, want, archiveObjectExt(raw), "ключ %q", raw)
	}
}

// neverEndingByte — источник, который всегда отдаёт байты; нужен, чтобы проверять именно потолок,
// а не длину фикстуры.
type neverEndingByte struct{}

func (neverEndingByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
