package entity

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

func fusingDec(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

func fusingMode(v TechCardPieceFusingMode) sql.NullString {
	return sql.NullString{String: string(v), Valid: true}
}

// НЕРАЗМЕЧЕННАЯ ДЕТАЛЬ СЧИТАЕТСЯ КАК РАНЬШЕ. Весь код до 0304 читал голую галку единственным
// возможным способом — «клеевая по тому же лекалу», — и разворот NULL в full сохраняет ровно это.
// Если однажды здесь окажется что-то другое, каждая существующая карточка молча переоценит клеевую.
func TestUnmarkedPieceReadsAsFullFusing(t *testing.T) {
	if got := PieceFusingModeOrFull(sql.NullString{}); got != PieceFusingModeFull {
		t.Fatalf("неразмеченная деталь развернулась в %q, ожидалось %q", got, PieceFusingModeFull)
	}
	// Мусор в колонке тоже разворачивается в full, а не роняет расчёт: значение туда попасть не
	// может (CHECK + словарь), но расчёт денег — не то место, где стоит паниковать из-за строки.
	if got := PieceFusingModeOrFull(sql.NullString{String: "нечто", Valid: true}); got != PieceFusingModeFull {
		t.Fatalf("неизвестный режим развернулся в %q, ожидалось %q", got, PieceFusingModeFull)
	}
}

// СНЯТАЯ ГАЛКА ГАСИТ РАЗМЕТКУ. Уцелевший режим при fused=false — это строка, которая на экране
// говорит «не дублируется», а расчёту отдаёт полосу; в базе такую пару не пускает chk_tcp_fusing_mode,
// и оператор увидел бы 3819 с именем колонки, которую не трогал.
func TestNormalizeFusingClearsMarkingOnAnUnfusedPiece(t *testing.T) {
	p := TechCardPiece{
		Fused:         false,
		FusingMode:    fusingMode(PieceFusingModeStrip),
		FusingWidthMm: fusingDec("25"),
	}
	p.NormalizeFusing()
	if p.FusingMode.Valid || p.FusingWidthMm.Valid {
		t.Fatalf("разметка пережила снятую галку: mode=%v width=%v", p.FusingMode, p.FusingWidthMm)
	}
}

// ШИРИНА ЖИВЁТ ТОЛЬКО У ПОЛОСЫ. Число рядом с «дублируется целиком» описывает ничто: на экране
// виден полный контур, а в расчёт ушёл бы остаток от прошлой правки.
func TestNormalizeFusingDropsWidthOutsideStrip(t *testing.T) {
	for _, mode := range []TechCardPieceFusingMode{PieceFusingModeFull} {
		p := TechCardPiece{Fused: true, FusingMode: fusingMode(mode), FusingWidthMm: fusingDec("25")}
		p.NormalizeFusing()
		if p.FusingWidthMm.Valid {
			t.Errorf("режим %q сохранил свою ширину", mode)
		}
		if !p.FusingMode.Valid {
			t.Errorf("режим %q потерялся вместе с шириной", mode)
		}
	}
	p := TechCardPiece{Fused: true, FusingMode: fusingMode(PieceFusingModeStrip), FusingWidthMm: fusingDec("25")}
	p.NormalizeFusing()
	if !p.FusingWidthMm.Valid {
		t.Fatal("полоса потеряла свою ширину — считать её стало нечем")
	}
}

// ПОЛОСА БЕЗ ЧИСЛА — ЗАКОННЫЙ ОТВЕТ «ПО ЭТАЛОНУ ПРИПУСКА» (0328), И ЭТО РАЗВОРОТ ПРЕЖНЕГО ПРАВИЛА.
//
// Раньше здесь стоял отказ, а «ширину берём из эталона» говорилось ОТДЕЛЬНЫМ режимом
// `seam_allowance` — то есть один приём был записан двумя значениями, различавшимися лишь тем,
// названо ли число. Довод прежнего отказа («подставить припуск значило бы молча превратить
// „полосой“ в „по припуску“») держался ровно на существовании второго режима: без него подставлять
// нечего во что, потому что «полосой» и «по припуску» — теперь одно и то же утверждение с
// заполненным и незаполненным числом.
//
// Ничего не выдумывается за технолога и сейчас: пустая ширина ОСТАЁТСЯ пустой в колонке, а
// разворачивает её в число только читатель, которому число нужно (dto.fusingGeometryFor), и по
// каскаду эталона 0277.
func TestValidatePieceFusingAcceptsAStripWithNoWidth(t *testing.T) {
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode(PieceFusingModeStrip), decimal.NullDecimal{}); err != nil {
		t.Fatalf("полоса без ширины отклонена: %v — сказать «по эталону припуска» стало нечем", err)
	}
	// НЕГАТИВНЫЙ КОНТРОЛЬ: снятый режим не должен вернуться другим путём. Он не член словаря, и
	// проверка обязана называть его именно неизвестным, а не молча принимать.
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode("seam_allowance"), decimal.NullDecimal{}); err == nil {
		t.Fatal("режим seam_allowance принят — 0328 снял его, и полоса по эталону выражается пустой шириной у strip")
	}
	// И ширина при «целиком» по-прежнему отвергается: пара атомарна, число там описывает ничто.
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode(PieceFusingModeFull), fusingDec("25")); err == nil {
		t.Fatal("ширина при «целиком» принята — на экране полный контур, а в расчёт ушло бы 25 мм")
	}
}

