package accounting

import (
	"testing"

	"github.com/shopspring/decimal"
)

func di(i int64) decimal.Decimal  { return decimal.NewFromInt(i) }
func ds(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestComputeCashFlow_SignsAndReconciliation(t *testing.T) {
	in := CashFlowInputs{
		Currency:          "EUR",
		NetProfit:         di(1000),
		Depreciation:      di(200),
		ARDelta:           di(300), // asset grew → use of cash (−300)
		InventoryDelta:    di(100), // −100
		APDelta:           di(50),  // liability grew → source (+50)
		AccruedDelta:      di(20),
		IncomeTaxDelta:    di(10),
		VatDelta:          di(15),
		PrepaymentsDelta:  di(40),
		CapexDelta:        di(400),  // investing −400
		EquityDelta:       di(500),  // +500
		DrawsDelta:        di(-150), // a draw (Dr 3030) → −150
		LoansDelta:        di(300),  // loan drawn → +300
		OpeningCash:       di(100),
		ClosingCashActual: di(1285),
	}
	cf := ComputeCashFlow(in)

	// Operating: 1000 + 200 − 300 − 100 + 50 + 20 + 10 + 15 + 40 = 935.
	if !cf.Operating.Subtotal.Equal(di(935)) {
		t.Fatalf("operating subtotal = %s, want 935", cf.Operating.Subtotal)
	}
	if !cf.Investing.Subtotal.Equal(di(-400)) {
		t.Fatalf("investing subtotal = %s, want -400", cf.Investing.Subtotal)
	}
	if !cf.Financing.Subtotal.Equal(di(650)) { // 500 − 150 + 300
		t.Fatalf("financing subtotal = %s, want 650", cf.Financing.Subtotal)
	}
	if !cf.NetChange.Equal(di(1185)) {
		t.Fatalf("net change = %s, want 1185", cf.NetChange)
	}
	if !cf.ClosingCash.Equal(di(1285)) {
		t.Fatalf("closing cash = %s, want 1285", cf.ClosingCash)
	}
	if !cf.Check.IsZero() || !cf.Balanced {
		t.Fatalf("expected reconciled: check=%s balanced=%v", cf.Check, cf.Balanced)
	}

	// AR line signs to a use of cash; AP line to a source.
	if got := cf.Operating.Lines[2]; got.Label != "Change in accounts receivable (1040)" || !got.Amount.Equal(di(-300)) {
		t.Fatalf("AR line = %+v, want -300", got)
	}
	if got := cf.Operating.Lines[5]; got.Label != "Change in accounts payable (2010)" || !got.Amount.Equal(di(50)) {
		t.Fatalf("AP line = %+v, want 50", got)
	}
}

func TestComputeCashFlow_UnbalancedSurfacesInCheck(t *testing.T) {
	in := CashFlowInputs{
		NetProfit:         di(1000),
		OpeningCash:       di(0),
		ClosingCashActual: di(950), // computed closing is 1000 → check −50
	}
	cf := ComputeCashFlow(in)
	if cf.Balanced {
		t.Fatalf("expected not balanced")
	}
	if !cf.Check.Equal(di(-50)) {
		t.Fatalf("check = %s, want -50", cf.Check)
	}
}

func healthByName(t *testing.T, in FinancialHealthInputs) map[string]struct {
	value  decimal.Decimal
	status string
	unit   string
} {
	t.Helper()
	fh := ComputeFinancialHealth(in)
	out := map[string]struct {
		value  decimal.Decimal
		status string
		unit   string
	}{}
	for _, r := range fh.Rows {
		out[r.Name] = struct {
			value  decimal.Decimal
			status string
			unit   string
		}{r.Value, r.Status, r.Unit}
	}
	return out
}

func TestComputeFinancialHealth_Values(t *testing.T) {
	in := FinancialHealthInputs{
		Currency:           "EUR",
		NetRevenue:         di(1000),
		GrossRevenue:       di(1200),
		Cogs:               di(400),
		NetProfit:          di(100),
		Discounts:          di(120),
		Returns:            di(60),
		TotalAssets:        di(2000),
		TotalLiabilities:   di(800),
		Equity:             di(1200),
		CurrentAssets:      di(900),
		CurrentLiabilities: di(300),
		Cash:               di(200),
		AR:                 di(100),
		OpeningInventory:   di(300),
		ClosingInventory:   di(500), // avg 400
		UnitsSold:          di(80),
		UnitsReceived:      di(100),
		ActiveSKU:          di(50),
	}
	m := healthByName(t, in)

	cases := []struct {
		name   string
		value  decimal.Decimal
		status string
	}{
		{"Gross Margin", di(60), HealthStatusOK},
		{"Net Profit Margin", di(10), HealthStatusOK},
		{"Return on Assets (ROA)", di(5), HealthStatusOK}, // 5 ≥ 5
		{"Return on Equity (ROE)", ds("8.33"), HealthStatusWarn},
		{"Current Ratio", di(3), HealthStatusOK},
		{"Quick Ratio", di(1), HealthStatusOK},
		{"Inventory Turnover", di(1), HealthStatusWarn},
		{"Days Inventory Outstanding (DIO)", di(365), HealthStatusWarn},
		{"Sell-Through", di(80), HealthStatusOK},
		{"Days Sales Outstanding (DSO)", ds("36.5"), HealthStatusWarn},
		{"COGS %", di(40), HealthStatusOK},
		{"Discount Rate", di(10), HealthStatusOK},
		{"Return Rate", di(5), HealthStatusOK},
		{"Debt-to-Equity", ds("0.67"), HealthStatusOK},
		{"Revenue per SKU", di(20), HealthStatusTrack},
		{"GMROI", ds("1.5"), HealthStatusWarn},
		{"Cost per Unit", di(5), HealthStatusTrack},
	}
	if len(m) != len(cases) {
		t.Fatalf("row count = %d, want %d", len(m), len(cases))
	}
	for _, c := range cases {
		got, ok := m[c.name]
		if !ok {
			t.Errorf("missing row %q", c.name)
			continue
		}
		if !got.value.Equal(c.value) {
			t.Errorf("%s value = %s, want %s", c.name, got.value, c.value)
		}
		if got.status != c.status {
			t.Errorf("%s status = %s, want %s", c.name, got.status, c.status)
		}
	}
	// Track rows display the base currency as their unit.
	if m["Revenue per SKU"].unit != "EUR" {
		t.Errorf("Revenue per SKU unit = %q, want EUR", m["Revenue per SKU"].unit)
	}
}

func TestComputeFinancialHealth_DivideByZeroIsNA(t *testing.T) {
	// All-zero inputs: every quotient must guard to a zero value with status na (no panic, no NaN).
	m := healthByName(t, FinancialHealthInputs{Currency: "EUR"})
	for name, r := range m {
		if r.status != HealthStatusNA {
			t.Errorf("%s status = %s, want na on zero inputs", name, r.status)
		}
		if !r.value.IsZero() {
			t.Errorf("%s value = %s, want 0 on zero inputs", name, r.value)
		}
	}
}
