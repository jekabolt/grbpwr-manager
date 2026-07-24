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

	// Output: domestic only — WNT/import self-charge is deliberately NOT declared (the file
	// carries no K_23..K_26 evidence for it; its legs cancel, so P_51 is unchanged — H-1).
	if p.P_38 != 230 {
		t.Errorf("P_38 (total output VAT) = %d, want 230 (domestic only)", p.P_38)
	}
	if p.P_23 != nil || p.P_24 != nil || p.P_25 != nil || p.P_26 != nil {
		t.Errorf("WNT/import boxes must be unset (un-evidenced): P_23=%v P_24=%v P_25=%v P_26=%v", p.P_23, p.P_24, p.P_25, p.P_26)
	}
	// Input side is register-backed only: P_43 = 92 (domestic incl. documented opex), P_42 = 400;
	// P_48 = P_43; P_51 = 230 − 92 = 138 — exactly the store's NetPayable (the WNT legs cancel).
	if p.P_48 != 92 {
		t.Errorf("P_48 (input VAT) = %d, want 92", p.P_48)
	}
	if p.P_43 == nil || *p.P_43 != 92 {
		t.Errorf("P_43 (input VAT, other purchases) = %v, want 92", p.P_43)
	}
	if p.P_42 == nil || *p.P_42 != 400 {
		t.Errorf("P_42 (input net, other purchases) = %v, want 400", p.P_42)
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
