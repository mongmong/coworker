// Package quality implements the LLM-judge quality sub-behavior of the
// supervisor. Quality checks run at every checkpoint (not after every job)
// and are advisory by default. A small allowlist of block-capable categories
// can create attention items and block progress.
package quality

// Category identifies the kind of quality issue a rule checks for.
type Category string

const (
	// CategoryMissingTests fires when new functions or methods lack test coverage.
	CategoryMissingTests Category = "missing_required_tests"

	// CategorySpecContradiction fires when the implementation contradicts
	// the spec requirements provided in context.
	CategorySpecContradiction Category = "spec_contradiction"

	// CategorySecurityUnreviewed fires when security-sensitive changes
	// (auth, crypto, secrets, SQL, network) have not been reviewed.
	CategorySecurityUnreviewed Category = "security_sensitive_unreviewed_change"

	// CategoryShipperReport fires when a shipper job's plan file is missing
	// a non-empty "## Post-Execution Report" section.
	CategoryShipperReport Category = "shipper_report_missing"
)

// BlockCapableCategories is the allowlist of categories that may block progress
// when the judge returns a failing verdict. All other categories are advisory.
// This set is hard-coded per the spec — policy cannot extend or shrink it.
var BlockCapableCategories = map[Category]bool{
	CategoryMissingTests:       true,
	CategorySpecContradiction:  true,
	CategorySecurityUnreviewed: true,
	CategoryShipperReport:      true,
}

// IsBlockCapable returns true if the category is in the block-capable allowlist.
func IsBlockCapable(cat Category) bool {
	return BlockCapableCategories[cat]
}

// Rule is a single quality rule loaded from YAML.
type Rule struct {
	// Name is populated from the YAML map key; not present in the YAML value.
	Name string `yaml:"-"`

	// Category is the quality category this rule checks.
	Category Category `yaml:"category"`

	// Prompt is the instruction sent to the LLM judge. It must include
	// a JSON output format instruction so the judge can parse the verdict.
	Prompt string `yaml:"prompt"`

	// Severity is either "block" or "advisory". A "block" severity rule
	// may create attention items; an "advisory" rule only logs findings.
	// Note: only categories in BlockCapableCategories can actually block
	// even when severity is "block" — the category is the authoritative gate.
	Severity string `yaml:"severity"`
}

// RuleSet is the top-level structure of a quality rules YAML file.
type RuleSet struct {
	Rules map[string]Rule `yaml:"quality_rules"`
}

// Verdict is the structured JSON response returned by the LLM judge.
type Verdict struct {
	// Pass is true if the rule's quality criterion is satisfied.
	Pass bool `json:"pass"`

	// Category echoes the rule's category for audit purposes.
	Category string `json:"category"`

	// Findings is a list of human-readable issue descriptions.
	// Non-empty only when Pass is false.
	Findings []string `json:"findings"`

	// Confidence is a 0.0–1.0 score indicating the judge's certainty.
	Confidence float64 `json:"confidence"`
}

// Finding is a single finding from a quality rule evaluation.
type Finding struct {
	// RuleName is the name of the rule that produced the finding.
	RuleName string

	// Category is the quality category of the finding.
	Category Category

	// Findings contains the human-readable issue descriptions from the verdict.
	Findings []string

	// Confidence is the judge's confidence score.
	Confidence float64

	// IsBlocking is true if the category is block-capable.
	IsBlocking bool
}

// Result aggregates the outcome of evaluating all quality rules
// at a single checkpoint.
type Result struct {
	// Pass is true when no blocking findings were produced.
	Pass bool

	// BlockingFindings are from block-capable categories that failed.
	// Each one has triggered an attention item.
	BlockingFindings []Finding

	// AdvisoryFindings are from categories not in the block-capable allowlist.
	// They are logged to the adherence report but do not block progress.
	AdvisoryFindings []Finding

	// AttentionItemIDs are the IDs of attention items created for
	// blocking findings.
	AttentionItemIDs []string

	// QualityGateEscalated is true when the max-retry ceiling was reached
	// for blocking findings and a quality-gate event was emitted.
	QualityGateEscalated bool
}