func TestValidatePieceFusingRefusesUnitSlipAndZero(t *testing.T) {
	// 250 мм = 25 см: это дублирование целиком, набранное в чужой единице.
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("250")); err == nil {
		t.Error("ширина 250 мм принята — потолок 0304 не повторён в словах")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("0")); err == nil {
		t.Error("нулевая полоса принята — это «не дублируется», а не полоса")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25")); err != nil {
		t.Errorf("законная полоса 25 мм отклонена: %v", err)
	}
}

// «НЕ РАЗМЕЧЕНО» — ЗАКОННОЕ СОСТОЯНИЕ, и оно обязано проходить: иначе ни одна карточка, заведённая
// до 0304, не сохранится.
func TestValidatePieceFusingAcceptsUnmarked(t *testing.T) {
	if err := ValidatePieceFusing("f", true, sql.NullString{}, decimal.NullDecimal{}); err != nil {
		t.Fatalf("неразмеченная fused-деталь отклонена: %v", err)
	}
	if err := ValidatePieceFusing("f", false, sql.NullString{}, decimal.NullDecimal{}); err != nil {
		t.Fatalf("обычная недублируемая деталь отклонена: %v", err)
	}
}

// МАСШТАБ КОЛОНКИ. DECIMAL(6,1) округлит 25.25 до 25.3 — но дайджест CONSTRUCTION к тому моменту
// уже посчитан по 25.25, и подпись, поставленная тем же сохранением, разойдётся с первым же
// чтением карточки. Второй сценарий злее: 0.04 округлится до 0.0 и уронит всю карточку в 3819.
func TestValidatePieceFusingRefusesSubMillimetrePrecision(t *testing.T) {
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25.25")); err == nil {
		t.Error("25.25 мм принято — БД сохранит 25.3, и подпись разойдётся с хранимым")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("0.04")); err == nil {
		t.Error("0.04 мм принято — БД округлит до нуля и отобьёт карточку CHECK'ом")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25.3")); err != nil {
		t.Errorf("законная десятая доля отклонена: %v", err)
	}
}

func splineTestPoint(x, y string, corner bool) TechCardPieceFusingSplinePoint {
	return TechCardPieceFusingSplinePoint{
		X: decimal.RequireFromString(x), Y: decimal.RequireFromString(y), Corner: corner,
	}
}

func splineTestPiece(points ...TechCardPieceFusingSplinePoint) *TechCardPiece {
	return &TechCardPiece{
		Fused:      true,
		FusingMode: fusingMode(PieceFusingModeZone),
		FusingZone: &TechCardPieceFusingZone{Spline: &TechCardPieceFusingSpline{Points: points}},
	}
}

