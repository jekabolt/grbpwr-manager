package dto

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// Фикстуры блоба (layoutBlob / piece / placements / comp / marked / unmarked) живут в
// production_lay_yield_test.go и переиспользуются здесь: второй набор строителей блоба означал бы,
// что два теста одного файла считают маркер по-разному.

// ------------------------------------------------------------------ fixtures

const (
	fixtureFabricSlot = 11 // bom_item.id основной ткани
	fixtureLiningSlot = 12 // bom_item.id подкладки
	fixtureLabelSlot  = 13 // bom_item.id этикетки — секция ВНЕ planSlotSections
	fixtureCw1        = 101
	fixtureCw2        = 102
	fixtureSize       = 10
)

func slotRef(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
func idxRef(i int32) sql.NullInt32   { return sql.NullInt32{Int32: i, Valid: true} }

// twoClothCard is a sellable card with two cloths and two colourways: полочка кроится из основной,
// подкладочная деталь — из подкладки, и так у обоих цветов.
func twoClothCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.Purpose = entity.TechCardPurposeSellable
	card.BomItems = []entity.TechCardBomItem{
		{Id: fixtureFabricSlot, LineKey: "BOM_MAIN", Name: "ОСНОВНАЯ ТКАНЬ", Section: entity.BomSectionFabric},
		{Id: fixtureLiningSlot, LineKey: "BOM_LINING", Name: "ПОДКЛАДКА", Section: entity.BomSectionLining},
		{Id: fixtureLabelSlot, LineKey: "BOM_LABEL", Name: "ЭТИКЕТКА", Section: entity.BomSectionLabel},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, ProductId: idxRef(fixtureCw1)},
		{Id: 2, ProductId: idxRef(fixtureCw2)},
	}
	card.Pieces = []entity.TechCardPiece{
		{
			Id: 1, Name: "ПОЛОЧКА", LineKey: "K_FRONT", PiecesPerGarment: 2,
			CutSymmetry: marked(entity.PieceCutSymmetryMirrored),
			Materials: []entity.TechCardPieceMaterial{
				{ColorwayID: fixtureCw1, BomItemId: slotRef(fixtureFabricSlot)},
				{ColorwayID: fixtureCw2, BomItemId: slotRef(fixtureFabricSlot)},
			},
		},
		{
			Id: 2, Name: "ПОДКЛАДКА ПОЛОЧКИ", LineKey: "K_LINING", PiecesPerGarment: 1,
			CutSymmetry: marked(entity.PieceCutSymmetryIdentical),
			Materials: []entity.TechCardPieceMaterial{
				{ColorwayID: fixtureCw1, BomItemId: slotRef(fixtureLiningSlot)},
				{ColorwayID: fixtureCw2, BomItemId: slotRef(fixtureLiningSlot)},
			},
		},
	}
	return card
}

func runLine(lineKey string, colorwayID, sizeID, qty int) entity.ProductionRunLine {
	l := entity.ProductionRunLine{LineKey: lineKey, SizeId: sizeID, PlannedQty: qty}
	if colorwayID > 0 {
		l.ProductId = idxRef(int32(colorwayID))
	}
	return l
}

// frontMarker lays ONE mirrored pair of fronts per layer for one garment of fixtureSize.
func frontMarker(t *testing.T) MarkerYield {
	t.Helper()
	return mustYield(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{fixtureSize, 1}),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", fixtureSize, 2)},
		Placements:    placements(1, 1, 1),
	})
}

// liningMarker lays ONE lining panel per layer for one garment of fixtureSize.
func liningMarker(t *testing.T) MarkerYield {
	t.Helper()
	return mustYield(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{fixtureSize, 1}),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОДКЛАДКА ПОЛОЧКИ", "K_LINING", fixtureSize, 1)},
		Placements:    placements(1, 1, 0),
	})
}

func mustYield(t *testing.T, l *pb_common.TechCardMarkerLayout) MarkerYield {
	t.Helper()
	y, err := MarkerYieldFromBlob(layoutBlob(t, l))
	if err != nil {
		t.Fatalf("fixture blob does not distil: %v", err)
	}
	return y
}

func lay(name string, colorwayID int, slot int64, plies int, y MarkerYield) Lay {
	return Lay{
		LayKey: name, Name: name, ColorwayID: colorwayID, BomItemID: slot, Mode: LayFaceModeFaceUp,
		Sections: []LaySection{{MarkerID: 1, Plies: plies, Yield: y}},
	}
}

