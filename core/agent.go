package core

import "context"

// Agent is the protocol for dispatching jobs to CLI coding agents.
// Shipped with one implementation (CliAgent) in Plan 100.
// Future HTTP-backed agents or library agents drop in without
// touching dispatch code.
type Agent interface {
	// Dispatch starts a job asynchronously and returns a handle to
	// wait for the result or cancel the execution.
	Dispatch(ctx context.Context, job *Job, prompt string) (JobHandle, error)
}

// JobHandle represents a running job. Callers use Wait to block
// until the job completes, or Cancel to abort it.
type JobHandle interface {
	// Wait blocks until the job completes and returns the result.
	Wait(ctx context.Context) (*JobResult, error)
	// Cancel aborts the running job.
	Cancel() error
}
