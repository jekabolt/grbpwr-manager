package acctposting

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Wave-2 delivered-recognition BRANCH ROUTING tests for internal/acctposting/outbox.go. These assert
// WHICH store calls happen (CreateJournalEntry with which source_type, MarkEventProcessed,
// MarkEventFailed with which reason, MarkEventNeedsReview) for a given posting state / cutover
// configuration — not the posted amounts, which are covered by internal/accounting's own unit tests.

// testOrderUUID / testStartDate / testCutover are the fixed identifiers/dates shared by every test.
const testOrderUUID = "order-uuid-1"

var (
	testStartDate = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	testCutover   = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

// newTestWorker builds a Worker with the mock repo and startDate/deliveredRecognitionFrom set (the
// feature armed, cutover = testCutover); callers that need the feature off zero deliveredRecognitionFrom.
func newTestWorker(repo *mocks.MockRepository) *Worker {
	return &Worker{
		repo:                     repo,
		c:                        &Config{OriginCountry: "PL", SettledWaitMax: defaultSettledWaitMax},
		startDate:                testStartDate,
		deliveredRecognitionFrom: testCutover,
	}
}

// newMocks wires a MockRepository whose Accounting() always returns the given MockAccounting (both
// on the pool and inside a Tx callback — Tx is stubbed to invoke its callback with the SAME repo mock,
// so rep.Accounting().CreateJournalEntry(...) inside postOrDefer's short Tx hits this same mock).
func newMocks(t *testing.T) (*mocks.MockRepository, *mocks.MockAccounting) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	acct := mocks.NewMockAccounting(t)
	repo.EXPECT().Accounting().Return(acct)
	// Tx is only reached on a clean-build post; a skip/defer/needs-review path never calls it, so this
	// expectation is optional (.Maybe()) rather than required by every test.
	repo.EXPECT().Tx(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
			return f(ctx, repo)
		},
	).Maybe()
	return repo, acct
}

// baseFacts is a minimal, clean order fact set that builds without error regardless of payment
// method: TotalSettledBase is always valid (grossEUR's top-priority branch), so isStripe/currency
// never matter for readiness. DestCountry is a non-EU destination with no buyer VAT id, which
// resolves to the export regime (no VAT), so no GetVatRatesFor stub is needed anywhere.
func baseFacts(method entity.PaymentMethodName) *entity.AcctOrderFacts {
	return &entity.AcctOrderFacts{
		Id:                1,
		UUID:              testOrderUUID,
		Placed:            testCutover,
		TotalPrice:        decimal.NewFromInt(100),
		Currency:          "EUR",
		TotalSettledBase:  decimal.NullDecimal{Decimal: decimal.NewFromInt(100), Valid: true},
		PaymentMethodName: method,
		DestCountry:       "US",
		VatRegime:         sql.NullString{String: string(entity.VatRegimeExport), Valid: true},
	}
}

// factsWithCostedItem is baseFacts plus one costed order line, so BuildOrderTransitEntry has
// something positive to move into 1140 instead of ErrSkipEmpty.
func factsWithCostedItem(method entity.PaymentMethodName) *entity.AcctOrderFacts {
	f := baseFacts(method)
	f.Items = []entity.AcctOrderItemFact{
		{Id: 1, ProductId: 10, Quantity: decimal.NewFromInt(2), UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(15), Valid: true}},
	}
	return f
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func paidEvent(t *testing.T, occurredAt time.Time) entity.AcctEvent {
	return entity.AcctEvent{
		Id:         1,
		EventType:  entity.AcctEventOrderPaid,
		SourceKey:  testOrderUUID,
		Payload:    mustJSON(t, entity.AcctOrderPaidPayload{OrderUUID: testOrderUUID}),
		OccurredAt: occurredAt,
	}
}

func shippedEvent(t *testing.T, attempts int) entity.AcctEvent {
	return entity.AcctEvent{
		Id:         2,
		EventType:  entity.AcctEventOrderShipped,
		SourceKey:  testOrderUUID,
		Payload:    mustJSON(t, entity.AcctOrderShippedPayload{OrderUUID: testOrderUUID}),
		OccurredAt: testCutover,
		Attempts:   attempts,
	}
}

func deliveredEvent(t *testing.T, attempts int) entity.AcctEvent {
	return entity.AcctEvent{
		Id:         3,
		EventType:  entity.AcctEventOrderDelivered,
		SourceKey:  testOrderUUID,
		Payload:    mustJSON(t, entity.AcctOrderDeliveredPayload{OrderUUID: testOrderUUID}),
		OccurredAt: testCutover,
		Attempts:   attempts,
	}
}

