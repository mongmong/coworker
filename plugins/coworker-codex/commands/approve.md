# coworker-approve (Codex)

Approve a pending checkpoint or attention item, allowing the run to continue.

> Codex uses **bare MCP tool names**. Use `orch_checkpoint_list`,
> `orch_checkpoint_advance`, and `orch_attention_answer` — not the
> `mcp__coworker__*` namespaced forms.

## Usage

```
Call orch_checkpoint_list and display results, then approve
Call orch_checkpoint_advance with {"checkpoint_id": "<id>"}
Call orch_attention_answer with {"id": "<id>", "answer": "yes"}
```

## Steps

1. Call `orch_checkpoint_list` with `{}` to get pending checkpoints.
2. If a specific checkpoint ID is known, approve it directly via
   `orch_checkpoint_advance` with `{"checkpoint_id": "<id>"}`.
3. If no ID is specified and exactly one checkpoint is pending, ask the user
   to confirm before approving it.
4. If multiple checkpoints are pending, display them and ask the user which
   one to approve.

For attention items (permission requests, questions from agents), use
`orch_attention_answer` with the item ID and the user's answer.

## Notes

- Approving `spec-approved` or `plan-approved` allows the run to proceed to
  implementation.
- Approving `ready-to-ship` triggers PR creation by the shipper role.
- `compliance-breach` and `quality-gate` checkpoints may require reviewing
  the supervisor's findings before approving.
