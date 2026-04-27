// Package predicates provides shared git-diff predicate functions used by
// both the supervisor rule engine and the phase-loop role fan-out.
package predicates

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ChangesTouchInDir reports whether the committed diff (HEAD~1..HEAD, with an
// initial-commit fallback to the empty tree) touches at least one file
// matching any of the given glob patterns. workDir sets the working directory
// for git commands; empty string uses the current directory.
//
// Pattern semantics:
//   - Patterns containing slashes are matched against the full repo-relative
//     path using filepath.Match (e.g. "web/**", "src/*.go").
//   - Slash-free patterns (e.g. "*.tsx") are matched against both the full
//     path AND the basename, so "*.tsx" matches "web/app/Page.tsx".
//   - "prefix/**" matches any file whose path starts with "prefix/".
//   - "**/<suffix>" matches any file whose basename matches <suffix>.
func ChangesTouchInDir(workDir string, globs []string) (bool, error) {
	if len(globs) == 0 {
		return false, fmt.Errorf("ChangesTouchInDir requires at least one glob pattern")
	}

	files, err := gitDiffChangedFiles(workDir)
	if err != nil {
		return false, fmt.Errorf("changes_touch: %w", err)
	}

	for _, f := range files {
		for _, g := range globs {
			matched, err := globMatch(g, f)
			if err != nil {
				return false, fmt.Errorf("changes_touch: invalid glob %q: %w", g, err)
			}
			if matched {
				return true, nil
			}
			// Also match the basename against patterns that don't contain a slash.
			if !strings.Contains(g, "/") {
				base := filepath.Base(f)
				matched, err = globMatch(g, base)
				if err != nil {
					return false, fmt.Errorf("changes_touch: invalid glob %q: %w", g, err)
				}
				if matched {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// globMatch matches a file path against a glob pattern that may contain **.
//
// filepath.Match does not support **, which is a common convention meaning
// "any depth of subdirectories". This function handles two ** forms:
//
//   - "web/**"  → true if file starts with "web/" (any depth under web/)
//   - "**/*.tsx" → true if the file's base name matches "*.tsx" at any depth
//
// All other patterns are delegated to filepath.Match.
func globMatch(pattern, file string) (bool, error) {
	if strings.Contains(pattern, "**") {
		// "prefix/**" — match any file under the prefix directory.
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			return strings.HasPrefix(file, prefix+"/"), nil
		}
		// "**/<rest>" — match the base name (or sub-path) at any depth.
		if strings.HasPrefix(pattern, "**/") {
			suffix := strings.TrimPrefix(pattern, "**/")
			return filepath.Match(suffix, filepath.Base(file))
		}
		// Other ** patterns (e.g. "a/**/b") — not supported; return no-match
		// without error so callers are not broken by unknown patterns.
		return false, nil
	}
	return filepath.Match(pattern, file)
}

// gitDiffChangedFiles returns the list of files changed between HEAD~1 and
// HEAD. If there is only one commit (no parent), it returns the files
// introduced by that commit using the empty-tree comparison.
func gitDiffChangedFiles(workDir string) ([]string, error) {
	// Try HEAD~1..HEAD first.
	cmd := exec.Command("git", "diff", "--name-only", "HEAD~1..HEAD")
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if err != nil {
		// If HEAD has no parent (initial commit), fall back to HEAD vs empty tree.
		emptyTree := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
		cmd2 := exec.Command("git", "diff", "--name-only", emptyTree, "HEAD")
		if workDir != "" {
			cmd2.Dir = workDir
		}
		out2, err2 := cmd2.Output()
		if err2 != nil {
			return nil, fmt.Errorf("git diff HEAD~1..HEAD: %w; git diff empty-tree HEAD: %v", err, err2)
		}
		out = out2
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}
