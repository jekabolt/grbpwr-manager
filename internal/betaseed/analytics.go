package betaseed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	decimal "google.golang.org/genproto/googleapis/type/decimal"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// AnalyticsResult summarises everything SeedAnalytics created and the acceptance
// proof it read back. Counts are by FINAL order status so a caller can reconcile
// against the dashboards. SectionPopulated maps each requested GetMetrics section
// to whether it came back non-empty — the acceptance gate for this phase.
type AnalyticsResult struct {
	// Part 1 — operator-entry config rows written.
	VatRateRows         int
	OpexLineRows        int
	OpexRecurringRows   int
	EmployeeRows        int
	ChannelSpendRows    int
	InventoryTargetRows int
	AlertSettingsSet    bool

	// Part 2 — orders by final status.
	OrdersDelivered         int
	OrdersShipped           int
	OrdersConfirmed         int
	OrdersPartiallyRefunded int
	OrdersAwaitingPayment   int
	OrdersCancelled         int
	OrdersTotal             int
	OrdersOnCostPriced      int            // subset placed on the PLM cost-priced style (MARGIN/COGS)
	CountriesUsed           map[string]int // shipping country -> net-revenue order count (GEOGRAPHY)

	// Part 3 — fulfillment board.
	FulfillmentCardsTouched int
	FulfillmentShipped      int
	FulfillmentDelivered    int
	ShippingBestEffort      map[string]string // carrier-API RPC -> outcome / skip reason

	// Part 4 — acceptance proof (read back from GetMetrics + GetDashboard).
	AssertedRevenue  float64
	AssertedOrders   int32
	DashboardRevenue float64
	DashboardOrders  int32
	SectionPopulated map[string]bool

	Warnings []string
}

func (r *AnalyticsResult) warn(s *Seeder, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	s.logf("  WARN " + msg)
}

// analyticsProduct is a sellable handle SeedAnalytics places orders against.
type analyticsProduct struct {
	label      string
	styleID    int32
	colorwayID int32
	variants   []VariantResult // >= 2 (m,l) with minted SKUs + internal ids
	costPriced bool            // true only for the PLM style (has product.cost_price)
}

// SeedAnalytics lights up the analytics surface of the beta admin: it writes the
// operator-entry config (VAT / FX / opex / employees / channel spend / inventory
// targets / alert thresholds), generates a realistic spread of net-revenue orders
// through CreateCustomOrder (bank_invoice, born Confirmed) plus a lifecycle spread
// (Shipped / Delivered / partially-refunded) and a handful of AwaitingPayment +
// Cancelled storefront orders, exercises the fulfillment kanban, and finally reads
// GetMetrics + GetDashboard back to PROVE revenue > 0 and orders > 0.
//
// It is idempotent/re-runnable: per-run suffixes (s.Run) keep emails/labels distinct
// and every stock/threshold write is an absolute upsert.
func (s *Seeder) SeedAnalytics(ctx context.Context, cat []CatalogResult, plm *PLMResult) (*AnalyticsResult, error) {
	r := &AnalyticsResult{
		CountriesUsed:      map[string]int{},
		ShippingBestEffort: map[string]string{},
		SectionPopulated:   map[string]bool{},
	}

	// Build the product pool: every catalog style + the PLM cost-priced style.
	pool := make([]analyticsProduct, 0, len(cat)+1)
	for _, c := range cat {
		if len(c.Variants) < 2 {
			continue
		}
		pool = append(pool, analyticsProduct{label: c.BaseSku, styleID: c.StyleID, colorwayID: c.ColorwayID, variants: c.Variants})
	}
	var plmProd *analyticsProduct
	if plm != nil && len(plm.Colorway1Variants) >= 2 {
		plmProd = &analyticsProduct{
			label: plm.Colorway1BaseSku, styleID: plm.StyleID, colorwayID: plm.Colorway1ID,
			variants: plm.Colorway1Variants, costPriced: true,
		}
	}
	if len(pool) == 0 && plmProd == nil {
		return r, fmt.Errorf("SeedAnalytics: no sellable products (need >=1 catalog style with 2 variants or a PLM style)")
	}
	if len(pool) == 0 && plmProd != nil {
		pool = append(pool, *plmProd)
	}

	// Part 1 — operator-entry config (best-effort per item; failures warn, don't abort).
	s.logf("=== SeedAnalytics [1/4]: operator-entry config ===")
	s.seedAnalyticsConfig(ctx, cat, plmProd, r)

	// Raise stock high on every variant we'll order against (catalog seeds 5/variant).
	s.logf("=== SeedAnalytics: raising stock to 1000 on order variants ===")
	s.raiseStock(ctx, pool, plmProd, r)

	// Part 2 — order volume.
	s.logf("=== SeedAnalytics [2/4]: order volume ===")
	if err := s.seedOrderVolume(ctx, pool, plmProd, r); err != nil {
		return r, fmt.Errorf("order volume: %w", err)
	}

	// Part 3 — fulfillment board (kanban + best-effort carrier API).
	s.logf("=== SeedAnalytics [3/4]: fulfillment board ===")
	s.seedFulfillment(ctx, pool, plmProd, r)

	// Part 4 — prove it (acceptance gate).
	s.logf("=== SeedAnalytics [4/4]: acceptance readback ===")
	if err := s.proveAnalytics(ctx, r); err != nil {
		return r, err
	}
	return r, nil
}

