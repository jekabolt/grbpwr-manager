package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// acctAssetMoneyLimit bounds the integer part of an asset cost — generous for a micro-entity.
const acctAssetMoneyLimit = 1_000_000_000

// ConvertCreateFixedAssetReq validates and maps a CreateFixedAsset request to the store insert.
func ConvertCreateFixedAssetReq(req *pb_admin.CreateFixedAssetRequest) (entity.FixedAssetInsert, error) {
	if strings.TrimSpace(req.GetName()) == "" {
		return entity.FixedAssetInsert{}, fmt.Errorf("name is required")
	}
	acquired, err := ParseAcctMonth(req.GetAcquiredOn())
	if err != nil {
		return entity.FixedAssetInsert{}, fmt.Errorf("acquired_on: %w", err)
	}
	cost, err := requiredDecimalFromPb(req.GetCostBase(), "cost_base", 2, acctAssetMoneyLimit)
	if err != nil {
		return entity.FixedAssetInsert{}, err
	}
	if !cost.IsPositive() {
		return entity.FixedAssetInsert{}, fmt.Errorf("cost_base must be > 0")
	}
	if req.GetUsefulLifeMonths() <= 0 {
		return entity.FixedAssetInsert{}, fmt.Errorf("useful_life_months must be > 0")
	}
	return entity.FixedAssetInsert{
		Name:             strings.TrimSpace(req.GetName()),
		CostBase:         cost,
		AcquiredOn:       acquired,
		UsefulLifeMonths: int(req.GetUsefulLifeMonths()),
	}, nil
}

// ConvertFixedAssetsToPb maps the register to protobuf.
func ConvertFixedAssetsToPb(assets []entity.FixedAsset) []*pb_admin.FixedAsset {
	out := make([]*pb_admin.FixedAsset, 0, len(assets))
	for _, a := range assets {
		disposed := ""
		if a.DisposedOn.Valid {
			disposed = a.DisposedOn.Time.Format(acctDateLayout)
		}
		out = append(out, &pb_admin.FixedAsset{
			Id:               int32(a.ID),
			Name:             a.Name,
			CostBase:         pbDecimalFromDecimal(a.CostBase),
			AcquiredOn:       a.AcquiredOn.Format(acctDateLayout),
			UsefulLifeMonths: int32(a.UsefulLifeMonths),
			DisposedOn:       disposed,
		})
	}
	return out
}

// ConvertAccrueCorpTaxResp maps the corporation-tax accrual result to protobuf.
func ConvertAccrueCorpTaxResp(ct decimal.Decimal, alreadyPosted bool) *pb_admin.AccrueCorporationTaxResponse {
	return &pb_admin.AccrueCorporationTaxResponse{
		CorpTax:       pbDecimalFromDecimal(ct),
		AlreadyPosted: alreadyPosted,
	}
}

// RequiredRateFromPb parses a percentage rate (0..100, up to 4 dp) from a pb decimal.
func RequiredRateFromPb(d *pb_decimal.Decimal) (decimal.Decimal, error) {
	r, err := requiredDecimalFromPb(d, "rate_pct", 4, 100)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if r.IsNegative() {
		return decimal.Decimal{}, fmt.Errorf("rate_pct must be >= 0")
	}
	return r, nil
}
