//go:build live

package live

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireLiveEnv skips the test unless COWORKER_LIVE=1.
func requireLiveEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("COWORKER_LIVE") != "1" {
		t.Skip("set COWORKER_LIVE=1 to enable live agent tests")
	}
}

// requireBinary skips the test if the named binary is not on PATH.
// Returns the absolute binary path.
func requireBinary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("required binary %q not found on PATH: %v", name, err)
	}
	return path
}

// budgetUSD returns the per-test budget in USD, defaulting to 0.50.
// Reserved for future enforcement (cost wiring is Plan 121).
func budgetUSD() float64 {
	s := os.Getenv("COWORKER_LIVE_BUDGET_USD")
	if s == "" {
		return 0.50
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0.50
	}
	return v
}

// withTimeout returns a context with the given duration applied.
func withTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}

// hasJSONLine returns true if any line in s parses as a JSON object that
// contains the given top-level key. Used to verify that the CLI emitted
// at least one stream-json event of the expected shape.
func hasJSONLine(s, requireKey string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if _, ok := m[requireKey]; ok {
			return true
		}
	}
	return false
}
