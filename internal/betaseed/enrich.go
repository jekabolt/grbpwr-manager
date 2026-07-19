package betaseed

import (
	"context"
	"encoding/base64"
	"fmt"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// EnrichResult counts the additional REST-seedable coverage the enrichment phase adds on top of the
// core phases: rows that light up otherwise-empty admin screens (collections, tech-card dev expenses,
// order comments, colourway customs, material write-offs, the back-in-stock waitlist, newsletter
// unsubscribes, customer-initiated cancels) and the "archived / hidden / deleted / revoked" filter
// tabs across the dictionaries and registries. Everything here is plain REST — no backend change and
// no DB access — so it is safe to re-run and beta-only like the rest of the seeder.
//
// Deliberately NOT covered (documented gaps, not oversights): UpsertCostingFxRates (server returns
// Unimplemented — FX is auto-maintained by the ECB fxsync worker) and RevokeHackerStatus (needs a
// member who already holds hacker status, which no REST path grants — only RevokeHackerInvite is
// seedable). The GA4/BigQuery analytics sections (funnel, abandoned-cart, notify-me-intent) are also
// unreachable over REST and are left to the ga4sync worker.
type EnrichResult struct {
	CollectionsCreated int
	DictArchived       int // archived rows across collection/color/tag/fiber
	DevExpenses        int
	OrderComments      int
	ShipmentCosts      int
	Customs            int
	MaterialWriteOffs  int
	MaterialsArchived  int
	ColorwaysHidden    int
	ColorwaysArchived  int
	TasksArchived      int
	MembersDeleted     int
	EmployeesArchived  int
	OpexRecurArchived  int
	HackerRevoked      int
	Waitlisted         int
	Subscribers        int
	Unsubscribed       int
	CustomerCancelled  int

	Warnings []string
}

func (r *EnrichResult) warn(s *Seeder, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	s.logf("  WARN " + msg)
}

// SeedEnrichment runs LAST (after catalog/plm/extras/analytics) and fills the remaining
// REST-seedable gaps. It is self-sufficient: it uses the catalog/plm results for freshly-created
// colourway/material/tech-card ids and resolves the rest (tasks, members, hacker invites, orders) via
// List RPCs against what beta already holds — so it does NOT depend on the extras phase running in the
// same invocation (which lets `--only=enrich` skip the rate-limited storefront reviews in extras).
// Dedicated throwaway rows are created where a state (archived/deleted) must not touch an in-use
// entity. Every step is tolerant: a soft failure (incl. a storefront 429) is logged and never aborts.
func (s *Seeder) SeedEnrichment(ctx context.Context, cat []CatalogResult, plm *PLMResult) (*EnrichResult, error) {
	r := &EnrichResult{}

	s.enrichCollections(ctx, r)
	s.enrichDictionaryArchives(ctx, r)
	s.enrichDevExpenses(ctx, r, cat, plm)
	s.enrichColorwayCustoms(ctx, r, cat)
	s.enrichColorwayLifecycle(ctx, r, cat)
	s.enrichOrders(ctx, r, plm)
	s.enrichMaterial(ctx, r, plm)
	s.enrichEmployeeOpexArchive(ctx, r)
	s.enrichTaskArchive(ctx, r)
	s.enrichMemberDelete(ctx, r)
	s.enrichHackerRevoke(ctx, r)
	// Place the customer-cancel order BEFORE the waitlist step zeroes a variant's stock — otherwise
	// both pick the first active variant and the cancel order can't reserve the (now zero) stock.
	s.enrichCustomerCancel(ctx, r, cat)
	s.enrichWaitlist(ctx, r, cat)
	s.enrichNewsletter(ctx, r)

	s.logf("  enrich: collections=%d dictArchived=%d devExpenses=%d customs=%d comments=%d shipCosts=%d "+
		"writeoffs=%d matArchived=%d cwHidden=%d cwArchived=%d tasksArchived=%d membersDeleted=%d "+
		"empArchived=%d opexArchived=%d hackerRevoked=%d waitlisted=%d subscribers=%d unsub=%d custCancel=%d",
		r.CollectionsCreated, r.DictArchived, r.DevExpenses, r.Customs, r.OrderComments, r.ShipmentCosts,
		r.MaterialWriteOffs, r.MaterialsArchived, r.ColorwaysHidden, r.ColorwaysArchived, r.TasksArchived,
		r.MembersDeleted, r.EmployeesArchived, r.OpexRecurArchived, r.HackerRevoked, r.Waitlisted,
		r.Subscribers, r.Unsubscribed, r.CustomerCancelled)
	return r, nil
}

// enrichCollections lights up the Collections dictionary screen (which the core seeder never touched)
// plus its "archived" filter: one collection stays active, a second is archived.
func (s *Seeder) enrichCollections(ctx context.Context, r *EnrichResult) {
	if _, err := s.C.CreateCollection(ctx, &admin.CreateCollectionRequest{Name: "Beta Seed Drop " + s.Run}); err != nil {
		if !isAlreadyExists(err) {
			r.warn(s, "CreateCollection(active): %v", err)
		}
	} else {
		r.CollectionsCreated++
	}
	arch, err := s.C.CreateCollection(ctx, &admin.CreateCollectionRequest{Name: "Beta Seed Retired " + s.Run})
	if err != nil {
		if !isAlreadyExists(err) {
			r.warn(s, "CreateCollection(archive): %v", err)
		}
		return
	}
	r.CollectionsCreated++
	id := arch.GetCollection().GetId()
	if _, err := s.C.ArchiveCollection(ctx, &admin.ArchiveCollectionRequest{Id: id}); err != nil {
		r.warn(s, "ArchiveCollection(%d): %v", id, err)
	} else {
		r.DictArchived++
	}
}

// enrichDictionaryArchives populates the "archived" tab of the colour / fibre / tag dictionaries using
// dedicated throwaway rows, so archiving never hits an in-use entry (FK RESTRICT).
func (s *Seeder) enrichDictionaryArchives(ctx context.Context, r *EnrichResult) {
	// Colour (keyed by code). On a re-run the throwaway is already archived — that is the goal state,
	// so treat "already archived" as success.
	if _, err := s.C.CreateColor(ctx, &admin.CreateColorRequest{Code: "ZSA", Name: "Seed Archived Colour", Hex: "#123456"}); err != nil && !isAlreadyExists(err) {
		r.warn(s, "CreateColor(throwaway): %v", err)
	}
	if _, err := s.C.ArchiveColor(ctx, &admin.ArchiveColorRequest{Code: "ZSA"}); err != nil && !isAlreadyArchived(err) {
		r.warn(s, "ArchiveColor(ZSA): %v", err)
	} else {
		r.DictArchived++
	}
	// Fibre (keyed by code).
	if _, err := s.C.CreateFiber(ctx, &admin.CreateFiberRequest{Code: "ZSF", Name: "Seed Archived Fibre"}); err != nil && !isAlreadyExists(err) {
		r.warn(s, "CreateFiber(throwaway): %v", err)
	}
	if _, err := s.C.ArchiveFiber(ctx, &admin.ArchiveFiberRequest{Code: "ZSF"}); err != nil && !isAlreadyArchived(err) {
		r.warn(s, "ArchiveFiber(ZSF): %v", err)
	} else {
		r.DictArchived++
	}
	// Tag (keyed by id — capture from create).
	tg, err := s.C.CreateTag(ctx, &admin.CreateTagRequest{Name: "seed-archived-" + s.Run})
	if err != nil {
		if !isAlreadyExists(err) {
			r.warn(s, "CreateTag(throwaway): %v", err)
		}
		return
	}
	tid := tg.GetTag().GetId()
	if _, err := s.C.ArchiveTag(ctx, &admin.ArchiveTagRequest{Id: tid}); err != nil {
		r.warn(s, "ArchiveTag(%d): %v", tid, err)
	} else {
		r.DictArchived++
	}
}

// enrichDevExpenses posts R&D dev-cost journal rows on a few tech cards, lighting up the dev-cost
// roll-up on Style Economics + the plan/fact split in GetStyleCostEstimate (currently zero).
func (s *Seeder) enrichDevExpenses(ctx context.Context, r *EnrichResult, cat []CatalogResult, plm *PLMResult) {
	kinds := []string{"sample", "materials", "labour"}
	var techCards []int32
	for i := 0; i < len(cat) && i < 3; i++ {
		if cat[i].StyleID > 0 {
			techCards = append(techCards, cat[i].StyleID)
		}
	}
	if plm != nil && plm.StyleID > 0 {
		techCards = append(techCards, plm.StyleID)
	}
	for i, tc := range techCards {
		if _, err := s.C.AddTechCardDevExpense(ctx, &admin.AddTechCardDevExpenseRequest{Expense: &common.TechCardDevExpenseInsert{
			TechCardId:  tc,
			Kind:        kinds[i%len(kinds)],
			Description: "beta seed dev expense",
			Amount:      decv(fmt.Sprintf("%d.00", 80+40*i)),
			Currency:    "EUR",
		}}); err != nil {
			r.warn(s, "AddTechCardDevExpense(tc=%d): %v", tc, err)
			continue
		}
		r.DevExpenses++
	}
}

// enrichColorwayCustoms fills the customs tab (HS code + description) on a few colourways, so the
// international label build has data.
func (s *Seeder) enrichColorwayCustoms(ctx context.Context, r *EnrichResult, cat []CatalogResult) {
	for i := 0; i < len(cat) && i < 4; i++ {
		if cat[i].ColorwayID <= 0 {
			continue
		}
		if _, err := s.C.SetColorwayCustoms(ctx, &admin.SetColorwayCustomsRequest{
			ColorwayId: cat[i].ColorwayID,
			Customs:    &admin.ColorwayCustoms{HsCode: "6109100010", CustomsDescription: "cotton knitted garment"},
		}); err != nil {
			r.warn(s, "SetColorwayCustoms(cw=%d): %v", cat[i].ColorwayID, err)
			continue
		}
		r.Customs++
	}
}

// enrichColorwayLifecycle moves one published colourway to HIDDEN and another to ARCHIVED so those
// status filters in the catalogue are not empty. It takes them from the END of the catalogue (least
// likely to be a hero/showcase colourway) and only touches ACTIVE ones.
func (s *Seeder) enrichColorwayLifecycle(ctx context.Context, r *EnrichResult, cat []CatalogResult) {
	var active []int32
	for _, c := range cat {
		if c.ColorwayID > 0 && c.Status == "COLORWAY_LIFECYCLE_STATUS_ACTIVE" {
			active = append(active, c.ColorwayID)
		}
	}
	if len(active) < 2 {
		return
	}
	hideID := active[len(active)-1]
	archID := active[len(active)-2]
	if _, err := s.C.TransitionColorwayStatus(ctx, &admin.TransitionColorwayStatusRequest{
		ColorwayId: hideID, Target: common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_HIDDEN,
	}); err != nil {
		r.warn(s, "TransitionColorwayStatus HIDDEN(cw=%d): %v", hideID, err)
	} else {
		r.ColorwaysHidden++
	}
	if _, err := s.C.ArchiveColorwayByID(ctx, &admin.ArchiveColorwayByIDRequest{ColorwayId: archID}); err != nil {
		r.warn(s, "ArchiveColorwayByID(cw=%d): %v", archID, err)
	} else {
		r.ColorwaysArchived++
	}
}

// enrichOrders adds an internal comment + a real carrier-invoice logistics cost to a handful of
// delivered orders: fills the order-detail timeline and makes contribution-margin logistics non-zero.
func (s *Seeder) enrichOrders(ctx context.Context, r *EnrichResult, plm *PLMResult) {
	var uuids []string
	if od, err := s.C.ListOrders(ctx, &admin.ListOrdersRequest{Status: common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED, Limit: 8}); err == nil {
		for _, o := range od.GetOrders() {
			if u := o.GetUuid(); u != "" {
				uuids = append(uuids, u)
			}
		}
	} else {
		r.warn(s, "ListOrders(delivered): %v", err)
	}
	if plm != nil && plm.OrderBUUID != "" {
		uuids = append(uuids, plm.OrderBUUID)
	}
	seen := map[string]bool{}
	n := 0
	for _, u := range uuids {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if n >= 5 {
			break
		}
		n++
		if _, err := s.C.AddOrderComment(ctx, &admin.AddOrderCommentRequest{OrderUuid: u, Comment: "beta seed: address verified, packed & quality-checked"}); err != nil {
			r.warn(s, "AddOrderComment(%s): %v", u, err)
		} else {
			r.OrderComments++
		}
		if _, err := s.C.SetShipmentActualCost(ctx, &admin.SetShipmentActualCostRequest{OrderUuid: u, ActualCost: decv("14.50")}); err != nil {
			r.warn(s, "SetShipmentActualCost(%s): %v", u, err)
		} else {
			r.ShipmentCosts++
		}
	}
}

// enrichMaterial establishes a stock balance, writes some off (a damage write-off ledger row +
// non-zero write-off value in inventory valuation), and archives a spare material (archive-not-delete)
// to light up the "archived materials" filter.
func (s *Seeder) enrichMaterial(ctx context.Context, r *EnrichResult, plm *PLMResult) {
	if plm == nil {
		return
	}
	if mid := plm.MaterialIDs.Fabric; mid > 0 {
		// Set an opening balance so the subsequent write-off can't drive on-hand negative.
		if _, err := s.C.AdjustMaterialStock(ctx, &admin.AdjustMaterialStockRequest{
			MaterialId: int32(mid), Mode: "set", Quantity: decv("50"), Reason: "stock_count", Comment: "beta seed opening balance",
		}); err != nil {
			r.warn(s, "AdjustMaterialStock set(mat=%d): %v", mid, err)
		}
		if _, err := s.C.AdjustMaterialStock(ctx, &admin.AdjustMaterialStockRequest{
			MaterialId: int32(mid), Mode: "writeoff", Quantity: decv("3"), Reason: "damage", Comment: "beta seed damage write-off",
		}); err != nil {
			r.warn(s, "AdjustMaterialStock writeoff(mat=%d): %v", mid, err)
		} else {
			r.MaterialWriteOffs++
		}
	}
	if amid := plm.MaterialIDs.DustBag; amid > 0 {
		if _, err := s.C.ArchiveMaterial(ctx, &admin.ArchiveMaterialRequest{Id: amid, Archived: true}); err != nil {
			r.warn(s, "ArchiveMaterial(%d): %v", amid, err)
		} else {
			r.MaterialsArchived++
		}
	}
}

// enrichEmployeeOpexArchive creates a throwaway employee + recurring-salary template purely to
// populate the "archived employees" and "archived recurring OPEX" filters, then archives both.
func (s *Seeder) enrichEmployeeOpexArchive(ctx context.Context, r *EnrichResult) {
	emp, err := s.C.UpsertEmployee(ctx, &admin.UpsertEmployeeRequest{Employee: &admin.EmployeeInsert{
		FullName: "Seed Archived Employee " + s.Run, Role: "seasonal", EmploymentStart: "2025-01-01",
		DefaultCurrency: "EUR", DefaultMonthlyCost: decv("1500.00"), Note: "beta seed — archived showcase",
	}})
	if err != nil {
		r.warn(s, "UpsertEmployee(throwaway): %v", err)
		return
	}
	eid := emp.GetId()
	if rec, err := s.C.UpsertOpexRecurring(ctx, &admin.UpsertOpexRecurringRequest{Recurring: &admin.OpexRecurringInsert{
		Label: "seed-archived-salary-" + s.Run, Category: "salaries", Amount: decv("1500.00"),
		Currency: "EUR", ActiveFrom: "2025-01-01", Note: "beta seed — archived showcase", EmployeeId: eid,
	}}); err != nil {
		r.warn(s, "UpsertOpexRecurring(throwaway): %v", err)
	} else if _, err := s.C.ArchiveOpexRecurring(ctx, &admin.ArchiveOpexRecurringRequest{Id: rec.GetId()}); err != nil {
		r.warn(s, "ArchiveOpexRecurring(%d): %v", rec.GetId(), err)
	} else {
		r.OpexRecurArchived++
	}
	if _, err := s.C.ArchiveEmployee(ctx, &admin.ArchiveEmployeeRequest{Id: eid}); err != nil {
		r.warn(s, "ArchiveEmployee(%d): %v", eid, err)
	} else {
		r.EmployeesArchived++
	}
}

// enrichTaskArchive archives one existing kanban task (resolved via ListTasks) so the archived-tasks
// board view is not empty.
func (s *Seeder) enrichTaskArchive(ctx context.Context, r *EnrichResult) {
	lt, err := s.C.ListTasks(ctx, &admin.ListTasksRequest{Limit: 50})
	if err != nil {
		r.warn(s, "enrichTaskArchive ListTasks: %v", err)
		return
	}
	tasks := lt.GetTasks()
	if len(tasks) == 0 {
		return
	}
	id := tasks[len(tasks)-1].GetId()
	if _, err := s.C.ArchiveTask(ctx, &admin.ArchiveTaskRequest{Id: id}); err != nil {
		r.warn(s, "ArchiveTask(%d): %v", id, err)
	} else {
		r.TasksArchived++
	}
}

// enrichMemberDelete soft-deletes one existing member (resolved via ListMembers, skipping any already
// deleted) so the "deleted" members filter is not empty.
func (s *Seeder) enrichMemberDelete(ctx context.Context, r *EnrichResult) {
	lm, err := s.C.ListMembers(ctx, &admin.ListMembersRequest{Limit: 100})
	if err != nil {
		r.warn(s, "enrichMemberDelete ListMembers: %v", err)
		return
	}
	var id int64
	for _, m := range lm.GetMembers() {
		if m.GetUserId() > 0 && m.GetStatus() != "deleted" {
			id = m.GetUserId()
		}
	}
	if id == 0 {
		return
	}
	if _, err := s.C.SoftDeleteMember(ctx, &admin.SoftDeleteMemberRequest{UserId: id}); err != nil {
		r.warn(s, "SoftDeleteMember(%d): %v", id, err)
	} else {
		r.MembersDeleted++
	}
}

// enrichHackerRevoke revokes one active hacker invite (resolved via ListHackerInvites) so the
// revoked-invite state appears in the list. (RevokeHackerStatus is intentionally NOT seeded — no
// member holds hacker status; see the type doc.)
func (s *Seeder) enrichHackerRevoke(ctx context.Context, r *EnrichResult) {
	li, err := s.C.ListHackerInvites(ctx, &admin.ListHackerInvitesRequest{ActiveOnly: true})
	if err != nil {
		r.warn(s, "enrichHackerRevoke ListHackerInvites: %v", err)
		return
	}
	invites := li.GetInvites()
	if len(invites) == 0 {
		return
	}
	id := invites[len(invites)-1].GetId()
	if _, err := s.C.RevokeHackerInvite(ctx, &admin.RevokeHackerInviteRequest{InviteId: id}); err != nil {
		r.warn(s, "RevokeHackerInvite(%d): %v", id, err)
	} else {
		r.HackerRevoked++
	}
}

// enrichWaitlist drives a published variant's stock to zero (genuinely out of stock) and registers a
// few back-in-stock waitlist requests against its SKU — filling product_waitlist and making the admin
// restock → back-in-stock email flow demonstrable.
func (s *Seeder) enrichWaitlist(ctx context.Context, r *EnrichResult, cat []CatalogResult) {
	var sku string
	var vid int32
	for _, c := range cat {
		if c.Status != "COLORWAY_LIFECYCLE_STATUS_ACTIVE" {
			continue
		}
		for _, v := range c.Variants {
			if v.Sku != "" && v.VariantID > 0 {
				sku, vid = v.Sku, v.VariantID
				break
			}
		}
		if sku != "" {
			break
		}
	}
	if sku == "" {
		return
	}
	if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
		VariantId: int64(vid), Mode: common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET, Quantity: 0,
		Reason: common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
	}); err != nil {
		r.warn(s, "UpdateVariantStock zero(v=%d): %v", vid, err)
	}
	for i := 0; i < 3; i++ {
		email := fmt.Sprintf("seed-waitlist-%s-%02d@grbpwr.com", s.Run, i+1)
		if _, err := s.C.SFNotifyMe(ctx, &frontend.NotifyMeRequest{Email: email, VariantSku: sku}); err != nil {
			r.warn(s, "SFNotifyMe(%s): %v", email, err)
			continue
		}
		r.Waitlisted++
	}
}

