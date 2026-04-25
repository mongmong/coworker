# /coworker-approve (OpenCode)

Approve a pending checkpoint or attention item, allowing the run to continue.

> In interactive MCP mode, OpenCode uses the same namespaced tool names as
> Claude Code: `mcp__coworker__orch_checkpoint_list`, etc.

## Usage

```
/coworker-approve
/coworker-approve <checkpoint-id>
/coworker-approve <attention-id> --answer "yes"
```

When called without arguments, lists all pending checkpoints and prompts the
user to select one.

## Steps

1. Call `mcp__coworker__orch_checkpoint_list` with `{}` to get pending
   checkpoints.
2. If a `<checkpoint-id>` argument was provided, approve that checkpoint
   directly via `mcp__coworker__orch_checkpoint_advance` with
   `{"checkpoint_id": "<id>"}`.
3. If no argument was provided and exactly one checkpoint is pending, ask the
   user to confirm before approving it.
4. If multiple checkpoints are pending, display them and ask the user which
   one to approve.

For attention items (permission requests, questions from agents), use
`mcp__coworker__orch_attention_answer` with the item ID and the user's answer.

## Notes

- Approving `spec-approved` or `plan-approved` allows the run to proceed to
  implementation.
- Approving `ready-to-ship` triggers PR creation by the shipper role.
- `compliance-breach` and `quality-gate` checkpoints may require reviewing
  the supervisor's findings before approving.
