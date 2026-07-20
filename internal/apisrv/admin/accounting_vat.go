package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/oss"
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

// ExportJpkV7M builds the official Polish JPK_V7M (JPK_VAT) XML for a month: the taxpayer header, the
// VAT-7 output-side declaration, and the sales evidence register. The purchase register is left empty
// for the accountant to merge. It needs the JPK_* taxpayer identity configured — otherwise it returns
// FailedPrecondition rather than an invalid filing.
func (s *Server) ExportJpkV7M(ctx context.Context, req *pb_admin.ExportJpkV7MRequest) (*pb_admin.ExportJpkV7MResponse, error) {
	month, err := dto.ParseVatReturnMonth(req.GetMonth())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !s.jpkTaxpayer.Configured() {
		return nil, status.Error(codes.FailedPrecondition, "JPK export is not configured: set the JPK_NIP / JPK_FULL_NAME / JPK_EMAIL / JPK_TAX_OFFICE taxpayer identity")
	}
	ret, err := s.repo.Accounting().GetVatReturnPL(ctx, month)
	if err != nil {
		return nil, mapAcctErr(ctx, "jpk vat return", err)
	}
	rows, err := s.repo.Accounting().VatSalesEvidence(ctx, month)
	if err != nil {
		return nil, mapAcctErr(ctx, "jpk sales evidence", err)
	}
	xmlBytes, err := jpk.Generate(s.jpkTaxpayer, ret, rows, month, time.Now())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb_admin.ExportJpkV7MResponse{
		Filename:   fmt.Sprintf("JPK_V7M_%s.xml", month.Format("2006-01")),
		XmlContent: string(xmlBytes),
	}, nil
}

// ExportOssReturn builds the quarterly OSS (Union scheme, VIU-DO) return XML from the per-country OSS
// aggregate. Like the JPK export it needs the taxpayer identity configured; the wrapper is a draft to
// validate against the official schema / transcribe into the OSS portal.
func (s *Server) ExportOssReturn(ctx context.Context, req *pb_admin.ExportOssReturnRequest) (*pb_admin.ExportOssReturnResponse, error) {
	quarterStart, err := dto.ParseAcctQuarterStart(req.GetQuarter())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !s.jpkTaxpayer.Configured() {
		return nil, status.Error(codes.FailedPrecondition, "OSS export is not configured: set the JPK_NIP / JPK_FULL_NAME / JPK_EMAIL / JPK_TAX_OFFICE taxpayer identity")
	}
	ret, err := s.repo.Accounting().GetOssReturn(ctx, quarterStart)
	if err != nil {
		return nil, mapAcctErr(ctx, "oss return", err)
	}
	xmlBytes, err := oss.Generate(s.jpkTaxpayer, ret, time.Now())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	q := (int(quarterStart.Month())-1)/3 + 1
	return &pb_admin.ExportOssReturnResponse{
		Filename:   fmt.Sprintf("OSS_%d-Q%d.xml", quarterStart.Year(), q),
		XmlContent: string(xmlBytes),
	}, nil
}
