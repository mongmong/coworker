package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// DefaultMaxQualityRetries is the maximum number of times a blocking quality
// finding may be retried before the evaluator escalates to a quality-gate event.
// Matches DefaultMaxRetries in coding/dispatch.go.
const DefaultMaxQualityRetries = 3

// CheckpointContext provides the runtime context for a quality evaluation
// at a specific checkpoint.
type CheckpointContext struct {
	// RunID is the ID of the current run.
	RunID string

	// JobID is the ID of the job at the checkpoint (for attention items).
	// May be empty for run-level checkpoints.
	JobID string

	// RetryCount is the number of times quality evaluation has already been
	// attempted at this checkpoint. Zero on first attempt.
	RetryCount int

	// MaxRetries is the maximum retries before escalating. Zero uses DefaultMaxQualityRetries.
	MaxRetries int
}

// effectiveMaxRetries returns the retry ceiling for the given context.
func (c *CheckpointContext) effectiveMaxRetries() int {
	if c.MaxRetries > 0 {
		return c.MaxRetries
	}
	return DefaultMaxQualityRetries
}

// Evaluator runs all quality rules at checkpoint time, routes
// findings to advisory or blocking paths, and creates attention items
// for blocking findings.
//
// It does NOT import coding/supervisor — both are sibling packages
// that each import core and store.
type Evaluator struct {
	// Judge is the LLM judge used to evaluate each rule.
	Judge Judge

	// Rules is the list of quality rules to evaluate.
	Rules []*Rule

	// AttentionStore is used to create attention items for blocking findings.
	// May be nil in tests that don't exercise blocking paths.
	AttentionStore *store.AttentionStore

	// CheckpointWriter, when non-nil, is paired with AttentionStore: every
	// quality-gate attention item also gets a matching checkpoints row so the
	// durable record exists for later resolution. Optional; legacy callers
	// that leave it nil only get the attention row.
	CheckpointWriter core.CheckpointWriter

	// EventStore is used to write quality.verdict and quality-gate events.
	EventStore *store.EventStore

	// Logger is used for info/debug/error logging. If nil, slog.Default() is used.
	Logger *slog.Logger
}

// EvaluateAtCheckpoint runs all quality rules against the given diff and
// job context, routes findings, creates attention items for blocking results,
// and emits events.
//
// The checkpoint context controls retry counting and escalation.
func (e *Evaluator) EvaluateAtCheckpoint(
	ctx context.Context,
	cpCtx *CheckpointContext,
	diff string,
	jobContext string,
) (*Result, error) {
	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}

	result := &Result{Pass: true}

	for _, rule := range e.Rules {
		verdict, err := e.Judge.Evaluate(ctx, rule, diff, jobContext)
		if err != nil {
			// Judge error: emit a verdict event with status=error so the failure
			// is durable, then handle per category (block-capable vs advisory).
			logger.Error("quality judge error", "rule", rule.Name, "error", err)
			errorVerdict := &Verdict{
				Pass:     false,
				Category: string(rule.Category),
				Findings: []string{fmt.Sprintf("judge error: %v", err)},
			}
			e.writeVerdictEvent(ctx, cpCtx.RunID, cpCtx.JobID, rule, errorVerdict, "error")

			errorFinding := Finding{
				RuleName:   rule.Name,
				Category:   rule.Category,
				Findings:   errorVerdict.Findings,
				Confidence: 0,
				IsBlocking: IsBlockCapable(rule.Category),
			}

			if errorFinding.IsBlocking {
				// Block-capable judge errors must not silently pass.
				itemID, attErr := e.createAttentionItem(ctx, cpCtx, rule, errorVerdict)
				if attErr != nil {
					logger.Error("failed to create attention item for judge error", "rule", rule.Name, "error", attErr)
				} else if itemID != "" {
					result.AttentionItemIDs = append(result.AttentionItemIDs, itemID)
				}
				result.BlockingFindings = append(result.BlockingFindings, errorFinding)
				result.Pass = false
			} else {
				result.AdvisoryFindings = append(result.AdvisoryFindings, errorFinding)
			}
			continue
		}

		// Write a quality.verdict event for every rule regardless of outcome.
		status := "pass"
		if !verdict.Pass {
			status = "fail"
		}
		e.writeVerdictEvent(ctx, cpCtx.RunID, cpCtx.JobID, rule, verdict, status)

		if verdict.Pass {
			logger.Debug("quality rule passed", "rule", rule.Name, "confidence", verdict.Confidence)
			continue
		}

		finding := Finding{
			RuleName:   rule.Name,
			Category:   rule.Category,
			Findings:   verdict.Findings,
			Confidence: verdict.Confidence,
			IsBlocking: IsBlockCapable(rule.Category),
		}

		if finding.IsBlocking {
			logger.Info("quality blocking finding", "rule", rule.Name, "category", rule.Category)

			// Create an attention item for the blocking finding.
			itemID, attErr := e.createAttentionItem(ctx, cpCtx, rule, verdict)
			if attErr != nil {
				logger.Error("failed to create attention item", "rule", rule.Name, "error", attErr)
			} else if itemID != "" {
				result.AttentionItemIDs = append(result.AttentionItemIDs, itemID)
			}

			result.BlockingFindings = append(result.BlockingFindings, finding)
			result.Pass = false
		} else {
			logger.Info("quality advisory finding", "rule", rule.Name, "category", rule.Category)
			result.AdvisoryFindings = append(result.AdvisoryFindings, finding)
		}
	}

	// Check if we should escalate to quality-gate.
	if !result.Pass && cpCtx.RetryCount >= cpCtx.effectiveMaxRetries() {
		logger.Warn("quality-gate escalation: max retries reached",
			"retry_count", cpCtx.RetryCount,
			"max_retries", cpCtx.effectiveMaxRetries(),
			"blocking_findings", len(result.BlockingFindings))

		e.writeQualityGateEvent(ctx, cpCtx.RunID, cpCtx.JobID, result)
		result.QualityGateEscalated = true
	}

	return result, nil
}

