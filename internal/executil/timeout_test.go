package executil

import (
	"context"
	"testing"
	"time"
)

func TestBudgetTimeout_ZeroReturnsOriginalContext(t *testing.T) {
	ctx := context.Background()
	got, cancel := BudgetTimeout(ctx, 0)
	defer cancel()

	if got != ctx {
		t.Error("expected original context when maxWallclockMinutes=0")
	}
	if _, ok := got.Deadline(); ok {
		t.Error("expected no deadline when maxWallclockMinutes=0")
	}
}

func TestBudgetTimeout_NegativeReturnsOriginalContext(t *testing.T) {
	ctx := context.Background()
	got, cancel := BudgetTimeout(ctx, -1)
	defer cancel()

	if got != ctx {
		t.Error("expected original context when maxWallclockMinutes<0")
	}
	if _, ok := got.Deadline(); ok {
		t.Error("expected no deadline when maxWallclockMinutes<0")
	}
}

func TestBudgetTimeout_PositiveSetsDeadline(t *testing.T) {
	ctx := context.Background()
	before := time.Now()
	got, cancel := BudgetTimeout(ctx, 1)
	defer cancel()

	deadline, ok := got.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set when maxWallclockMinutes=1")
	}

	// Deadline should be roughly 1 minute from now (within a generous window).
	expectedMin := before.Add(59 * time.Second)
	expectedMax := before.Add(61 * time.Second)
	if deadline.Before(expectedMin) || deadline.After(expectedMax) {
		t.Errorf("deadline %v is not within 1 minute of %v", deadline, before)
	}
}

func TestBudgetTimeout_CancelIsSafe(t *testing.T) {
	ctx := context.Background()
	_, cancel := BudgetTimeout(ctx, 0)
	// Calling the no-op cancel must not panic.
	cancel()
	cancel() // safe to call multiple times

	_, cancel2 := BudgetTimeout(ctx, 1)
	cancel2()
	cancel2() // safe to call multiple times
}
