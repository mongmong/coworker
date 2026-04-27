package cli

import (
	"encoding/json"
	"net/http"

	"github.com/chris/coworker/coding/eventbus"
	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// httpStores bundles the store-layer objects used by the HTTP handlers.
// All fields are expected to be non-nil when the HTTP server is active.
type httpStores struct {
	run       *store.RunStore
	job       *store.JobStore
	attention *store.AttentionStore
}

// buildHTTPMux constructs the HTTP mux for the daemon's REST + SSE surface.
// It registers:
//
//	GET  /events                — SSE stream
//	GET  /runs                  — list all runs (JSON)
//	GET  /runs/{id}             — get a single run (JSON)
//	GET  /runs/{id}/jobs        — list jobs for a run (JSON)
//	GET  /attention             — list pending attention items (JSON)
//	POST /attention/{id}/answer — answer an attention item (JSON body)
func buildHTTPMux(bus *eventbus.InMemoryBus, s httpStores) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /events", eventbus.SSEHandler(bus))
	mux.HandleFunc("GET /runs", handleListRuns(s.run))
	mux.HandleFunc("GET /runs/{id}", handleGetRun(s.run))
	mux.HandleFunc("GET /runs/{id}/jobs", handleListJobs(s.job))
	mux.HandleFunc("GET /attention", handleListAttention(s.attention))
	mux.HandleFunc("POST /attention/{id}/answer", handleAnswerAttention(s.attention))

	return mux
}

// respondJSON encodes v as JSON and writes it to w with the given status code.
func respondJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleListRuns returns an http.HandlerFunc that lists all runs.
func handleListRuns(rs *store.RunStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runs, err := rs.ListRuns(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"runs": runs})
	}
}

// handleGetRun returns an http.HandlerFunc that retrieves a single run by ID.
func handleGetRun(rs *store.RunStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		run, err := rs.GetRun(r.Context(), id)
		if err != nil {
			// GetRun wraps sql.ErrNoRows but does not expose it cleanly — treat
			// any error on a bare ID lookup as not-found.
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		respondJSON(w, http.StatusOK, run)
	}
}

// handleListJobs returns an http.HandlerFunc that lists all jobs for a run.
func handleListJobs(js *store.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		jobs, err := js.ListJobsByRun(r.Context(), runID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"jobs": jobs})
	}
}

// handleListAttention returns an http.HandlerFunc that lists pending attention items.
func handleListAttention(as *store.AttentionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := as.ListAllPending(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if items == nil {
			items = []*core.AttentionItem{}
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"items": items})
	}
}

// answerRequest is the JSON body for POST /attention/{id}/answer.
type answerRequest struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
}

// handleAnswerAttention returns an http.HandlerFunc that answers an attention item.
func handleAnswerAttention(as *store.AttentionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var req answerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Answer == "" {
			http.Error(w, "answer is required", http.StatusBadRequest)
			return
		}
		if req.AnsweredBy == "" {
			req.AnsweredBy = "http"
		}

		if err := as.AnswerAttention(r.Context(), id, req.Answer, req.AnsweredBy); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Best-effort: the answer is already persisted; resolution is a convenience.
		//nolint:errcheck // best-effort resolve
		_ = as.ResolveAttention(r.Context(), id)

		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