// enrichNewsletter grows the subscriber base (the one email metric genuinely seedable over REST:
// BUSINESS.NewSubscribers + today's SubscribersByDay bar) and unsubscribes a quarter of them so the
// opt-in list is non-uniform.
func (s *Seeder) enrichNewsletter(ctx context.Context, r *EnrichResult) {
	// The public subscribe endpoint is rate-limited to 10/IP/10min (ip_subscribe), shared with
	// notify-me above, so keep the batch small — a soft 429 on the tail is tolerated and just warns.
	n := s.scaleN(3, 5, 6)
	var emails []string
	for i := 0; i < n; i++ {
		email := fmt.Sprintf("seed-news-%s-%03d@grbpwr.com", s.Run, i+1)
		if _, err := s.C.SFSubscribeNewsletter(ctx, &frontend.SubscribeNewsletterRequest{
			Email:                email,
			Name:                 fmt.Sprintf("Seed News %03d", i+1),
			ShoppingPreference:   frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_ALL,
			SubscribeNewsletter:  true,
			SubscribeNewArrivals: true,
			SubscribeEvents:      i%2 == 0,
		}); err != nil {
			r.warn(s, "SFSubscribeNewsletter(%s): %v", email, err)
			continue
		}
		r.Subscribers++
		emails = append(emails, email)
	}
	for i := 0; i < len(emails); i += 4 {
		if _, err := s.C.SFUnsubscribeNewsletter(ctx, &frontend.UnsubscribeNewsletterRequest{Email: emails[i]}); err != nil {
			r.warn(s, "SFUnsubscribeNewsletter(%s): %v", emails[i], err)
			continue
		}
		r.Unsubscribed++
	}
}

