package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ B-09: ДВЕРЬ РЕЖИМА РЕФЕРЕНСА И ЦЕНА СБОРКИ ПО ОПЦИЯМ ════════════════════════════════════════

func threedParams(t *pb_common.DesignThreedParams) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Threed: t}
}

// TestTheThreedReferenceDoorRefusesEveryMalformedShape — таблица отказов, все InvalidArgument, все
// с причиной, которую читает клиент. Каждая строка — своя мутация: убрать соответствующую ветку в
// designRefuseMalformedThreedReferences, и строка краснеет (nil вместо отказа).
func TestTheThreedReferenceDoorRefusesEveryMalformedShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		t      *pb_common.DesignThreedParams
		reason string
		meta   map[string]string
	}{
		{"references on a render run", entity.DesignRunKindRender,
			&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{5}}, "threed_forbidden",
			map[string]string{"kind": entity.DesignRunKindRender}},
		{"an option on a flat run", entity.DesignRunKindFlat,
			&pb_common.DesignThreedParams{Quality: "detailed"}, "threed_forbidden", nil},
		{"a surface hint on a freeform run", entity.DesignRunKindFreeform,
			&pb_common.DesignThreedParams{SurfaceHint: "matte"}, "threed_forbidden", nil},
		{"five pictures", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{1, 2, 3, 4, 5}}, "too_many_pictures",
			map[string]string{"ceiling": "4", "pictures": "5"}},
		{"one picture twice", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{7, 8, 7}}, "duplicate_picture",
			map[string]string{"media_id": "7"}},
		{"texture yes", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{Texture: "yes"}, entity.DesignErrorCodeUnknownOption,
			map[string]string{"field": "texture"}},
		{"pbr true", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{Pbr: "true"}, entity.DesignErrorCodeUnknownOption,
			map[string]string{"field": "pbr"}},
		{"quality ultra", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{Quality: "ultra"}, entity.DesignErrorCodeUnknownOption,
			map[string]string{"field": "quality"}},
		{"pbr on an untextured model", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{Texture: "off", Pbr: "on"}, entity.DesignErrorCodeOptionNotRead,
			map[string]string{"field": "pbr"}},
		{"follow is not advertised", entity.DesignRunKindThreed,
			&pb_common.DesignThreedParams{Follow: "photo"}, entity.DesignErrorCodeOptionNotRead,
			map[string]string{"field": "follow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseMalformedThreedReferences(tc.kind, threedParams(tc.t))
			require.Error(t, err)
			code, md := errorReason(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, tc.reason, md["reason"])
			for k, v := range tc.meta {
				require.Equal(t, v, md[k], k)
			}
		})
	}

	// A NON-POSITIVE ID IS A SHAPE ERROR WITHOUT A TOKEN, like freeform's media_id check.
	err := designRefuseMalformedThreedReferences(entity.DesignRunKindThreed,
		threedParams(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{5, 0}}))
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.threed.reference_media_ids.1")
}

// TestTheThreedReferenceDoorLetsLegalShapesThrough — положительные контроли: без них таблица выше
// доказывала бы лишь, что дверь отказывает всем.
func TestTheThreedReferenceDoorLetsLegalShapesThrough(t *testing.T) {
	for _, tc := range []struct {
		kind string
		p    *pb_common.DesignRunParams
	}{
		{entity.DesignRunKindThreed, nil}, // молчащий реран
		{entity.DesignRunKindThreed, threedParams(nil)},
		{entity.DesignRunKindThreed, threedParams(&pb_common.DesignThreedParams{Presentation: "air"})},
		{entity.DesignRunKindThreed, threedParams(&pb_common.DesignThreedParams{
			ReferenceMediaIds: []int32{11, 12, 13, 14}, Texture: "on", Pbr: "on", Quality: "detailed",
			SurfaceHint: "brushed cotton"})},
		{entity.DesignRunKindThreed, threedParams(&pb_common.DesignThreedParams{
			ReferenceMediaIds: []int32{11}, Texture: "off", Pbr: "off", Quality: "standard"})},
		// Старые поля блока на чужом роде несли молча и раньше — отказ сломал бы старого клиента.
		{entity.DesignRunKindRender, threedParams(&pb_common.DesignThreedParams{Presentation: "model", Frames: 0})},
		{entity.DesignRunKindFlat, &pb_common.DesignRunParams{}},
	} {
		require.NoError(t, designRefuseMalformedThreedReferences(tc.kind, tc.p), "%s %v", tc.kind, tc.p)
	}
}

// TestTheReferencesAreInputsOfTheRun — названные картинки попадают в список, который читают двери
// «не картинка», «только для показа» и «спрятан». МУТАЦИЯ: убрать цикл в designRunInputMediaRefs —
// display-only референс уезжает поставщику за деньги.
func TestTheReferencesAreInputsOfTheRun(t *testing.T) {
	refs := designRunInputMediaRefs(threedParams(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 32}}), nil)
	require.Equal(t, []designInputMediaRef{
		{ID: 31, Where: "params.threed.reference_media_ids.0"},
		{ID: 32, Where: "params.threed.reference_media_ids.1"},
	}, refs)
	require.Equal(t, []int{31, 32}, designThreedReferenceMediaIDs(threedParams(
		&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 32}})))
}

