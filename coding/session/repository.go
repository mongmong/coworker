package session

import (
	"context"

	"github.com/chris/coworker/core"
)

// RunRepository is the persistence contract that Manager requires for run lifecycle.
// *store.RunStore satisfies this interface.
type RunRepository interface {
	CreateRun(ctx context.Context, run *core.Run) error
	GetRun(ctx context.Context, runID string) (*core.Run, error)
	CompleteRun(ctx context.Context, runID string, state core.RunState) error
}
