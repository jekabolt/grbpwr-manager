package acctposting

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Wave-3 pull-logic tests for internal/acctposting/pull.go (shipping_actual + dev_expense). Like the
// wave-2 outbox tests they assert WHICH store calls happen (create / reverse / checkpoint) for a given
// source state — not the posted amounts, which internal/accounting's builders cover.

// ndd wraps a decimal string as a valid NullDecimal.
func ndd(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// hasSourceKey matches a CreateJournalEntry call by its entry's source_key.
func hasSourceKey(key string) any {
	return mock.MatchedBy(func(e entity.AcctJournalEntryInsert) bool { return e.SourceKey == key })
}

// ---- processShipping -------------------------------------------------------------------------

func TestProcessShipping_FirstRunCreatesV1(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	now := testCutover.Add(72 * time.Hour)
	repo.EXPECT().Now().Return(now)

	acct.EXPECT().GetCheckpoint(mock.Anything, checkpointShipmentCost).
		Return(entity.AcctCheckpoint{Source: checkpointShipmentCost}, nil)

	sh := entity.AcctShipmentCostFacts{
		ShipmentID:   5,
		OrderUUID:    "o5",
		ActualCost:   ndd("12.00"),
		ShippingDate: sql.NullTime{Time: testCutover.Add(24 * time.Hour), Valid: true},
		UpdatedAt:    now,
	}
	acct.EXPECT().ListChangedShipmentsForActualCost(mock.Anything, mock.Anything, mock.Anything).
		Return([]entity.AcctShipmentCostFacts{sh}, nil)
	// No existing versions → active nil → create v1.
	acct.EXPECT().ListJournalEntries(mock.Anything, mock.Anything).Return(nil, 0, nil)
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceKey("ship:5")).Return(1, false, nil)
	acct.EXPECT().SetCheckpoint(mock.Anything, checkpointShipmentCost, mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, w.processShipping(context.Background()))
}

func TestProcessShipping_PreCutoverSkipped(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	now := testCutover.Add(72 * time.Hour)
	repo.EXPECT().Now().Return(now)
	acct.EXPECT().GetCheckpoint(mock.Anything, checkpointShipmentCost).
		Return(entity.AcctCheckpoint{Source: checkpointShipmentCost}, nil)

	// shipping_date BEFORE the cutover start month → skipped, no posting.
	sh := entity.AcctShipmentCostFacts{
		ShipmentID:   6,
		ActualCost:   ndd("9.00"),
		ShippingDate: sql.NullTime{Time: testStartDate.AddDate(0, -1, 0), Valid: true},
		UpdatedAt:    now,
	}
	acct.EXPECT().ListChangedShipmentsForActualCost(mock.Anything, mock.Anything, mock.Anything).
		Return([]entity.AcctShipmentCostFacts{sh}, nil)
	// Only the checkpoint advances; no ListJournalEntries / CreateJournalEntry.
	acct.EXPECT().SetCheckpoint(mock.Anything, checkpointShipmentCost, mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, w.processShipping(context.Background()))
}

func TestProcessShipping_NoOpWhenUnchanged(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	now := testCutover.Add(72 * time.Hour)
	repo.EXPECT().Now().Return(now)
	acct.EXPECT().GetCheckpoint(mock.Anything, checkpointShipmentCost).
		Return(entity.AcctCheckpoint{Source: checkpointShipmentCost}, nil)

	sh := entity.AcctShipmentCostFacts{
		ShipmentID:   5,
		ActualCost:   ndd("12.00"),
		ShippingDate: sql.NullTime{Time: testCutover.Add(24 * time.Hour), Valid: true},
		UpdatedAt:    now,
	}
	acct.EXPECT().ListChangedShipmentsForActualCost(mock.Anything, mock.Anything, mock.Anything).
		Return([]entity.AcctShipmentCostFacts{sh}, nil)

	// An active v1 already exists with the SAME lines the candidate would build → no-op (no create).
	active := entity.AcctJournalEntry{Id: 9, SourceType: entity.AcctSourceShippingActual, SourceKey: "ship:5"}
	acct.EXPECT().ListJournalEntries(mock.Anything, mock.Anything).
		Return([]entity.AcctJournalEntry{active}, 1, nil)
	acct.EXPECT().GetJournalEntry(mock.Anything, 9).Return(&entity.AcctJournalEntryFull{
		Lines: []entity.AcctJournalLine{
			{AccountCode: "6030", Side: entity.AcctSideDebit, Amount: decimal.RequireFromString("12.00")},
			{AccountCode: "2030", Side: entity.AcctSideCredit, Amount: decimal.RequireFromString("12.00")},
		},
	}, nil)
	acct.EXPECT().SetCheckpoint(mock.Anything, checkpointShipmentCost, mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, w.processShipping(context.Background()))
}

// ---- processDevExpenses ----------------------------------------------------------------------

func TestProcessDevExpenses_CreateSkipReverse(t *testing.T) {
	repo, acct := newMocks(t)
	w := newTestWorker(repo)

	devs := []entity.AcctDevExpenseFacts{
		{Id: 1, TechCardName: "A", Kind: "sample", AmountBase: ndd("100.00"), CreatedAt: testCutover.Add(24 * time.Hour)},
		{Id: 2, TechCardName: "B", Kind: "other", CreatedAt: testCutover.Add(24 * time.Hour)}, // uncosted → skip
	}
	acct.EXPECT().ListDevExpensesForPosting(mock.Anything, mock.Anything).Return(devs, nil)

	// An active entry for a dev expense (id 99) that no longer exists → reversed as deleted.
	deleted := entity.AcctJournalEntry{Id: 50, SourceType: entity.AcctSourceDevExpense, SourceKey: "dev:99"}
	acct.EXPECT().ListJournalEntries(mock.Anything, mock.Anything).
		Return([]entity.AcctJournalEntry{deleted}, 1, nil)

	// id 1 costed, no active version → create v1 ("dev:1"). id 2 uncosted → no call. id 99 → reverse.
	acct.EXPECT().CreateJournalEntry(mock.Anything, hasSourceKey("dev:1")).Return(1, false, nil)
	acct.EXPECT().ReverseJournalEntry(mock.Anything, 50, mock.Anything, "system").Return(2, nil)

	require.NoError(t, w.processDevExpenses(context.Background()))
}