// enrichCustomerCancel exercises the customer-initiated cancel path (distinct from the admin
// CancelOrder the core seeder uses): it places a fresh storefront order (lands AWAITING_PAYMENT, so it
// is directly cancellable) with a known email, then cancels it as the shopper.
func (s *Seeder) enrichCustomerCancel(ctx context.Context, r *EnrichResult, cat []CatalogResult) {
	var sku string
	for _, c := range cat {
		if c.Status == "COLORWAY_LIFECYCLE_STATUS_ACTIVE" && len(c.Variants) > 0 && c.Variants[0].Sku != "" {
			sku = c.Variants[0].Sku
			break
		}
	}
	if sku == "" {
		return
	}
	carrier := s.carrierID()
	email := fmt.Sprintf("seed-cancel-%s@grbpwr.com", s.Run)
	uuid := ""
	for _, pm := range []common.PaymentMethodNameEnum{
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD,
	} {
		vr, err := s.C.SFValidateOrderItemsInsert(ctx, &frontend.ValidateOrderItemsInsertRequest{
			Items:             []*common.OrderItemInsert{{Quantity: 1, VariantSku: sku}},
			ShipmentCarrierId: carrier,
			Country:           "DE",
			PaymentMethod:     pm,
			Currency:          "eur",
		})
		if err != nil {
			s.logf("  [enrich cancel] validate(%s) failed: %v", pm.String(), err)
			continue
		}
		if vr.GetPaymentIntentId() == "" {
			s.logf("  [enrich cancel] validate(%s): no paymentIntentId", pm.String())
			continue
		}
		sr, err := s.C.SFSubmitOrder(ctx, &frontend.SubmitOrderRequest{
			Order: &common.OrderNew{
				Items:             []*common.OrderItemInsert{{Quantity: 1, VariantSku: sku}},
				ShippingAddress:   seedAddress(),
				BillingAddress:    seedAddress(),
				Buyer:             &common.BuyerInsert{FirstName: "Seed", LastName: "Cancel", Email: email, Phone: "+49301234567"},
				PaymentMethod:     pm,
				ShipmentCarrierId: carrier,
				Currency:          "eur",
			},
			PaymentIntentId: vr.GetPaymentIntentId(),
		})
		if err != nil {
			s.logf("  [enrich cancel] submit(%s) failed: %v", pm.String(), err)
			continue
		}
		uuid = sr.GetOrderUuid()
		break
	}
	if uuid == "" {
		r.warn(s, "customer-cancel: could not place an order to cancel")
		return
	}
	if _, err := s.C.SFCancelOrderByUser(ctx, &frontend.CancelOrderByUserRequest{
		OrderUuid: uuid,
		B64Email:  base64.StdEncoding.EncodeToString([]byte(email)),
		Reason:    "beta seed customer cancel",
	}); err != nil {
		r.warn(s, "SFCancelOrderByUser(%s): %v", uuid, err)
	} else {
		r.CustomerCancelled++
	}
}
