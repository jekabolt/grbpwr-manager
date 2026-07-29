package campaigndispatch

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func (w *Worker) advanceFanout(ctx context.Context) (*entity.EmailCampaignFanoutPageResult, error) {
	return w.repo.Campaigns().AdvanceEmailCampaignFanout(
		ctx,
		w.c.FanoutPageSize,
		AssignVariant,
	)
}
