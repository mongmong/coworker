# coworker Tutorial

This tutorial shows the **current runnable path** in coworker: a single ephemeral role invocation from the command line.

It is written so you can copy the commands directly.

## 1. Build or run from source

From the repo root:

```bash
make build
./coworker --help
```

If you do not want to build first:

```bash
go run ./cmd/coworker --help
```

## 2. Understand what this tutorial uses

The tutorial uses:

- role: `reviewer.arch`
- role definition: [coding/roles/reviewer_arch.yaml](/home/chris/workshop/coworker/coding/roles/reviewer_arch.yaml)
- prompt template: [coding/prompts/reviewer_arch.md](/home/chris/workshop/coworker/coding/prompts/reviewer_arch.md)
- mock CLI agent: [testdata/mocks/codex](/home/chris/workshop/coworker/testdata/mocks/codex)

The mock agent is intentional. It gives you a deterministic success path without needing a real LLM provider.

## 3. Run the first successful invoke

Run:

```bash
go run ./cmd/coworker invoke reviewer.arch \
  --diff go.mod \
  --spec docs/specs/000-coworker-runtime-design.md \
  --cli-binary ./testdata/mocks/codex \
  --role-dir coding/roles \
  --prompt-dir coding
```

Expected output:

```text
Run: <run-id>
Job: <job-id>
Findings: 2
  1. {"body":"Missing error check on Close()","fingerprint":"...","line":42,"path":"main.go","severity":"important"}
  2. {"body":"Consider using prepared statement","fingerprint":"...","line":17,"path":"store.go","severity":"minor"}
```

What happened:

1. coworker loaded `reviewer.arch`
2. it rendered the prompt template with your `--diff` and `--spec` paths
3. it invoked the mock Codex binary
4. it parsed stream-JSON findings from stdout
5. it saved run/job/finding/event rows into `.coworker/state.db`

## 4. Inspect the database file

The default database path is:

```text
.coworker/state.db
```

Confirm it exists:

```bash
ls -l .coworker/state.db
```

If you have `sqlite3` installed, inspect the schema:

```bash
sqlite3 .coworker/state.db '.tables'
```

List runs:

```bash
sqlite3 .coworker/state.db 'select id, mode, state, started_at, ended_at from runs;'
```

List jobs:

```bash
sqlite3 .coworker/state.db 'select id, run_id, role, state, cli from jobs;'
```

List findings:

```bash
sqlite3 .coworker/state.db 'select run_id, job_id, path, line, severity, body from findings;'
```

List event sequence:

```bash
sqlite3 .coworker/state.db 'select run_id, sequence, kind from events order by run_id, sequence;'
```

The expected event order for one successful invoke is:

1. `run.created`
2. `job.created`
3. `job.leased`
4. `finding.created`
5. `finding.created`
6. `job.completed`
7. `run.completed`

## 5. Run with an explicit database path

If you want to keep tutorial data separate:

```bash
go run ./cmd/coworker invoke reviewer.arch \
  --diff go.mod \
  --spec docs/specs/000-coworker-runtime-design.md \
  --cli-binary ./testdata/mocks/codex \
  --role-dir coding/roles \
  --prompt-dir coding \
  --db /tmp/coworker-tutorial.db
```

Then inspect it:

```bash
sqlite3 /tmp/coworker-tutorial.db '.tables'
```

## 6. Run the same flow with the built binary

```bash
./coworker invoke reviewer.arch \
  --diff go.mod \
  --spec docs/specs/000-coworker-runtime-design.md \
  --cli-binary ./testdata/mocks/codex \
  --role-dir coding/roles \
  --prompt-dir coding
```

## 7. Try a failure mode

If you omit a required role input, coworker should fail fast.

Example:

```bash
go run ./cmd/coworker invoke reviewer.arch \
  --diff go.mod \
  --cli-binary ./testdata/mocks/codex \
  --role-dir coding/roles \
  --prompt-dir coding
```

Expected result: an error complaining about a missing required input.

## 8. Optional: point at a real CLI binary

The shipped implementation is generic: it shells out to whatever binary you pass via `--cli-binary`.

Example shape:

```bash
go run ./cmd/coworker invoke reviewer.arch \
  --diff go.mod \
  --spec docs/specs/000-coworker-runtime-design.md \
  --cli-binary /path/to/real/agent \
  --role-dir coding/roles \
  --prompt-dir coding
```

Current caveat:

- the implemented runtime expects the agent to emit the same stream-JSON shape the mock script emits
- the deeper Claude/Codex/OpenCode integration work has been validated in spikes, but is not yet wired into the shipped runtime

## 9. Run the test suite

To confirm the thin runtime still passes:

```bash
go test ./... -count=1 -timeout 60s
```

## 10. What to read next

- [README.md](/home/chris/workshop/coworker/README.md)
- [docs/spike-rerun-guide.md](/home/chris/workshop/coworker/docs/spike-rerun-guide.md)
- [docs/plans/100-thin-end-to-end.md](/home/chris/workshop/coworker/docs/plans/100-thin-end-to-end.md)
- [docs/specs/000-coworker-runtime-design.md](/home/chris/workshop/coworker/docs/specs/000-coworker-runtime-design.md)
