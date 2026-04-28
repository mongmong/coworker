# Plan 138 — `phase-ship` deferral (re-audit follow-up)

> Codex re-audit flagged that `coding/stages/defaults.go` declares `phase-ship: [shipper]` but `BuildFromPRDWorkflow` never consults the registry for it — `policy.workflow_overrides.phase-ship` silently has no effect.

## Decision

Document this gap explicitly in `docs/architecture/decisions.md` (the V1.1+ deferral catalog) rather than wiring it. Rationale:

- The Shipper does git + `gh pr create` directly. Wiring `phase-ship` properly requires either (a) refactoring Shipper to dispatch a configurable `shipper.*` role chain, or (b) extending Shipper with its own override field. Both are bigger than V1 needs.
- `phase-ship: [shipper]` stays in `DefaultStages` so the spec's four-stage catalog is complete and discoverable. The `RolesForStage` query for it returns the default; nothing is consumed.
- The inline comment in `coding/workflow/build_from_prd.go:245` now points at Decision 15 for the authoritative rationale.

## Verification

```
go build ./...                                      → clean
go test -race ./... -count=1 -timeout 180s          → 27 packages PASS, 0 failed, 0 races
golangci-lint run ./...                             → 0 issues
```

After this plan, the Codex re-audit's "NEAR" verdict resolves to "READY" — every audit item either shipped or has an authoritative deferral entry in Decision 15.