func cellOf(t *testing.T, c LayCoverage, colorwayID, sizeID int) LayCoverageCell {
	t.Helper()
	for _, cell := range c.Cells {
		if cell.ColorwayID == colorwayID && cell.SizeID == sizeID {
			return cell
		}
	}
	t.Fatalf("no cell for colourway %d size %d in %+v", colorwayID, sizeID, c.Cells)
	return LayCoverageCell{}
}

// ------------------------------------------------------- §13, зонд 1

// Прогон на 2 ткани × 2 колорвея: покрытие зелёное ТОЛЬКО когда раскроены все четыре пары, и
// «22 полочки + 20 подкладок = 20 изделий» выпадает из минимума, а не из ветки про разные ткани.
func TestLayCoverageTwoClothsTwoColorways(t *testing.T) {
	card := twoClothCard()
	lines := []entity.ProductionRunLine{
		runLine("L1", fixtureCw1, fixtureSize, 22),
		runLine("L2", fixtureCw2, fixtureSize, 22),
	}

	t.Run("22 полочек и 20 подкладок дают 20, и виноват слот ПОДКЛАДКИ", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: lines,
			Lays: []Lay{
				lay("основная ц1", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t)),
				lay("подкладка ц1", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
				lay("основная ц2", fixtureCw2, fixtureFabricSlot, 22, frontMarker(t)),
				lay("подкладка ц2", fixtureCw2, fixtureLiningSlot, 22, liningMarker(t)),
			},
		})
		if !cov.Applicable {
			t.Fatalf("sellable card must be applicable: %q", cov.NotApplicableReason)
		}

		c1 := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c1.CoveredQty != 20 {
			t.Errorf("covered = %d, want 20 — min(22 полочки, 20 подкладок)", c1.CoveredQty)
		}
		if c1.Status != CoverageStatusBlocker {
			t.Errorf("status = %s, want BLOCKER", c1.Status)
		}
		if len(c1.BlockingBomItemIDs) != 1 || c1.BlockingBomItemIDs[0] != fixtureLiningSlot {
			t.Errorf("blocking = %v, want [%d] — слот ПОДКЛАДКИ, не основной ткани",
				c1.BlockingBomItemIDs, fixtureLiningSlot)
		}
		if len(c1.BlockingPieceNames) != 1 || c1.BlockingPieceNames[0] != "ПОДКЛАДКА ПОЛОЧКИ" {
			t.Errorf("blocking pieces = %v, want [ПОДКЛАДКА ПОЛОЧКИ]", c1.BlockingPieceNames)
		}
		if c1.Source != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS {
			t.Errorf("source = %v, want LAYS — обе пары настелены", c1.Source)
		}

		// Второй колорвей раскроен целиком — он зелёный, и его зелень не красит первый.
		c2 := cellOf(t, cov, fixtureCw2, fixtureSize)
		if c2.Status != CoverageStatusOK || c2.CoveredQty != 22 {
			t.Errorf("colourway 2: status = %s, covered = %d, want OK / 22", c2.Status, c2.CoveredQty)
		}
		if cov.Status() != CoverageStatusBlocker {
			t.Errorf("run status = %s, want BLOCKER", cov.Status())
		}
		if cov.ColorwayStatus(fixtureCw2) != CoverageStatusOK {
			t.Errorf("colourway 2 status = %s, want OK", cov.ColorwayStatus(fixtureCw2))
		}
	})

	t.Run("не раскроена ни одна пара второго цвета — честный ноль, BLOCKER, источник NORM", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: lines,
			Lays: []Lay{
				lay("основная ц1", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t)),
				lay("подкладка ц1", fixtureCw1, fixtureLiningSlot, 22, liningMarker(t)),
			},
		})
		c2 := cellOf(t, cov, fixtureCw2, fixtureSize)
		if c2.Status != CoverageStatusBlocker || c2.CoveredQty != 0 {
			t.Errorf("status = %s, covered = %d, want BLOCKER / 0 — ткань не раскроена", c2.Status, c2.CoveredQty)
		}
		if c2.Source != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM {
			t.Errorf("source = %v, want NORM — у цвета нет ни одного настила", c2.Source)
		}
		if len(c2.SlotsWithoutLays) != 2 {
			t.Errorf("slots without lays = %v, want both slots", c2.SlotsWithoutLays)
		}
		if cov.ColorwayStatus(fixtureCw1) != CoverageStatusOK {
			t.Errorf("colourway 1 = %s, want OK — обе его пары раскроены на 22", cov.ColorwayStatus(fixtureCw1))
		}
	})

	t.Run("раскроена только основная — MIXED, и подкладка называет свой слот", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 22)},
			Lays:  []Lay{lay("основная ц1", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t))},
		})
		c := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c.Source != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED {
			t.Errorf("source = %v, want MIXED", c.Source)
		}
		if len(c.SlotsWithoutLays) != 1 || c.SlotsWithoutLays[0] != fixtureLiningSlot {
			t.Errorf("slots without lays = %v, want [%d]", c.SlotsWithoutLays, fixtureLiningSlot)
		}
		if c.Status != CoverageStatusBlocker || c.CoveredQty != 0 {
			t.Errorf("status = %s, covered = %d, want BLOCKER / 0", c.Status, c.CoveredQty)
		}
	})
}

