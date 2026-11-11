package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A SAVE ANSWERS WITH THE VERSION IT COMMITTED. The store's header UPDATE is guarded by
// `lock_version = expected` and is the save's only bump, so the committed version is expected + 1;
// the client sends it as its next expected_lock_version (a seam change right after an autosave)
// instead of re-reading the whole card.
func TestUpdateTechCardAnswersTheCommittedLockVersion(t *testing.T) {
	restoreHosts(t)
	rig := newRestoreRig(t)
	stored := restoreCardAtV6()
	stored.LockVersion = 3
	// The handler's read, then the post-commit reads (cost seed, release snapshot).
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(stored, nil)
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).Return(nil, nil)

	payload := dto.ConvertEntityTechCardToPb(restoreCardNow(), dto.CostingFx{}).GetTechCard()
	payload.OperationReviewAware = true
	payload.MachineFieldsAware = true
	payload.LabelsAware = true
	resp, err := rig.srv.UpdateTechCard(restoreCtx(), &pb_admin.UpdateTechCardRequest{
		Id: restoreCardID, ExpectedLockVersion: 3, TechCard: payload,
	})
	require.NoError(t, err)
	require.Equal(t, int32(4), resp.GetLockVersion())
}
