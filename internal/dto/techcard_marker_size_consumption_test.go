package dto

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// РАСХОД ПО ПЛОЩАДИ НА ПРОВОДЕ (Ф2.4). The arithmetic is entity's and is asserted there; what is
// asserted here is the wire contract around it — that the areas are taken off the SAME blob the
// geometry is stored from, that they never travel back INTO that blob, and that the summary emits the
// состав, its per-size норма, total_units and the refusal as four readings of one slice.
//
// The раскладка used throughout: 3 × S and 2 × M in one spread, graded pieces plus a size-agnostic
// pocket. a_S = 5200, a_M = 6200, A = 28000 cm²; at used_length 1400 cm the shares are 260 and 310,
// against a mean of 280.
func markerAreaLayoutPb() *pb_common.TechCardMarkerLayout {
	return &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition: []*pb_common.TechCardMarkerCompositionEntry{
			{SizeId: 20, Quantity: 2}, {SizeId: 10, Quantity: 3}, // deliberately out of order
		},
		Pieces: []*pb_common.TechCardMarkerPiece{
			{PieceId: 1, Quantity: 1, SizeId: 10, AreaCm2: 3000},
			{PieceId: 2, Quantity: 1, SizeId: 20, AreaCm2: 3600},
			{PieceId: 3, Quantity: 2, SizeId: 10, AreaCm2: 900},
			{PieceId: 4, Quantity: 2, SizeId: 20, AreaCm2: 1100},
			{PieceId: 5, Quantity: 2, AreaCm2: 200}, // безразмерный: по одному набору в КАЖДОЕ изделие
		},
		Placements: []*pb_common.TechCardMarkerPlacement{{PieceId: 1}},
	}
}

// Площади снимаются там же, где собирается состав, и потому не могут не сняться. The insert is the
// only thing the store is handed, so an area that failed to reach it is a marker with no per-size
// норма — which looks exactly like one taken before Ф2.4 and would never be noticed.
func TestMarkerInsertCarriesPerSizeAreas(t *testing.T) {
	pb := validMarkerInsertPb()
	pb.Layout = markerAreaLayoutPb()
	out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)

	// Sorted by size_id, each line carrying the area of ONE garment of that size.
	require.Equal(t, []string{"10:3:5200", "20:2:6200"}, compositionDigest(out.Composition))

	// The denominator is the area of everything the spread lays out — the same number the instance
	// formula gives, which is what makes the distribution converge.
	require.Equal(t, "28000", entity.MarkerCompositionAreaCm2(out.Composition).Decimal.String())
}

// Легаси-полезная нагрузка (size_id + sets, блоб без размеров на деталях) тоже получает площадь: в
// ней все детали безразмерные, значит площадь одного изделия — вся площадь набора. Однородной
// раскладке площадь для нормы не нужна, но она — измерение, и продолжение нормы на соседние размеры
// (клиентская половина Ф2.4) делается именно против таких площадей.
func TestLegacyMarkerInsertStillRecordsItsArea(t *testing.T) {
	pb := validMarkerInsertPb() // size_id 3, sets 4, no layout.composition
	pb.Layout = &pb_common.TechCardMarkerLayout{
		SchemaVersion: 1,
		Pieces: []*pb_common.TechCardMarkerPiece{
			{PieceId: 1, Quantity: 1, AreaCm2: 3000},
			{PieceId: 2, Quantity: 2, AreaCm2: 900},
		},
		Placements: []*pb_common.TechCardMarkerPlacement{{PieceId: 1}},
	}
	out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)
	require.Equal(t, []string{"3:4:4800"}, compositionDigest(out.Composition))
}

// compositionDigest renders a состав as «size:quantity:area» so a comparison is about the VALUES.
// A struct-level require.Equal would compare decimal representations instead — 5200 and 5200.00 are
// the same area and different structs, and the areas arrive rounded to the stored scale.
func compositionDigest(cs []entity.MarkerCompositionEntry) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		area := "—"
		if c.AreaPerGarmentCm2.Valid {
			area = c.AreaPerGarmentCm2.Decimal.String()
		}
		out = append(out, fmt.Sprintf("%d:%d:%s", c.SizeId, c.Quantity, area))
	}
	return out
}

// ПРОИЗВОДНОЕ НЕ ЕДЕТ В БЛОБ. TechCardMarkerCompositionEntry is one message on two surfaces, and the
// admin is an SPA holding both: round-tripping a summary's состав back into a save is the obvious
// thing for a client to do. Frozen inside immutable geometry, a derived расход outlives the
// used_length it came from and is indistinguishable from a measurement to every later reader — the
// exact failure the whole withholding mechanism exists to prevent.
func TestSaveStripsDerivedFieldsFromTheLayoutComposition(t *testing.T) {
	layout := markerAreaLayoutPb()
	for _, c := range layout.Composition {
		c.ConsumptionPerUnitCm = &pb_decimal.Decimal{Value: "999"}
		c.AreaPerGarmentCm2 = &pb_decimal.Decimal{Value: "888"}
	}
	pb := validMarkerInsertPb()
	pb.Layout = layout

	// The insert reads size_id/quantity only, so a forged area cannot become the stored basis: the
	// areas below are the ones computed off the PIECES.
	out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)
	require.Equal(t, "5200", out.Composition[0].AreaPerGarmentCm2.Decimal.String())

	// …and the message that is marshalled into the blob moments later carries neither field.
	_, err = MarkerLayoutFactsFromPb(layout)
	require.NoError(t, err)
	for _, c := range layout.Composition {
		require.Nil(t, c.ConsumptionPerUnitCm, "a derived расход must not be frozen into the blob")
		require.Nil(t, c.AreaPerGarmentCm2, "nor the basis it was derived from")
	}
}