// ------------------------------------------------------- §13, зонд 10

// UNKNOWN НЕ ЧИТАЕТСЯ КАК OK ни на каком шаге, и доказанная нехватка не смягчается неизвестностью.
func TestLayCoverageUnknownNeverReadsAsOK(t *testing.T) {
	card := twoClothCard()
	// Деталь без разметки cut_symmetry на основной ткани — «НЕ РАЗМЕЧЕНО» (0275).
	card.Pieces = append(card.Pieces, entity.TechCardPiece{
		Id: 3, Name: "КАРМАН", LineKey: "K_POCKET", PiecesPerGarment: 1, CutSymmetry: unmarked,
		Materials: []entity.TechCardPieceMaterial{{ColorwayID: fixtureCw1, BomItemId: slotRef(fixtureFabricSlot)}},
	})
	pocketMarker := mustYield(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   comp([2]int32{fixtureSize, 1}),
		Pieces: []*pb_common.TechCardMarkerPiece{
			piece(1, "ПОЛОЧКА", "K_FRONT", fixtureSize, 2),
			piece(2, "КАРМАН", "K_POCKET", fixtureSize, 1),
		},
		Placements: concat(placements(1, 1, 1), placements(2, 1, 0)),
	})

	t.Run("неразмеченная деталь даёт UNKNOWN, а не OK", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
			Lays: []Lay{
				lay("основная", fixtureCw1, fixtureFabricSlot, 20, pocketMarker),
				lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
			},
		})
		c := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c.Status != CoverageStatusUnknown {
			t.Fatalf("status = %s, want UNKNOWN — карман не размечен", c.Status)
		}
		if c.UnknownPieceCount != 1 {
			t.Errorf("unknown pieces = %d, want 1", c.UnknownPieceCount)
		}
		if c.CoveredQty != 20 {
			t.Errorf("covered = %d, want 20 as a LOWER BOUND", c.CoveredQty)
		}
		if cov.UnknownCount != 1 {
			t.Errorf("unknown count = %d, want 1", cov.UnknownCount)
		}
		if cov.Status() != CoverageStatusUnknown {
			t.Errorf("run status = %s, want UNKNOWN", cov.Status())
		}
	})

	t.Run("та же клетка с ДОКАЗАННОЙ нехваткой по другой детали даёт BLOCKER, а не UNKNOWN", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
			Lays: []Lay{
				lay("основная", fixtureCw1, fixtureFabricSlot, 20, pocketMarker),
				lay("подкладка", fixtureCw1, fixtureLiningSlot, 18, liningMarker(t)), // 18 < 20 — доказано
			},
		})
		c := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c.Status != CoverageStatusBlocker {
			t.Fatalf("status = %s, want BLOCKER — нехватка подкладки доказана", c.Status)
		}
		if c.UnknownPieceCount != 1 {
			t.Errorf("unknown pieces = %d, want 1 — карман всё ещё молчит", c.UnknownPieceCount)
		}
		if len(c.BlockingBomItemIDs) != 1 || c.BlockingBomItemIDs[0] != fixtureLiningSlot {
			t.Errorf("blocking = %v, want [%d]", c.BlockingBomItemIDs, fixtureLiningSlot)
		}
	})
}

// Свёртка статусов прогона обязана идти по ЛЕСТНИЦЕ, а не по числовому порядку констант: max по
// константам ответил бы OK для прогона с неизвестной клеткой (UNKNOWN = 0 < OK = 1).
func TestRunStatusFoldsThroughTheLadderNotTheConstants(t *testing.T) {
	cov := LayCoverage{Applicable: true, Cells: []LayCoverageCell{
		{ColorwayID: fixtureCw1, SizeID: 1, Status: CoverageStatusOK},
		{ColorwayID: fixtureCw1, SizeID: 2, Status: CoverageStatusUnknown},
	}}
	if got := cov.Status(); got != CoverageStatusUnknown {
		t.Errorf("status = %s, want UNKNOWN (max по константам дал бы OK — это и есть ловушка)", got)
	}
	cov.Cells = append(cov.Cells, LayCoverageCell{ColorwayID: fixtureCw1, SizeID: 3, Status: CoverageStatusBlocker})
	if got := cov.Status(); got != CoverageStatusBlocker {
		t.Errorf("status = %s, want BLOCKER", got)
	}
	if got := (LayCoverage{}).Status(); got != CoverageStatusUnknown {
		t.Errorf("empty coverage = %s, want UNKNOWN", got)
	}
}

