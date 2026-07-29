package mail

import (
	"context"
	"testing"
	"time"
)

func TestResendBudgetCampaignCannotConsumeReservedToken(t *testing.T) {
	budget := NewResendBudget(0.0001, 2, 1)
	if !budget.TryCampaign() {
		t.Fatal("first campaign request should consume the non-reserved token")
	}
	if budget.TryCampaign() {
		t.Fatal("campaign consumed the transactional reserve token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := budget.WaitTransactional(ctx); err != nil {
		t.Fatalf("transactional request could not consume reserved token: %v", err)
	}
}

func TestResendBudgetCampaignYieldsToTransactionalWaiter(t *testing.T) {
	budget := NewResendBudget(0.0001, 2, 1)
	if !budget.limiter.AllowN(budget.now(), 2) {
		t.Fatal("failed to drain initial tokens")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = budget.WaitTransactional(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		budget.mu.Lock()
		waiters := budget.transactionalWaiters
		budget.mu.Unlock()
		if waiters > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transactional waiter did not register")
		}
	}
	if budget.TryCampaign() {
		t.Fatal("campaign acquired while transactional mail was waiting")
	}
	cancel()
	<-done
}

func TestResendBudgetClampsTransactionalReserveInvariant(t *testing.T) {
	tests := []struct {
		name        string
		burst       int
		reserve     int
		wantBurst   int
		wantReserve int
	}{
		{name: "default unchanged", burst: 2, reserve: 1, wantBurst: 2, wantReserve: 1},
		{name: "burst one", burst: 1, reserve: 1, wantBurst: 2, wantReserve: 1},
		{name: "reserve zero", burst: 3, reserve: 0, wantBurst: 3, wantReserve: 1},
		{name: "reserve capped below burst", burst: 3, reserve: 5, wantBurst: 3, wantReserve: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewResendBudget(1, tt.burst, tt.reserve)
			if got := budget.limiter.Burst(); got != tt.wantBurst {
				t.Fatalf("burst = %d, want %d", got, tt.wantBurst)
			}
			if got := budget.transactionalReserve; got != tt.wantReserve {
				t.Fatalf("transactional reserve = %d, want %d", got, tt.wantReserve)
			}
			if budget.transactionalReserve < 1 ||
				budget.limiter.Burst() < budget.transactionalReserve+1 {
				t.Fatalf(
					"invalid invariant: burst=%d reserve=%d",
					budget.limiter.Burst(),
					budget.transactionalReserve,
				)
			}
		})
	}
}
