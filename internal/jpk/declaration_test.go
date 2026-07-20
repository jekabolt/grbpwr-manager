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
	// Output-only: the input/deduction side is deferred to the accountant, so P_48 = 0 and P_51 = P_38.
	if p.P_48 != 0 {
		t.Errorf("P_48 (input VAT) = %d, want 0 (accountant merges the input side)", p.P_48)
	}
	if p.P_51 == nil || *p.P_51 != 276 {
		t.Errorf("P_51 (payable) = %v, want 276 (= P_38, output-only)", p.P_51)
	}
	if p.P_42 != nil || p.P_43 != nil || p.P_53 != nil {
		t.Errorf("input/refund boxes should be unset in an output-only declaration")
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
	if p.P_24 == nil || *p.P_24 != 46 {
		t.Errorf("P_24 (WNT self-charge output VAT) = %v, want 46", p.P_24)
	}
}