// ------------------------------------------------------- §14 п.4

// Строка прогона без колорвея (product_id NULLable, 0110:20) не может быть покрыта ни одним
// настилом и обязана дать ЯВНУЮ находку, а не тихо уменьшить знаменатель.
func TestRunLineWithoutColorwayIsAnExplicitFinding(t *testing.T) {
	card := twoClothCard()
	cov := ComputeLayCoverage(LayCoverageInput{
		Card: card,
		Lines: []entity.ProductionRunLine{
			runLine("L1", fixtureCw1, fixtureSize, 20),
			runLine("L2", 0, fixtureSize, 5), // без колорвея
			runLine("L3", fixtureCw1, 0, 5),  // без размера (0236)
		},
		Lays: []Lay{
			lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t)),
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})

	if len(cov.Cells) != 1 {
		t.Fatalf("cells = %d, want 1 — непокрываемые строки не становятся клетками", len(cov.Cells))
	}
	if len(cov.Findings) != 2 {
		t.Fatalf("findings = %+v, want 2", cov.Findings)
	}
	byKey := map[string]LayCoverageFinding{}
	for _, f := range cov.Findings {
		byKey[f.Key] = f
	}
	f, ok := byKey[LayCoverageFindingKeyLineWithoutColorway]
	if !ok {
		t.Fatalf("no %s finding in %+v", LayCoverageFindingKeyLineWithoutColorway, cov.Findings)
	}
	if f.LineKey != "L2" {
		t.Errorf("finding names line %q, want L2", f.LineKey)
	}
	if !strings.Contains(f.Detail, "без колорвея") {
		t.Errorf("detail = %q, must say the line has no colourway", f.Detail)
	}
	if _, ok := byKey[LayCoverageFindingKeyLineWithoutSize]; !ok {
		t.Fatalf("no %s finding in %+v", LayCoverageFindingKeyLineWithoutSize, cov.Findings)
	}
	// И они СЧИТАЮТСЯ: ноль неизвестных рядом с зелёной сеткой читался бы как «всё проверено».
	if cov.UnknownCount != 2 {
		t.Errorf("unknown count = %d, want 2 — обе находки считаются", cov.UnknownCount)
	}
	if c := cellOf(t, cov, fixtureCw1, fixtureSize); c.Status != CoverageStatusOK {
		t.Errorf("покрытая клетка = %s, want OK — находки не портят чужую клетку", c.Status)
	}
}

// ------------------------------------------------------- «ни одна обязательная деталь не теряется»

// Деталь, за которую цикл заполнения не ответил, ОБЯЗАНА попасть в клетку как UNKNOWN. Ни один тип
// этого не ловит — Add с нулевым PieceYield неотличим от невызванного Add, — поэтому гарантия
// структурная: CoverageCell.Add вызывается по СПИСКУ ОБЯЗАТЕЛЬНЫХ ДЕТАЛЕЙ в finish(), а не в цикле
// заполнения. Этот тест приколачивает именно её: убери проход по required в finish() — и он падает.
func TestForgottenRequiredPieceCannotVanishFromTheCell(t *testing.T) {
	card := twoClothCard()
	required := requiredPiecesForColorway(card, fixtureCw1)
	if len(required) != 2 {
		t.Fatalf("required = %d, want 2", len(required))
	}
	enough := PieceYield{Garments: 20, Known: true}

	t.Run("ответили за обе — OK", func(t *testing.T) {
		b := newCoverageCellBuilder(20, required)
		b.answer(0, cellAnswer{yield: enough})
		b.answer(1, cellAnswer{yield: enough})
		cell := b.finish()
		if cell.Status() != CoverageStatusOK {
			t.Fatalf("status = %s, want OK", cell.Status())
		}
		if len(b.missing) != 0 {
			t.Fatalf("missing = %v, want none", b.missing)
		}
	})

	t.Run("забыли одну — клетка UNKNOWN, и забытая названа", func(t *testing.T) {
		b := newCoverageCellBuilder(20, required)
		b.answer(0, cellAnswer{yield: enough}) // за подкладку никто не ответил
		cell := b.finish()
		if cell.Status() != CoverageStatusUnknown {
			t.Fatalf("status = %s, want UNKNOWN — забытая деталь не может исчезнуть из минимума", cell.Status())
		}
		if cell.UnknownPieceCount() != 1 {
			t.Errorf("unknown pieces = %d, want 1", cell.UnknownPieceCount())
		}
		if len(b.missing) != 1 || b.missing[0] != "ПОДКЛАДКА ПОЛОЧКИ" {
			t.Fatalf("missing = %v, want [ПОДКЛАДКА ПОЛОЧКИ]", b.missing)
		}
	})

	t.Run("две безымянные детали не схлопываются в один голос", func(t *testing.T) {
		// Легаси-строки без line_key: если ключ клетки брать «как есть», обе станут одной записью,
		// и минимум потеряет одну из них молча.
		legacy := []requiredPiece{
			{piece: &entity.TechCardPiece{Id: 41, Name: "A"}, resolved: true, bomItemID: fixtureFabricSlot},
			{piece: &entity.TechCardPiece{Id: 42, Name: "B"}, resolved: true, bomItemID: fixtureFabricSlot},
		}
		if legacy[0].key() == legacy[1].key() {
			t.Fatalf("two keyless pieces share the cell key %q", legacy[0].key())
		}
		b := newCoverageCellBuilder(20, legacy)
		b.answer(0, cellAnswer{yield: enough})
		b.answer(1, cellAnswer{yield: PieceYield{Garments: 3, Known: true}})
		cell := b.finish()
		if cell.CoveredQty() != 3 {
			t.Errorf("covered = %d, want 3 — вторая деталь обязана остаться в минимуме", cell.CoveredQty())
		}
	})
}

