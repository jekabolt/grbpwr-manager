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
		NetImport:           d("100"),
		InputImport:         d("23"), // import art. 33a self-charge, same identity
		OutputWntSelfCharge: d("69"),
		NetInputDomestic:    d("400"),
		InputDomestic:       d("92"),
		// NetPayable as the store computes it: output + self-charge − all input.
		NetPayable: d("138"),
	}

	dec := BuildDeclaration(ret)
	p := dec.Pozycje

	// Output: domestic 23% + both reverse-charge self-assessments. P_38 is the sum of the declared
	// boxes (P_20 + P_24 + P_26), and each of those is evidenced by a sales-register row, so the
	// declaration matches the ewidencja AND the VAT-UE statement built from the same acquisitions.
	if p.P_38 != 299 {
		t.Errorf("P_38 (total output VAT) = %d, want 299 (230 domestic + 46 WNT + 23 import)", p.P_38)
	}
	if p.P_23 == nil || *p.P_23 != 200 || p.P_24 == nil || *p.P_24 != 46 {
		t.Errorf("WNT boxes = P_23:%v P_24:%v, want 200 / 46", p.P_23, p.P_24)
	}
	if p.P_25 == nil || *p.P_25 != 100 || p.P_26 == nil || *p.P_26 != 23 {
		t.Errorf("import boxes = P_25:%v P_26:%v, want 100 / 23", p.P_25, p.P_26)
	}
	// Input side is register-backed: P_43 = 92 domestic (incl. documented opex) + 46 WNT + 23 import;
	// P_42 = 400 + 200 + 100; P_48 = P_43. P_51 = 299 − 161 = 138 — the store's NetPayable, i.e. the
	// self-charge legs cancel EXACTLY and never move the payable.
	if p.P_48 != 161 {
		t.Errorf("P_48 (input VAT) = %d, want 161", p.P_48)
	}
	if p.P_43 == nil || *p.P_43 != 161 {
		t.Errorf("P_43 (input VAT, other purchases) = %v, want 161", p.P_43)
	}
	if p.P_42 == nil || *p.P_42 != 700 {
		t.Errorf("P_42 (input net, other purchases) = %v, want 700", p.P_42)
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
}

// A month with no reverse charge must leave P_23..P_26 unset (omitempty) — the boxes are declared
// only when there is something to declare.
func TestBuildDeclarationNoSelfCharge(t *testing.T) {
	p := BuildDeclaration(&entity.AcctVatReturnPL{
		NetDomestic: d("1000"), OutputDomestic: d("230"),
		NetInputDomestic: d("400"), InputDomestic: d("92"),
	}).Pozycje
	if p.P_23 != nil || p.P_24 != nil || p.P_25 != nil || p.P_26 != nil {
		t.Errorf("no WNT/import in the month, boxes must stay unset: P_23=%v P_24=%v P_25=%v P_26=%v", p.P_23, p.P_24, p.P_25, p.P_26)
	}
	if p.P_38 != 230 || p.P_48 != 92 {
		t.Errorf("P_38/P_48 = %d/%d, want 230/92", p.P_38, p.P_48)
	}
}
