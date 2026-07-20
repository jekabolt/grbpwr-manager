package jpk

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBuildDeclarationBalances(t *testing.T) {
	ret := &entity.AcctVatReturnPL{
		NetDomestic:         d("1000"), // 23% base
		OutputDomestic:      d("230"),  // 23% VAT
		NetWdt:              d("500"),
		NetExport:           d("300"),
		NetWnt:              d("200"),
		InputWnt:            d("46"), // WNT self-charge: output VAT == input VAT
		OutputWntSelfCharge: d("46"),
		NetInputDomestic:    d("400"),
		InputDomestic:       d("92"),
		// NetPayable as the store computes it: output + self-charge − all input.
		NetPayable: d("138"),
	}

	dec := BuildDeclaration(ret)
	p := dec.Pozycje

	// Output total: 230 (domestic) + 46 (WNT self-charge) + 0 (import) = 276.
	if p.P_38 != 276 {
		t.Errorf("P_38 (total output VAT) = %d, want 276", p.P_38)
	}
	// Input total: 92 (domestic) + 46 (WNT reclaim) + 0 (import) = 138.
	if p.P_48 != 138 {
		t.Errorf("P_48 (total input VAT) = %d, want 138", p.P_48)
	}
	// Settlement balances and matches NetPayable.
	if p.P_51 == nil || *p.P_51 != 138 {
		t.Errorf("P_51 (payable) = %v, want 138", p.P_51)
	}
	if p.P_53 != nil {
		t.Errorf("P_53 (refund) should be nil when there is tax to pay, got %v", p.P_53)
	}
	if int64(p.P_38)-int64(p.P_48) != *p.P_51 {
		t.Errorf("declaration does not balance: P_38-P_48=%d, P_51=%d", p.P_38-p.P_48, *p.P_51)
	}
	// Base fields present.
	if p.P_11 == nil || *p.P_11 != 500 {
		t.Errorf("P_11 (WDT net) = %v, want 500", p.P_11)
	}
	if p.P_12 == nil || *p.P_12 != 300 {
		t.Errorf("P_12 (export net) = %v, want 300", p.P_12)
	}
	if p.P_19 == nil || *p.P_19 != 1000 {
		t.Errorf("P_19 (domestic net) = %v, want 1000", p.P_19)
	}
}

func TestBuildDeclarationRefundMonth(t *testing.T) {
	// Input exceeds output → carry-forward/refund on P_53, no P_51.
	ret := &entity.AcctVatReturnPL{
		NetDomestic:      d("100"),
		OutputDomestic:   d("23"),
		NetInputDomestic: d("500"),
		InputDomestic:    d("115"),
		NetPayable:       d("-92"),
	}
	p := BuildDeclaration(ret).Pozycje
	if p.P_51 != nil {
		t.Errorf("P_51 should be nil in a refund month, got %v", p.P_51)
	}
	if p.P_53 == nil || *p.P_53 != 92 {
		t.Errorf("P_53 (refund) = %v, want 92", p.P_53)
	}
}
