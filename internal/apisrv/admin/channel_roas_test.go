package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetChannelRoasSettled: the store returns per-channel settled revenue + counts; the handler
// layers channel_spend to compute ROAS and per-channel CAC. A channel with spend gets roas/cac +
// has_spend=true; a channel without spend keeps revenue but roas/cac are 0 / has_spend=false.
func TestGetChannelRoasSettled(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	mtr := mocks.NewMockMetrics(t)
	bq := mocks.NewMockBQCacheStore(t)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().BQCache().Return(bq)

	mtr.EXPECT().GetChannelRoasSettled(mock.Anything, mock.Anything, mock.Anything).Return([]entity.ChannelSettledRow{
		{UTMSource: "ig", UTMMedium: "social", UTMCampaign: "camp_a", SettledRevenue: decimal.NewFromInt(300), Orders: 2, NewCustomers: 2},
		{UTMSource: "google", UTMMedium: "cpc", UTMCampaign: "camp_b", SettledRevenue: decimal.NewFromInt(300), Orders: 1, NewCustomers: 1},
	}, nil)
	// spend only for ig → 100. google has none.
	bq.EXPECT().GetChannelSpendByCampaign(mock.Anything, mock.Anything, mock.Anything).Return([]entity.ChannelSpendRow{
		{UTMSource: "ig", UTMMedium: "social", UTMCampaign: "camp_a", Spend: decimal.NewFromInt(100)},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetChannelRoasSettled(context.Background(), &pb_admin.GetChannelRoasSettledRequest{Period: "30d"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 2)

	byCh := map[string]*pb_admin.ChannelSettledRoasRow{}
	for _, r := range resp.Rows {
		byCh[r.UtmSource] = r
	}

	ig := byCh["ig"]
	require.True(t, ig.HasSpend, "ig has spend")
	require.Equal(t, "100.00", ig.Spend.GetValue())
	require.InDelta(t, 3.0, ig.Roas, 1e-9, "ROAS = 300/100")
	require.InDelta(t, 50.0, ig.Cac, 1e-9, "CAC = 100/2 new customers")
	require.Equal(t, "300.00", ig.SettledRevenue.GetValue())

	g := byCh["google"]
	require.False(t, g.HasSpend, "google has no spend")
	require.Zero(t, g.Roas, "ROAS N/A without spend")
	require.Zero(t, g.Cac, "CAC N/A without spend")
	require.Equal(t, "300.00", g.SettledRevenue.GetValue(), "revenue still shown")
	require.Nil(t, g.Spend)
}

func TestGetChannelRoasSettledBadPeriod(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.GetChannelRoasSettled(context.Background(), &pb_admin.GetChannelRoasSettledRequest{Period: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