// TestAReferenceRunDoesNotReadTheCard — отбор плит и референсы карточки молчат только у 3D с
// названными картинками; 3D верстака и все прочие роды читают карточку, как вчера.
func TestAReferenceRunDoesNotReadTheCard(t *testing.T) {
	ref := threedParams(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{1}})
	require.True(t, designThreedReferenceMode(ref))
	require.False(t, designRunReadsTheCard(entity.DesignRunKindThreed, ref))
	require.True(t, designRunReadsTheCard(entity.DesignRunKindThreed, threedParams(nil)),
		"3D верстака читает свой верстак, как вчера")
	require.True(t, designRunReadsTheCard(entity.DesignRunKindThreed, nil))
	for _, kind := range []string{entity.DesignRunKindRender, entity.DesignRunKindFlat,
		entity.DesignRunKindFreeform, entity.DesignRunKindRecolor} {
		require.Equal(t, designKindReadsTheCard(kind), designRunReadsTheCard(kind, ref), kind)
	}
}

// ─────────────────────────── цена ───────────────────────────

// TestTheDefaultThreedCeilingIsToday — с пустыми опциями новая функция отвечает РОВНО сегодняшним
// числом, поэтому TestThreedReservationCoversWhatTheRouteActuallyCHARGES и резерв каждого прогона
// верстака не сдвигаются. С 29.09.2026 маршрут 3D один — fal: потолок — его опубликованная цена ($1.20,
// $1.40 за detailed), прямого Meshy в максимуме больше нет. МУТАЦИЯ: fal-оценка detailed по
// умолчанию — красно.
func TestTheDefaultThreedCeilingIsToday(t *testing.T) {
	require.Equal(t, "1.2", designThreedCeilingUSD().String(), "fal's published per-build price")
	require.Equal(t, "1.4", designThreedCeilingUSDFor("", "detailed").String(), "fal's «ultra mode» price")
	require.Equal(t, "1.2", designThreedCeilingUSDFor("off", "").String(), "no cheaper untextured price is published")
	require.True(t, designThreedCeilingUSDFor("", "").Equal(designThreedCeilingUSD()),
		"%s != %s", designThreedCeilingUSDFor("", ""), designThreedCeilingUSD())
	require.True(t, designThreedCeilingUSDFor("on", "standard").Equal(designThreedCeilingUSD()))
	est, ok := (&Server{}).designThreedRunEstimate(entity.DesignRunKindThreed, &pb_common.DesignRunParams{}, 1)
	require.True(t, ok)
	require.True(t, est.Decimal.Equal(designEstimateFor(entity.DesignRunKindThreed, 1).Decimal))
	_, ok = (&Server{}).designThreedRunEstimate(entity.DesignRunKindRender, nil, 1)
	require.False(t, ok, "чужой род оценивает designEstimateFor")
}

// TestADetailedReservationCoversADetailedCharge — ШОВ ДВЕРИ И СПИСАНИЯ НА КАЖДОМ ТИРЕ: резерв не
// ниже того, что попытка запишет без тарифа, и сборка без текстуры не дешевле опубликованного.
//
// МУТАЦИЯ: designThreedCeilingUSDFor, не передающий quality в fal (→ detailed резервирует 1.20
// против списания 1.40 — красно); выдуманная скидка для texture=off на fal (→ красно).
func TestADetailedReservationCoversADetailedCharge(t *testing.T) {
	noTariff := fal.New(fal.Config{APIKey: "k"})
	for _, tc := range []struct{ texture, quality string }{
		{"", ""}, {"", "detailed"}, {"off", ""}, {"off", "detailed"}, {"on", "standard"},
	} {
		reserve := designThreedCeilingUSDFor(tc.texture, tc.quality)
		for _, units := range []float64{1, 100} {
			charge := noTariff.CostUSDForQuality("", units, tc.quality)
			require.Truef(t, reserve.GreaterThanOrEqual(charge),
				"%+v: reserve %s < charge %s", tc, reserve, charge)
		}
	}
	require.Equal(t, "1.4", designThreedCeilingUSDFor("", "detailed").String())
	require.Equal(t, "1.2", designThreedCeilingUSDFor("off", "").String(),
		"fal публикует только цену с текстурой; меньшего числа не выдумываем")
	est, ok := (&Server{}).designThreedRunEstimate(entity.DesignRunKindThreed,
		threedParams(&pb_common.DesignThreedParams{Quality: "detailed"}), 1)
	require.True(t, ok)
	require.Equal(t, "1.4", est.Decimal.String())
}
