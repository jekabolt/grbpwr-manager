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
	// Input side is now register-backed (statutory review 13): P_43 = 92 (domestic incl. documented
	// opex) + 46 (WNT) + 0 (import) = 138; P_42 = 400 + 200 + 0 = 600; P_48 = P_43; P_51 = 276 −
	// 138 = 138 — exactly the store's NetPayable.
	if p.P_48 != 138 {
		t.Errorf("P_48 (input VAT) = %d, want 138", p.P_48)
	}
	if p.P_43 == nil || *p.P_43 != 138 {
		t.Errorf("P_43 (input VAT, other purchases) = %v, want 138", p.P_43)
	}
	if p.P_42 == nil || *p.P_42 != 600 {
		t.Errorf("P_42 (input net, other purchases) = %v, want 600", p.P_42)
	}
	if p.P_51 == nil || *p.P_51 != 138 {
		t.Errorf("P_51 (payable) = %v, want 138 (= P_38 − P_48 = NetPayable)", p.P_51)
	}
	if p.P_53 != nil {
		t.Errorf("P_53 (excess input) = %v, want unset (no excess when P_38 >= P_48)", p.P_53)
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
