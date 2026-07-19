package betaseed

import (
	"context"
	"fmt"
	"time"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxRepeatedField returns the length of the largest repeated field in m — a
// shape-agnostic "how many rows did this list RPC return" without coupling to
// each response's getter name.
func maxRepeatedField(m proto.Message) int {
	best := 0
	fields := m.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.IsList() {
			if n := m.ProtoReflect().Get(fd).List().Len(); n > best {
				best = n
			}
		}
	}
	return best
}

// PrintCoverage reads back a count per seeded domain plus the analytics section
// coverage and prints an acceptance table. Read-only; safe to call any time. It
// is invoked at the end of a full seed run so every run self-reports what beta
// now holds.
func (s *Seeder) PrintCoverage(ctx context.Context) {
	out := func(format string, a ...any) {
		if s.Log != nil {
			s.Log(format, a...)
		} else {
			fmt.Printf(format+"\n", a...)
		}
	}

	count := func(name string, m proto.Message, err error) {
		if err != nil {
			out("  %-28s ERR %v", name, err)
			return
		}
		out("  %-28s %d", name, maxRepeatedField(m))
	}

	out("========== BETA COVERAGE (read-back) ==========")
	cw, e := s.C.GetColorwaysPaged(ctx, &admin.GetColorwaysPagedRequest{Limit: 500, Statuses: []common.ColorwayLifecycleStatus{common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ACTIVE}})
	count("active products", cw, e)
	pr, e := s.C.ListPromos(ctx, &admin.ListPromosRequest{})
	count("promo codes", pr, e)
	md, e := s.C.ListModels(ctx, &admin.ListModelsRequest{})
	count("showroom models", md, e)
	tk, e := s.C.ListTasks(ctx, &admin.ListTasksRequest{})
	count("tasks", tk, e)
	mb, e := s.C.ListMembers(ctx, &admin.ListMembersRequest{Limit: 500})
	count("members", mb, e)
	st, e := s.C.GetSupportTicketsPaged(ctx, &admin.GetSupportTicketsPagedRequest{Limit: 200})
	count("support tickets", st, e)
	rv, e := s.C.GetOrderReviewsPaged(ctx, &admin.GetOrderReviewsPagedRequest{Limit: 200})
	count("order reviews", rv, e)
	ac, e := s.C.ListAccounts(ctx, &admin.ListAccountsRequest{})
	count("admin accounts", ac, e)
	od, e := s.C.ListOrders(ctx, &admin.ListOrdersRequest{Status: common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED, Limit: 500})
	count("delivered orders", od, e)

	// Tag dictionary readback: the controlled tags an admin creates (CreateTag) must now surface in
	// GetDictionary().Tags immediately (backend fix A3). A non-zero count here confirms the created
	// tags are visible/reusable by name, not just the usage-derived product_tags.
	if dict, err := s.C.GetDictionary(ctx, &admin.GetDictionaryRequest{}); err != nil {
		out("  %-28s ERR %v", "dictionary tags", err)
	} else {
		out("  %-28s %d", "dictionary tags", len(dict.GetDictionary().GetTags()))
	}

	out("---- analytics ----")
	secs := []admin.MetricsSection{
		admin.MetricsSection_METRICS_SECTION_BUSINESS, admin.MetricsSection_METRICS_SECTION_MARGIN_BY_STYLE,
		admin.MetricsSection_METRICS_SECTION_COGS_STRUCTURE, admin.MetricsSection_METRICS_SECTION_GEOGRAPHY,
		admin.MetricsSection_METRICS_SECTION_DELIVERY, admin.MetricsSection_METRICS_SECTION_RETURN_ANALYSIS,
		admin.MetricsSection_METRICS_SECTION_REVENUE_PARETO, admin.MetricsSection_METRICS_SECTION_RFM,
		admin.MetricsSection_METRICS_SECTION_PROFITABILITY, admin.MetricsSection_METRICS_SECTION_INVENTORY_HEALTH,
	}
	m, err := s.C.GetMetrics(ctx, &admin.GetMetricsRequest{
		Period: "365d", EndAt: timestamppb.New(time.Now().AddDate(0, 0, 1)), Sections: secs, Limit: 50,
	})
	if err != nil {
		out("  GetMetrics ERR %v", err)
		return
	}
	com := m.GetBusiness().GetCommerce()
	out("  business revenue=%s orders=%s uniqueCustomers=%s countries=%d",
		com.GetRevenue().GetValue(), com.GetOrdersCount().GetValue(), com.GetUniqueCustomers().GetValue(), len(com.GetRevenueByCountry()))
	sec := func(name string, n int) {
		mark := "EMPTY"
		if n > 0 {
			mark = fmt.Sprintf("OK(%d)", n)
		}
		out("  [%-8s] %s", mark, name)
	}
	sec("GEOGRAPHY", len(m.GetGeography().GetByCountry()))
	sec("REVENUE_PARETO", len(m.GetRevenuePareto()))
	sec("RFM", len(m.GetRfmAnalysis()))
	sec("MARGIN_BY_STYLE", len(m.GetMarginByStyle()))
	sec("COGS_STRUCTURE", len(m.GetCogsStructure()))
	sec("RETURN_ANALYSIS", len(m.GetReturnByProduct()))
	sec("INVENTORY_HEALTH", len(m.GetInventoryHealth()))
	if m.GetProfitability() != nil && m.GetProfitability().GetGrossMargin() != nil {
		sec("PROFITABILITY", 1)
	} else {
		sec("PROFITABILITY", 0)
	}
	d := m.GetDelivery()
	sec("DELIVERY (samples)", int(d.GetDeliveredSample()+d.GetShippedSample()))
}