// Сквозная половина той же гарантии: деталь, чей слот вообще не настелен, тянет клетку вниз, а не
// выпадает из неё.
func TestEveryRequiredPieceReachesTheCell(t *testing.T) {
	card := twoClothCard()
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays:  []Lay{lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t))},
	})
	seen := map[string]bool{}
	for _, y := range cov.PieceYields {
		if y.ColorwayID == fixtureCw1 && y.SizeID == fixtureSize {
			seen[y.PieceLineKey] = true
		}
	}
	for _, want := range []string{"K_FRONT", "K_LINING"} {
		if !seen[want] {
			t.Errorf("деталь %s не дошла до клетки: %+v", want, cov.PieceYields)
		}
	}
	// Этикеточный слот вне planSlotSections, поэтому деталей на нём в required быть не может —
	// определение required(C) берётся из существующего planSlotSections, а не заводится второе.
	if len(seen) != 2 {
		t.Errorf("required pieces = %v, want exactly the two garment pieces", seen)
	}
}

// Деталь, привязанная к слоту ВНЕ planSlotSections (этикетка), не обязательна: ярлыки едут по
// сборочному рецепту, и их отсутствие в раскрое не нехватка.
func TestPieceOnNonRecipeSlotIsNotRequired(t *testing.T) {
	card := twoClothCard()
	card.Pieces = append(card.Pieces, entity.TechCardPiece{
		Id: 9, Name: "ВШИВНОЙ ЯРЛЫК", LineKey: "K_LABEL", PiecesPerGarment: 1,
		CutSymmetry: marked(entity.PieceCutSymmetryIdentical),
		Materials:   []entity.TechCardPieceMaterial{{ColorwayID: fixtureCw1, BomItemId: slotRef(fixtureLabelSlot)}},
	})
	for _, rp := range requiredPiecesForColorway(card, fixtureCw1) {
		if rp.piece.LineKey == "K_LABEL" {
			t.Fatalf("этикеточная деталь попала в required: %+v", rp)
		}
	}
}

// ------------------------------------------------------- §14 п.5

