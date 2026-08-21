package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// fusedCard: основная ткань (слот 56, скоуп BOMKEY1) + клеевая (слот 57, скоуп FUSEKEY). Деталь
// PIECE1 назначена рецептом на ОБА слота — ровно так это и заводят: «полочка кроится из основной
// ткани и дублируется вот этой клеевой».
//
// Геометрия измерена ТОЛЬКО у основной ткани, потому что лекал у клеевой не бывает.
func fusedCard(mode entity.TechCardPieceFusingMode, widthMm string) *entity.TechCard {
	tc := measuredCard()
	tc.SizeIds = []int{4}
	tc.PieceAreaScopes = map[string]entity.PieceAreaScope{
		"BOMKEY1": {ScopeKey: "BOMKEY1", Rows: []entity.PieceAreaRow{{
			PieceLineKey:    "PIECE1",
			SizeId:          sql.NullInt64{Int64: 4, Valid: true},
			AreaCm2:         decimal.RequireFromString("4200"),
			PerimeterCm:     decimal.NullDecimal{Decimal: decimal.RequireFromString("260"), Valid: true},
			ContourLayer:    "1",
			SeamAllowanceMm: decimal.RequireFromString("10"),
			ParsedBy:        "kate",
			ParsedAt:        time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
		}}},
	}
	tc.BomItems = append(tc.BomItems, entity.TechCardBomItem{
		Id:          57,
		LineKey:     "FUSEKEY",
		Name:        "клеевая G210",
		Section:     entity.BomSectionInterlining,
		Unit:        sql.NullString{String: "m", Valid: true},
		UnitPrice:   decimal.NullDecimal{Decimal: decimal.RequireFromString("10"), Valid: true},
		Currency:    sql.NullString{String: "EUR", Valid: true},
		FabricWidth: decimal.NullDecimal{Decimal: decimal.RequireFromString("100"), Valid: true},
	})
	tc.Pieces[0].Fused = true
	if mode != "" {
		tc.Pieces[0].FusingMode = sql.NullString{String: string(mode), Valid: true}
	}
	if widthMm != "" {
		tc.Pieces[0].FusingWidthMm = decimal.NullDecimal{
			Decimal: decimal.RequireFromString(widthMm), Valid: true,
		}
	}
	tc.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{Id: 34, BomItemId: sql.NullInt64{Int64: 56, Valid: true}, PieceId: sql.NullInt64{Int64: 14, Valid: true}},
		{Id: 35, BomItemId: sql.NullInt64{Int64: 57, Valid: true}, PieceId: sql.NullInt64{Int64: 14, Valid: true}},
	}
	return tc
}

func fusingSlotEstimate(t *testing.T, tc *entity.TechCard) slotEstimate {
	t.Helper()
	for i := range tc.BomItems {
		if tc.BomItems[i].Section == entity.BomSectionInterlining {
			return slotAreaEstimate(tc, &tc.Colorways[0], &tc.BomItems[i], nil, tc.CostingBasis(), "EUR")
		}
	}
	t.Fatal("в карточке нет клеевого слота")
	return slotEstimate{}
}

// КЛЕЕВОЙ СЛОТ СЧИТАЕТСЯ, ХОТЯ СВОИХ ВЫКРОЕК У НЕГО НЕТ. До 0304 он упирался бы в «площади не
// измерены» на карточке, где измерено всё, что вообще может быть измерено.
func TestFusingSlotBorrowsFabricGeometry(t *testing.T) {
	tc := fusedCard(entity.PieceFusingModeStrip, "25")
	est := fusingSlotEstimate(t, tc)
	if !est.normDerived {
		t.Fatalf("норма клеевой не выведена, отказ %q", est.refusal)
	}
	// 260 см × 2.5 см = 650 см² ÷ 100 см = 6.5 см = 0.065 м.
	if !est.perGarment.Equal(decimal.RequireFromString("0.065")) {
		t.Fatalf("норма клеевой = %s м, ожидалось 0.065 (периметр 260 × полоса 2.5 ÷ 100)", est.perGarment)
	}
}

// ЦЕЛИКОМ — ЭТО ПЛОЩАДЬ, и та же деталь на том же слоте стоит в 6.5 раза дороже. Это и есть цена
// вопроса, ради которого заведён режим.
func TestFusingFullUsesArea(t *testing.T) {
	est := fusingSlotEstimate(t, fusedCard(entity.PieceFusingModeFull, ""))
	if !est.normDerived {
		t.Fatalf("норма не выведена, отказ %q", est.refusal)
	}
	// 4200 см² ÷ 100 см = 42 см = 0.42 м.
	if !est.perGarment.Equal(decimal.RequireFromString("0.42")) {
		t.Fatalf("норма при дублировании целиком = %s м, ожидалось 0.42", est.perGarment)
	}
}

// НЕРАЗМЕЧЕННАЯ ДЕТАЛЬ ВЕДЁТ СЕБЯ КАК «ЦЕЛИКОМ» — то самое, как её читал весь код до 0304. Иначе
// выкатка молча переоценила бы клеевую на каждой существующей карточке.
func TestUnmarkedFusingCostsAsFull(t *testing.T) {
	est := fusingSlotEstimate(t, fusedCard("", ""))
	if !est.normDerived || !est.perGarment.Equal(decimal.RequireFromString("0.42")) {
		t.Fatalf("неразмеченная деталь дала %s (отказ %q), ожидалось 0.42 — как до 0304",
			est.perGarment, est.refusal)
	}
}

