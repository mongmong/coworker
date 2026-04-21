// Package architecture contains cross-cutting tests that enforce
// project-wide architectural invariants. No non-test source files live
// here; Go supports test-only packages.
package architecture

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/chris/coworker"

// TestCoreDoesNotImportCoding enforces the architecture posture from
// docs/specs/001-plan-manifest.md: imports flow core → coding, never
// the reverse. A violation means the core/ package has reached into
// coding/, which would couple the domain-neutral layer to the
// coding-specific one.
func TestCoreDoesNotImportCoding(t *testing.T) {
	cmd := exec.Command("go", "list",
		"-f", "{{.ImportPath}}: {{.Imports}}",
		modulePath+"/core/...")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}

	forbidden := modulePath + "/coding"
	var violations []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, forbidden) {
			violations = append(violations, line)
		}
	}
	if len(violations) > 0 {
		t.Errorf("core imports coding (forbidden):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
