package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chris/coworker/core"
	"github.com/spf13/cobra"
)

type watchOptions struct {
	runID string
	kind  string
	port  int
}

func init() {
	rootCmd.AddCommand(newWatchCmd())
}

func newWatchCmd() *cobra.Command {
	opts := &watchOptions{}

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream live runtime events over SSE.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return watchLoop(
				cmd.Context(),
				&http.Client{},
				buildEventsURL(opts.port, opts.runID, opts.kind),
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
			)
		},
	}

	cmd.Flags().StringVar(&opts.runID, "run", "", "Filter to one run ID")
	cmd.Flags().StringVar(&opts.kind, "kind", "", "Filter to one event kind")
	cmd.Flags().IntVar(&opts.port, "port", 7700, "Port for the local coworker SSE server")

	return cmd
}

func buildEventsURL(port int, runID, kind string) string {
	query := url.Values{}
	if runID != "" {
		query.Set("run_id", runID)
	}
	if kind != "" {
		query.Set("kind", kind)
	}

	u := url.URL{
		Scheme:   "http",
		Host:     fmt.Sprintf("localhost:%d", port),
		Path:     "/events",
		RawQuery: query.Encode(),
	}

	return u.String()
}

func watchLoop(ctx context.Context, client *http.Client, streamURL string, out, errOut io.Writer) error {
	backoff := 250 * time.Millisecond

	for {
		startedAt := time.Now()
		err := watchStream(ctx, client, streamURL, out)
		if ctx.Err() != nil {
			return nil
		}

		backoff = watchBackoffAfterStream(backoff, startedAt, time.Now(), err)

		if err != nil && errOut != nil {
			if _, writeErr := fmt.Fprintf(errOut, "coworker watch: %v; reconnecting in %s\n", err, backoff); writeErr != nil {
				return fmt.Errorf("write watch status: %w", writeErr)
			}
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}

		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}
}

func watchBackoffAfterStream(backoff time.Duration, startedAt, endedAt time.Time, err error) time.Duration {
	if err == nil && endedAt.Sub(startedAt) > 10*time.Second {
		return 250 * time.Millisecond
	}
	return backoff
}

func watchStream(ctx context.Context, client *http.Client, streamURL string, out io.Writer) error {
	if client == nil {
		client = &http.Client{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return fmt.Errorf("build watch request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("connect to %s: %w", streamURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("watch %s returned %s: %s", streamURL, resp.Status, strings.TrimSpace(string(body)))
	}

	reader := bufio.NewReader(resp.Body)
	var dataLines []string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(dataLines) > 0 {
					if err := printWatchEvent(out, strings.Join(dataLines, "\n")); err != nil {
						return err
					}
				}
				if ctx.Err() != nil {
					return nil
				}
				return nil
			}
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("read SSE stream: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			if err := printWatchEvent(out, strings.Join(dataLines, "\n")); err != nil {
				return err
			}
			dataLines = dataLines[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		value := strings.TrimPrefix(line, "data:")
		value = strings.TrimPrefix(value, " ")
		dataLines = append(dataLines, value)
	}
}

func printWatchEvent(out io.Writer, raw string) error {
	var event core.Event
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return fmt.Errorf("decode SSE event: %w", err)
	}

	if _, err := fmt.Fprintln(out, formatWatchEvent(&event)); err != nil {
		return fmt.Errorf("write watch output: %w", err)
	}

	return nil
}

func formatWatchEvent(event *core.Event) string {
	ts := "-"
	if !event.CreatedAt.IsZero() {
		ts = event.CreatedAt.UTC().Format(time.RFC3339)
	}

	return fmt.Sprintf(
		"%s %-18s run=%s payload=%s",
		ts,
		string(event.Kind),
		event.RunID,
		summarizePayload(event.Payload),
	)
}

func summarizePayload(payload string) string {
	if strings.TrimSpace(payload) == "" {
		return "{}"
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(payload)); err == nil {
		payload = compact.String()
	}

	if len(payload) > 120 {
		return payload[:117] + "..."
	}

	return payload
}
