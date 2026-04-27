package shipper

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ghPRTimeout is the deadline applied to the gh pr create subprocess.
// The gh CLI may be slower than git due to network I/O; 60 seconds is generous.
const ghPRTimeout = 60 * time.Second

// ghCreatePR shells out to `gh pr create` and returns the PR URL.
//
// branch is the feature branch to open the PR from.
// title is the PR title.
// body is the PR body (markdown).
//
// The command is: gh pr create --title <title> --body <body> --head <branch>
//
// Returns the PR URL string parsed from gh's stdout.
func ghCreatePR(ctx context.Context, branch, title, body string) (string, error) {
	ghCtx, cancel := context.WithTimeout(ctx, ghPRTimeout)
	defer cancel()

	//nolint:gosec // Arguments are controlled by the runtime, not user-supplied shell input.
	cmd := exec.CommandContext(ghCtx, "gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--head", branch,
	)
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("gh pr create: %s", errMsg)
	}

	url := strings.TrimSpace(stdout.String())
	if url == "" {
		return "", fmt.Errorf("gh pr create: empty output (no PR URL returned)")
	}

	return url, nil
}
