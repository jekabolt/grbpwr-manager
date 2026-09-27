package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// AISpendReportToPb maps the spend report onto the wire.
//
// UNKNOWN TRAVELS AS AN ABSENT DECIMAL, ZERO AS "0". Every USD on the report is NULL when no row of
// its group carries a price, and pbDecimalFromNull keeps that as a nil field; a group whose only calls
// were free sums to a real 0 and is sent as "0". Amounts go out unrounded: one chat call costs a
// fraction of a cent, and rounding to cents would turn a real spend into a "0" the page cannot tell
// from free.
//
// Every line is mapped, none filtered: a provider line with zero calls is the provider's own number
// with nothing of ours beside it (the store's union), and dropping it would hide exactly that.
//
// their_bucket_tz travels beside their number (D-17): the zone THEIR days are days of, which the panel
// labels («provider days (UTC)») against the report's own timezone instead of pretending the two agree.
func AISpendReportToPb(r *entity.AISpendReport) *pb_admin.GetAiSpendReportResponse {
	if r == nil {
		return &pb_admin.GetAiSpendReportResponse{}
	}
	out := &pb_admin.GetAiSpendReportResponse{
		FromDay:    r.FromDay,
		ToDay:      r.ToDay,
		Timezone:   r.Timezone,
		TotalUsd:   pbDecimalFromNull(r.TotalUSD),
		Calls:      int32(r.Calls),
		Failed:     int32(r.Failed),
		Unpriced:   int32(r.Unpriced),
		ByProvider: make([]*pb_admin.AiSpendProviderRow, 0, len(r.ByProvider)),
		ByActor:    make([]*pb_admin.AiSpendActorRow, 0, len(r.ByActor)),
	}
	for _, p := range r.ByProvider {
		out.ByProvider = append(out.ByProvider, &pb_admin.AiSpendProviderRow{
			ProviderKey:   p.ProviderKey,
			OurUsd:        pbDecimalFromNull(p.OurUSD),
			TheirUsd:      pbDecimalFromNull(p.TheirUSD),
			TheirBucketTz: p.TheirBucketTZ,
			Calls:         int32(p.Calls),
			Failed:        int32(p.Failed),
			Unpriced:      int32(p.Unpriced),
		})
	}
	for _, a := range r.ByActor {
		row := &pb_admin.AiSpendActorRow{
			Actor:       a.Actor,
			Purpose:     a.Purpose,
			ProviderKey: a.ProviderKey,
			Model:       a.Model,
			Usd:         pbDecimalFromNull(a.USD),
			Calls:       int32(a.Calls),
		}
		if a.ActorAdminID != nil {
			row.ActorAdminId = int32(*a.ActorAdminID)
		}
		out.ByActor = append(out.ByActor, row)
	}
	return out
}
