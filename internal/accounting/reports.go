package accounting

import (
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Wave-5 reporting math (docs/plan-accounting-phase2/05-wave5-reporting.md §5.1/§5.2). These are the
// PURE derivations behind GetCashFlowStatement and GetFinancialHealth: the store gathers ledger
// balances (and operational units) and hands them here; no SQL, no clock, no store — so the sign
// conventions and the divide-by-zero guards are unit-tested in isolation (reports_test.go). Money is
// EUR (the ledger base); the store labels any borrowed operational unit counts by source.

// Cash-flow section names (the wire "name" of each AcctCashFlowSection).
const (
	CashFlowOperating = "operating"
	CashFlowInvesting = "investing"
	CashFlowFinancing = "financing"
)

// Financial-health row statuses. na is a guarded divide-by-zero (value 0); track is a no-benchmark row.
const (
	HealthStatusOK    = "ok"
	HealthStatusWarn  = "warn"
	HealthStatusNA    = "na"
	HealthStatusTrack = "track"
)

// Financial-health display units.
const (
	HealthUnitPct  = "%"
	HealthUnitX    = "x"
	HealthUnitDays = "days"
)

// CashFlowInputs is the ledger-derived input to ComputeCashFlow. Every *Delta is the SECTION-SIGNED
// balance change over the period (closing balance strictly before `to`, minus opening balance strictly
// before `from`) of that account group — i.e. positive means the account grew on its own normal side.
// ComputeCashFlow applies the cash-flow sign itself (an asset growing is a USE of cash; a liability or
// equity growing is a SOURCE), so the store never signs ad-hoc. NetProfit is the period Σ_PL(cr − dr);
// Depreciation is the period 6370 charge (a non-cash add-back whose 1225 contra is deliberately left
// out of CapexDelta, so the two cancel and cash reconciles).
type CashFlowInputs struct {
	From, To     time.Time
	Currency     string
	NetProfit    decimal.Decimal // Σ_PL(cr − dr) over [from, to)
	Depreciation decimal.Decimal // 6370 charge over [from, to) (add-back)

	// Working capital — asset groups (a positive delta is cash tied up).
	ARDelta        decimal.Decimal // 1040 Accounts Receivable
	InventoryDelta decimal.Decimal // 1110 + 1120 + 1130 + 1140
	PrepaidDelta   decimal.Decimal // 1210 Prepaid Expenses — a CURRENT operating asset, not capex

	// Working capital — liability groups (a positive delta is a cash source).
	APDelta          decimal.Decimal // 2010 Accounts Payable
	AccruedDelta     decimal.Decimal // 2030 Accrued Expenses
	IncomeTaxDelta   decimal.Decimal // 2050 Income Tax Payable
	VatDelta         decimal.Decimal // 2070 VAT Payable + 2080 VAT Input (recoverable)
	PrepaymentsDelta decimal.Decimal // 2090 Customer Prepayments

	// Investing — non-current asset groups (a purchase is a use of cash). 1210 Prepaid Expenses is NOT
	// here — it is a current operating asset (see PrepaidDelta).
	CapexDelta decimal.Decimal // 1220 Equipment (+ 1230 + 1240 when they exist)

	// Financing.
	EquityDelta decimal.Decimal // 3010 Owner's Equity + 3020 Retained Earnings
	DrawsDelta  decimal.Decimal // 3030 Draws / Distributions
	LoansDelta  decimal.Decimal // 2015 Director's/Shareholder Loan (+ 2060 when it exists)

	OpeningCash       decimal.Decimal // 1010 + 1030 (+ 1050) as of From
	ClosingCashActual decimal.Decimal // 1010 + 1030 (+ 1050) as of To
}

// ComputeCashFlow builds the indirect-method statement from CashFlowInputs. Operating starts from net
// profit, adds back depreciation, then folds the working-capital deltas (assets negated, liabilities
// kept); investing folds capex (negated); financing folds equity, draws and loans (kept). NetChange is
// the sum of the three subtotals; ClosingCash = OpeningCash + NetChange; Check compares that to the
// actual closing cash balance and Balanced flags a zero difference (the trust panel).
func ComputeCashFlow(in CashFlowInputs) *entity.AcctCashFlowStatement {
	neg := func(d decimal.Decimal) decimal.Decimal { return d.Neg() }

	// Codes name the chart-of-accounts set behind each line so the UI can drill into the ledgers —
	// keep them in lockstep with the delta sets in wave5_reports.go (GetCashFlowStatement). Net
	// profit carries none: its story is the whole P&L, not one ledger.
	operating := entity.AcctCashFlowSection{Name: CashFlowOperating, Lines: []entity.AcctCashFlowLine{
		{Label: "Net profit for the period", Amount: in.NetProfit},
		{Label: "Add back: depreciation (6370)", Amount: in.Depreciation, Codes: []string{"6370"}},
		{Label: "Change in accounts receivable (1040)", Amount: neg(in.ARDelta), Codes: []string{"1040"}},
		{Label: "Change in inventory (1110–1140)", Amount: neg(in.InventoryDelta), Codes: []string{"1110", "1120", "1130", "1140"}},
		{Label: "Change in prepaid expenses (1210)", Amount: neg(in.PrepaidDelta), Codes: []string{"1210"}},
		{Label: "Change in accounts payable (2010)", Amount: in.APDelta, Codes: []string{"2010"}},
		{Label: "Change in accrued expenses (2030)", Amount: in.AccruedDelta, Codes: []string{"2030"}},
		{Label: "Change in income tax payable (2050)", Amount: in.IncomeTaxDelta, Codes: []string{"2050"}},
		{Label: "Change in VAT (2070/2080)", Amount: in.VatDelta, Codes: []string{"2070", "2080"}},
		{Label: "Change in customer prepayments (2090)", Amount: in.PrepaymentsDelta, Codes: []string{"2090"}},
	}}
	operating.Subtotal = sumCashFlowLines(operating.Lines)

	investing := entity.AcctCashFlowSection{Name: CashFlowInvesting, Lines: []entity.AcctCashFlowLine{
		{Label: "Purchase of non-current assets (1220)", Amount: neg(in.CapexDelta), Codes: []string{"1220", "1230", "1240"}},
	}}
	investing.Subtotal = sumCashFlowLines(investing.Lines)

	financing := entity.AcctCashFlowSection{Name: CashFlowFinancing, Lines: []entity.AcctCashFlowLine{
		{Label: "Owner equity contributions (3010/3020)", Amount: in.EquityDelta, Codes: []string{"3010", "3020"}},
		{Label: "Owner draws / distributions (3030)", Amount: in.DrawsDelta, Codes: []string{"3030"}},
		{Label: "Loans (2015)", Amount: in.LoansDelta, Codes: []string{"2015", "2060"}},
	}}
	financing.Subtotal = sumCashFlowLines(financing.Lines)

	netChange := operating.Subtotal.Add(investing.Subtotal).Add(financing.Subtotal)
	closingCash := in.OpeningCash.Add(netChange)
	check := in.ClosingCashActual.Sub(closingCash)

	return &entity.AcctCashFlowStatement{
		From:              in.From,
		To:                in.To,
		Currency:          in.Currency,
		Operating:         operating,
		Investing:         investing,
		Financing:         financing,
		NetChange:         netChange,
		OpeningCash:       in.OpeningCash,
		ClosingCash:       closingCash,
		ClosingCashActual: in.ClosingCashActual,
		Check:             check,
		Balanced:          check.IsZero(),
	}
}

// sumCashFlowLines totals a section's line amounts.
func sumCashFlowLines(lines []entity.AcctCashFlowLine) decimal.Decimal {
	sum := decimal.Zero
	for _, l := range lines {
		sum = sum.Add(l.Amount)
	}
	return sum
}

// FinancialHealthInputs is the ledger-derived (+ operational-units) input to ComputeFinancialHealth.
// Period figures (NetRevenue, GrossRevenue, Cogs, Opex, NetProfit, Discounts, Returns) are signed to
// their natural positive direction over [from, to); balance-sheet figures are as of To. Units come
// from operational metrics (labelled by source in the caveats), not the ledger.
type FinancialHealthInputs struct {
	Currency string

	// Period P&L (ledger, natural-positive).
	NetRevenue   decimal.Decimal // revenue section net of 4030/4040/4050 contra
	GrossRevenue decimal.Decimal // 4010 + 4020 + 4110 + 4310 before contra
	Cogs         decimal.Decimal // cogs section
	NetProfit    decimal.Decimal // Σ_PL(cr − dr) — after tax
	Discounts    decimal.Decimal // 4030 magnitude
	Returns      decimal.Decimal // 4040 magnitude

	// Balance sheet as of To (ledger).
	TotalAssets        decimal.Decimal
	TotalLiabilities   decimal.Decimal
	Equity             decimal.Decimal // equity section + current-period retained earnings
	CurrentAssets      decimal.Decimal
	CurrentLiabilities decimal.Decimal
	Cash               decimal.Decimal // 1010 + 1030 (+ 1050)
	AR                 decimal.Decimal // 1040
	OpeningInventory   decimal.Decimal // 1110–1140 as of From
	ClosingInventory   decimal.Decimal // 1110–1140 as of To

	// Operational units (metrics; lifetime, collection-tagged products — labelled by source).
	UnitsSold     decimal.Decimal
	UnitsReceived decimal.Decimal
	ActiveSKU     decimal.Decimal

	// PeriodDays is the length of [from, to) in days (>= 1). Flow-over-stock ratios (ROA/ROE/turnover/
	// DIO/DSO/GMROI) whose benchmarks are ANNUAL are annualized by 365/PeriodDays so a one-month window
	// is not read against a yearly threshold; margins (same-period flow ratios) are scale-invariant and
	// use raw flows. Zero/negative ⇒ no annualization (factor 1) rather than a bad divisor.
	PeriodDays int
}

// ComputeFinancialHealth builds the ratio set with the owner's benchmark strings and an ok/warn/na/track
// status per row. Every quotient is guarded (a zero denominator yields a zero value with status "na");
// "track" rows have no pass/fail. Benchmarks and thresholds are the owner's Excel values verbatim.
func ComputeFinancialHealth(in FinancialHealthInputs) *entity.AcctFinancialHealth {
	grossProfit := in.NetRevenue.Sub(in.Cogs)
	avgInventory := in.OpeningInventory.Add(in.ClosingInventory).Div(decimal.NewFromInt(2))
	quickAssets := in.Cash.Add(in.AR)

	// Annualize period flows for the stock-based ratios whose benchmarks are yearly. periodDays drives
	// DSO/DIO directly (days), annFactor scales NetProfit/COGS/GrossProfit up to a yearly run-rate.
	periodDays := decimal.NewFromInt(365)
	annFactor := decimal.NewFromInt(1)
	if in.PeriodDays > 0 {
		periodDays = decimal.NewFromInt(int64(in.PeriodDays))
		annFactor = decimal.NewFromInt(365).Div(periodDays)
	}
	annNetProfit := in.NetProfit.Mul(annFactor)
	annCogs := in.Cogs.Mul(annFactor)
	annGrossProfit := grossProfit.Mul(annFactor)

	rows := make([]entity.AcctFinancialHealthRow, 0, 17)

	// --- Profitability ---
	gmV, gmOK := pct(grossProfit, in.NetRevenue)
	rows = append(rows, healthRow("Gross Margin", "(Revenue − COGS) / Revenue", "50–70%", HealthUnitPct,
		gmV, statusRange(gmOK, gmV, 50, 70)))

	npmV, npmOK := pct(in.NetProfit, in.NetRevenue)
	rows = append(rows, healthRow("Net Profit Margin", "Net Profit / Revenue", ">5%", HealthUnitPct,
		npmV, statusMin(npmOK, npmV, decimal.NewFromInt(5))))

	roaV, roaOK := pct(annNetProfit, in.TotalAssets)
	rows = append(rows, healthRow("Return on Assets (ROA)", "Net Profit (annualized) / Total Assets", ">5%", HealthUnitPct,
		roaV, statusMin(roaOK, roaV, decimal.NewFromInt(5))))

	roeV, roeOK := pct(annNetProfit, in.Equity)
	rows = append(rows, healthRow("Return on Equity (ROE)", "Net Profit (annualized) / Equity", ">20%", HealthUnitPct,
		roeV, statusMin(roeOK, roeV, decimal.NewFromInt(20))))

	// --- Liquidity ---
	crV, crOK := ratio(in.CurrentAssets, in.CurrentLiabilities)
	rows = append(rows, healthRow("Current Ratio", "Current Assets / Current Liabilities", ">1.5", HealthUnitX,
		crV, statusMin(crOK, crV, decimal.NewFromFloat(1.5))))

	qrV, qrOK := ratio(quickAssets, in.CurrentLiabilities)
	rows = append(rows, healthRow("Quick Ratio", "(Cash + AR) / Current Liabilities", ">1.0", HealthUnitX,
		qrV, statusMin(qrOK, qrV, decimal.NewFromInt(1))))

	// --- Inventory ---
	turnV, turnOK := ratio(annCogs, avgInventory)
	rows = append(rows, healthRow("Inventory Turnover", "COGS (annualized) / Avg Inventory", "4–8×", HealthUnitX,
		turnV, statusRange(turnOK, turnV, 4, 8)))

	// DIO derives from the turnover value; guard both the turnover denominator and a zero turnover.
	dioV, dioOK := decimal.Zero, false
	if turnOK && turnV.IsPositive() {
		dioV, dioOK = decimal.NewFromInt(365).Div(turnV).Round(2), true
	}
	rows = append(rows, healthRow("Days Inventory Outstanding (DIO)", "365 / Inventory Turnover", "45–90 days", HealthUnitDays,
		dioV, statusRange(dioOK, dioV, 45, 90)))

	stV, stOK := pct(in.UnitsSold, in.UnitsReceived)
	rows = append(rows, healthRow("Sell-Through", "Units Sold / Units Received", ">70%", HealthUnitPct,
		stV, statusMin(stOK, stV, decimal.NewFromInt(70))))

	// DSO = AR / (revenue per day) = AR / Revenue × days-in-period (NOT ×365, or a one-month window
	// reads ~12× too high against the <30-day annual benchmark).
	dsoV, dsoOK := decimal.Zero, false
	if !in.NetRevenue.IsZero() {
		dsoV, dsoOK = in.AR.Div(in.NetRevenue).Mul(periodDays).Round(2), true
	}
	rows = append(rows, healthRow("Days Sales Outstanding (DSO)", "AR / Revenue × days in period", "<30 days", HealthUnitDays,
		dsoV, statusMax(dsoOK, dsoV, decimal.NewFromInt(30))))

	// --- Cost structure ---
	cogsPctV, cogsPctOK := pct(in.Cogs, in.NetRevenue)
	rows = append(rows, healthRow("COGS %", "COGS / Revenue", "30–50%", HealthUnitPct,
		cogsPctV, statusRange(cogsPctOK, cogsPctV, 30, 50)))

	drV, drOK := pct(in.Discounts, in.GrossRevenue)
	rows = append(rows, healthRow("Discount Rate", "Discounts (4030) / Gross Revenue", "<20%", HealthUnitPct,
		drV, statusMax(drOK, drV, decimal.NewFromInt(20))))

	rrV, rrOK := pct(in.Returns, in.GrossRevenue)
	rows = append(rows, healthRow("Return Rate", "Returns (4040) / Gross Revenue", "<15%", HealthUnitPct,
		rrV, statusMax(rrOK, rrV, decimal.NewFromInt(15))))

	// --- Leverage ---
	deV, deOK := ratio(in.TotalLiabilities, in.Equity)
	rows = append(rows, healthRow("Debt-to-Equity", "Total Liabilities / Equity", "<1.0", HealthUnitX,
		deV, statusMax(deOK, deV, decimal.NewFromInt(1))))

	// --- Fashion / track (no pass/fail) ---
	// Revenue per SKU / Cost per Unit are "track" only: they divide PERIOD money by LIFETIME unit/SKU
	// counts (the metrics source is not date-windowed — see caveats), so the formula labels say so and
	// they carry no pass/fail.
	rpsV, rpsOK := ratio(in.NetRevenue, in.ActiveSKU)
	rows = append(rows, healthRow("Revenue per SKU", "Period Revenue / active SKU count (lifetime)", "track", in.Currency,
		rpsV, trackStatus(rpsOK)))

	gmroiV, gmroiOK := ratio(annGrossProfit, avgInventory)
	rows = append(rows, healthRow("GMROI", "Gross Profit (annualized) / Avg Inventory Cost", ">2.0", HealthUnitX,
		gmroiV, statusMin(gmroiOK, gmroiV, decimal.NewFromInt(2))))

	cpuV, cpuOK := ratio(in.Cogs, in.UnitsSold)
	rows = append(rows, healthRow("Cost per Unit", "Period COGS / units sold (lifetime)", "track", in.Currency,
		cpuV, trackStatus(cpuOK)))

	return &entity.AcctFinancialHealth{
		Currency: in.Currency,
		Rows:     rows,
		Caveats: []string{
			"Money figures are ledger-first (EUR); revenue is net of returns/discounts.",
			"Units Sold / Received and Active SKU come from operational metrics (lifetime drop sell-through, collection-tagged products), not the ledger.",
		},
	}
}

// healthRow assembles one ratio row.
func healthRow(name, formula, benchmark, unit string, value decimal.Decimal, status string) entity.AcctFinancialHealthRow {
	return entity.AcctFinancialHealthRow{
		Name:      name,
		Formula:   formula,
		Value:     value,
		Benchmark: benchmark,
		Status:    status,
		Unit:      unit,
	}
}

// pct returns num/den×100 rounded to 2dp and true, or (0, false) when den is zero (a guarded n/a).
func pct(num, den decimal.Decimal) (decimal.Decimal, bool) {
	if den.IsZero() {
		return decimal.Zero, false
	}
	return num.Div(den).Mul(decimal.NewFromInt(100)).Round(2), true
}

// ratio returns num/den rounded to 2dp and true, or (0, false) when den is zero (a guarded n/a).
func ratio(num, den decimal.Decimal) (decimal.Decimal, bool) {
	if den.IsZero() {
		return decimal.Zero, false
	}
	return num.Div(den).Round(2), true
}

// statusRange is ok when lo ≤ v ≤ hi, warn otherwise; na when the value could not be computed.
func statusRange(ok bool, v decimal.Decimal, lo, hi int64) string {
	if !ok {
		return HealthStatusNA
	}
	if v.GreaterThanOrEqual(decimal.NewFromInt(lo)) && v.LessThanOrEqual(decimal.NewFromInt(hi)) {
		return HealthStatusOK
	}
	return HealthStatusWarn
}

// statusMin is ok when v ≥ min, warn otherwise; na when not computable.
func statusMin(ok bool, v, min decimal.Decimal) string {
	if !ok {
		return HealthStatusNA
	}
	if v.GreaterThanOrEqual(min) {
		return HealthStatusOK
	}
	return HealthStatusWarn
}

// statusMax is ok when v ≤ max, warn otherwise; na when not computable.
func statusMax(ok bool, v, max decimal.Decimal) string {
	if !ok {
		return HealthStatusNA
	}
	if v.LessThanOrEqual(max) {
		return HealthStatusOK
	}
	return HealthStatusWarn
}

// trackStatus is track (no benchmark) when computable, na otherwise.
func trackStatus(ok bool) string {
	if !ok {
		return HealthStatusNA
	}
	return HealthStatusTrack
}
