# Adding a new role

Coworker roles are YAML + prompt-template pairs. Adding one is mechanical: drop two files, the runtime picks them up automatically. This guide shows the full flow with a hypothetical `security-auditor` role.

## 1. Pick a name

Role names use dotted notation: `developer`, `reviewer.arch`, `reviewer.frontend`, `tester`, `shipper`, `architect`, `planner`, `supervisor`. The dot is a namespace; on disk, dots become underscores: `reviewer.arch` → `coding/roles/reviewer_arch.yaml`.

For a security auditor, two reasonable names:

- `security` (top-level, like `tester`)
- `reviewer.security` (a sub-kind of reviewer; runs in the phase-review fanout by default)

The second is usually right — security findings are reviewer findings.

## 2. Write the YAML

`coding/roles/reviewer_security.yaml`:

```yaml
name: reviewer.security
concurrency: many              # parallel-safe with other reviewers
cli: codex                     # default CLI; user can override per-repo
prompt_template: prompts/reviewer_security.md
inputs:
  required:
    - diff_path
    - spec_path
outputs:
  contract:
    findings_line_anchored: true
  emits:
    findings: []
sandbox: read-only
permissions:
  allowed_tools:
    - read
    - grep
    - glob
    - "bash:codex"
  never:
    - write
    - edit
    - "bash:rm"
  requires_human: []
budget:
  max_tokens_per_job: 200000
  max_wallclock_minutes: 30
  max_cost_usd: 5.00
retry_policy:
  on_contract_fail: retry_with_feedback
  on_job_error: retry_once
```

Field reference: `core.Role` in [`core/role.go`](../../core/role.go). Required fields are `name`, `concurrency`, `cli`, `prompt_template`, and at least one `inputs.required` entry.

### Optional `applies_when`

If the role should only fire under certain conditions:

```yaml
applies_when:
  changes_touch:
    - "auth/**"
    - "crypto/**"
  commit_msg_contains: '(?i)security|auth|crypto'
  phase_index_in: "0,3-5"
```

All populated predicates AND together. See [`core/role.go::RoleAppliesWhen`](../../core/role.go) and [`internal/predicates/`](../../internal/predicates/).

## 3. Write the prompt template

`coding/prompts/reviewer_security.md`:

```markdown
# Security Review

You are reviewing this diff for security issues.

## Inputs

- **Diff**: `{{ .DiffPath }}`
- **Spec**: `{{ .SpecPath }}`

## Instructions

1. Read the diff. Look for: hardcoded secrets, missing input validation,
   improper auth checks, SQL injection vectors, XSS sinks.
2. For each issue, emit a stream-json finding line:

   {"type":"finding","path":"<file>","line":<n>,"severity":"<critical|important|minor|nit>","body":"<message>"}

3. Finish with a done event:

   {"type":"done","exit_code":0}

## Severity scale

- **critical**: exploitable today; block ship.
- **important**: likely exploitable; fix before ship.
- **minor**: defense-in-depth; can ship with this open.
- **nit**: style or doc; not a security issue.
```

Template fields use `{{ .PascalCase }}` for inputs declared as `snake_case` in the YAML. The dispatcher does the conversion via `coding/dispatch.go::snakeToPascal`.

## 4. Wire into the workflow (optional)

By default, only the spec's catalog roles (developer, reviewer.arch, reviewer.frontend, tester) run in phase-review and phase-test. To add the new role to the phase-review fanout, edit `policy.yaml`:

```yaml
workflow_overrides:
  build-from-prd:
    phase-review:
      - reviewer.arch
      - reviewer.frontend
      - reviewer.security  # new
```

`StageRegistry.NewStageRegistry` reads this at workflow start. Empty list disables a stage; nil (omitted) keeps the default.

## 5. Verify

Run `coworker init` (if needed — it copies bundled roles into `.coworker/roles/`), then:

```bash
coworker invoke reviewer.security \
  --diff path/to/diff.patch \
  --spec docs/specs/000-coworker-runtime-design.md
```

The dispatcher loads the YAML, renders the prompt with your inputs, dispatches via Codex, parses findings, and persists everything to SQLite + the event log.

## 6. Test

Add a focused unit test if your role has non-obvious behavior. For most roles, the existing role-loader tests in [`coding/roles/loader_test.go`](../../coding/roles/loader_test.go) iterate every yaml under `coding/roles/` and validate parsing — so the new file is already covered structurally.

For a replay-driven end-to-end test, see [adding-a-replay-scenario.md](adding-a-replay-scenario.md).

## 7. Plugin updates (optional)

If the new role is meant to be invoked from a CLI plugin (Claude Code / Codex / OpenCode), add a matching skill or command file under `plugins/coworker-{claude,codex,opencode}/` that knows how to call it. The plugin assets are CLI-specific; see [`plugins/`](../../plugins/) for examples.
