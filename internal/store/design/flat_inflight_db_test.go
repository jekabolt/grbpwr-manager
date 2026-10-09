package design_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// M8 (07.10): ONE FLAT RUN IN FLIGHT PER CARD. Two tabs pressing GENERATE on one card booked two paid
// flat runs. StartRun refuses a flat run while a flat run of the card is pending or running — inside
// its SERIALIZABLE transaction, before the day's reservation.
//
// Run like every probe of this package: a disposable MySQL container and CI=1 (wave2_db_test.go).
//
// MUTATIONS THEY CATCH: the check removed (two rows); the check after moveBudgetDay (the refused press
// leaves a reservation); the check before priorStart (a replay of the first press is refused); status
// list without `running`; FORCE INDEX dropped is not caught here (a plan, not a result).

func flatInflightStart(card int, est string) entity.DesignRunStart {
	return entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString(est), Valid: true},
	}
}

func flatRunsOfCard(t *testing.T, raw *sql.DB, card int) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_run WHERE tech_card_id = ? AND kind = 'flat'`, card).Scan(&n))
	return n
}

func TestDesignDBSecondFlatRunOfACardIsRefusedWhileOneIsInFlight(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	firstReq := flatInflightStart(card, "0.30")
	first, err := rep.Design().StartRun(ctx, firstReq)
	require.NoError(t, err)

	// a second press (another tab: another client_request_id) — refused, typed, naming the first
	_, err = rep.Design().StartRun(ctx, flatInflightStart(card, "0.30"))
	var live *entity.DesignFlatRunInFlightError
	require.True(t, errors.As(err, &live), "a second flat run while one is pending: %v", err)
	require.True(t, errors.Is(err, entity.ErrDesignFlatRunInFlight))
	require.Equal(t, first.Run.Id, live.RunID)
	require.Equal(t, entity.DesignRunPending, live.Status)
	require.Equal(t, 1, flatRunsOfCard(t, raw, card), "no second row")
	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.3")),
		"the refused press reserved nothing: %s", budget.Reserved)

	// a replay of the FIRST press is the first run, not a refusal (step 1 answers it)
	again, err := rep.Design().StartRun(ctx, firstReq)
	require.NoError(t, err)
	require.True(t, again.Idempotent)
	require.Equal(t, first.Run.Id, again.Run.Id)

	// another kind on the card, and a flat of another card, are not held
	_, err = rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindRender,
		RequestedOutputs: 1, Author: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().StartRun(ctx, flatInflightStart(probeCard(t, raw), "0.10"))
	require.NoError(t, err)

	// running holds the card too
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, uuid.NewString())
	require.NoError(t, err)
	status, _ := runStatus(t, raw, first.Run.Id)
	require.Equal(t, entity.DesignRunRunning, status)
	_, err = rep.Design().StartRun(ctx, flatInflightStart(card, "0.30"))
	require.True(t, errors.As(err, &live), "a second flat run while one is running: %v", err)
	require.Equal(t, entity.DesignRunRunning, live.Status)

	// a settled run frees it: a fresh card's pending run cancelled → the next press passes
	other := probeCard(t, raw)
	pending, err := rep.Design().StartRun(ctx, flatInflightStart(other, "0.10"))
	require.NoError(t, err)
	_, err = rep.Design().CancelRun(ctx, pending.Run.Id, "probe")
	require.NoError(t, err)
	_, err = rep.Design().StartRun(ctx, flatInflightStart(other, "0.10"))
	require.NoError(t, err, "a cancelled flat run holds nothing")
}

// TWO TABS AT ONCE: N concurrent presses on one card — exactly one row, one reservation; every other
// press is the typed refusal (the SERIALIZABLE read + the deadlock retry order them).
func TestDesignDBConcurrentFlatPressesOnOneCardStartOneRun(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	const n = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = rep.Design().StartRun(ctx, flatInflightStart(card, "0.25"))
		}(i)
	}
	close(start)
	wg.Wait()

	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, entity.ErrDesignFlatRunInFlight):
			refused++
		default:
			t.Fatalf("unexpected error from a concurrent press: %v", err)
		}
	}
	require.Equal(t, 1, ok, "exactly one press starts a run")
	require.Equal(t, n-1, refused)
	require.Equal(t, 1, flatRunsOfCard(t, raw, card))
	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.25")),
		"one reservation for one run: %s", budget.Reserved)
}
