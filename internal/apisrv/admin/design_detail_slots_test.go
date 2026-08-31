package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ИМЯ ДЕТАЛИ НЕ ЯВЛЯЕТСЯ ЕЁ ИДЕНТИЧНОСТЬЮ. Оно редактируется, тогда как id слота остаётся
// адресом, к которому замороженный прогон обязан вернуть именно свою картинку. Эти пробы держат
// соответствие двух представлений одной просьбы: `detail` считает выходы, detail_slot_ids называет
// их адреса в том же порядке.

func TestDesignEffectiveParamsDetailSlotRefusesNonPositiveID(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewDetail},
		DetailSlotIds: []int32{0},
	}, nil)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids.0")
}

func TestDesignEffectiveParamsDetailSlotRefusesDuplicateID(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewDetail, entity.DesignViewDetail},
		DetailSlotIds: []int32{17, 17},
	}, nil)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids.1")
	require.Contains(t, err.Error(), "params.detail_slot_ids.0")
}

func TestDesignEffectiveParamsDetailSlotRefusesMissingID(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewDetail, entity.DesignViewDetail},
		DetailSlotIds: []int32{17},
	}, nil)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids has 1 elements")
	require.Contains(t, err.Error(), "params.views has 2 detail elements")
}

func TestDesignEffectiveParamsDetailSlotRefusesExtraID(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewDetail},
		DetailSlotIds: []int32{17, 18},
	}, nil)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids has 2 elements")
	require.Contains(t, err.Error(), "params.views has 1 detail elements")
}

func TestDesignEffectiveParamsDetailSlotAcceptsOneIDPerDetailView(t *testing.T) {
	params, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views: []string{
			entity.DesignViewDetail,
			entity.DesignViewFront,
			entity.DesignViewDetail,
		},
		DetailSlotIds: []int32{17, 18},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []int32{17, 18}, params.GetDetailSlotIds())
	require.Equal(t, designLayoutOne, params.GetLayout())
}

func TestStartDesignRunRefusesDetailSlotOutsideTheCard(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithDetailSlot(17))
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail}
	req.Params.DetailSlotIds = []int32{18}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids.0 18")
	require.Nil(t, rig.sent, "a foreign slot must be refused before the paid run is reserved")
}

func TestStartDesignRunRefusesSilhouetteSlotAsADetailSlot(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithDetailSlot(17))
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail}
	req.Params.DetailSlotIds = []int32{5} // slot 5 exists on this card, but it is the front silhouette

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "params.detail_slot_ids.0 5")
	require.Nil(t, rig.sent, "a silhouette address cannot identify a detail output")
}

func TestStartDesignRunAcceptsDetailSlotFromTheCard(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithDetailSlot(17))
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params.Views = []string{entity.DesignViewDetail}
	req.Params.DetailSlotIds = []int32{17}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []int32{17}, stored.GetDetailSlotIds(),
		"the immutable slot address must survive in the run's frozen parameters")
}

func designBandWithDetailSlot(id int) *entity.DesignBand {
	band := designBandWith(true)
	band.Bench = append(band.Bench, entity.DesignBenchSlot{
		Id:         id,
		TechCardId: designRunCardID,
		ViewKey:    entity.DesignViewDetail,
		Kind:       entity.DesignPictureKindFlat,
		DetailName: sql.NullString{String: "collar", Valid: true},
		PictureId:  sql.NullInt32{Int32: 78, Valid: true},
		Picture: &entity.DesignPicture{
			Id: 78, TechCardId: designRunCardID, MediaId: designPlateMediaID + 1,
			Kind: entity.DesignPictureKindFlat,
		},
	})
	return band
}

// ИСТОРИЯ НЕ ОСУЖДАЕТСЯ ПРАВИЛОМ, ЗАВЕДЁННЫМ ПОСЛЕ НЕЁ.
//
// Прогон, замороженный до появления `detail_slot_ids`, несёт `views:["detail"]` и ни одного
// идентификатора. Если правило длин применять и к УНАСЛЕДОВАННЫМ параметрам, каждый такой прогон
// становится невозможно перезапустить — навсегда, потому что снимок никогда не чинят задним
// числом. Проверка обязана связывать того, кто ГОВОРИТ, а не того, кто молчит.
func TestInheritedParamsFromBeforeTheFieldExistedStillRerun(t *testing.T) {
	parent := &entity.DesignRun{
		Id:     7,
		Params: entity.RawJSON(`{"views":["detail","front"],"layout":"one"}`),
	}
	got, err := designEffectiveParams(nil, parent)
	require.NoError(t, err,
		"прогон, записанный до появления поля, обязан перезапускаться: правило связывает говорящего")
	require.Equal(t, []string{"detail", "front"}, got.GetViews())
	require.Empty(t, got.GetDetailSlotIds(), "молчание не превращается в выдуманный слот")
}

// ...и ровно то же молчание ОТ КЛИЕНТА отвергается: он поле знает и обязан его заполнить.
func TestCallerThatSpeaksMustNameEveryDetailSlot(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{
		Views: []string{"detail"}, Layout: "one",
	}, nil)
	require.Error(t, err, "клиент, назвавший detail в views и промолчавший про слот, противоречит сам себе")
}