func refundEvent(t *testing.T, amount decimal.Decimal, attempts int) entity.AcctEvent {
	return entity.AcctEvent{
		Id:        4,
		EventType: entity.AcctEventOrderRefund,
		SourceKey: testOrderUUID + ":1",
		Payload: mustJSON(t, entity.AcctOrderRefundPayload{
			OrderUUID:      testOrderUUID,
			RefundAmount:   amount,
			OrderCurrency:  "EUR",
			RefundedByItem: map[int]int64{},
		}),
		OccurredAt: testCutover,
		Attempts:   attempts,
	}
}

// hasSourceType matches a CreateJournalEntry call by its entry's source_type — the routing signal
// these tests care about, not the posted amounts.
func hasSourceType(st entity.AcctSourceType) any {
	return mock.MatchedBy(func(e entity.AcctJournalEntryInsert) bool { return e.SourceType == st })
}

// ---- processOrderPaid ------------------------------------------------------------------------

func TestProcessOrderPaid_FeatureOff_UsesOldSale(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)
	w.deliveredRecognitionFrom = time.Time{} // feature off

	facts := baseFacts(entity.CARD)
	ev := paidEvent(t, testCutover.Add(24*time.Hour))

	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderSale)).Return(1, false, nil)
	acct.EXPECT().SetOrderVatRegime(mock.Anything, testOrderUUID, string(entity.VatRegimeExport)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderPaid(context.Background(), ev))
}

func TestProcessOrderPaid_PostCutoverStripe_UsesPrepayment(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	facts := baseFacts(entity.CARD)
	ev := paidEvent(t, testCutover.Add(24*time.Hour)) // paid after cutover

	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderPrepayment)).Return(1, false, nil)
	acct.EXPECT().SetOrderVatRegime(mock.Anything, testOrderUUID, string(entity.VatRegimeExport)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderPaid(context.Background(), ev))
}

func TestProcessOrderPaid_PreCutover_UsesOldSale(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	facts := baseFacts(entity.CARD)
	ev := paidEvent(t, testCutover.Add(-24*time.Hour)) // paid before cutover

	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderSale)).Return(1, false, nil)
	acct.EXPECT().SetOrderVatRegime(mock.Anything, testOrderUUID, string(entity.VatRegimeExport)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderPaid(context.Background(), ev))
}

func TestProcessOrderPaid_CashCustom_UsesOldSale(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	facts := baseFacts(entity.CASH)
	ev := paidEvent(t, testCutover.Add(24*time.Hour)) // post-cutover, but not a card payment

	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	// ResolveVatRegime special-cases entity.CASH to uk_stock_domestic (a VAT-bearing regime)
	// regardless of destination, so — unlike the CARD tests, which land on the no-VAT export regime —
	// this path does look up a rate (RegimeRateCountry(uk_stock_domestic) = GB, fixed).
	acct.EXPECT().GetVatRatesFor(mock.Anything, []string{"GB"}).Return(map[string]decimal.Decimal{"GB": decimal.NewFromInt(20)}, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderSale)).Return(1, false, nil)
	acct.EXPECT().SetOrderVatRegime(mock.Anything, testOrderUUID, string(entity.VatRegimeUKStockDomestic)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderPaid(context.Background(), ev))
}

func TestProcessOrderPaid_BoundaryEqualsCutover_UsesPrepayment(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	facts := baseFacts(entity.CARD)
	ev := paidEvent(t, testCutover) // paid AT the cutover instant

	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderPrepayment)).Return(1, false, nil)
	acct.EXPECT().SetOrderVatRegime(mock.Anything, testOrderUUID, string(entity.VatRegimeExport)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderPaid(context.Background(), ev))
}

// ---- processOrderShipped ---------------------------------------------------------------------

func TestProcessOrderShipped_PrePolicy_Skip(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := shippedEvent(t, 0)
	st := entity.AcctOrderPostingState{LegacySale: true, Prepayment: false}

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().MarkEventFailed(mock.Anything, ev.Id, "pre-policy order", time.Duration(0)).Return(nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)
	// No CreateJournalEntry expectation: the mock panics/fails the test if it is called unexpectedly.

	require.NoError(t, w.processOrderShipped(context.Background(), ev))
}

func TestProcessOrderShipped_NewChain_PostsTransit(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := shippedEvent(t, 0)
	st := entity.AcctOrderPostingState{Prepayment: true}
	facts := factsWithCostedItem(entity.CARD)

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderTransit)).Return(1, false, nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)
	// No SetOrderVatRegime: processOrderShipped's postOrDefer call passes vatRegime="".

	require.NoError(t, w.processOrderShipped(context.Background(), ev))
}

