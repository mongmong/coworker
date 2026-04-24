package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/core"
)

// FreeformWorkflow dispatches user-requested roles without phase constraints.
type FreeformWorkflow struct {
	Dispatcher *coding.Dispatcher
}

// NewFreeformWorkflow creates a FreeformWorkflow using the provided dispatcher.
func NewFreeformWorkflow(dispatcher *coding.Dispatcher) *FreeformWorkflow {
	return &FreeformWorkflow{
		Dispatcher: dispatcher,
	}
}

// Name identifies this workflow type.
func (w *FreeformWorkflow) Name() string {
	return "freeform"
}

// Dispatch triggers a one-off role dispatch through the underlying Dispatcher.
func (w *FreeformWorkflow) Dispatch(ctx context.Context, input *core.DispatchInput) (string, error) {
	if w == nil {
		return "", fmt.Errorf("freeform workflow is nil")
	}
	if w.Dispatcher == nil {
		return "", errors.New("freeform workflow missing dispatcher")
	}
	if input == nil {
		return "", errors.New("dispatch input is nil")
	}
	if input.Role == "" {
		return "", errors.New("dispatch input role is required")
	}

	result, err := w.Dispatcher.Orchestrate(ctx, &coding.DispatchInput{
		RoleName: input.Role,
		Inputs:   input.Inputs,
	})
	if err != nil {
		return "", err
	}
	return result.JobID, nil
}
