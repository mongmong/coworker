package executil

import (
	"context"
	"time"
)

// BudgetTimeout wraps ctx with a deadline equal to maxWallclockMinutes * time.Minute.
// Returns the original ctx and a no-op cancel function if maxWallclockMinutes <= 0
// (no deadline applied).
func BudgetTimeout(ctx context.Context, maxWallclockMinutes int) (context.Context, context.CancelFunc) {
	if maxWallclockMinutes <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(maxWallclockMinutes)*time.Minute)
}