// КРИТЕРИЙ СХОДИМОСТИ НА ПРОВОДЕ: Σ(quantity × consumption_per_unit_cm) = used_length_cm, читая
// ровно те цифры, которые получает клиент. Он же — то место, где скалярный отказ обязан УСТОЯТЬ:
// пер-размерный расход не делает смешанный настил обладателем одной безразмерной цифры.
func TestSummaryEmitsConvergingPerSizeConsumption(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "смешанная",
		UsedLengthCm: decimal.RequireFromString("1400"),
		TotalUnits:   sql.NullInt64{Int64: 5, Valid: true},
		Composition: []entity.MarkerCompositionEntry{
			{SizeId: 10, Quantity: 3, AreaPerGarmentCm2: nd("5200")},
			{SizeId: 20, Quantity: 2, AreaPerGarmentCm2: nd("6200")},
		},
	}
	pb := TechCardMarkerSummaryToPb(m)

	require.Len(t, pb.Composition, 2)
	require.Equal(t, "260", pb.Composition[0].ConsumptionPerUnitCm.Value)
	require.Equal(t, "310", pb.Composition[1].ConsumptionPerUnitCm.Value)
	require.Equal(t, "5200", pb.Composition[0].AreaPerGarmentCm2.Value, "the basis rides beside the result")
	require.Equal(t, "6200", pb.Composition[1].AreaPerGarmentCm2.Value)

	sum := decimal.Zero
	for _, c := range pb.Composition {
		sum = sum.Add(decimal.RequireFromString(c.ConsumptionPerUnitCm.Value).
			Mul(decimal.NewFromInt(int64(c.Quantity))))
	}
	require.Equal(t, "1400", sum.String(),
		"Σ(quantity × расход) must be the length that was measured — this is the acceptance criterion")
	require.Equal(t, pb.UsedLengthCm.Value, sum.String())

	// The scalar stays withheld: «cloth per garment» with no size named still has no true answer here,
	// and this is still the field a client copies into a sizeless costing row.
	require.Nil(t, pb.ConsumptionPerUnitCm)
	require.NotEmpty(t, pb.ScalarApplyRefusal)
	require.Contains(t, pb.ScalarApplyRefusal, "ПО РАЗМЕРАМ", "the remedy now exists and must be named")
	require.NotContains(t, pb.ScalarApplyRefusal, "Ф2.4", "and must stop pointing at a future phase")
	require.Equal(t, int32(5), pb.TotalUnits, "derived from the same slice as the composition above")
}

// Однородная раскладка отвечает на пер-размерный вопрос без площадей — и той же цифрой, что скаляр.
// Это то, что делает пер-размерный режим применимым к КАЖДОМУ маркеру, а не только к снятым после
// Ф2.4: площади — цена смешивания размеров, а не цена нормы.
func TestHomogeneousSummaryEmitsItsSizeNormBesideTheScalar(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "M · основная",
		SizeId:       sql.NullInt64{Int64: 3, Valid: true},
		Sets:         sql.NullInt64{Int64: 4, Valid: true},
		TotalUnits:   sql.NullInt64{Int64: 4, Valid: true},
		Composition:  []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 4}},
		UsedLengthCm: decimal.RequireFromString("512.4"),
	}
	pb := TechCardMarkerSummaryToPb(m)
	require.Empty(t, pb.ScalarApplyRefusal)
	require.Equal(t, "128.1", pb.ConsumptionPerUnitCm.Value)
	require.Len(t, pb.Composition, 1)
	require.Equal(t, "128.1", pb.Composition[0].ConsumptionPerUnitCm.Value,
		"the per-size answer and the scalar are the same number on a homogeneous раскладка")
	require.Nil(t, pb.Composition[0].AreaPerGarmentCm2, "which is why it needs no area at all")
}

// Смешанная раскладка, снятая до Ф2.4: состав есть, площадей нет. Ни одна строка не получает цифры —
// подстановка среднего здесь и есть тот дефект, ради устранения которого фаза существует, — а отказ
// называет действие, которое площади создаёт.
func TestMixedSummaryWithoutAreasEmitsNoPerSizeNumbers(t *testing.T) {
	m := entity.TechCardMarkerSummary{
		Name:         "старая смешанная",
		UsedLengthCm: decimal.RequireFromString("900"),
		TotalUnits:   sql.NullInt64{Int64: 3, Valid: true},
		Composition:  []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 1}, {SizeId: 4, Quantity: 2}},
	}
	pb := TechCardMarkerSummaryToPb(m)
	require.Len(t, pb.Composition, 2, "the состав still says what it cuts")
	for _, c := range pb.Composition {
		require.Nil(t, c.ConsumptionPerUnitCm, "no basis, no number — and never the mean")
		require.Nil(t, c.AreaPerGarmentCm2)
	}
	require.Nil(t, pb.ConsumptionPerUnitCm)
	require.Contains(t, pb.ScalarApplyRefusal, "Пересохраните")
}
