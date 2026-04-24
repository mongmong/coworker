package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chris/coworker/core"
	"github.com/chris/coworker/store"
)

// --- orch_register -----------------------------------------------------------

type registerInput struct {
	Role      string `json:"role"`
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	CLI       string `json:"cli"`
}

type registerOutput struct {
	Handle string `json:"handle"`
	Status string `json:"status"`
}

func handleRegister(ws *store.WorkerStore) mcp.ToolHandlerFor[registerInput, registerOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in registerInput,
	) (*mcp.CallToolResult, registerOutput, error) {
		if in.Role == "" {
			return nil, registerOutput{}, fmt.Errorf("role is required")
		}
		if in.CLI == "" {
			return nil, registerOutput{}, fmt.Errorf("cli is required")
		}

		w := &core.Worker{
			Handle:       core.NewID(),
			Role:         in.Role,
			PID:          in.PID,
			SessionID:    in.SessionID,
			CLI:          in.CLI,
			RegisteredAt: time.Now(),
		}

		if err := ws.Register(ctx, w); err != nil {
			return nil, registerOutput{}, fmt.Errorf("register worker: %w", err)
		}

		return nil, registerOutput{Handle: w.Handle, Status: "registered"}, nil
	}
}

// CallRegister is an exported test helper.
func CallRegister(ctx context.Context, ws *store.WorkerStore, role, cli, sessionID string, pid int) (map[string]interface{}, error) {
	h := handleRegister(ws)
	_, out, err := h(ctx, nil, registerInput{Role: role, PID: pid, SessionID: sessionID, CLI: cli})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_heartbeat ----------------------------------------------------------

type heartbeatInput struct {
	Handle string `json:"handle"`
}

type heartbeatOutput struct {
	Status string `json:"status"`
}

func handleHeartbeat(ws *store.WorkerStore) mcp.ToolHandlerFor[heartbeatInput, heartbeatOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in heartbeatInput,
	) (*mcp.CallToolResult, heartbeatOutput, error) {
		if in.Handle == "" {
			return nil, heartbeatOutput{}, fmt.Errorf("handle is required")
		}

		if err := ws.Heartbeat(ctx, in.Handle); err != nil {
			return nil, heartbeatOutput{}, fmt.Errorf("heartbeat: %w", err)
		}

		return nil, heartbeatOutput{Status: "ok"}, nil
	}
}

// CallHeartbeat is an exported test helper.
func CallHeartbeat(ctx context.Context, ws *store.WorkerStore, handle string) (map[string]interface{}, error) {
	h := handleHeartbeat(ws)
	_, out, err := h(ctx, nil, heartbeatInput{Handle: handle})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}

// --- orch_deregister ---------------------------------------------------------

type deregisterInput struct {
	Handle string `json:"handle"`
}

type deregisterOutput struct {
	Status string `json:"status"`
}

func handleDeregister(ws *store.WorkerStore) mcp.ToolHandlerFor[deregisterInput, deregisterOutput] {
	return func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		in deregisterInput,
	) (*mcp.CallToolResult, deregisterOutput, error) {
		if in.Handle == "" {
			return nil, deregisterOutput{}, fmt.Errorf("handle is required")
		}

		if err := ws.Deregister(ctx, in.Handle); err != nil {
			return nil, deregisterOutput{}, fmt.Errorf("deregister: %w", err)
		}

		return nil, deregisterOutput{Status: "deregistered"}, nil
	}
}

// CallDeregister is an exported test helper.
func CallDeregister(ctx context.Context, ws *store.WorkerStore, handle string) (map[string]interface{}, error) {
	h := handleDeregister(ws)
	_, out, err := h(ctx, nil, deregisterInput{Handle: handle})
	if err != nil {
		return nil, err
	}
	return toMap(out)
}
