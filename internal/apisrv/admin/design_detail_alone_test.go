package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// T07 (owner item 7): a flat detail is drawn alone. A run that mixes `detail` with garment views is
// refused at the door, before the store reserves money.
// MUTATION: drop the designFlatViewsMixDetail gate in StartDesignRun — the mixed run reaches the
// store and this probe goes red.
func TestAFlatRunMixingDetailWithViewsIsRefused(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	req := designStartRequest(entity.DesignRunKindFlat)
	req.Params = &pb_common.DesignRunParams{
		Views:         []string{entity.DesignViewFront, entity.DesignViewDetail},
		DetailSlotIds: []int32{41},
		Layout:        designLayoutOne,
	}
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "detail_mixed_with_views", md["reason"])
	require.Nil(t, rig.sent, "refused before the store")
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