// Резолв слота идёт ЧЕРЕЗ ОБЩИЙ РЕЗОЛВЕР (planBomLine), а не по позиции: приоритет — FK, потом
// легаси-индекс. Резолв только по позиции уже давал ПУСТОЙ материал-план на бете.
func TestPieceSlotResolvesByFkBeforePosition(t *testing.T) {
	items := twoClothCard().BomItems // индекс 0 = основная, 1 = подкладка, 2 = этикетка

	t.Run("FK бьёт позиционный индекс", func(t *testing.T) {
		m := &entity.TechCardPieceMaterial{
			ColorwayID:   fixtureCw1,
			BomItemId:    slotRef(fixtureLiningSlot), // подкладка
			BomItemIndex: idxRef(0),                  // а позиция указывает на основную
		}
		got := pieceSlotBomLine(m, items)
		if got == nil || got.Id != fixtureLiningSlot {
			t.Fatalf("resolved %+v, want the lining slot %d", got, fixtureLiningSlot)
		}
	})

	t.Run("легаси-строка без FK резолвится позиционно", func(t *testing.T) {
		got := pieceSlotBomLine(&entity.TechCardPieceMaterial{BomItemIndex: idxRef(1)}, items)
		if got == nil || got.Id != fixtureLiningSlot {
			t.Fatalf("resolved %+v, want the lining slot", got)
		}
	})

	t.Run("нерезолвимая ссылка даёт UNKNOWN, а деталь остаётся в клетке", func(t *testing.T) {
		card := twoClothCard()
		card.Pieces[1].Materials = []entity.TechCardPieceMaterial{
			{ColorwayID: fixtureCw1, BomItemIndex: idxRef(99)}, // индекс за пределами BOM
		}
		required := requiredPiecesForColorway(card, fixtureCw1)
		if len(required) != 2 {
			t.Fatalf("required = %d, want 2 — деталь с нерезолвимым слотом НЕ выпадает", len(required))
		}
		var lining requiredPiece
		for _, rp := range required {
			if rp.piece.LineKey == "K_LINING" {
				lining = rp
			}
		}
		if lining.resolved {
			t.Fatalf("lining must stay unresolved, got %+v", lining)
		}
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
			Lays:  []Lay{lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t))},
		})
		c := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c.Status != CoverageStatusUnknown || c.UnknownPieceCount != 1 {
			t.Errorf("status = %s, unknown = %d, want UNKNOWN / 1", c.Status, c.UnknownPieceCount)
		}
	})

	t.Run("деталь без привязки к этому колорвею тоже UNKNOWN, а не пропуск", func(t *testing.T) {
		card := twoClothCard()
		card.Pieces[1].Materials = nil
		required := requiredPiecesForColorway(card, fixtureCw1)
		if len(required) != 2 {
			t.Fatalf("required = %d, want 2", len(required))
		}
	})
}

// ------------------------------------------------------- режим и разбор

// Нечётные слои в режиме «лицом к лицу»: секция не вносит НИЧЕГО. Это доказанный ноль (BLOCKER), а
// не пробел (UNKNOWN) — последний непарный слой отдаёт только одну хиральность.
func TestFaceToFaceOddPliesContributesNothingAndBlocks(t *testing.T) {
	card := twoClothCard()
	odd := lay("основная", fixtureCw1, fixtureFabricSlot, 21, frontMarker(t))
	odd.Mode = LayFaceModeFaceToFace
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays: []Lay{
			odd,
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})
	c := cellOf(t, cov, fixtureCw1, fixtureSize)
	if c.Status != CoverageStatusBlocker {
		t.Fatalf("status = %s, want BLOCKER", c.Status)
	}
	if c.CoveredQty != 0 {
		t.Errorf("covered = %d, want 0 — нечётная секция не вносит ничего", c.CoveredQty)
	}
	if c.UnknownPieceCount != 0 {
		t.Errorf("unknown = %d, want 0 — это доказанный ноль, а не пробел", c.UnknownPieceCount)
	}
	joined := strings.Join(cov.Caveats, " | ")
	if !strings.Contains(joined, "нечётная") {
		t.Errorf("caveats = %q, must name the odd section", joined)
	}
}

// Нечитаемый блоб и неатрибутируемый маркер дают UNKNOWN, а не ноль: иначе сбой разбора
// превращается в нехватку, которую цех не может воспроизвести.
func TestUnreadableMarkerIsUnknownNotZero(t *testing.T) {
	card := twoClothCard()
	broken := lay("основная", fixtureCw1, fixtureFabricSlot, 20, MarkerYield{})
	broken.Sections[0].YieldErr = errors.New("stored marker layout does not parse")
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays: []Lay{
			broken,
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})
	c := cellOf(t, cov, fixtureCw1, fixtureSize)
	if c.Status != CoverageStatusUnknown {
		t.Fatalf("status = %s, want UNKNOWN", c.Status)
	}
	if c.UnknownPieceCount != 1 {
		t.Errorf("unknown = %d, want 1 — полочка молчит, подкладка ответила", c.UnknownPieceCount)
	}
}

