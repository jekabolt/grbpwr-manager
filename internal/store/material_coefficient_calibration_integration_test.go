package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// КАЛИБРОВКА КОЭФФИЦИЕНТА РАСКРОЯ end to end (Ф5б.3, §4.2) — от строк в базе до ответа RPC.
//
// ЭТОТ ТЕСТ СУЩЕСТВУЕТ РАДИ ОДНОГО УТВЕРЖДЕНИЯ: НАСТИЛЫ, ЧЕЙ АРТИКУЛ ПРИКОЛОТ КОЛОРВЕЕМ, НЕ ТЕРЯЮТСЯ.
//
// Настил не хранит material_id — он хранит пару (колорвей, слот BOM). Здесь у карточки ОДИН слот, и
// его артикул по умолчанию — matDefault. Артикул matPinned не назван НИ НА ОДНОМ слоте: он
// существует только как пин колорвея в tech_card_colorway_usage. Значит:
//
//   - отбор настилов джойном по tech_card_bom_item.material_id вернул бы для matPinned ПУСТО, и
//     калибровка вечно отвечала бы «фактов пока мало» при полном журнале замеров;
//   - и наоборот, для matDefault такой джойн вернул бы ВСЕ настилы карточки, включая приколотые к
//     чужому артикулу, и медиана считалась бы по смеси двух тканей.
//
// Проверяются обе половины: SQL-отбор (ListMeasuredLayCandidates) обязан ДОСТАВИТЬ приколотый
// настил, а резолвер (dto.LayArticleMaterialId, через настоящую карточку из GetTechCardById) —
// РАЗВЕСТИ два артикула. Карточка грузится настоящим загрузчиком специально: резолвер умеет
// откатываться на позиционный индекс слота, и лёгкая выборка с другим порядком разрешила бы
// legacy-юзедж в чужой слот.
// measuredLayScanLimit mirrors the API-layer window (internal/apisrv/admin/bom_wastage.go): the
// candidate scan is bounded, so the test has to name a window like every real caller does.
const measuredLayScanLimit = 50