// ================================================================= Part 1: config

func (s *Seeder) seedAnalyticsConfig(ctx context.Context, cat []CatalogResult, plmProd *analyticsProduct, r *AnalyticsResult) {
	now := time.Now()
	monthOf := func(t time.Time) string { return t.Format("2006-01-02") }

	// VAT rates per destination country (feeds vat_amount + PROFITABILITY / operating result).
	vat := []*admin.VatRate{
		{CountryCode: "DE", RatePct: decv("19.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
		{CountryCode: "FR", RatePct: decv("20.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
		{CountryCode: "IT", RatePct: decv("22.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
		{CountryCode: "ES", RatePct: decv("21.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
		{CountryCode: "NL", RatePct: decv("21.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
		{CountryCode: "PL", RatePct: decv("23.00"), ValidFrom: timestamppb.New(now.AddDate(-2, 0, 0))},
	}
	if _, err := s.C.UpsertVatRates(ctx, &admin.UpsertVatRatesRequest{Rates: vat}); err != nil {
		r.warn(s, "UpsertVatRates: %v", err)
	} else {
		r.VatRateRows = len(vat)
		s.logf("  vat rates: %d countries", len(vat))
	}

	// Costing FX rates are no longer seeded here: the fxsync worker auto-populates costing_fx_rate
	// from ECB reference rates on the beta backend (FX_SYNC_ENABLED=true), so a manual seed would be
	// immediately superseded. Manual FX entry has been removed.

	// One-off opex lines across the last 3 months (feeds operating result / opex_total).
	var opex []*admin.OpexLineInsert
	for m := 0; m < 3; m++ {
		month := monthOf(now.AddDate(0, -m, 0))
		opex = append(opex,
			&admin.OpexLineInsert{Month: month, Category: "rent", Label: "studio-" + s.Run, Amount: decv("1800.00"), Currency: "EUR", Note: "beta seed"},
			&admin.OpexLineInsert{Month: month, Category: "software", Label: "saas-" + s.Run, Amount: decv("420.00"), Currency: "EUR", Note: "beta seed"},
			&admin.OpexLineInsert{Month: month, Category: "professional_services", Label: "accounting-" + s.Run, Amount: decv("650.00"), Currency: "EUR", Note: "beta seed"},
		)
	}
	if _, err := s.C.UpsertOpexLines(ctx, &admin.UpsertOpexLinesRequest{Lines: opex}); err != nil {
		r.warn(s, "UpsertOpexLines: %v", err)
	} else {
		r.OpexLineRows = len(opex)
		s.logf("  opex lines: %d (3 months x 3 categories)", len(opex))
	}

	// Employees + one recurring salary line each (feeds opex + PROFITABILITY assembly labour).
	empSpecs := []struct {
		name, role, cur, cost string
	}{
		{"Seed Seamstress " + s.Run, "швея", "EUR", "2200.00"},
		{"Seed Patternmaker " + s.Run, "конструктор", "EUR", "2600.00"},
		{"Seed Ops " + s.Run, "operations", "EUR", "3000.00"},
	}
	for _, e := range empSpecs {
		resp, err := s.C.UpsertEmployee(ctx, &admin.UpsertEmployeeRequest{Employee: &admin.EmployeeInsert{
			FullName:           e.name,
			Role:               e.role,
			EmploymentStart:    now.AddDate(-1, 0, 0).Format("2006-01-02"),
			DefaultCurrency:    e.cur,
			DefaultMonthlyCost: decv(e.cost),
			Note:               "beta seed employee",
		}})
		if err != nil {
			r.warn(s, "UpsertEmployee(%s): %v", e.name, err)
			continue
		}
		r.EmployeeRows++
		empID := resp.GetId()
		if _, err := s.C.UpsertOpexRecurring(ctx, &admin.UpsertOpexRecurringRequest{Recurring: &admin.OpexRecurringInsert{
			Label:      "salary-" + e.name,
			Category:   "salaries",
			Amount:     decv(e.cost),
			Currency:   e.cur,
			ActiveFrom: now.AddDate(-1, 0, 0).Format("2006-01-02"),
			Note:       "beta seed recurring salary",
			EmployeeId: empID,
		}}); err != nil {
			r.warn(s, "UpsertOpexRecurring(%s): %v", e.name, err)
		} else {
			r.OpexRecurringRows++
		}
	}
	s.logf("  employees: %d, recurring salaries: %d", r.EmployeeRows, r.OpexRecurringRows)

	// Channel spend across the current period (feeds marketing_spend / ROAS / PROFITABILITY CAC).
	var spend []*admin.ChannelSpendInsert
	chans := []struct{ src, med, camp, amt string }{
		{"instagram", "paid_social", "ss26-launch", "180.00"},
		{"google", "cpc", "brand", "120.00"},
		{"tiktok", "paid_social", "ugc", "90.00"},
		{"newsletter", "email", "drop", "40.00"},
	}
	for d := 0; d < 8; d++ {
		date := now.AddDate(0, 0, -d*3).Format("2006-01-02")
		c := chans[d%len(chans)]
		spend = append(spend, &admin.ChannelSpendInsert{
			Date: date, UtmSource: c.src, UtmMedium: c.med, UtmCampaign: c.camp,
			Amount: decv(c.amt), Currency: "EUR",
		})
	}
	if _, err := s.C.UpsertChannelSpend(ctx, &admin.UpsertChannelSpendRequest{Spend: spend}); err != nil {
		r.warn(s, "UpsertChannelSpend: %v", err)
	} else {
		r.ChannelSpendRows = len(spend)
		s.logf("  channel spend: %d rows across %d channels", len(spend), len(chans))
	}

	// Inventory reorder targets for a few catalog SKUs (feeds INVENTORY_HEALTH / dashboard reorder).
	var targets []*admin.InventoryTargetInsert
	maxT := s.scaleN(1, 3, 6)
	for i, c := range cat {
		if i >= maxT || len(c.Variants) == 0 {
			break
		}
		for _, v := range c.Variants {
			targets = append(targets, &admin.InventoryTargetInsert{
				ProductId: c.ColorwayID, SizeId: v.SizeID,
				ReorderPoint: 8, TargetDaysCover: 30, LeadTimeDays: 21,
			})
		}
	}
	if len(targets) > 0 {
		if _, err := s.C.UpsertInventoryTargets(ctx, &admin.UpsertInventoryTargetsRequest{Targets: targets}); err != nil {
			r.warn(s, "UpsertInventoryTargets: %v", err)
		} else {
			r.InventoryTargetRows = len(targets)
			s.logf("  inventory targets: %d variant rows", len(targets))
		}
	}

	// Alert thresholds (feeds the dashboard alerts panel).
	if _, err := s.C.UpsertAlertSettings(ctx, &admin.UpsertAlertSettingsRequest{Settings: &admin.AlertSettings{
		CoverageWarnPct:        70,
		RefundRateWarnPct:      8,
		RateFloorN:             20,
		ContributionTrustPct:   60,
		Ga4CoverageWarnPct:     50,
		ProductionRunStaleDays: 45,
	}}); err != nil {
		r.warn(s, "UpsertAlertSettings: %v", err)
	} else {
		r.AlertSettingsSet = true
		s.logf("  alert settings: thresholds set")
	}
}

// raiseStock sets stock to 1000 on every variant SeedAnalytics may order against, so
// 100+ reserving custom orders never run dry (catalog seeds only 5/variant).
func (s *Seeder) raiseStock(ctx context.Context, pool []analyticsProduct, plmProd *analyticsProduct, r *AnalyticsResult) {
	seen := map[int64]bool{}
	bump := func(prods []analyticsProduct) {
		for _, p := range prods {
			for _, v := range p.variants {
				id := int64(v.VariantID)
				if seen[id] {
					continue
				}
				seen[id] = true
				if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
					Mode:      common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET,
					Quantity:  1000,
					Reason:    common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
					VariantId: id,
				}); err != nil {
					r.warn(s, "UpdateVariantStock(variant=%d): %v", id, err)
				}
			}
		}
	}
	bump(pool)
	if plmProd != nil {
		bump([]analyticsProduct{*plmProd})
	}
	s.logf("  stock raised on %d variants", len(seen))
}

// ================================================================= Part 2: order volume

func (s *Seeder) seedOrderVolume(ctx context.Context, pool []analyticsProduct, plmProd *analyticsProduct, r *AnalyticsResult) error {
	deliveredN := s.scaleN(3, 12, 60)
	shippedN := s.scaleN(1, 5, 22)
	confirmedN := s.scaleN(1, 4, 15)
	refundedN := s.scaleN(1, 3, 10)
	awaitingN := s.scaleN(1, 2, 5)
	cancelledN := s.scaleN(1, 2, 3)

	// Small customer pool so some buyers repeat (drives RFM / repeat-customer rate).
	custPool := s.scaleN(2, 6, 30)
	seq := 0 // global order sequence for product/qty/price/country spread
	custEmail := func() string {
		e := fmt.Sprintf("an-cust-%s-%02d@grbpwr.com", s.Run, seq%custPool)
		return e
	}

	// pickProduct biases ~1 in 4 orders onto the PLM cost-priced style (MARGIN/COGS),
	// spreading the rest across the catalog.
	pickProduct := func(i int) analyticsProduct {
		if plmProd != nil && i%4 == 3 {
			return *plmProd
		}
		return pool[i%len(pool)]
	}
	countries := s.orderCountries()

	// --- single-item lifecycle buckets (Confirmed -> Shipped -> Delivered) ---
	type bucket struct {
		name   string
		n      int
		ship   bool
		deliv  bool
		countP *int
	}
	buckets := []bucket{
		{"delivered", deliveredN, true, true, &r.OrdersDelivered},
		{"shipped", shippedN, true, false, &r.OrdersShipped},
		{"confirmed", confirmedN, false, false, &r.OrdersConfirmed},
	}
	for _, b := range buckets {
		made := 0
		for i := 0; i < b.n; i++ {
			p := pickProduct(seq)
			v := p.variants[seq%len(p.variants)]
			qty := int32(1 + seq%3)
			country := countries[seq%len(countries)]
			price := s.orderPrice(p, seq)
			seq++
			uuid, used, err := s.anCreateOrder(ctx, []*common.CustomOrderItemInsert{
				{Quantity: qty, VariantId: int64(v.VariantID), CustomPrice: price},
			}, country, custEmail())
			if err != nil {
				r.warn(s, "%s order %d (%s): %v", b.name, i, p.label, err)
				continue
			}
			if b.ship {
				if _, err := s.C.SetTrackingNumber(ctx, &admin.SetTrackingNumberRequest{OrderUuid: uuid, TrackingCode: fmt.Sprintf("AN-%s-%s-%03d", s.Run, b.name, i)}); err != nil {
					r.warn(s, "SetTrackingNumber(%s %s): %v", b.name, uuid, err)
				}
			}
			if b.deliv {
				if _, err := s.C.DeliveredOrder(ctx, &admin.DeliveredOrderRequest{OrderUuid: uuid}); err != nil {
					r.warn(s, "DeliveredOrder(%s %s): %v", b.name, uuid, err)
				}
			}
			*b.countP++
			r.CountriesUsed[used]++
			if p.costPriced {
				r.OrdersOnCostPriced++
			}
			made++
		}
		s.logf("  %s: %d/%d created", b.name, made, b.n)
	}

	// --- partially-refunded (multi-item Delivered -> refund one item) ---
	refMade := 0
	for i := 0; i < refundedN; i++ {
		p := pickProduct(seq)
		country := countries[seq%len(countries)]
		price := s.orderPrice(p, seq)
		email := custEmail()
		seq++
		v0, v1 := p.variants[0], p.variants[1]
		uuid, used, err := s.anCreateOrder(ctx, []*common.CustomOrderItemInsert{
			{Quantity: 1, VariantId: int64(v0.VariantID), CustomPrice: price},
			{Quantity: 1, VariantId: int64(v1.VariantID), CustomPrice: price},
		}, country, email)
		if err != nil {
			r.warn(s, "refund order %d (%s): %v", i, p.label, err)
			continue
		}
		// Resolve the item id for the second variant's size, then track -> deliver -> refund it.
		ob, err := s.C.GetOrderByUUID(ctx, &admin.GetOrderByUUIDRequest{OrderUuid: uuid})
		if err != nil {
			r.warn(s, "refund GetOrderByUUID(%s): %v", uuid, err)
			continue
		}
		var refundItemID int32
		for _, it := range ob.GetOrder().GetOrderItems() {
			if it.GetSizeNameSnapshot() == v1.SizeName {
				refundItemID = it.GetId()
				break
			}
		}
		if refundItemID == 0 {
			r.warn(s, "refund order %s: no item for size %s", uuid, v1.SizeName)
			continue
		}
		if _, err := s.C.SetTrackingNumber(ctx, &admin.SetTrackingNumberRequest{OrderUuid: uuid, TrackingCode: fmt.Sprintf("AN-%s-ref-%03d", s.Run, i)}); err != nil {
			r.warn(s, "refund SetTrackingNumber(%s): %v", uuid, err)
			continue
		}
		if _, err := s.C.DeliveredOrder(ctx, &admin.DeliveredOrderRequest{OrderUuid: uuid}); err != nil {
			r.warn(s, "refund DeliveredOrder(%s): %v", uuid, err)
			continue
		}
		if _, err := s.C.RefundOrder(ctx, &admin.RefundOrderRequest{
			OrderUuid: uuid, OrderItemIds: []int32{refundItemID},
			Reason: "beta seed: partial return of one item", RefundShipping: false,
			ReasonCode: admin.RefundReason_REFUND_REASON_CHANGED_MIND,
		}); err != nil {
			r.warn(s, "RefundOrder(%s): %v", uuid, err)
			continue
		}
		r.OrdersPartiallyRefunded++
		r.CountriesUsed[used]++
		if p.costPriced {
			r.OrdersOnCostPriced++
		}
		refMade++
	}
	s.logf("  partially_refunded: %d/%d created", refMade, refundedN)

	// --- storefront AwaitingPayment (never counted as revenue — status-mix only) ---
	skuM, skuL := s.storefrontSkus(pool, plmProd)
	awMade := 0
	for i := 0; i < awaitingN; i++ {
		st := &plmState{carrier: s.carrierID(), cw1VarMSku: skuM, cw1VarLSku: skuL}
		uuid, status, err := s.submitStorefrontOrder(ctx, st)
		if err != nil {
			r.warn(s, "storefront awaiting order %d: %v", i, err)
			continue
		}
		_ = status
		_ = uuid
		r.OrdersAwaitingPayment++
		awMade++
	}
	s.logf("  awaiting_payment (storefront): %d/%d created", awMade, awaitingN)

	// --- storefront Cancelled (born AwaitingPayment/Placed, then admin-cancel) ---
	cxMade := 0
	for i := 0; i < cancelledN; i++ {
		st := &plmState{carrier: s.carrierID(), cw1VarMSku: skuM, cw1VarLSku: skuL}
		uuid, _, err := s.submitStorefrontOrder(ctx, st)
		if err != nil {
			r.warn(s, "storefront cancel order %d: %v", i, err)
			continue
		}
		if _, err := s.C.CancelOrder(ctx, &admin.CancelOrderRequest{OrderUuid: uuid}); err != nil {
			r.warn(s, "CancelOrder(%s): %v", uuid, err)
			continue
		}
		r.OrdersCancelled++
		cxMade++
	}
	s.logf("  cancelled (storefront): %d/%d created", cxMade, cancelledN)

	r.OrdersTotal = r.OrdersDelivered + r.OrdersShipped + r.OrdersConfirmed +
		r.OrdersPartiallyRefunded + r.OrdersAwaitingPayment + r.OrdersCancelled
	s.logf("  VOLUME TOTAL: %d orders (delivered=%d shipped=%d confirmed=%d partial_refund=%d awaiting=%d cancelled=%d; cost-priced=%d)",
		r.OrdersTotal, r.OrdersDelivered, r.OrdersShipped, r.OrdersConfirmed,
		r.OrdersPartiallyRefunded, r.OrdersAwaitingPayment, r.OrdersCancelled, r.OrdersOnCostPriced)

	netRevenueOrders := r.OrdersDelivered + r.OrdersShipped + r.OrdersConfirmed + r.OrdersPartiallyRefunded
	if netRevenueOrders == 0 {
		return fmt.Errorf("no net-revenue orders created (delivered/shipped/confirmed/partial-refund all 0)")
	}
	return nil
}

// anCreateOrder places one admin custom order (bank_invoice -> born Confirmed). It
// tries the requested destination country and, if the carrier does not serve that
// region, falls back to DE so the order (and its revenue) still lands. Returns the
// order uuid and the country actually used.
func (s *Seeder) anCreateOrder(ctx context.Context, items []*common.CustomOrderItemInsert, country, email string) (uuid, used string, err error) {
	mk := func(cc string) (string, error) {
		resp, e := s.C.CreateCustomOrder(ctx, &admin.CreateCustomOrderRequest{
			Items:             items,
			ShippingAddress:   seedAddrIn(cc),
			BillingAddress:    seedAddrIn(cc),
			Buyer:             &common.BuyerInsert{FirstName: "Ana", LastName: "Lytics", Email: email, Phone: "+49301230000"},
			PaymentMethod:     common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE,
			ShipmentCarrierId: s.carrierID(),
			Currency:          "eur",
		})
		if e != nil {
			return "", e
		}
		u := resp.GetOrder().GetUuid()
		if u == "" {
			return "", fmt.Errorf("CreateCustomOrder returned empty uuid")
		}
		return u, nil
	}
	u, e := mk(country)
	if e != nil {
		if country != "DE" {
			if u2, e2 := mk("DE"); e2 == nil {
				return u2, "DE", nil
			}
		}
		return "", "", e
	}
	return u, country, nil
}

// orderPrice returns a per-item custom price. Cost-priced (PLM) orders carry a fixed
// price comfortably above cost so MARGIN_BY_STYLE shows positive margin; catalog orders
// vary 100..180 for AOV / order-value-band spread.
func (s *Seeder) orderPrice(p analyticsProduct, seq int) *decimal.Decimal {
	if p.costPriced {
		return decv("150.00")
	}
	return decv(fmt.Sprintf("%d.00", 100+(seq%9)*10))
}

// orderCountries returns the destination countries to spread orders across, filtered to
// the regions the seed carrier actually serves (DE always included as a safe fallback).
func (s *Seeder) orderCountries() []string {
	type cand struct {
		code   string
		region common.ShippingRegion
	}
	cands := []cand{
		{"DE", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"FR", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"IT", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"ES", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"NL", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"PL", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"AT", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"BE", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"SE", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"GB", common.ShippingRegion_SHIPPING_REGION_EUROPE},
		{"US", common.ShippingRegion_SHIPPING_REGION_AMERICAS},
		{"JP", common.ShippingRegion_SHIPPING_REGION_ASIA_PACIFIC},
	}
	// Resolve the seed carrier's allowed regions (empty => serves all).
	allowed := map[common.ShippingRegion]bool{}
	cid := s.carrierID()
	for _, c := range s.Dict.GetShipmentCarriers() {
		if c.GetId() == cid {
			for _, rg := range c.GetAllowedRegions() {
				allowed[rg] = true
			}
			break
		}
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if len(allowed) == 0 || allowed[c.region] {
			out = append(out, c.code)
		}
	}
	if len(out) == 0 {
		out = []string{"DE"}
	}
	return out
}

// storefrontSkus returns two public variant SKUs for storefront orders, preferring the
// PLM style, else the first catalog product.
func (s *Seeder) storefrontSkus(pool []analyticsProduct, plmProd *analyticsProduct) (skuM, skuL string) {
	src := pool[0]
	if plmProd != nil {
		src = *plmProd
	}
	return src.variants[0].Sku, src.variants[1].Sku
}

// seedAddrIn is seedAddress with a destination-country override (geography joins on the
// shipping address country). Only Country/AddressLineOne/PostalCode are validated.
func seedAddrIn(country string) *common.AddressInsert {
	if country == "DE" {
		return seedAddress()
	}
	return &common.AddressInsert{
		Country:        country,
		State:          country,
		City:           country + " City",
		AddressLineOne: "1 Seed Street",
		PostalCode:     "10115",
	}
}

// ================================================================= Part 3: fulfillment

func (s *Seeder) seedFulfillment(ctx context.Context, pool []analyticsProduct, plmProd *analyticsProduct, r *AnalyticsResult) {
	// Create dedicated Confirmed custom orders so the kanban has fresh TO_FULFILL cards.
	n := s.scaleN(2, 4, 6)
	var fresh []string
	countries := s.orderCountries()
	for i := 0; i < n; i++ {
		p := pool[i%len(pool)]
		if plmProd != nil && i%3 == 2 {
			p = *plmProd
		}
		v := p.variants[i%len(p.variants)]
		uuid, _, err := s.anCreateOrder(ctx, []*common.CustomOrderItemInsert{
			{Quantity: 1, VariantId: int64(v.VariantID), CustomPrice: s.orderPrice(p, i)},
		}, countries[i%len(countries)], fmt.Sprintf("an-fulfil-%s-%02d@grbpwr.com", s.Run, i))
		if err != nil {
			r.warn(s, "fulfillment order %d: %v", i, err)
			continue
		}
		fresh = append(fresh, uuid)
		r.OrdersConfirmed++
		r.OrdersTotal++
	}
	if len(fresh) == 0 {
		r.warn(s, "fulfillment: no TO_FULFILL cards created; skipping board")
		return
	}

	// Resolve an assignee (own admin username; else first admin).
	assignee := ""
	if acc, err := s.C.GetCurrentAccount(ctx, &admin.GetCurrentAccountRequest{}); err == nil {
		assignee = acc.GetAccount().GetUsername()
	}
	if assignee == "" {
		if la, err := s.C.ListAdmins(ctx, &admin.ListAdminsRequest{}); err == nil && len(la.GetAdmins()) > 0 {
			assignee = la.GetAdmins()[0].GetUsername()
		}
	}

	// Read the board and confirm our fresh orders are in TO_FULFILL.
	board, err := s.C.GetFulfillmentBoard(ctx, &admin.GetFulfillmentBoardRequest{DeliveredLimit: 10})
	if err != nil {
		r.warn(s, "GetFulfillmentBoard: %v", err)
	} else {
		toFulfill := 0
		for _, col := range board.GetColumns() {
			if col.GetColumn() == common.FulfillmentColumn_FULFILLMENT_COLUMN_TO_FULFILL {
				toFulfill = len(col.GetCards())
			}
		}
		s.logf("  fulfillment board: TO_FULFILL has %d cards", toFulfill)
	}

	// Annotate every fresh card; ship+deliver a couple of them through the kanban RPCs.
	shipDeliverN := s.scaleN(1, 2, 2)
	for i, uuid := range fresh {
		if assignee != "" {
			if _, err := s.C.SetFulfillmentAssignee(ctx, &admin.SetFulfillmentAssigneeRequest{OrderUuid: uuid, Assignee: assignee}); err != nil {
				r.warn(s, "SetFulfillmentAssignee(%s): %v", uuid, err)
			}
		}
		if _, err := s.C.SetFulfillmentNotes(ctx, &admin.SetFulfillmentNotesRequest{OrderUuid: uuid, Notes: "beta seed: pick, pack, quality-check before shipping"}); err != nil {
			r.warn(s, "SetFulfillmentNotes(%s): %v", uuid, err)
		}
		if ci, err := s.C.AddFulfillmentChecklistItem(ctx, &admin.AddFulfillmentChecklistItemRequest{OrderUuid: uuid, Content: "Pick items from shelf"}); err != nil {
			r.warn(s, "AddFulfillmentChecklistItem(%s): %v", uuid, err)
		} else if _, err := s.C.SetFulfillmentChecklistItemDone(ctx, &admin.SetFulfillmentChecklistItemDoneRequest{Id: ci.GetId(), IsDone: true}); err != nil {
			r.warn(s, "SetFulfillmentChecklistItemDone(%d): %v", ci.GetId(), err)
		}
		r.FulfillmentCardsTouched++

		if i < shipDeliverN {
			s.attemptCarrierAPI(ctx, uuid, r)
			if _, err := s.C.ShipFulfillmentOrder(ctx, &admin.ShipFulfillmentOrderRequest{OrderUuid: uuid, TrackingCode: fmt.Sprintf("AN-FUL-%s-%02d", s.Run, i)}); err != nil {
				r.warn(s, "ShipFulfillmentOrder(%s): %v", uuid, err)
				continue
			}
			r.FulfillmentShipped++
			// This order left the Confirmed pool.
			if r.OrdersConfirmed > 0 {
				r.OrdersConfirmed--
			}
			r.OrdersShipped++
			if _, err := s.C.MarkFulfillmentDelivered(ctx, &admin.MarkFulfillmentDeliveredRequest{OrderUuid: uuid}); err != nil {
				r.warn(s, "MarkFulfillmentDelivered(%s): %v", uuid, err)
				continue
			}
			r.FulfillmentDelivered++
			if r.OrdersShipped > 0 {
				r.OrdersShipped--
			}
			r.OrdersDelivered++
		}
	}
	s.logf("  fulfillment: %d cards annotated, %d shipped, %d delivered via kanban",
		r.FulfillmentCardsTouched, r.FulfillmentShipped, r.FulfillmentDelivered)
}

// attemptCarrierAPI exercises the Sendcloud-backed label/pickup RPCs as BEST-EFFORT.
// On beta these usually require external carrier credentials that are absent, so a
// failure is recorded as a documented skip (WARN) and never fails the phase.
func (s *Seeder) attemptCarrierAPI(ctx context.Context, uuid string, r *AnalyticsResult) {
	labelsEnabled := false
	if resp, err := s.C.PrepareShippingLabel(ctx, &admin.PrepareShippingLabelRequest{OrderUuid: uuid}); err != nil {
		r.ShippingBestEffort["PrepareShippingLabel"] = "skip: " + err.Error()
	} else {
		labelsEnabled = resp.GetLabelsEnabled()
		r.ShippingBestEffort["PrepareShippingLabel"] = fmt.Sprintf("ok (labels_enabled=%v complete=%v carrier=%q)", resp.GetLabelsEnabled(), resp.GetComplete(), resp.GetCarrierName())
	}
	parcel := &admin.ShippingParcel{WeightGrams: 800, LengthCm: 30, WidthCm: 25, HeightCm: 8}
	if _, err := s.C.GetShippingOptions(ctx, &admin.GetShippingOptionsRequest{OrderUuid: uuid, Parcel: parcel}); err != nil {
		r.ShippingBestEffort["GetShippingOptions"] = "skip: " + err.Error()
	} else {
		r.ShippingBestEffort["GetShippingOptions"] = "ok"
	}
	if !labelsEnabled {
		r.ShippingBestEffort["GenerateShippingLabel"] = "skip: labels not enabled on beta (no carrier credentials)"
		r.ShippingBestEffort["SchedulePickup"] = "skip: labels not enabled on beta (no carrier credentials)"
		return
	}
	if _, err := s.C.GenerateShippingLabel(ctx, &admin.GenerateShippingLabelRequest{OrderUuid: uuid, Parcel: parcel}); err != nil {
		r.ShippingBestEffort["GenerateShippingLabel"] = "skip: " + err.Error()
	} else {
		r.ShippingBestEffort["GenerateShippingLabel"] = "ok"
	}
	if _, err := s.C.SchedulePickup(ctx, &admin.SchedulePickupRequest{CarrierCode: "dhl", Date: time.Now().AddDate(0, 0, 1).Format("2006-01-02"), Quantity: 1}); err != nil {
		r.ShippingBestEffort["SchedulePickup"] = "skip: " + err.Error()
	} else {
		r.ShippingBestEffort["SchedulePickup"] = "ok"
	}
}

// ================================================================= Part 4: acceptance

func (s *Seeder) proveAnalytics(ctx context.Context, r *AnalyticsResult) error {
	// Period covering now: end = tomorrow, look back 30 days. Every REST order is
	// placed=now, so the current window is where the revenue lives.
	endAt := timestamppb.New(time.Now().AddDate(0, 0, 1))
	sections := []admin.MetricsSection{
		admin.MetricsSection_METRICS_SECTION_BUSINESS,
		admin.MetricsSection_METRICS_SECTION_MARGIN_BY_STYLE,
		admin.MetricsSection_METRICS_SECTION_COGS_STRUCTURE,
		admin.MetricsSection_METRICS_SECTION_GEOGRAPHY,
		admin.MetricsSection_METRICS_SECTION_DELIVERY,
		admin.MetricsSection_METRICS_SECTION_RETURN_ANALYSIS,
		admin.MetricsSection_METRICS_SECTION_REVENUE_PARETO,
		admin.MetricsSection_METRICS_SECTION_RFM,
		admin.MetricsSection_METRICS_SECTION_PROFITABILITY,
		admin.MetricsSection_METRICS_SECTION_INVENTORY_HEALTH,
	}
	// NOTE: the generated c.GetMetrics wrapper cannot carry the repeated `sections`
	// query param — the client's queryFromMessage only encodes scalar fields and silently
	// drops slices, so the server sees an empty sections list and computes BUSINESS only.
	// We therefore issue the GET ourselves with sections encoded correctly (verified the
	// server returns every section fully populated for this data). See getMetricsSections.
	resp, err := s.getMetricsSections(ctx, "30d", endAt, sections, 50)
	if err != nil {
		return fmt.Errorf("GetMetrics: %w", err)
	}

	// Headline commerce figures from the BUSINESS section.
	commerce := resp.GetBusiness().GetCommerce()
	r.AssertedRevenue = decFloat(commerce.GetRevenue().GetValue())
	r.AssertedOrders = int32(decFloat(commerce.GetOrdersCount().GetValue()))

	// Section-populated map (per-section non-empty predicate).
	pop := r.SectionPopulated
	pop["BUSINESS"] = r.AssertedRevenue > 0
	pop["MARGIN_BY_STYLE"] = len(resp.GetMarginByStyle()) > 0
	pop["COGS_STRUCTURE"] = len(resp.GetCogsStructure()) > 0
	pop["GEOGRAPHY"] = resp.GetGeography() != nil && len(resp.GetGeography().GetByCountry()) > 0
	pop["DELIVERY"] = resp.GetDelivery() != nil && (resp.GetDelivery().GetDeliveredSample() > 0 || resp.GetDelivery().GetShippedSample() > 0)
	pop["RETURN_ANALYSIS"] = len(resp.GetReturnByProduct()) > 0 || len(resp.GetReturnBySize()) > 0
	pop["REVENUE_PARETO"] = len(resp.GetRevenuePareto()) > 0
	pop["RFM"] = len(resp.GetRfmAnalysis()) > 0
	pop["PROFITABILITY"] = resp.GetProfitability() != nil && resp.GetProfitability().GetGrossMargin() != nil
	pop["INVENTORY_HEALTH"] = len(resp.GetInventoryHealth()) > 0

	// GetDashboard cross-check (authoritative headline revenue + orders).
	dash, err := s.C.GetDashboard(ctx, &admin.GetDashboardRequest{Period: "30d", EndAt: endAt, Limit: 20})
	if err != nil {
		return fmt.Errorf("GetDashboard: %w", err)
	}
	r.DashboardRevenue = decFloat(dash.GetRevenue())
	r.DashboardOrders = dash.GetOrders()

	// Print the acceptance table.
	s.logf("  ---- ACCEPTANCE ----")
	s.logf("  GetMetrics.BUSINESS: revenue=%.2f EUR, net-revenue orders=%d", r.AssertedRevenue, r.AssertedOrders)
	s.logf("  GetDashboard: revenue=%.2f EUR, orders=%d, gross_margin=%s, opex_total=%s, marketing_spend=%s, alerts=%d",
		r.DashboardRevenue, r.DashboardOrders,
		valOrNA(dash.GetGrossMargin().GetValue()), valOrNA(dash.GetOpexTotal().GetValue()),
		valOrNA(dash.GetMarketingSpend().GetValue()), len(dash.GetAlerts()))
	s.logf("  section populated (requested):")
	for _, name := range []string{"BUSINESS", "MARGIN_BY_STYLE", "COGS_STRUCTURE", "GEOGRAPHY", "DELIVERY", "RETURN_ANALYSIS", "REVENUE_PARETO", "RFM", "PROFITABILITY", "INVENTORY_HEALTH"} {
		mark := "EMPTY"
		if pop[name] {
			mark = "non-empty"
		}
		s.logf("    [%-9s] %s", mark, name)
	}

	// The gate: real revenue + at least one net-revenue order must be present.
	revenue := r.AssertedRevenue
	if revenue <= 0 {
		revenue = r.DashboardRevenue
	}
	orders := r.AssertedOrders
	if orders <= 0 {
		orders = r.DashboardOrders
	}
	if revenue <= 0 || orders <= 0 {
		return fmt.Errorf("acceptance FAILED: revenue=%.2f orders=%d (expected both > 0; metrics count only Confirmed/Shipped/Delivered/partially_refunded with placed in period)", revenue, orders)
	}
	s.logf("  ACCEPTANCE PASSED: revenue=%.2f EUR, orders=%d", revenue, orders)
	return nil
}

// getMetricsSections issues GET /api/admin/metrics with the repeated `sections` query
// param encoded correctly, then parses the response with the same protojson options the
// client uses. It exists because the generated c.GetMetrics wrapper routes through
// queryFromMessage, which only serialises scalar query fields and silently drops the
// repeated `sections` slice — so the gateway receives no sections and the server falls
// back to BUSINESS-only. This helper reuses the Client's own base/hc/token (same package)
// and touches no foundation file. The gateway accepts the full enum value names
// (e.g. "METRICS_SECTION_GEOGRAPHY"), which is exactly MetricsSection.String().
func (s *Seeder) getMetricsSections(ctx context.Context, period string, endAt *timestamppb.Timestamp, sections []admin.MetricsSection, limit int32) (*admin.GetMetricsResponse, error) {
	q := url.Values{}
	q.Set("period", period)
	if endAt != nil {
		q.Set("endAt", endAt.AsTime().UTC().Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(int(limit)))
	}
	for _, sec := range sections {
		q.Add("sections", sec.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.C.base+"/api/admin/metrics?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if tok := s.C.token; tok != "" {
		req.Header.Set(authHeader, "Bearer "+tok)
	}
	resp, err := s.C.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /api/admin/metrics: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Method: "GET", Path: "/api/admin/metrics", Code: resp.StatusCode, Body: string(rb)}
	}
	var out admin.GetMetricsResponse
	if err := jsonUnmarshal.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("unmarshal metrics response: %w", err)
	}
	return &out, nil
}
