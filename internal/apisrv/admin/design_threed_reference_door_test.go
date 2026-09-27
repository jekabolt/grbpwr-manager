package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ═══ B-09 × B-core: ДВЕРЬ StartDesignRun В РЕЖИМЕ РЕФЕРЕНСА (живёт вместе с патчем интеграции) ═══

func designThreedReferenceRequest(q string) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Threed: &pb_common.DesignThreedParams{
		ReferenceMediaIds: []int32{designRefMediaID, designBoardMediaID}, Quality: q,
	}}
}

// TestAReferenceRunNeedsNoFabricRender — на карточке без рендер-верстака (где 3D верстака получает
// `no_fabric_render`) прогон с названными картинками доходит до стора: без плит, без
// source_picture_ids, с резервом по своему тиру. МУТАЦИЯ: снять `!designThreedReferenceMode` с
// ворот :694 — no_fabric_render, красно; снять пропуск в designSelectBench — слоты в снимке, красно.
func TestAReferenceRunNeedsNoFabricRender(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	rig.srv.SetDesignThreedRoute(pgFalRoute(fal.Config{}, false)) // the default fal meshy route reads quality
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindThreed)
	req.Params = designThreedReferenceRequest("detailed")
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	var params pb_common.DesignRunParams
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, &params))
	require.Equal(t, []int32{designRefMediaID, designBoardMediaID}, params.GetThreed().GetReferenceMediaIds())
	require.Nil(t, params.GetThreed().GetSourcePictureIds(), "плит не было — провенанс пуст")

	var inputs pb_common.DesignInputSnapshot
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, &inputs))
	require.Empty(t, inputs.GetSlots(), "режим референса не читает верстак")
	require.Empty(t, inputs.GetRefs(), "и референсы карточки тоже")

	require.Equal(t, "1.4", rig.sent.PriceEstimate.Decimal.String(), "detailed резервируется как ultra")
}

// TestABenchRunStillNeedsItsFabricRender — положительный контроль: без названных картинок ворота
// верстака на месте.
func TestABenchRunStillNeedsItsFabricRender(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, "no_fabric_render", md["reason"])
}

// TestAMalformedReferenceRunIsRefusedBeforeTheStore — дверь формы стоит до денег.
func TestAMalformedReferenceRunIsRefusedBeforeTheStore(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	req := designStartRequest(entity.DesignRunKindThreed)
	req.Params = &pb_common.DesignRunParams{Threed: &pb_common.DesignThreedParams{
		ReferenceMediaIds: []int32{1, 2, 3, 4, 5}}}
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, "too_many_pictures", md["reason"])
	require.Nil(t, rig.sent)
}

// TestAReferenceRunTakesNoPlateEvenWhenTheBenchIsFull — на карточке С рендер-верстаком снимок
// режима референса всё равно без плит, и source_picture_ids пуст. МУТАЦИЯ: designSelectBench,
// спрашивающий designKindReadsTheCard вместо designRunReadsTheCard — плита 210 в снимке, красно.
func TestAReferenceRunTakesNoPlateEvenWhenTheBenchIsFull(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindThreed)
	req.Params = designThreedReferenceRequest("")
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)

	var params pb_common.DesignRunParams
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, &params))
	require.Nil(t, params.GetThreed().GetSourcePictureIds())
	var inputs pb_common.DesignInputSnapshot
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, &inputs))
	require.Empty(t, inputs.GetSlots())
	require.Equal(t, designThreedCeilingUSD().String(), rig.sent.PriceEstimate.Decimal.String(),
		"опции по умолчанию резервируют сегодняшнюю цену")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: тот же верстак у 3D верстака даёт плиту.
	bench := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	_, err = bench.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err)
	var benchParams pb_common.DesignRunParams
	require.NoError(t, designUnmarshalJSON(bench.sent.Params, &benchParams))
	require.NotEmpty(t, benchParams.GetThreed().GetSourcePictureIds())
}

// TestAReferenceRunSENDS_NO_GARMENT_NOTE — G-02 Fable m-3: a reference-mode 3D run is built from
// pictures the person named (possibly another garment), so the card's garment description and fit
// do not travel into its texture steer — on a fresh run AND on a rerun of a run whose snapshot
// still carries them. The bench run keeps both. MUTATIONS (each measured red): either call site back
// on designKindReadsTheGarmentNote (the fresh row, the rerun row).
func TestAReferenceRunSENDS_NO_GARMENT_NOTE(t *testing.T) {
	card := designMoodCard()
	card.GarmentDescription = sql.NullString{String: "olive shirt, spread collar", Valid: true}
	inputsOf := func(rig *designRunRig) *pb_common.DesignInputSnapshot {
		var in pb_common.DesignInputSnapshot
		require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, &in))
		return &in
	}

	ref := newDesignRunRig(t, card, designBandWith(true))
	ref.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindThreed)
	req.Params = designThreedReferenceRequest("")
	_, err := ref.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.Empty(t, inputsOf(ref).GetGarmentNote(), "the named pictures are not the card's garment")
	require.Empty(t, inputsOf(ref).GetFit())

	bench := newDesignRunRig(t, card, designBandWith(true))
	_, err = bench.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err)
	require.Equal(t, "olive shirt, spread collar", inputsOf(bench).GetGarmentNote(), "the bench run reads its card")

	// A rerun of a reference run frozen with the note (before this fix) drops it too.
	parentInputs, err := designMarshalJSON(&pb_common.DesignInputSnapshot{GarmentNote: "olive shirt", Fit: "oversized"})
	require.NoError(t, err)
	parentParams, err := designMarshalJSON(designThreedReferenceRequest(""))
	require.NoError(t, err)
	parent := &entity.DesignRun{Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindThreed,
		Params: entity.RawJSON(parentParams), Inputs: entity.RawJSON(parentInputs)}
	rerun := newDesignRunRig(t, card, designBandWith(true))
	rerun.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	rerun.design.EXPECT().GetRun(mock.Anything, parent.Id).Return(parent, nil).Maybe()
	req = designStartRequest(entity.DesignRunKindThreed)
	req.RerunOfRunId = int32(parent.Id)
	req.Params = nil // a silent rerun: inherits the parent's params and snapshot
	_, err = rerun.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.Empty(t, inputsOf(rerun).GetGarmentNote())
	require.Empty(t, inputsOf(rerun).GetFit())
}
