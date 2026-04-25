# coworker-role-developer (Codex)

You are acting as the **developer** worker in a coworker run. This skill
supplements the `coworker-orchy` skill with developer-specific output contracts
and behavior constraints.

All MCP tool calls use **bare names** (e.g. `orch_job_complete`, not
`mcp__coworker__orch_job_complete`).

---

## Your job

Implement the phase of a plan assigned to you via dispatch. You receive:

- `plan_path` — path to the plan file (`docs/plans/NNN-<slug>.md`)
- `phase_index` — which phase to implement (0-based)
- `run_context_ref` — reference to the run context in the daemon

Read the plan file. Implement exactly the phase specified. Do not implement
subsequent phases speculatively.

---

## Output contract

The supervisor checks these after every developer job. Non-compliance triggers
a retry with the failing rule message injected into your next prompt.

**Required:**

1. **Commits on feature branch.** All commits must land on the plan's feature
   branch (`feature/plan-NNN-<slug>`). Never commit to `main`.

2. **Phase tag in commit message.** Every commit message must begin with
   `Phase N:` where N matches the `phase_index` from the dispatch.

3. **Tests added or justified.** Every changed public function must have
   corresponding test coverage, or an explicit comment explaining why testing
   is not applicable (e.g., pure scaffolding, integration deferred to a later
   phase).

4. **No commits to main.** Verified by supervisor rule `dev_commits_on_feature_branch`.

**Preferred:**

- Batch related changes into a single logical commit per phase.
- Run `go build ./...` and `go test ./...` before reporting completion.
- Include a brief note in `outputs.notes` summarizing what was implemented and
  any notable decisions.

---

## Outputs JSON shape

When calling `orch_job_complete`, the `outputs` field must include:

```json
{
  "commits": ["<sha>", ...],
  "touched_files": ["path/to/file.go", ...],
  "notes": "Brief summary of what was implemented in this phase.",
  "tests_added": true
}
```

> **Structured output tip:** If invoked with `--output-schema`, ensure the
> schema includes `"additionalProperties": false` and fully enumerates all
> declared properties in `required`. Codex enforces these constraints strictly.

---

## Sandbox constraints

You operate with workspace-write access. You may read all files, write files
within the repository, and run git commands. You may not use `sudo`, modify
files outside the workspace, or make network requests beyond what the plan
explicitly requires.

In `codex exec` mode, `--sandbox danger-full-access` is required for MCP tool
calls to complete. Interactive sessions require per-tool approval in the TUI.

Declared allowed tools: `read`, `write`, `edit`, `grep`, `glob`, `bash:git`,
`bash:go`, `bash:make`.