// Настил, потерявший слот BOM (fk SET NULL, 0257:63), кроил ЧТО-ТО этого цвета. Доказанная
// нехватка перестаёт быть доказанной; доказанная достаточность остаётся — лишняя ткань не может
// уменьшить выход.
func TestBrokenLayMakesShortageUnprovableButKeepsSufficiency(t *testing.T) {
	card := twoClothCard()
	orphan := Lay{LayKey: "X", Name: "настил без слота", ColorwayID: fixtureCw1, BomItemID: 0,
		Mode: LayFaceModeFaceUp, Sections: []LaySection{{MarkerID: 5, Plies: 10, Yield: frontMarker(t)}}}

	t.Run("нехватка становится UNKNOWN", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
			Lays: []Lay{
				orphan,
				lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t)),
				lay("подкладка", fixtureCw1, fixtureLiningSlot, 18, liningMarker(t)),
			},
		})
		c := cellOf(t, cov, fixtureCw1, fixtureSize)
		if c.Status != CoverageStatusUnknown {
			t.Fatalf("status = %s, want UNKNOWN", c.Status)
		}
		if !strings.Contains(strings.Join(cov.Caveats, " | "), "потерял слот BOM") {
			t.Errorf("caveats = %q, must name the broken lay", cov.Caveats)
		}
	})

	t.Run("достаточность остаётся OK", func(t *testing.T) {
		cov := ComputeLayCoverage(LayCoverageInput{
			Card:  card,
			Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
			Lays: []Lay{
				orphan,
				lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t)),
				lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
			},
		})
		if c := cellOf(t, cov, fixtureCw1, fixtureSize); c.Status != CoverageStatusOK {
			t.Errorf("status = %s, want OK", c.Status)
		}
	})
}

// Легаси-маркер (схема < 3) не знает зеркальности: «нет зеркальных размещений» не доказательство,
// поэтому зеркальная деталь даёт UNKNOWN на КАЖДОМ таком маркере, а не ложную тревогу.
func TestLegacyMarkerLeavesMirroredPieceUnknown(t *testing.T) {
	card := twoClothCard()
	legacy := mustYield(t, &pb_common.TechCardMarkerLayout{
		SchemaVersion: 2,
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "ПОЛОЧКА", "K_FRONT", 0, 2)},
		Placements:    placements(1, 2, 0),
	})
	withComp, err := legacy.WithSummaryComposition(fixtureSize, 1)
	if err != nil {
		t.Fatalf("summary composition: %v", err)
	}
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays: []Lay{
			lay("основная", fixtureCw1, fixtureFabricSlot, 20, withComp),
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})
	c := cellOf(t, cov, fixtureCw1, fixtureSize)
	if c.Status != CoverageStatusUnknown || c.UnknownPieceCount != 1 {
		t.Fatalf("status = %s, unknown = %d, want UNKNOWN / 1", c.Status, c.UnknownPieceCount)
	}
}

// ------------------------------------------------------- §6.3, aux

func TestAuxCardYieldsNoCells(t *testing.T) {
	card := twoClothCard()
	card.Purpose = entity.TechCardPurposeAuxiliary
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
	})
	if cov.Applicable {
		t.Fatalf("aux card must not be applicable")
	}
	if len(cov.Cells) != 0 || len(cov.PieceYields) != 0 {
		t.Errorf("aux card produced cells: %+v", cov.Cells)
	}
	if cov.NotApplicableReason == "" {
		t.Errorf("aux card must say why")
	}
	// И наложение на гейт для неё — no-op: ноль означал бы «не покрыто», а покрывать нечего.
	rows := []*pb_admin.ProductionRunReadinessUnitCoverage{{ColorwayId: fixtureCw1, SizeId: fixtureSize, ProvisionedQty: 20}}
	if n := cov.ApplyToUnitCoverage(rows); n != 0 || rows[0].GetProvisionedQty() != 20 {
		t.Errorf("aux overlay switched %d rows and left %+v", n, rows[0])
	}
}

// ------------------------------------------------------- §6.4 дословно