// ПОЛОСА БЕЗ СВОЕГО ЧИСЛА БЕРЁТ ЭТАЛОН КАРТОЧКИ, а при его отсутствии — цеховой. Каскад 0277
// целиком, потому что цех, настроивший припуск один раз, не обязан повторять его на каждой
// карточке.
//
// До 0328 это же состояние записывалось ОТДЕЛЬНЫМ режимом `seam_allowance` — то есть один приём
// (полоса вдоль среза) был записан двумя значениями, различавшимися лишь тем, названо ли число.
// Арифметика не изменилась ни на цифру; изменилось только то, чем состояние обозначается: пустой
// шириной единственного режима вместо второго члена словаря.
func TestStripWithoutItsOwnWidthFollowsTheStandardCascade(t *testing.T) {
	tc := fusedCard(entity.PieceFusingModeStrip, "")
	tc.RequiredSeamAllowanceMm = decimal.NullDecimal{
		Decimal: decimal.RequireFromString("10"), Valid: true,
	}
	est := fusingSlotEstimate(t, tc)
	// 260 см × 1 см = 260 см² ÷ 100 см = 2.6 см = 0.026 м.
	if !est.normDerived || !est.perGarment.Equal(decimal.RequireFromString("0.026")) {
		t.Fatalf("эталон карточки дал %s (отказ %q), ожидалось 0.026", est.perGarment, est.refusal)
	}

	// Эталона на карточке нет — берётся цеховой.
	tc = fusedCard(entity.PieceFusingModeStrip, "")
	tc.WorkshopSeamAllowanceMm = decimal.NullDecimal{
		Decimal: decimal.RequireFromString("10"), Valid: true,
	}
	est = fusingSlotEstimate(t, tc)
	if !est.normDerived || !est.perGarment.Equal(decimal.RequireFromString("0.026")) {
		t.Fatalf("цеховой эталон дал %s (отказ %q), ожидалось 0.026", est.perGarment, est.refusal)
	}

	// ЭТАЛОНА НЕТ НИГДЕ — ОТКАЗ, а не подстановка полного контура. Подстановка была бы хуже нуля не
	// величиной, а тем, что молчалива: на экране осталось бы «полосой», а деньги стояли бы как за
	// «всю деталь» — 0.42 против 0.026, в 16 раз, и ни одна надпись об этом не сказала бы.
	est = fusingSlotEstimate(t, fusedCard(entity.PieceFusingModeStrip, ""))
	if est.normDerived {
		t.Fatalf("без эталона припуска норма посчиталась (%s) вместо отказа", est.perGarment)
	}
	if est.refusal != entity.AreaEstimateNoStripWidth {
		t.Fatalf("отказ %q, ожидался %q", est.refusal, entity.AreaEstimateNoStripWidth)
	}
}

// СВОЯ ГЕОМЕТРИЯ ПОБЕЖДАЕТ ЗАИМСТВОВАННУЮ. Клеевая, нарисованная СВОИМ лекалом, обязана считаться
// по нему: подменить его контуром основной ткани значило бы посчитать по чужому лекалу, имея на
// руках собственное.
func TestOwnFusingGeometryBeatsTheBorrowedOne(t *testing.T) {
	tc := fusedCard(entity.PieceFusingModeFull, "")
	tc.PieceAreaScopes["FUSEKEY"] = entity.PieceAreaScope{
		ScopeKey: "FUSEKEY", Rows: []entity.PieceAreaRow{{
			PieceLineKey: "PIECE1",
			SizeId:       sql.NullInt64{Int64: 4, Valid: true},
			// Втрое меньше основной: если ответ равен 0.42, взяли чужой контур.
			AreaCm2:      decimal.RequireFromString("1400"),
			PerimeterCm:  decimal.NullDecimal{Decimal: decimal.RequireFromString("150"), Valid: true},
			ContourLayer: "1",
			ParsedBy:     "kate",
			ParsedAt:     time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC),
		}},
	}
	est := fusingSlotEstimate(t, tc)
	if !est.normDerived || !est.perGarment.Equal(decimal.RequireFromString("0.14")) {
		t.Fatalf("норма = %s (отказ %q), ожидалось 0.14 — по СВОЕМУ лекалу клеевой",
			est.perGarment, est.refusal)
	}
}

// ЧИСЛО НЕ ПОКАЗЫВАЕТСЯ БЕЗ ПРОВЕНАНСА. Норма клеевой стоит на замере ОСНОВНОЙ ткани, и экран
// обязан назвать этот замер: цифра с пустым «измерено …» — ровно то, от чего провенанс заводился.
func TestBorrowedEstimateCarriesTheMeasurementProvenance(t *testing.T) {
	est := fusingSlotEstimate(t, fusedCard(entity.PieceFusingModeStrip, "25"))
	if !est.measured {
		t.Fatal("посчитанная по заимствованному контуру норма приехала без признака замера")
	}
	if est.parsedBy != "kate" || est.contourLayer != "1" {
		t.Fatalf("провенанс заимствованного замера потерян: by=%q layer=%q", est.parsedBy, est.contourLayer)
	}
}