// createAttentionItem creates an attention item for a blocking quality finding.
// Returns the new item's ID, or empty string if AttentionStore is nil.
func (e *Evaluator) createAttentionItem(
	ctx context.Context,
	cpCtx *CheckpointContext,
	rule *Rule,
	verdict *Verdict,
) (string, error) {
	if e.AttentionStore == nil {
		return "", nil
	}

	question := fmt.Sprintf("Quality gate blocked by rule %q (category: %s).\nFindings:\n", rule.Name, rule.Category)
	for i, f := range verdict.Findings {
		question += fmt.Sprintf("  %d. %s\n", i+1, f)
	}
	question += "\nReview the findings and decide how to proceed."

	item := &core.AttentionItem{
		ID:       core.NewID(),
		RunID:    cpCtx.RunID,
		Kind:     core.AttentionCheckpoint,
		Source:   "quality-judge",
		JobID:    cpCtx.JobID,
		Question: question,
		Options:  []string{"retry", "override", "abort"},
	}

	if err := e.AttentionStore.InsertAttention(ctx, item); err != nil {
		return "", fmt.Errorf("insert attention item: %w", err)
	}
	if e.CheckpointWriter != nil {
		if err := e.CheckpointWriter.CreateCheckpoint(ctx, core.CheckpointRecord{
			ID:    item.ID,
			RunID: cpCtx.RunID,
			Kind:  string(core.AttentionCheckpoint),
		}); err != nil {
			return "", fmt.Errorf("insert checkpoint row: %w", err)
		}
	}
	return item.ID, nil
}

// writeVerdictEvent writes a quality.verdict event for a single rule verdict.
// status is "pass", "fail", or "error" and is included in the event payload.
func (e *Evaluator) writeVerdictEvent(
	ctx context.Context,
	runID string,
	jobID string,
	rule *Rule,
	verdict *Verdict,
	status string,
) {
	if e.EventStore == nil {
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"rule_name":  rule.Name,
		"category":   string(rule.Category),
		"severity":   rule.Severity,
		"pass":       verdict.Pass,
		"findings":   verdict.Findings,
		"confidence": verdict.Confidence,
		"status":     status,
	})

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventQualityVerdict,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	if err := e.EventStore.WriteEventThenRow(ctx, event, nil); err != nil {
		e.logger().Error("failed to write quality.verdict event", "rule", rule.Name, "error", err)
	}
}

// writeQualityGateEvent writes a quality-gate event when escalation occurs.
func (e *Evaluator) writeQualityGateEvent(
	ctx context.Context,
	runID string,
	jobID string,
	result *Result,
) {
	if e.EventStore == nil {
		return
	}

	type findingJSON struct {
		RuleName string   `json:"rule_name"`
		Category string   `json:"category"`
		Findings []string `json:"findings"`
	}
	var findings []findingJSON
	for _, f := range result.BlockingFindings {
		findings = append(findings, findingJSON{
			RuleName: f.RuleName,
			Category: string(f.Category),
			Findings: f.Findings,
		})
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"job_id":            jobID,
		"blocking_count":    len(result.BlockingFindings),
		"attention_items":   result.AttentionItemIDs,
		"blocking_findings": findings,
	})

	event := &core.Event{
		ID:            core.NewID(),
		RunID:         runID,
		Kind:          core.EventQualityGate,
		SchemaVersion: 1,
		CorrelationID: jobID,
		Payload:       string(payload),
		CreatedAt:     time.Now(),
	}

	if err := e.EventStore.WriteEventThenRow(ctx, event, nil); err != nil {
		e.logger().Error("failed to write quality-gate event", "error", err)
	}
}

// logger returns the configured logger or slog.Default().
func (e *Evaluator) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}
