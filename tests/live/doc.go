//go:build live

// Package live contains end-to-end smoke tests that invoke real CLI
// agents (Claude Code, Codex, OpenCode). Tests skip unless
// COWORKER_LIVE=1 is set in the environment AND the live build tag is
// enabled. Run with:
//
//	COWORKER_LIVE=1 go test -tags live ./tests/live/...
//
// Each test should consume <1 second of CLI time and well under
// $0.50 of provider cost. Cost is not currently asserted (Dispatcher
// has no CostWriter wiring yet — see Plan 121); instead, tests use
// trivial prompts and short timeouts.
package live
