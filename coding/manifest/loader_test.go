package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chris/coworker/coding/manifest"
)

const validYAML = `
spec_path: docs/specs/2026-04-20-coworker.md
plans:
  - id: 100
    title: "Core runtime"
    phases: ["SQLite schema", "event intake"]
    blocks_on: []
  - id: 101
    title: "Review workflow"
    phases: ["dispatch", "fan-in"]
    blocks_on: [100]
  - id: 102
    title: "TUI dashboard"
    phases: ["layout"]
    blocks_on: []
`

func TestParseManifest_Valid(t *testing.T) {
	m, err := manifest.ParseManifest([]byte(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.SpecPath != "docs/specs/2026-04-20-coworker.md" {
		t.Errorf("spec_path = %q, want %q", m.SpecPath, "docs/specs/2026-04-20-coworker.md")
	}
	if len(m.Plans) != 3 {
		t.Fatalf("len(plans) = %d, want 3", len(m.Plans))
	}
	if m.Plans[0].ID != 100 {
		t.Errorf("plans[0].id = %d, want 100", m.Plans[0].ID)
	}
	if m.Plans[1].BlocksOn[0] != 100 {
		t.Errorf("plans[1].blocks_on[0] = %d, want 100", m.Plans[1].BlocksOn[0])
	}
}

func TestParseManifest_MissingSpecPath(t *testing.T) {
	yaml := `
plans:
  - id: 100
    title: "Core runtime"
    phases: ["phase1"]
    blocks_on: []
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing spec_path, got nil")
	}
}

func TestParseManifest_EmptyPlans(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
plans: []
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty plans, got nil")
	}
}

func TestParseManifest_DuplicateID(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
plans:
  - id: 100
    title: "Plan A"
    phases: ["p1"]
    blocks_on: []
  - id: 100
    title: "Plan B"
    phases: ["p1"]
    blocks_on: []
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for duplicate plan IDs, got nil")
	}
}

func TestParseManifest_InvalidBlocksOnRef(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
plans:
  - id: 100
    title: "Plan A"
    phases: ["p1"]
    blocks_on: [999]
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for unknown blocks_on reference, got nil")
	}
}

func TestParseManifest_ZeroID(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
plans:
  - id: 0
    title: "Plan zero"
    phases: ["p1"]
    blocks_on: []
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for id=0, got nil")
	}
}

func TestParseManifest_EmptyTitle(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
plans:
  - id: 100
    title: ""
    phases: ["p1"]
    blocks_on: []
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty title, got nil")
	}
}

func TestParseManifest_UnknownFieldsIgnored(t *testing.T) {
	yaml := `
spec_path: docs/specs/foo.md
future_field: "ignored"
plans:
  - id: 100
    title: "Plan A"
    phases: ["p1"]
    blocks_on: []
    unknown_new_field: true
`
	_, err := manifest.ParseManifest([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error for extra fields: %v", err)
	}
}

func TestLoadManifest_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	m, err := manifest.LoadManifest(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Plans) != 3 {
		t.Errorf("len(plans) = %d, want 3", len(m.Plans))
	}
}

func TestLoadManifest_FileNotFound(t *testing.T) {
	_, err := manifest.LoadManifest("/nonexistent/path/manifest.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestPlanSlug(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Core runtime + job queue", "core-runtime-job-queue"},
		{"Review workflow", "review-workflow"},
		{"TUI dashboard", "tui-dashboard"},
		{"Plan with UPPERCASE", "plan-with-uppercase"},
		{"Plan!@#$%special", "planspecial"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"multiple---hyphens", "multiple-hyphens"},
	}
	for _, tc := range cases {
		got := manifest.PlanSlug(tc.title)
		if got != tc.want {
			t.Errorf("PlanSlug(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	got := manifest.BranchName(106, "Build from PRD")
	want := "feature/plan-106-build-from-prd"
	if got != want {
		t.Errorf("BranchName(106, %q) = %q, want %q", "Build from PRD", got, want)
	}
}

func TestPlanSlug_MaxLength(t *testing.T) {
	longTitle := "this-is-a-very-long-title-that-should-be-truncated-at-the-forty-character-boundary"
	slug := manifest.PlanSlug(longTitle)
	if len(slug) > 40 {
		t.Errorf("PlanSlug returned slug of length %d, want <= 40: %q", len(slug), slug)
	}
}
