package dispatch

import (
	"errors"
	"sync"
	"testing"
)

// TestSpend_ConcurrentSpendCannotExceedCeiling pins the budget invariant
// under concurrency: N concurrent spends that individually fit under the
// ceiling but jointly exceed it must produce exactly ceiling-fit worth of
// successful spends and ErrBudgetExhausted for the rest. The read-modify-write
// in Acceptor.Spend races unless the store provides an atomic conditional
// spend (Store.SpendIfWithinCeiling).
func TestSpend_ConcurrentSpendCannotExceedCeiling(t *testing.T) {
	a := newAcceptor()
	acc, err := a.Accept(testDispatch(), 7)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const amount = 30 // ceiling is 100: at most 3 can succeed
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, exhausted, other := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.Spend(acc.WorksExecutionID, amount)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrBudgetExhausted):
				exhausted++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if other > 0 {
		t.Fatalf("unexpected non-budget errors: %d", other)
	}
	if ok != 3 || exhausted != workers-3 {
		t.Fatalf("ok=%d exhausted=%d, want exactly 3 spends (90/100) and %d rejections", ok, exhausted, workers-3)
	}
	got, _ := a.store.LoadByExecution(acc.WorksExecutionID)
	if got.BudgetSpent != 90 {
		t.Fatalf("BudgetSpent=%d, want 90 (ceiling never exceeded)", got.BudgetSpent)
	}
}
