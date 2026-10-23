package entity

import (
	"database/sql"
	"fmt"
	"math"
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

func splineTestFloatPoints(spline *TechCardPieceFusingSpline) []fusingSplineFloatPoint {
	points := make([]fusingSplineFloatPoint, len(spline.Points))
	for i, point := range spline.Points {
		points[i] = fusingSplineFloatPoint{x: point.X.InexactFloat64(), y: point.Y.InexactFloat64()}
	}
	return points
}

func splineTestRing(points ...TechCardPieceFusingSplinePoint) TechCardPieceFusingSpline {
	return TechCardPieceFusingSpline{Points: points}
}

func splineTestPointNear(t *testing.T, label string, got, want fusingSplineFloatPoint, tolerance float64) {
	t.Helper()
	if math.Abs(got.x-want.x) > tolerance || math.Abs(got.y-want.y) > tolerance {
		t.Errorf("%s = (%.17g, %.17g), want (%.17g, %.17g)", label, got.x, got.y, want.x, want.y)
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
	points := splineTestFloatPoints(curved.FusingZone.Spline)
	wantSteps := []int{5, 3, 5, 3}
	offset := 0
	for i, want := range wantSteps {
		m1, m2, zeroLength := fusingSplineCentripetalTangents(points, curved.FusingZone.Spline.Points, i)
		if zeroLength {
			t.Fatalf("segment %d unexpectedly has zero length", i)
		}
		got := fusingSplineSteps(points[i], m1, points[(i+1)%len(points)], m2, fusingSplineChordErrorMm)
		if got != want {
			t.Errorf("segment %d steps = %d, want TypeScript stepsOf result %d", i, got, want)
		}
		if flat[offset] != points[i] {
			t.Errorf("vertex %d = %+v, want control point %+v", offset, flat[offset], points[i])
		}
		offset += want
	}
	m1, m2, _ := fusingSplineCentripetalTangents(points, curved.FusingZone.Spline.Points, 0)
	if got := fusingSplineSteps(points[0], m1, points[1], m2, 0); got != 1 {
		t.Errorf("non-finite step calculation = %d, want TypeScript fallback 1", got)
	}
	if len(flat) != 16 {
		t.Fatalf("flattened vertex count = %d, want TypeScript flattenSpline result 16", len(flat))
	}
	if got, want := flat[1], (fusingSplineFloatPoint{x: 2.8752900397563432, y: -1.3254833995939042}); got != want {
		t.Errorf("first t=1/5 vertex = %+v, want TypeScript result %+v", got, want)
	}
	if flat[len(flat)-1] == flat[0] {
		t.Fatal("flattened spline duplicated its closing point")
	}
}

func TestFlattenPieceFusingSplineCentripetalGolden(t *testing.T) {
	spline := splineTestPiece(
		splineTestPoint("0", "0", false), splineTestPoint("100", "0", false),
		splineTestPoint("101", "1", false), splineTestPoint("101", "100", false),
	).FusingZone.Spline
	points := splineTestFloatPoints(spline)
	wantSteps := []int{12, 2, 12, 14}
	offset := 0
	for i, want := range wantSteps {
		m1, m2, zeroLength := fusingSplineCentripetalTangents(points, spline.Points, i)
		if zeroLength {
			t.Fatalf("segment %d unexpectedly has zero length", i)
		}
		got := fusingSplineSteps(points[i], m1, points[(i+1)%len(points)], m2, fusingSplineChordErrorMm)
		if got != want {
			t.Errorf("segment %d steps = %d, want %d", i, got, want)
		}
		offset += want
	}
	flat, err := flattenPieceFusingSpline(spline)
	if err != nil {
		t.Fatalf("flatten golden spline: %v", err)
	}
	if len(flat) != offset || len(flat) != 40 {
		t.Fatalf("flattened vertex count = %d, want %d", len(flat), offset)
	}
	offset = 0
	for i, steps := range wantSteps {
		if flat[offset] != points[i] {
			t.Errorf("segment %d starts at %+v, want %+v", i, flat[offset], points[i])
		}
		offset += steps
	}

	m1, m2, _ := fusingSplineCentripetalTangents(points, spline.Points, 1)
	half := fusingSplineHermitePoint(points[1], m1, points[2], m2, 0.5)
	wantHalf := fusingSplineFloatPoint{x: 100.65804836398324, y: 0.34215574839178853}
	splineTestPointNear(t, "segment 1 H(0.5)", half, wantHalf, 1e-12)
	wantSegment := []fusingSplineFloatPoint{
		{x: 100, y: 0},
		{x: 100.65804836398324, y: 0.34215574839178853},
	}
	segment := flat[wantSteps[0] : wantSteps[0]+wantSteps[1]]
	for i, want := range wantSegment {
		splineTestPointNear(t, fmt.Sprintf("segment 1 vertex %d", i), segment[i], want, 1e-12)
	}
}

func TestFlattenPieceFusingSplineCornerMidpointRectangle(t *testing.T) {
	spline := splineTestPiece(
		splineTestPoint("0", "0", true), splineTestPoint("10", "0", false),
		splineTestPoint("20", "0", true), splineTestPoint("20", "5", false),
		splineTestPoint("20", "10", true), splineTestPoint("10", "10", false),
		splineTestPoint("0", "10", true), splineTestPoint("0", "5", false),
	).FusingZone.Spline
	flat, err := flattenPieceFusingSpline(spline)
	if err != nil {
		t.Fatalf("flatten corner-midpoint rectangle: %v", err)
	}
	const boundaryTolerance = 1e-12
	for i, point := range flat {
		onBoundary := math.Abs(point.x) <= boundaryTolerance || math.Abs(point.x-20) <= boundaryTolerance ||
			math.Abs(point.y) <= boundaryTolerance || math.Abs(point.y-10) <= boundaryTolerance
		if !onBoundary {
			t.Errorf("vertex %d = (%.17g, %.17g), not on rectangle boundary", i, point.x, point.y)
		}
	}
	area2 := 0.0
	for i, point := range flat {
		next := flat[(i+1)%len(flat)]
		area2 += point.x*next.y - next.x*point.y
	}
	if area := math.Abs(area2) / 2; math.Abs(area-200) > 1e-9 {
		t.Errorf("corner-midpoint rectangle area = %.17g, want 200", area)
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

func TestValidatePieceFusingSplineHoles(t *testing.T) {
	outer := func() *TechCardPiece {
		return splineTestPiece(
			splineTestPoint("0", "0", true), splineTestPoint("20", "0", true),
			splineTestPoint("20", "10", true), splineTestPoint("0", "10", true),
		)
	}
	hole := func(x0, y0, x1, y1 string) TechCardPieceFusingSpline {
		return splineTestRing(
			splineTestPoint(x0, y0, true), splineTestPoint(x1, y0, true),
			splineTestPoint(x1, y1, true), splineTestPoint(x0, y1, true),
		)
	}

	valid := outer()
	valid.FusingZone.Holes = []TechCardPieceFusingSpline{
		hole("2", "2", "6", "5"),
		hole("10", "2", "14", "5"),
	}
	if err := ValidatePieceFusingDetails("pieces[0]", valid); err != nil {
		t.Fatalf("valid holes rejected: %v", err)
	}

	tests := map[string][]TechCardPieceFusingSpline{
		"too few points": {
			splineTestRing(splineTestPoint("2", "2", true), splineTestPoint("6", "2", true)),
		},
		"outside outer ring": {hole("18", "2", "22", "5")},
		"touches outer ring": {hole("0", "2", "4", "5")},
		"overlapping holes": {
			hole("2", "2", "8", "6"), hole("6", "3", "12", "7"),
		},
		"nested holes": {
			hole("2", "2", "12", "8"), hole("4", "3", "8", "6"),
		},
	}
	for name, holes := range tests {
		t.Run(name, func(t *testing.T) {
			piece := outer()
			piece.FusingZone.Holes = holes
			if err := ValidatePieceFusingDetails("pieces[0]", piece); err == nil {
				t.Fatal("invalid holes accepted")
			}
		})
	}

	tooMany := outer()
	tooMany.FusingZone.Holes = make([]TechCardPieceFusingSpline, TechCardPieceFusingZoneMaxHoles+1)
	for i := range tooMany.FusingZone.Holes {
		tooMany.FusingZone.Holes[i] = hole("2", "2", "3", "3")
	}
	if err := ValidatePieceFusingDetails("pieces[0]", tooMany); err == nil {
		t.Fatal("too many holes accepted")
	}

	rect := &TechCardPiece{
		Fused: true, FusingMode: fusingMode(PieceFusingModeZone),
		FusingZone: &TechCardPieceFusingZone{
			Rect:  &TechCardPieceFusingRect{X1: decimal.NewFromInt(20), Y1: decimal.NewFromInt(10)},
			Holes: []TechCardPieceFusingSpline{hole("2", "2", "6", "5")},
		},
	}
	if err := ValidatePieceFusingDetails("pieces[0]", rect); err == nil {
		t.Fatal("holes on a rectangle zone accepted")
	}
}

func TestPieceFusingSplineZoneAreaSubtractsHoles(t *testing.T) {
	piece := splineTestPiece(
		splineTestPoint("0", "0", true), splineTestPoint("20", "0", true),
		splineTestPoint("20", "10", true), splineTestPoint("0", "10", true),
	)
	piece.FusingZone.Holes = []TechCardPieceFusingSpline{
		splineTestRing(
			splineTestPoint("2", "2", true), splineTestPoint("6", "2", true),
			splineTestPoint("6", "5", true), splineTestPoint("2", "5", true),
		),
		splineTestRing(
			splineTestPoint("10", "2", true), splineTestPoint("14", "2", true),
			splineTestPoint("14", "5", true), splineTestPoint("10", "5", true),
		),
	}
	got, err := pieceFusingSplineZoneAreaMm2(piece.FusingZone)
	if err != nil {
		t.Fatalf("zone area: %v", err)
	}
	if math.Abs(got-176) > 1e-9 {
		t.Fatalf("zone area = %.17g mm², want 200 - 12 - 12 = 176", got)
	}
}
