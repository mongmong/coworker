package developer_then_reviewer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chris/coworker/agent"
	"github.com/chris/coworker/coding"
	"github.com/chris/coworker/store"
)

// TestReplay_DeveloperThenReviewer runs the dispatch pipeline end-to-end
// using recorded transcripts in place of real CLI output. It exercises:
//   - role loading (developer, reviewer.arch),
//   - prompt rendering with the role's required inputs,
//   - the agent boundary via ReplayAgent (parses stream-json the same way
//     as the live CliAgent),
//   - finding persistence into the findings table.
//
// Skipped unless COWORKER_REPLAY=1 is set.
func TestReplay_DeveloperThenReviewer(t *testing.T) {
	if os.Getenv("COWORKER_REPLAY") != "1" {
		t.Skip("set COWORKER_REPLAY=1 to enable replay tests")
	}

	fixtureDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	transcriptsDir := filepath.Join(fixtureDir, "transcripts")
	inputsDir := filepath.Join(fixtureDir, "inputs")

	// Locate the repo root (three levels up from tests/replay/<scenario>/).
	repoRoot, err := filepath.Abs(filepath.Join(fixtureDir, "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	roleDir := filepath.Join(repoRoot, "coding", "roles")
	// Role YAML's prompt_template is "prompts/<file>.md" relative to the
	// PromptDir. Since the prompts live at coding/prompts/, PromptDir is
	// "<repo>/coding" (not "<repo>/coding/prompts").
	promptDir := filepath.Join(repoRoot, "coding")

	expected := loadExpected(t, fixtureDir)
	replay := &agent.ReplayAgent{TranscriptDir: transcriptsDir}

	// --- Developer ---
	devDB, devCleanup := newReplayDB(t)
	defer devCleanup()
	devEs := store.NewEventStore(devDB)
	devCe := store.NewCostEventStore(devDB, devEs)
	devDisp := &coding.Dispatcher{
		Agent:      replay,
		DB:         devDB,
		RoleDir:    roleDir,
		PromptDir:  promptDir,
		CostWriter: devCe,
	}
	devOut, err := devDisp.Orchestrate(context.Background(), &coding.DispatchInput{
		RoleName: "developer",
		Inputs: map[string]string{
			"plan_path":       filepath.Join(inputsDir, "plan.md"),
			"phase_index":     "1",
			"run_context_ref": "replay-1",
		},
	})
	if err != nil {
		t.Fatalf("developer dispatch: %v", err)
	}
	assertDispatch(t, "developer", devOut, expected["developer"])

	// Verify cost persisted: at least one cost_events row, sum matches.
	devCostRows, err := devCe.ListByJob(context.Background(), devOut.JobID)
	if err != nil {
		t.Fatalf("cost ListByJob: %v", err)
	}
	if len(devCostRows) != 1 {
		t.Errorf("developer cost rows = %d, want 1", len(devCostRows))
	}
	devCostSum, err := devCe.SumByRun(context.Background(), devOut.RunID)
	if err != nil {
		t.Fatalf("cost SumByRun: %v", err)
	}
	if devCostSum != expected["developer"].ExpectCostUSD {
		t.Errorf("developer cost sum = %v, want %v",
			devCostSum, expected["developer"].ExpectCostUSD)
	}

	// --- Reviewer.arch ---
	revDB, revCleanup := newReplayDB(t)
	defer revCleanup()
	revDisp := &coding.Dispatcher{
		Agent:     replay,
		DB:        revDB,
		RoleDir:   roleDir,
		PromptDir: promptDir,
	}
	revOut, err := revDisp.Orchestrate(context.Background(), &coding.DispatchInput{
		RoleName: "reviewer.arch",
		Inputs: map[string]string{
			"diff_path": filepath.Join(inputsDir, "diff.patch"),
			"spec_path": filepath.Join(inputsDir, "spec.md"),
		},
	})
	if err != nil {
		t.Fatalf("reviewer.arch dispatch: %v", err)
	}
	assertDispatch(t, "reviewer.arch", revOut, expected["reviewer.arch"])

	// Verify findings persisted in the findings table for the reviewer run.
	es := store.NewEventStore(revDB)
	fs := store.NewFindingStore(revDB, es)
	persisted, err := fs.ListFindings(context.Background(), revOut.RunID)
	if err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if got, want := len(persisted), expected["reviewer.arch"].FindingsCount; got != want {
		t.Errorf("persisted findings = %d, want %d", got, want)
	}
}

type roleExpected struct {
	ExitCode      int      `json:"exit_code"`
	FindingsCount int      `json:"findings_count"`
	Fingerprints  []string `json:"fingerprints,omitempty"`
	ExpectCostUSD float64  `json:"expect_cost_usd,omitempty"`
}

func loadExpected(t *testing.T, dir string) map[string]roleExpected {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]roleExpected{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertDispatch(t *testing.T, role string, got *coding.DispatchResult, want roleExpected) {
	t.Helper()
	if got.ExitCode != want.ExitCode {
		t.Errorf("%s: exit_code = %d, want %d", role, got.ExitCode, want.ExitCode)
	}
	if len(got.Findings) != want.FindingsCount {
		t.Errorf("%s: findings count = %d, want %d", role, len(got.Findings), want.FindingsCount)
	}
	if want.Fingerprints != nil {
		gotFps := make([]string, 0, len(got.Findings))
		for _, f := range got.Findings {
			gotFps = append(gotFps, fmt.Sprintf("%s:%d:%s", f.Path, f.Line, f.Severity))
		}
		// Compare as sorted sets so dedupe ordering does not matter.
		sort.Strings(gotFps)
		expectedFps := append([]string(nil), want.Fingerprints...)
		sort.Strings(expectedFps)
		if strings.Join(gotFps, ",") != strings.Join(expectedFps, ",") {
			t.Errorf("%s: fingerprints = %v, want %v", role, gotFps, expectedFps)
		}
	}
}

func newReplayDB(t *testing.T) (*store.DB, func()) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}
