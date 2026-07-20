package dto

import (
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// Phase 2, wave 1 VAT filing exports (docs/plan-accounting-phase2/01-wave1-vat.md §1.5): dto
// conversions for GetVatReturnPL / GetOssReturn, mirroring the accounting report converters in
// dto/accounting.go (money via pbDecimalFromDecimal, dates as plain YYYY-MM-DD strings via
// acctDateLayout — never google.protobuf.Timestamp). Both reports are read-only, so there is no
// entity→request direction here, only entity→pb.

// ParseVatReturnMonth parses GetVatReturnPLRequest.month. It delegates to the existing
// ParseAcctMonth (any YYYY-MM or YYYY-MM-DD date, normalised to the 1st) rather than duplicating a
// stricter parser: GetVatReturnPL's own store implementation
// (internal/store/accounting/vatreturn.go) re-normalises via firstOfMonthUTC(month) regardless of
// the exact day passed in, so a second, narrower parser here would add no safety — only an
// inconsistency with the sibling "month" fields (CloseAcctPeriod/ReopenAcctPeriod) that already
// accept this shape. Named separately (rather than calling ParseAcctMonth directly at the call site)
// so the RPC boundary reads clearly and can diverge later without touching apisrv.
func ParseVatReturnMonth(s string) (time.Time, error) {
	return ParseAcctMonth(s)
}

// ParseAcctQuarterStart parses GetOssReturnRequest.quarter: a required YYYY-MM-DD date, snapped down
// to the first day of the calendar quarter it falls in (Jan/Apr/Jul/Oct 1st) — the same
// quarter-start arithmetic as the "qtd" metrics preset (internal/apisrv/admin/metrics.go's
// GetBusinessMetrics period resolver).
//
// Decision (deliberately left open by the wave-1 task, no existing precedent to reuse): SNAP rather
// than reject a non-quarter-start date with InvalidArgument. GetOssReturn's store implementation
// (internal/store/accounting/vatreturn.go) does NOT itself validate quarter alignment — it takes
// firstOfMonthUTC(quarterStart) and unconditionally adds 3 months, so an unaligned input (e.g. the
// 2nd month of a quarter) would silently produce a non-calendar 3-month window if passed through
// unchecked; that is worse than the alternative of a hard-reject, so it is exactly the failure mode
// this parser exists to close. Snapping (rather than rejecting) mirrors this package's own
// ParseAcctMonth/firstOfMonth convention elsewhere in the accounting API ("any date within the
// target period is accepted, normalised to its start") instead of forcing the UI's quarter picker to
// compute the exact boundary itself. The wire contract (admin.proto: "first day of the quarter")
// documents the canonical value a well-behaved caller sends; snapping is a forgiving superset of it,
// never a correctness regression, since every date in a quarter snaps to that SAME quarter's start.
func ParseAcctQuarterStart(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("quarter is required")
	}
	t, err := time.Parse(acctDateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid quarter %q: want YYYY-MM-DD: %w", s, err)
	}
	qStartMonth := time.Month(((int(t.Month())-1)/3)*3 + 1)
	return time.Date(t.Year(), qStartMonth, 1, 0, 0, 0, 0, time.UTC), nil
}

// ConvertAcctVatReturnPLToPb converts the JPK_VAT monthly aggregate to protobuf.
func ConvertAcctVatReturnPLToPb(r entity.AcctVatReturnPL) *pb_admin.GetVatReturnPLResponse {
	return &pb_admin.GetVatReturnPLResponse{
		Month:                 r.Month.Format(acctDateLayout),
		OutputDomestic:        pbDecimalFromDecimal(r.OutputDomestic),
		OutputWntSelfCharge:   pbDecimalFromDecimal(r.OutputWntSelfCharge),
		OssInfoTotal:          pbDecimalFromDecimal(r.OssInfoTotal),
		InputDomestic:         pbDecimalFromDecimal(r.InputDomestic),
		InputWnt:              pbDecimalFromDecimal(r.InputWnt),
		InputImport:           pbDecimalFromDecimal(r.InputImport),
		NetPayable:            pbDecimalFromDecimal(r.NetPayable),
		OutputUkStockDomestic: pbDecimalFromDecimal(r.OutputUkStockDomestic),
		InputUkDomestic:       pbDecimalFromDecimal(r.InputUkDomestic),
		NetWdt:                pbDecimalFromDecimal(r.NetWdt),
		NetExport:             pbDecimalFromDecimal(r.NetExport),
		Caveats:               r.Caveats,
	}
}

// ConvertAcctOssReturnToPb converts the quarterly OSS aggregate to protobuf.
func ConvertAcctOssReturnToPb(r entity.AcctOssReturn) *pb_admin.GetOssReturnResponse {
	rows := make([]*pb_admin.AcctOssRow, 0, len(r.Rows))
	for _, row := range r.Rows {
		rows = append(rows, &pb_admin.AcctOssRow{
			Country: row.Country,
			RatePct: pbDecimalFromDecimal(row.RatePct),
			Net:     pbDecimalFromDecimal(row.Net),
			Vat:     pbDecimalFromDecimal(row.Vat),
		})
	}
	return &pb_admin.GetOssReturnResponse{
		QuarterStart: r.QuarterStart.Format(acctDateLayout),
		Rows:         rows,
		TotalNet:     pbDecimalFromDecimal(r.TotalNet),
		TotalVat:     pbDecimalFromDecimal(r.TotalVat),
	}
}