func TestApplyToUnitCoverageExecutesTheContract(t *testing.T) {
	card := twoClothCard()
	cov := ComputeLayCoverage(LayCoverageInput{
		Card: card,
		Lines: []entity.ProductionRunLine{
			runLine("L1", fixtureCw1, fixtureSize, 22),
			runLine("L2", fixtureCw2, fixtureSize, 22),
		},
		Lays: []Lay{
			lay("основная ц1", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t)),
			lay("подкладка ц1", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})

	rows := []*pb_admin.ProductionRunReadinessUnitCoverage{
		{ColorwayId: fixtureCw1, SizeId: fixtureSize, PlannedQty: 22, ProvisionedQty: 22, UnitsFromStock: 7,
			Source: pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM},
		{ColorwayId: fixtureCw2, SizeId: fixtureSize, PlannedQty: 22, ProvisionedQty: 22, UnitsFromStock: 9,
			Source: pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM},
	}
	switched := cov.ApplyToUnitCoverage(rows)
	if switched != 1 {
		t.Fatalf("switched %d rows, want 1 — только у первого цвета есть настилы", switched)
	}

	got := rows[0]
	if got.GetProvisionedQty() != 20 {
		t.Errorf("provisioned = %d, want 20 (covered_qty)", got.GetProvisionedQty())
	}
	if got.GetSource() != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS {
		t.Errorf("source = %v, want LAYS", got.GetSource())
	}
	if len(got.GetBlockingBomItemIds()) != 1 || got.GetBlockingBomItemIds()[0] != fixtureLiningSlot {
		t.Errorf("blocking = %v, want [%d]", got.GetBlockingBomItemIds(), fixtureLiningSlot)
	}
	if got.GetUnitsFromStock() != 7 {
		t.Errorf("units_from_stock = %d, want 7 — оценка по остатку не трогается", got.GetUnitsFromStock())
	}
	if got.GetPlannedQty() != 22 || got.GetColorwayId() != fixtureCw1 || got.GetSizeId() != fixtureSize {
		t.Errorf("ключ строки изменился: %+v", got)
	}

	untouched := rows[1]
	if untouched.GetProvisionedQty() != 22 ||
		untouched.GetSource() != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM {
		t.Errorf("строка без настилов изменилась: %+v", untouched)
	}
}

func TestCoverageCellsPbCarriesTheCell(t *testing.T) {
	card := twoClothCard()
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 22)},
		Lays: []Lay{
			lay("основная", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t)),
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 20, liningMarker(t)),
		},
	})
	pb := cov.CoverageCellsPb()
	if len(pb) != 1 {
		t.Fatalf("cells = %d, want 1", len(pb))
	}
	c := pb[0]
	if c.GetCoveredQty() != 20 || c.GetPlannedQty() != 22 {
		t.Errorf("covered/planned = %d/%d, want 20/22", c.GetCoveredQty(), c.GetPlannedQty())
	}
	if c.GetStatus() != pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER {
		t.Errorf("status = %v, want BLOCKER", c.GetStatus())
	}
	if len(c.GetBlockingBomItemIds()) != 1 || c.GetBlockingBomItemIds()[0] != fixtureLiningSlot {
		t.Errorf("blocking = %v", c.GetBlockingBomItemIds())
	}

	yields := cov.PieceYieldsPb()
	if len(yields) != 2 {
		t.Fatalf("piece yields = %d, want 2", len(yields))
	}
	for _, y := range yields {
		switch y.GetPieceLineKey() {
		case "K_FRONT":
			if y.GetGarmentYield() != 22 || y.GetCutAsDrawn() != 22 || y.GetCutMirrored() != 22 {
				t.Errorf("полочка: %+v, want 22 изделий из 22+22 экземпляров", y)
			}
			if y.GetStatus() != pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK {
				t.Errorf("полочка: status = %v, want OK", y.GetStatus())
			}
			// covered_qty = 20, а выкроено 44 экземпляра на 2 на изделие ⇒ 4 сверх нужного.
			if y.GetOvercutQty() != 4 {
				t.Errorf("полочка: overcut = %d, want 4", y.GetOvercutQty())
			}
		case "K_LINING":
			if y.GetGarmentYield() != 20 {
				t.Errorf("подкладка: yield = %d, want 20", y.GetGarmentYield())
			}
			if y.GetStatus() != pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER {
				t.Errorf("подкладка: status = %v, want BLOCKER", y.GetStatus())
			}
			if y.GetBomItemId() != fixtureLiningSlot {
				t.Errorf("подкладка: slot = %d, want %d", y.GetBomItemId(), fixtureLiningSlot)
			}
		default:
			t.Errorf("unexpected piece %q", y.GetPieceLineKey())
		}
	}
}

// Две строки прогона на одну пару (колорвей, размер) складываются: клетка ключуется парой, и два
// её плана — одно количество, которое надо покрыть.
func TestDuplicateRunLinesForOnePairAreSummed(t *testing.T) {
	card := twoClothCard()
	cov := ComputeLayCoverage(LayCoverageInput{
		Card: card,
		Lines: []entity.ProductionRunLine{
			runLine("L1", fixtureCw1, fixtureSize, 10),
			runLine("L2", fixtureCw1, fixtureSize, 12),
		},
		Lays: []Lay{
			lay("основная", fixtureCw1, fixtureFabricSlot, 22, frontMarker(t)),
			lay("подкладка", fixtureCw1, fixtureLiningSlot, 22, liningMarker(t)),
		},
	})
	if len(cov.Cells) != 1 {
		t.Fatalf("cells = %d, want 1", len(cov.Cells))
	}
	c := cov.Cells[0]
	if c.PlannedQty != 22 {
		t.Errorf("planned = %d, want 22 = 10 + 12", c.PlannedQty)
	}
	if c.Status != CoverageStatusOK {
		t.Errorf("status = %s, want OK", c.Status)
	}
}
