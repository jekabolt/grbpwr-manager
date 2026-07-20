package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// VAT filing exports (phase 2, wave 1 — docs/plan-accounting-phase2/01-wave1-vat.md §1.5), following
// the same handler pattern as the other reports in accounting.go: parse (dto) → store → convert
// (dto) → respond. Both are single read-only store calls, so — unlike CreateJournalEntry/
// ReverseJournalEntry — neither needs repo.Tx.

// GetVatReturnPL returns the JPK_VAT monthly aggregate (filed by the 25th): output VAT by regime,
// input VAT by purchase type, and the net payable, for the accountant's manual filing.
func (s *Server) GetVatReturnPL(ctx context.Context, req *pb_admin.GetVatReturnPLRequest) (*pb_admin.GetVatReturnPLResponse, error) {
	month, err := dto.ParseVatReturnMonth(req.GetMonth())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ret, err := s.repo.Accounting().GetVatReturnPL(ctx, month)
	if err != nil {
		return nil, mapAcctErr(ctx, "get vat return", err)
	}
	return dto.ConvertAcctVatReturnPLToPb(*ret), nil
}

// GetOssReturn returns the quarterly OSS aggregate: EU B2C sales (vat_regime=oss) broken down by
// destination country with the applied rate, net and VAT.
func (s *Server) GetOssReturn(ctx context.Context, req *pb_admin.GetOssReturnRequest) (*pb_admin.GetOssReturnResponse, error) {
	quarterStart, err := dto.ParseAcctQuarterStart(req.GetQuarter())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ret, err := s.repo.Accounting().GetOssReturn(ctx, quarterStart)
	if err != nil {
		return nil, mapAcctErr(ctx, "get oss return", err)
	}
	return dto.ConvertAcctOssReturnToPb(*ret), nil
}