func TestProcessOrderShipped_AwaitingPrepayment_Defers(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := shippedEvent(t, 0) // attempts below maxOrphanRefundAttempts
	st := entity.AcctOrderPostingState{}

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().MarkEventFailed(mock.Anything, ev.Id, "awaiting prepayment posting", settledRetryInterval).Return(nil)
	// No GetOrderFactsForPosting / CreateJournalEntry / MarkEventProcessed.

	require.NoError(t, w.processOrderShipped(context.Background(), ev))
}

// ---- processOrderDelivered -------------------------------------------------------------------

func TestProcessOrderDelivered_WithTransit_PostsDeliveredSale(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := deliveredEvent(t, 0)
	st := entity.AcctOrderPostingState{
		Prepayment:    true,
		Transit:       true,
		Remaining2090: decimal.NewFromInt(80),
		Remaining1140: decimal.NewFromInt(30),
	}
	facts := baseFacts(entity.CARD)

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderDeliveredSale)).Return(1, false, nil).Once()
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderDelivered(context.Background(), ev))
}

func TestProcessOrderDelivered_DirectFromConfirmed_SynthesizesTransit(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := deliveredEvent(t, 0)
	st := entity.AcctOrderPostingState{
		Prepayment:    true,
		Transit:       false, // no shipped event was ever posted — Confirmed -> Delivered direct
		Remaining2090: decimal.NewFromInt(80),
	}
	facts := factsWithCostedItem(entity.CARD)

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)

	transitCall := acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderTransit)).
		Return(1, false, nil).Once()
	deliveredCall := acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderDeliveredSale)).
		Return(2, false, nil).Once()
	mock.InOrder(transitCall, deliveredCall)

	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderDelivered(context.Background(), ev))
}

// ---- processOrderRefund ----------------------------------------------------------------------

func TestProcessOrderRefund_Legacy_UsesS2(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := refundEvent(t, decimal.NewFromInt(20), 0)
	st := entity.AcctOrderPostingState{LegacySale: true}
	facts := baseFacts(entity.CARD)

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	// BuildOrderRefundEntry (S2) path: posts order_refund with a 4040 contra-revenue line (not
	// asserted here — the builders are unit-tested separately; this test only asserts routing).
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderRefund)).Return(1, false, nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderRefund(context.Background(), ev))
}

func TestProcessOrderRefund_PreDelivered_UnwindsPrepayment(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := refundEvent(t, decimal.NewFromInt(20), 0)
	st := entity.AcctOrderPostingState{Prepayment: true} // no DeliveredSale, no DeliveredEvent, not shipped
	facts := baseFacts(entity.CARD)

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().GetOrderFactsForPosting(mock.Anything, testOrderUUID).Return(facts, nil)
	// BuildOrderPreDeliveredRefundEntry path: unwinds 2090, still source_type order_refund.
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceType(entity.AcctSourceOrderRefund)).Return(1, false, nil)
	acct.EXPECT().MarkEventProcessed(mock.Anything, ev.Id).Return(nil)

	require.NoError(t, w.processOrderRefund(context.Background(), ev))
}

func TestProcessOrderRefund_DeliveredEventPending_Defers(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := refundEvent(t, decimal.NewFromInt(20), 0)
	st := entity.AcctOrderPostingState{Prepayment: true, DeliveredSale: false, DeliveredEvent: true}

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().MarkEventFailed(mock.Anything, ev.Id, "awaiting delivered sale posting", settledRetryInterval).Return(nil)
	// No GetOrderFactsForPosting / CreateJournalEntry / MarkEventProcessed.

	require.NoError(t, w.processOrderRefund(context.Background(), ev))
}

func TestProcessOrderRefund_MixedChain_NeedsReview(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	ev := refundEvent(t, decimal.NewFromInt(20), 0)
	st := entity.AcctOrderPostingState{LegacySale: true, Prepayment: true}

	acct.EXPECT().GetOrderPostingState(mock.Anything, testOrderUUID).Return(st, nil)
	acct.EXPECT().MarkEventNeedsReview(mock.Anything, ev.Id, "mixed old+new recognition chain, manual entry required").Return(nil)
	// No GetOrderFactsForPosting / CreateJournalEntry / MarkEventProcessed.

	require.NoError(t, w.processOrderRefund(context.Background(), ev))
}
