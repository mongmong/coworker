package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventJSONUsesSnakeCaseFields(t *testing.T) {
	event := Event{
		ID:             "evt_1",
		RunID:          "run_1",
		Sequence:       7,
		Kind:           EventJobCompleted,
		SchemaVersion:  1,
		IdempotencyKey: "idem_1",
		CausationID:    "cause_1",
		CorrelationID:  "corr_1",
		Payload:        `{"job_id":"job_1"}`,
		CreatedAt:      time.Unix(1700, 0).UTC(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"id",
		"run_id",
		"sequence",
		"kind",
		"schema_version",
		"idempotency_key",
		"causation_id",
		"correlation_id",
		"payload",
		"created_at",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing JSON field %q in %s", key, string(data))
		}
	}
	if _, ok := decoded["RunID"]; ok {
		t.Fatalf("unexpected Go-style field name in %s", string(data))
	}
}
