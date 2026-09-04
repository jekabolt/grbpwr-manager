package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ D-28, ДВЕРЬ: ТРИ ЧЕТВЕРТИ — ЗАКОННАЯ СТОРОНА ФЛЭТА И РЕНДЕРА, НО НЕ ВХОД 3D ═══════════════
//
// Шесть сторон не должны сломать ни один существующий гейт, и 3D по-прежнему требует ФРОНТ, а не
// «все шесть». Здесь измеряется то, чего не доказать чтением: что 3D, суженное до трёх четвертей,
// отказано ДО `Design().StartRun` (стенд без заглушки StartRun роняет пробу, если резерв тронут),
// и что флэт с тем же сужением ПРОХОДИТ — иначе новая сторона была бы адресуема и неисполнима.

const (
	designThreeQuarterRenderMediaID = 8800
	designThreeQuarterFlatMediaID   = 8900
)

// threeQuarterBand — верстак designBandWith(true) плюс две три-четверти-плиты слева: рендер
// (слот 8) и флэт (слот 9). Рендер-верстак остаётся безколорвейным, поэтому ворота
// `no_fabric_render` открыты ровно как раньше.
func threeQuarterBand() *entity.DesignBand {
	band := designBandWith(true)
	band.Bench = append(band.Bench,
		entity.DesignBenchSlot{
			Id: 8, TechCardId: designRunCardID, ViewKey: entity.DesignViewThreeQuarterL,
			Kind: entity.DesignPictureKindRender, ExclusiveKey: entity.DesignViewThreeQuarterL,
			PictureId: sql.NullInt32{Int32: 88, Valid: true},
			Picture: &entity.DesignPicture{
				Id: 88, TechCardId: designRunCardID, MediaId: designThreeQuarterRenderMediaID,
				Kind: entity.DesignPictureKindRender,
			},
		},
		entity.DesignBenchSlot{
			Id: 9, TechCardId: designRunCardID, ViewKey: entity.DesignViewThreeQuarterL,
			Kind: entity.DesignPictureKindFlat, ExclusiveKey: entity.DesignViewThreeQuarterL,
			PictureId: sql.NullInt32{Int32: 99, Valid: true},
			Picture: &entity.DesignPicture{
				Id: 99, TechCardId: designRunCardID, MediaId: designThreeQuarterFlatMediaID,
				Kind: entity.DesignPictureKindFlat,
			},
		},
	)
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	return band
}

// TestAThreedRunNarrowedToAThreeQuarterSideIsRefusedBeforeTheReserve — обе половины сужения.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять проверку IsDesignCardinalView из designRefuseForeignFixSlots.
// Тогда прогон резервируется, а воркер молча отбросит три четверти (threedPictures) — оплаченная
// сборка из одной стороны вместо двух, неотличимая по истории от заказанной.
func TestAThreedRunNarrowedToAThreeQuarterSideIsRefusedBeforeTheReserve(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pb_common.DesignRunParams)
		field  string
	}{
		{
			name: "by view",
			mutate: func(p *pb_common.DesignRunParams) {
				p.FixTargets = []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL}
			},
			field: "params.fix_targets.1",
		},
		{
			name:   "by slot id",
			mutate: func(p *pb_common.DesignRunParams) { p.FixSlotIds = []int32{8} },
			field:  "params.fix_slot_ids.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := designFormatRig(t, threeQuarterBand(), false, nil)
			req := designStartRequest(entity.DesignRunKindThreed)
			tc.mutate(req.Params)

			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			require.Error(t, err)
			code, _ := errorReason(t, err)
			require.Equal(t, codes.InvalidArgument, code, "просьба не годится по форме: это не дефект карточки")
			require.Contains(t, err.Error(), tc.field, "отказ называет поле, которое человек чинит")
			require.Contains(t, err.Error(), entity.DesignViewThreeQuarterL, "…и сторону")
			require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
		})
	}
}

// TestAFlatFixOnAThreeQuarterSideIsAccepted — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, без которого первая проба
// зеленела бы и от «три четверти отказаны всюду». Флэт, суженный до three_quarter_l, проходит все
// двери, и в снимок уезжает ИМЕННО эта плита.
func TestAFlatFixOnAThreeQuarterSideIsAccepted(t *testing.T) {
	rig := designFormatRig(t, threeQuarterBand(), true, nil)
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewThreeQuarterL}
	req.Params.FixTargets = []string{entity.DesignViewThreeQuarterL}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Len(t, snap.GetSlots(), 1)
	require.Equal(t, entity.DesignViewThreeQuarterL, snap.GetSlots()[0].GetViewKey())
	require.Equal(t, int32(designThreeQuarterFlatMediaID), snap.GetSlots()[0].GetMediaId(),
		"флэт-плита трёх четвертей, а не рендер того же вида: ось рода не сдвинулась")
}

// TestAThreedRunWithAThreeQuarterRenderOnTheBenchStillStarts — карточка, на которой человек
// поставил три четверти на рендер-верстак, не теряет 3D: без сужения прогон стартует, перёд на
// месте, а лишнюю сторону отбрасывает воркер (designgen.threedPictures), как он уже делает с
// деталью-рендером. Снимок при этом честно перечисляет обе плиты — это то, что стояло на верстаке.
func TestAThreedRunWithAThreeQuarterRenderOnTheBenchStillStarts(t *testing.T) {
	rig := designFormatRig(t, threeQuarterBand(), true, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err, "шесть сторон не ломают ворота `no_fabric_render` и `no_front_render`")
	require.NotNil(t, rig.sent)

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	views := make([]string, 0, len(snap.GetSlots()))
	for _, s := range snap.GetSlots() {
		views = append(views, s.GetViewKey())
	}
	require.ElementsMatch(t, []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL}, views)
}

// TestThreeQuarterSidesPassTheParamsDoorForEveryPictureRun — форма параметров: три четверти —
// законный член `views` и законная цель `fix_targets`/`fix_target` (designEffectiveParams не знает
// рода; род спрашивает дверь выше).
func TestThreeQuarterSidesPassTheParamsDoorForEveryPictureRun(t *testing.T) {
	params, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views:      []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL, entity.DesignViewThreeQuarterR},
		FixTargets: []string{entity.DesignViewThreeQuarterR},
		FixTarget:  entity.DesignViewThreeQuarterR,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{entity.DesignViewThreeQuarterR}, params.GetFixTargets())
}
