// Package manifest provides the plan manifest schema, YAML loader,
// DAG scheduler, and worktree manager for the build-from-prd workflow.
package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

// PlanManifest is the top-level document produced by the architect role.
// It lists all plans for a run along with their dependency graph.
type PlanManifest struct {
	// SpecPath is the repo-relative path to the spec document that this
	// manifest was derived from. Required.
	SpecPath string `yaml:"spec_path"`

	// Plans is the ordered list of plan entries. The order is advisory;
	// the DAGScheduler uses BlocksOn to determine execution order.
	Plans []PlanEntry `yaml:"plans"`
}

// PlanEntry describes a single plan within a manifest.
type PlanEntry struct {
	// ID is the numeric plan identifier. Must be unique within the manifest
	// and positive.
	ID int `yaml:"id"`

	// Title is a short human-readable description. Used to derive branch
	// and worktree names.
	Title string `yaml:"title"`

	// Phases is an ordered list of phase summaries (one line each).
	// The architect provides these as skeletons; the planner elaborates them.
	Phases []string `yaml:"phases"`

	// BlocksOn is the list of plan IDs that must be completed before this
	// plan can start. An empty or nil slice means the plan has no dependencies
	// and may start immediately.
	BlocksOn []int `yaml:"blocks_on"`
}

// slugReplace matches characters that are not lowercase alphanumeric or hyphen.
var slugReplace = regexp.MustCompile(`[^a-z0-9-]+`)

// slugCollapse matches runs of two or more hyphens.
var slugCollapse = regexp.MustCompile(`-{2,}`)

const maxSlugLen = 40

// PlanSlug derives a URL-safe slug from a plan title.
// The slug is lowercased, spaces become hyphens, all non-[a-z0-9-]
// characters are removed, repeated hyphens are collapsed, and the
// result is trimmed to maxSlugLen characters with no leading/trailing hyphens.
func PlanSlug(title string) string {
	s := strings.ToLower(title)
	s = strings.ReplaceAll(s, " ", "-")
	s = slugReplace.ReplaceAllString(s, "")
	s = slugCollapse.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// BranchName returns the git branch name for a plan.
// Format: "feature/plan-{id}-{slug}".
func BranchName(id int, title string) string {
	return fmt.Sprintf("feature/plan-%d-%s", id, PlanSlug(title))
}

// WorktreeDirName returns the directory name for a plan worktree.
// Format: "plan-{id}-{slug}".
func WorktreeDirName(id int, title string) string {
	return fmt.Sprintf("plan-%d-%s", id, PlanSlug(title))
}

// Validate checks that the manifest is internally consistent.
// It returns a non-nil error if any of the following are violated:
//   - spec_path is non-empty
//   - plans list is non-empty
//   - each plan has id > 0 and a non-empty title
//   - all plan IDs are unique
//   - all blocks_on IDs refer to IDs present in the manifest
func (m *PlanManifest) Validate() error {
	if m.SpecPath == "" {
		return fmt.Errorf("manifest: spec_path is required")
	}
	if len(m.Plans) == 0 {
		return fmt.Errorf("manifest: plans must not be empty")
	}

	seen := make(map[int]bool, len(m.Plans))
	for i, p := range m.Plans {
		if p.ID <= 0 {
			return fmt.Errorf("manifest: plans[%d].id must be > 0, got %d", i, p.ID)
		}
		if p.Title == "" {
			return fmt.Errorf("manifest: plans[%d] (id=%d) must have a non-empty title", i, p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("manifest: duplicate plan id %d", p.ID)
		}
		seen[p.ID] = true
	}

	// Validate that all blocks_on references point to known IDs.
	for i, p := range m.Plans {
		for _, dep := range p.BlocksOn {
			if !seen[dep] {
				return fmt.Errorf(
					"manifest: plans[%d] (id=%d) blocks_on unknown plan id %d",
					i, p.ID, dep,
				)
			}
		}
	}

	// Detect cycles using Kahn's algorithm (topological sort).
	// Build in-degree map: for each plan, count how many plans it depends on.
	inDegree := make(map[int]int, len(m.Plans))
	// dependents maps a plan ID to the set of plans that list it in blocks_on.
	dependents := make(map[int][]int, len(m.Plans))
	for _, p := range m.Plans {
		if _, ok := inDegree[p.ID]; !ok {
			inDegree[p.ID] = 0
		}
		for _, dep := range p.BlocksOn {
			inDegree[p.ID]++
			dependents[dep] = append(dependents[dep], p.ID)
		}
	}

	// Queue all plans with no dependencies.
	queue := make([]int, 0, len(m.Plans))
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	processed := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		processed++
		for _, dep := range dependents[cur] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if processed != len(m.Plans) {
		return fmt.Errorf("manifest: blocks_on graph contains a cycle")
	}

	return nil
}
