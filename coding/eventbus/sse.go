package eventbus

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chris/coworker/core"
)

// SSEHandler streams committed events from the live event bus.
func SSEHandler(bus core.EventBus) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bus == nil {
			http.Error(w, "event bus unavailable", http.StatusServiceUnavailable)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		runIDFilter := r.URL.Query().Get("run_id")
		kindFilter := r.URL.Query().Get("kind")

		sub := make(chan *core.Event, 32)
		bus.Subscribe(sub)
		defer bus.Unsubscribe(sub)

		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-sub:
				if event == nil {
					continue
				}
				if runIDFilter != "" && event.RunID != runIDFilter {
					continue
				}
				if kindFilter != "" && string(event.Kind) != kindFilter {
					continue
				}

				data, err := json.Marshal(event)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}
