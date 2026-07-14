package admin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetChannelRoasSettled reports per-channel ROAS/CAC from the AUTHORITATIVE settled order revenue
// (task 20 step 2). The store attributes settled revenue to channels via the bq_order_channel map
// (order.ga_client_id → last non-direct UTM); this handler layers operator spend from channel_spend
// on top to compute ROAS (settled/spend) and per-channel CAC (spend/new_customers), matched by the
// UTM triple exactly like the GA4-based enrichCampaignSpend. Reuses the GetDashboard period grammar.
func (s *Server) GetChannelRoasSettled(ctx context.Context, req *pb_admin.GetChannelRoasSettledRequest) (*pb_admin.GetChannelRoasSettledResponse, error) {
	if strings.TrimSpace(req.Period) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "period is required (e.g. 7d, 30d, 90d, today, WTD, MTD, QTD, YTD)")
	}
	endAt := time.Now().UTC()
	if req.EndAt != nil {
		endAt = req.EndAt.AsTime().UTC()
	}
	from, to, isSpecial := computePeriodBounds(req.Period, endAt)
	if !isSpecial {
		dur, err := parseMetricsPeriod(req.Period)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid period %q: %v", req.Period, err)
		}
		to = endAt
		from = endAt.Add(-dur)
	}

	rows, err := s.repo.Metrics().GetChannelRoasSettled(ctx, from, to)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get channel roas settled", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get channel roas")
	}

	// Layer operator spend → ROAS/CAC (base currency), matched by the UTM triple. Missing spend
	// leaves roas/cac at 0 with has_spend=false (N/A, not a real zero) — a channel without entered
	// spend still shows its settled revenue and customers.
	spendByChannel := map[string]decimal.Decimal{}
	if spend, serr := s.repo.BQCache().GetChannelSpendByCampaign(ctx, from, to); serr != nil {
		slog.Default().WarnContext(ctx, "channel roas: spend lookup failed; showing revenue only", slog.String("err", serr.Error()))
	} else {
		for _, sp := range spend {
			spendByChannel[channelKey(sp.UTMSource, sp.UTMMedium, sp.UTMCampaign)] = sp.Spend
		}
	}

	out := make([]*pb_admin.ChannelSettledRoasRow, 0, len(rows))
	for _, r := range rows {
		row := &pb_admin.ChannelSettledRoasRow{
			UtmSource:      r.UTMSource,
			UtmMedium:      r.UTMMedium,
			UtmCampaign:    r.UTMCampaign,
			SettledRevenue: &pb_decimal.Decimal{Value: r.SettledRevenue.StringFixed(2)},
			Orders:         r.Orders,
			NewCustomers:   r.NewCustomers,
		}
		if sp, ok := spendByChannel[channelKey(r.UTMSource, r.UTMMedium, r.UTMCampaign)]; ok && sp.IsPositive() {
			row.HasSpend = true
			row.Spend = &pb_decimal.Decimal{Value: sp.StringFixed(2)}
			row.Roas = r.SettledRevenue.Div(sp).InexactFloat64()
			if r.NewCustomers > 0 {
				row.Cac = sp.Div(decimal.NewFromInt(r.NewCustomers)).InexactFloat64()
			}
		}
		out = append(out, row)
	}
	return &pb_admin.GetChannelRoasSettledResponse{Rows: out, BaseCurrency: cache.GetBaseCurrency()}, nil
}
