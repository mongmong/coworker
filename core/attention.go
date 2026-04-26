package core

import "time"

// AttentionKind identifies the type of attention item.
type AttentionKind string

const (
	AttentionPermission AttentionKind = "permission"
	AttentionSubprocess AttentionKind = "subprocess"
	AttentionQuestion   AttentionKind = "question"
	AttentionCheckpoint AttentionKind = "checkpoint"
)

// Standard answer values for checkpoint attention items.
const (
	// AttentionAnswerApprove is written by orch_checkpoint_advance and signals
	// that the checkpoint is approved and the workflow may continue.
	AttentionAnswerApprove = "approve"

	// AttentionAnswerReject is written by orch_checkpoint_rollback and signals
	// that the checkpoint was rejected; the run should be aborted.
	AttentionAnswerReject = "reject"
)

// AttentionItem is a unified human-input request blocking a run or job.
type AttentionItem struct {
	ID          string        `json:"id"`
	RunID       string        `json:"run_id"`
	Kind        AttentionKind `json:"kind"`
	Source      string        `json:"source"`
	JobID       string        `json:"job_id,omitempty"`
	Question    string        `json:"question,omitempty"`
	Options     []string      `json:"options,omitempty"`
	PresentedOn []string      `json:"presented_on,omitempty"` // e.g., ["tui", "cli_pane_1"]
	AnsweredOn  []string      `json:"answered_on,omitempty"`
	AnsweredBy  string        `json:"answered_by,omitempty"`
	Answer      string        `json:"answer,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	ResolvedAt  *time.Time    `json:"resolved_at,omitempty"`
}

// IsAnswered returns true if the item has been answered.
func (a *AttentionItem) IsAnswered() bool {
	return a.Answer != "" && a.AnsweredBy != ""
}