func TestValidatePieceFusingSplineGeometry(t *testing.T) {
	valid := splineTestPiece(
		splineTestPoint("0", "0", true), splineTestPoint("20", "0", true),
		splineTestPoint("20", "10", true), splineTestPoint("0", "10", true),
	)
	if err := ValidatePieceFusingDetails("pieces[0]", valid); err != nil {
		t.Fatalf("valid rectangular spline rejected: %v", err)
	}
	flat, err := flattenPieceFusingSpline(valid.FusingZone.Spline)
	if err != nil || len(flat) != 20 {
		t.Fatalf("sharp rectangle vertices = %d, want TypeScript result 20: err=%v", len(flat), err)
	}

	curved := splineTestPiece(
		splineTestPoint("0", "0", false), splineTestPoint("20", "0", false),
		splineTestPoint("20", "10", false), splineTestPoint("0", "10", false),
	)
	flat, err = flattenPieceFusingSpline(curved.FusingZone.Spline)
	if err != nil {
		t.Fatalf("flatten rounded spline: %v", err)
	}
	points := []fusingSplineFloatPoint{{0, 0}, {20, 0}, {20, 10}, {0, 10}}
	tangents := []fusingSplineFloatPoint{{10, -5}, {10, 5}, {-10, 5}, {-10, -5}}
	for i, want := range []int{4, 4, 4, 4} {
		got := fusingSplineSteps(points[i], tangents[i], points[(i+1)%4], tangents[(i+1)%4], fusingSplineChordErrorMm)
		if got != want {
			t.Errorf("segment %d steps = %d, want TypeScript stepsOf result %d", i, got, want)
		}
	}
	if got := fusingSplineSteps(points[0], tangents[0], points[1], tangents[1], 0); got != 1 {
		t.Errorf("non-finite step calculation = %d, want TypeScript fallback 1", got)
	}
	if len(flat) != 16 {
		t.Fatalf("flattened vertex count = %d, want TypeScript flattenSpline result 16", len(flat))
	}
	for i, want := range []fusingSplineFloatPoint{{0, 0}, {20, 0}, {20, 10}, {0, 10}} {
		if got := flat[i*4]; got != want {
			t.Errorf("vertex %d = %+v, want control point %+v", i*4, got, want)
		}
	}
	if got, want := flat[1], (fusingSplineFloatPoint{x: 4.0625, y: -0.9375}); got != want {
		t.Errorf("first t=1/4 vertex = %+v, want TypeScript result %+v", got, want)
	}
	if flat[len(flat)-1] == flat[0] {
		t.Fatal("flattened spline duplicated its closing point")
	}
}

func TestValidatePieceFusingSplineRejectsInvalidPolygons(t *testing.T) {
	tooMany := make([]TechCardPieceFusingSplinePoint, 65)
	for i := range tooMany {
		tooMany[i] = splineTestPoint(strconv.Itoa(i), strconv.Itoa(i%2), true)
	}
	tests := map[string]*TechCardPiece{
		"too few points": splineTestPiece(
			splineTestPoint("0", "0", true), splineTestPoint("10", "0", true)),
		"too many points": splineTestPiece(tooMany...),
		"zero area": splineTestPiece(
			splineTestPoint("0", "0", true), splineTestPoint("10", "0", true), splineTestPoint("20", "0", true)),
		"self intersection": splineTestPiece(
			splineTestPoint("0", "0", true), splineTestPoint("10", "10", true),
			splineTestPoint("0", "10", true), splineTestPoint("10", "0", true)),
	}
	for name, piece := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePieceFusingDetails("pieces[0]", piece); err == nil {
				t.Fatal("invalid spline accepted")
			}
		})
	}

	multiple := splineTestPiece(
		splineTestPoint("0", "0", true), splineTestPoint("10", "0", true), splineTestPoint("0", "10", true))
	multiple.FusingZone.Rect = &TechCardPieceFusingRect{X1: decimal.NewFromInt(1), Y1: decimal.NewFromInt(1)}
	if err := ValidatePieceFusingDetails("pieces[0]", multiple); err == nil {
		t.Fatal("zone with both rect and spline accepted")
	}
}
