package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// T07 / BX1 (owner item 7): a flat detail is drawn alone. The backend deploys before the client, and
// the deployed client sends [front, back, detail] in one press: the door canonicalizes that run to
// its details instead of refusing it.
// MUTATION: drop the designFlatDetailsOnly call in StartDesignRun — the stored views keep front/back,
// the requested outputs grow with them, and both probes below go red.
func TestDeployedClientMixedFlatRunIsDrawnDetailOnly(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithDetailSlot(41))
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = &pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewDetail},
		DetailSlotIds: []int32{41},
		Layout:        designLayoutPerView,
	}
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err, "the deployed client's press must still run")
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []string{entity.DesignViewDetail}, stored.GetViews())
	require.Equal(t, []int32{41}, stored.GetDetailSlotIds())
	require.Equal(t, 1, rig.sent.RequestedOutputs, "per_view pays for the detail only")

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Equal(t, []string{entity.DesignViewDetail}, snap.GetViews())
}

// A rerun with params omitted inherits a mixed run frozen before T07 and is drawn detail-only too.
func TestInheritedMixedFlatRerunIsDrawnDetailOnly(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWithDetailSlot(41))
	rig.design.EXPECT().GetRun(mock.Anything, 7).Return(&entity.DesignRun{
		Id: 7, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["front","detail","back"],"layout":"one","auto_split":true,"detail_slot_ids":[41]}`),
		Inputs: entity.RawJSON(`{"garment_note":"a shirt","slots":[` +
			`{"view_key":"detail","slot_id":41,"detail_name":"collar"}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = nil
	req.RerunOfRunId = 7

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []string{entity.DesignViewDetail}, stored.GetViews())
	require.Equal(t, []int32{41}, stored.GetDetailSlotIds())
	require.False(t, stored.GetAutoSplit(), "one view left: nothing to cut")

	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Equal(t, []string{entity.DesignViewDetail}, snap.GetViews())
}

func TestDesignFlatDetailsOnly(t *testing.T) {
	d, f, b := entity.DesignViewDetail, entity.DesignViewFront, entity.DesignViewBack

	p := &pb_common.DesignRunParams{Views: []string{d, f, d, b}, DetailSlotIds: []int32{3, 4}, AutoSplit: true, Layout: designLayoutOne}
	designFlatDetailsOnly(p)
	require.Equal(t, []string{d, d}, p.GetViews())
	require.Equal(t, []int32{3, 4}, p.GetDetailSlotIds(), "positional over the detail entries")
	require.True(t, p.GetAutoSplit(), "two details still make a composite to cut")

	// Positive controls: side-only and detail-only lists are untouched.
	sides := &pb_common.DesignRunParams{Views: []string{f, b}, AutoSplit: true}
	designFlatDetailsOnly(sides)
	require.Equal(t, []string{f, b}, sides.GetViews())
	require.True(t, sides.GetAutoSplit())
	one := &pb_common.DesignRunParams{Views: []string{d}}
	designFlatDetailsOnly(one)
	require.Equal(t, []string{d}, one.GetViews())
}

func TestDesignFlatViewsMixDetail(t *testing.T) {
	d, f, b := entity.DesignViewDetail, entity.DesignViewFront, entity.DesignViewBack
	require.True(t, designFlatViewsMixDetail([]string{f, d}))
	require.True(t, designFlatViewsMixDetail([]string{d, b, d}))
	require.False(t, designFlatViewsMixDetail([]string{d}))
	require.False(t, designFlatViewsMixDetail([]string{d, d}))
	require.False(t, designFlatViewsMixDetail([]string{f, b}))
	require.False(t, designFlatViewsMixDetail(nil))
}
