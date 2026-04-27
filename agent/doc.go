// Package agent provides concrete Agent implementations of core.Agent.
//
// # Implementations
//
// CliAgent (Plan 100) executes an external CLI binary via os/exec, feeds the
// prompt on stdin, and parses stream-JSON from stdout into findings.
//
// OpenCodeHTTPAgent (Plan 118) dispatches to an OpenCode server running in
// HTTP-primary mode (opencode serve). It:
//  1. Creates a session via POST /session.
//  2. Subscribes to the SSE event stream at GET /event.
//  3. Sends the prompt via POST /session/{id}/message.
//  4. Collects message.part.updated text events; uses session.idle as the
//     definitive completion signal.
//  5. Parses the final assistant text as JSONL findings (same format as
//     CliAgent) or places it in result.Stdout for free-form output.
//  6. Cleans up via DELETE /session/{id}.
//
// # HTTP Agent pattern
//
// Future HTTP-backed agents should follow the OpenCodeHTTPAgent pattern:
//   - Hold ServerURL and *http.Client (nil → http.DefaultClient).
//   - Spawn an SSE goroutine before posting the trigger request so no events
//     are missed.
//   - Use a buffered resultCh (cap 1) to communicate the JobResult from the
//     goroutine to JobHandle.Wait.
//   - Perform best-effort cleanup in the goroutine with context.Background so
//     DELETE / abort requests are sent even when the caller's context expires.
//   - Implement Cancel() by cancelling the SSE goroutine context and posting
//     an abort request.
//
// The Agent protocol itself lives in core/ to avoid circular imports.
package agent