func TestMaterialCuttingCoefficientCalibration(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	PR := s.ProductionRuns()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }
	nl := func(v int) sql.NullInt64 { return sql.NullInt64{Int64: int64(v), Valid: true} }
	dec := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	ndec := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	// Два РАЗНЫХ цвета: uniq_product_style_color не пускает два колорвея одного цвета на один стиль.
	var colorA, colorB string
	rows, err := testDB.QueryContext(ctx, "SELECT code FROM color ORDER BY code LIMIT 2")
	require.NoError(t, err)
	codes := []string{}
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		codes = append(codes, c)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, codes, 2, "нужны два цвета в словаре")
	colorA, colorB = codes[0], codes[1]

	newMaterial := func(name string) int {
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{Name: name, Section: "fabric", Unit: ns("m")})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", id)
		})
		return id
	}
	// matDefault — артикул СЛОТА. matPinned существует ТОЛЬКО как пин колорвея. matStranger не
	// назван на этой карточке нигде и служит контролем отсева.
	matDefault := newMaterial("F5B3 Default Fabric")
	matPinned := newMaterial("F5B3 Pinned Fabric")
	matStranger := newMaterial("F5B3 Stranger Fabric")

	fabric := entity.TechCardBomItem{
		LineKey: "01F5B3FABRIC000000000MAIN0", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"), MaterialId: nl(matDefault),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "F5B3 Calibration Style", Stage: entity.TechCardStageProto, StyleNumber: ns("F5B3-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	var fabricSlot int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?",
		tcID, fabric.LineKey).Scan(&fabricSlot))

	newColorway := func(sku, colorCode string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
			VALUES (?, ?, ?, '#000000', 'US', ?, ?, 1)`, sku, colorCode, colorCode, mediaID, tcID)
		require.NoError(t, err)
		raw, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", int(raw))
		})
		return int(raw)
	}
	cwPinned := newColorway("F5B3-CW-PIN", colorA)
	cwDefault := newColorway("F5B3-CW-DEF", colorB)

	// ПИН: этот колорвей кроит тот же слот ДРУГОЙ тканью. Единственное упоминание matPinned на всей
	// карточке — вот эта строка.
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_colorway_usage
		(colorway_id, bom_item_id, material_id, consumption, display_order)
		VALUES (?, ?, ?, 1.5, 0)`, cwPinned, fabricSlot, matPinned)
	require.NoError(t, err)

	runID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{LineKey: "01F5B3RUNLINE0000000000PIN", ProductId: ni(cwPinned), SizeId: szA, PlannedQty: 20},
			{LineKey: "01F5B3RUNLINE0000000000DEF", ProductId: ni(cwDefault), SizeId: szA, PlannedQty: 20},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

	newMarker := func(colorwayID int, name string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_marker
			(tech_card_id, run_id, bom_item_id, colorway_id, size_id, name, source, fabric_width_cm,
			 used_length_cm, total_units, placed_count, total_count, layout)
			VALUES (?, ?, ?, ?, NULL, ?, 'auto', 140.00, 900.00, 2, 4, 4, '{}')`,
			tcID, runID, fabricSlot, colorwayID, name)
		require.NoError(t, err)
		raw, err := res.LastInsertId()
		require.NoError(t, err)
		return int(raw)
	}
	markerPinned := newMarker(cwPinned, "маркер пин")
	markerDefault := newMarker(cwDefault, "маркер умолчание")

	// План КАЖДОГО настила: 900 см × 10 слоёв + 2 × 2 см × 10 слоёв = 9040 см = 90.4 м. Чистая
	// геометрия, БЕЗ коэффициента раскроя (Р4) — иначе калибровка стала бы круговой.
	const plannedM = "90.4"
	saveLay := func(n int, colorwayID, markerID int, actual string) entity.ProductionRunLay {
		ins := entity.ProductionRunLayInsert{
			LayKey:     fmt.Sprintf("01F5B3LAY%017d", n),
			ColorwayId: colorwayID, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, EndLossCm: dec("2"),
			Name: fmt.Sprintf("настил %d", n),
			Sections: []entity.ProductionRunLaySectionInsert{
				{SectionKey: fmt.Sprintf("01F5B3SEC%017d", n), MarkerId: markerID, Plies: 10, Position: 0},
			},
		}
		if actual != "" {
			ins.Actual = &entity.ProductionRunLayActualInput{
				Qty: ndec(actual), Uom: entity.MaterialUnitM, Method: entity.ProductionLayActualMethodRollBeforeAfter,
			}
		}
		lay, err := PR.SaveLay(ctx, runID, ins, entity.NoLockVersion(), false, "cutter")
		require.NoError(t, err)
		return lay
	}

	// Приколотый артикул: факт 94.92 м на плане 90.4 м ⇒ дрейф ровно +5%.
	saveLay(1, cwPinned, markerPinned, "94.92")
	saveLay(2, cwPinned, markerPinned, "94.92")
	saveLay(3, cwPinned, markerPinned, "94.92")
	// Артикул слота: факт 99.44 м ⇒ дрейф +10%. Одного мало для предложения — и это ровно то, чем
	// доказывается, что приколотые настилы к нему НЕ просочились.
	saveLay(4, cwDefault, markerDefault, "99.44")
	// Настил без факта: спланирован, ткань ещё не мерили. В выборку замеров попасть не должен.
	unmeasured := saveLay(5, cwDefault, markerDefault, "")
	require.False(t, unmeasured.HasActual())

	// ---- SQL-отбор: приколотый настил ДОСТАВЛЕН -------------------------------------------------

	t.Run("отбор доносит настилы, чей артикул назван ТОЛЬКО пином колорвея", func(t *testing.T) {
		cands, _, err := PR.ListMeasuredLayCandidates(ctx, matPinned, measuredLayScanLimit)
		require.NoError(t, err)
		require.NotEmpty(t, cands,
			"артикул matPinned не назван ни на одном слоте: джойн по tech_card_bom_item.material_id вернул бы пусто")
		require.Len(t, cands, 4, "отбор — СУПЕРМНОЖЕСТВО по карточке: он не различает артикулы и не должен")
		for _, c := range cands {
			require.True(t, c.HasActual(), "настил без факта в замеры не попадает")
			require.Equal(t, tcID, c.TechCardId)
			require.NotEmpty(t, c.Sections, "секции обязательны: без них план настила равен нулю")
			require.Equal(t, plannedM,
				dto.LayPlannedGeometryOf(&c.ProductionRunLay).TotalCm().Div(decimal.NewFromInt(100)).String())
		}
	})

	t.Run("карточка, не называющая артикул нигде, в отбор не попадает", func(t *testing.T) {
		cands, _, err := PR.ListMeasuredLayCandidates(ctx, matStranger, measuredLayScanLimit)
		require.NoError(t, err)
		require.Empty(t, cands, "ограничение отбора вправе отсеять только заведомо невозможное")
	})

	// ---- резолвер: два артикула РАЗВЕДЕНЫ -------------------------------------------------------

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	suggest := func(materialID int) *struct {
		Status pb_common.MaterialCoefficientSuggestionStatus
		Count  int32
		Coef   string
		Median string
	} {
		t.Helper()
		lays, _, err := PR.ListMeasuredLayCandidates(ctx, materialID, measuredLayScanLimit)
		require.NoError(t, err)
		got := dto.BuildMaterialCoefficientSuggestion(dto.MaterialCoefficientCalibrationInput{
			MaterialId: materialID, ArticleUom: "m", Lays: lays,
			Cards: map[int]*entity.TechCard{tcID: card},
		})
		return &struct {
			Status pb_common.MaterialCoefficientSuggestionStatus
			Count  int32
			Coef   string
			Median string
		}{got.GetStatus(), got.GetLayCount(), got.GetSuggestedCoefficient().GetValue(), got.GetMedianDriftPercent().GetValue()}
	}

	t.Run("приколотый артикул получает своё предложение", func(t *testing.T) {
		got := suggest(matPinned)
		require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_READY,
			got.Status)
		require.EqualValues(t, 3, got.Count)
		require.Equal(t, "1.05", got.Coef, "медиана дрейфов приколотых настилов")
		require.Equal(t, "5", got.Median, "измерение — в процентах, предложение — множителем")
	})

	t.Run("артикул слота считает ТОЛЬКО свои настилы", func(t *testing.T) {
		got := suggest(matDefault)
		require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_TOO_FEW_FACTS,
			got.Status)
		require.EqualValues(t, 1, got.Count,
			"просочись сюда три приколотых настила, было бы 4 и готовое предложение по смеси двух тканей")
		require.Empty(t, got.Coef)
	})
}
