package predicates

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CommitMsgContains returns true when the most recent commit message in
// workDir matches the given regex pattern. Plan 131 (I3).
//
// Implementation: shells out to `git log -1 --format=%B`. Empty workDir
// uses the current working directory.
func CommitMsgContains(workDir, pattern string) (bool, error) {
	if pattern == "" {
		return false, fmt.Errorf("commit_msg_contains: empty pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("commit_msg_contains: invalid regex %q: %w", pattern, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%B")
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("commit_msg_contains: git log: %w", err)
	}
	msg := strings.TrimSpace(string(out))
	return re.MatchString(msg), nil
}
