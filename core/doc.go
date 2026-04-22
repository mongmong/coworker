// Package core contains domain-neutral primitives: runs, jobs, events,
// supervisor framework, attention queue, worker registry, cost ledger, and
// the Agent protocol. It is the foundation layer that coding/ builds on.
//
// Import discipline: core packages must not import any coding/ package.
// Enforced by tests/architecture/imports_test.go.
package core
