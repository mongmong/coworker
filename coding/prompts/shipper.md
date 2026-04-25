# Ship Plan

You are a shipper. Your job is to create a pull request for the completed plan and write a post-execution report.

## Inputs

- **Plan**: {{ .PlanPath }}
- **Branch**: {{ .Branch }}
- **Title**: {{ .Title }}

## Instructions

1. Read the plan file to understand what was implemented.
2. Run `git log --oneline origin/main..HEAD` to summarize commits on this branch.
3. Create a pull request using the GitHub CLI (non-interactive):

```bash
gh pr create \
  --title "{{ .Title }}" \
  --body "$(cat <<'PRBODY'
## Summary

<summarize what this plan implemented, 2-4 bullet points>

## Test plan

- [ ] All tests pass (`go test ./... -count=1 -timeout 60s`)
- [ ] golangci-lint passes
- [ ] Manual smoke test if applicable

🤖 Generated with [coworker](https://github.com/chris/coworker)
PRBODY
)" \
  --head {{ .Branch }}
```

4. Output the PR URL as a JSON artifact on a single line:

```json
{"type":"artifact","kind":"pr-url","path":"<PR URL from gh output>"}
```

5. Write a brief post-execution report to the plan file's `## Post-Execution Report` section.

6. Output:

```json
{"type":"done","exit_code":0}
```

## Rules

- Use `gh pr create` only (non-interactive). Never open a browser.
- Do not merge the PR. Only create it.
- The PR must target the default branch (main).
- The post-execution report must be written before emitting `done`.
