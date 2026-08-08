package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GlediLami/kubetective/internal/action"
	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// MCP protocol version we speak.
const mcpProtocolVersion = "2024-11-05"

// MCPServer is a thin MCP wrapper over the same pipeline the CLI and REST
// server use: a hand-rolled minimal JSON-RPC 2.0 server (initialize,
// tools/list, tools/call, ping) speaking the Model Context Protocol over
// stdio.
//
// Tools are read-only: investigate, replay, list_incidents, read_incident,
// action_preview. There is deliberately no apply tool - remediation stays
// human-gated in the CLI.
type MCPServer struct {
	Inv   api.Investigator
	Store *record.Store
	// Replay re-runs a recorded incident; wired by the CLI with a
	// replay-backed engine. Required by the replay/action_preview tools.
	Replay func(ctx context.Context, incidentID string, target model.ResourceRef) (*api.InvestigationResult, error)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// HandleMessage processes one JSON-RPC message (one line of stdio) and
// returns the response bytes, or nil for notifications. Protocol errors are
// returned as JSON-RPC error responses, never as Go errors.
func (m *MCPServer) HandleMessage(msg []byte) ([]byte, error) {
	var req rpcRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return marshalRPC(json.RawMessage("null"), nil, &rpcError{Code: -32700, Message: "parse error: " + err.Error()}), nil
	}
	if req.Method == "" {
		return marshalRPC(req.ID, nil, &rpcError{Code: -32600, Message: "invalid request"}), nil
	}
	// Notifications carry no id → no response.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil, nil
	}

	switch req.Method {
	case "initialize":
		return marshalRPC(req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "kubetective", "version": engine.Version},
		}, nil), nil
	case "ping":
		out := marshalRPC(req.ID, map[string]any{}, nil)
		return out, nil
	case "tools/list":
		out := marshalRPC(req.ID, map[string]any{"tools": m.tools()}, nil)
		return out, nil
	case "tools/call":
		return m.handleToolCall(req.ID, req.Params)
	default:
		out := marshalRPC(req.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
		return out, nil
	}
}

// marshalRPC serializes a response; json.Marshal on this shape cannot fail.
func marshalRPC(id json.RawMessage, result any, rpcErr *rpcError) []byte {
	out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
	return out
}

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (m *MCPServer) tools() []toolSpec {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return []toolSpec{
		{Name: "investigate", Description: "Run an investigation against the cluster and return the full result",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"target":    str("resource to investigate, e.g. deployment/checkout"),
				"namespace": str("namespace (optional)"),
				"since":     str("window, e.g. 30m, 2h (optional)"),
			}, "required": []string{"target"}}},
		{Name: "replay", Description: "Replay a recorded incident through the current engine",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"incident_id": str("incident id or path to a .jsonl record"),
			}, "required": []string{"incident_id"}}},
		{Name: "list_incidents", Description: "List recorded incident ids, newest first",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "read_incident", Description: "Read a full incident record",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"incident_id": str("incident id or path"),
			}, "required": []string{"incident_id"}}},
		{Name: "action_preview", Description: "Preview remediation actions for a recorded incident (read-only, no cluster mutation)",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"incident_id": str("incident id or path"),
			}, "required": []string{"incident_id"}}},
	}
}

func (m *MCPServer) handleToolCall(id json.RawMessage, params json.RawMessage) ([]byte, error) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return marshalRPC(id, nil, &rpcError{Code: -32602, Message: "invalid params"}), nil
	}

	textResult := func(v any, isErr bool) ([]byte, error) {
		out, err := json.Marshal(v)
		if err != nil {
			out = []byte(fmt.Sprintf("{\"error\":%q}", err.Error()))
			isErr = true
		}
		content := []map[string]any{{"type": "text", "text": string(out)}}
		return marshalRPC(id, map[string]any{"content": content, "isError": isErr}, nil), nil
	}

	ctx := context.Background()
	switch call.Name {
	case "investigate":
		target, ns := strArg(call.Arguments, "target"), strArg(call.Arguments, "namespace")
		ref, err := parseRef(target, ns)
		if err != nil {
			return textResult(map[string]string{"error": err.Error()}, true)
		}
		req := &api.InvestigationRequest{Target: ref}
		if since := strArg(call.Arguments, "since"); since != "" {
			if d, derr := time.ParseDuration(since); derr == nil {
				req.Window = api.Since(d)
			}
		}
		res, err := m.Inv.Investigate(ctx, req)
		if err != nil {
			return textResult(map[string]string{"error": err.Error()}, true)
		}
		return textResult(res, false)
	case "replay", "read_incident", "action_preview":
		inc, err := m.loadIncident(strArg(call.Arguments, "incident_id"))
		if err != nil {
			return textResult(map[string]string{"error": err.Error()}, true)
		}
		switch call.Name {
		case "read_incident":
			return textResult(inc, false)
		case "action_preview":
			target, terr := parseRef(inc.Meta.Target, "")
			if terr != nil {
				target = model.ResourceRef{Kind: "pod", Name: "scenario"}
			}
			if m.Replay == nil {
				return textResult(map[string]string{"error": "replay not wired (no incident store-backed engine)"}, true)
			}
			res, ierr := m.Replay(ctx, inc.ID, target)
			if ierr != nil {
				return textResult(map[string]string{"error": ierr.Error()}, true)
			}
			return textResult(map[string]any{"actions": action.Plan(res)}, false)
		default: // replay - via the CLI-wired Replay hook
			target, terr := parseRef(inc.Meta.Target, "")
			if terr != nil {
				target = model.ResourceRef{Kind: "pod", Name: "scenario"}
			}
			if m.Replay == nil {
				return textResult(map[string]string{"error": "replay not wired (no incident store-backed engine)"}, true)
			}
			res, ierr := m.Replay(ctx, inc.ID, target)
			if ierr != nil {
				return textResult(map[string]string{"error": ierr.Error()}, true)
			}
			return textResult(res, false)
		}
	case "list_incidents":
		if m.Store == nil {
			return textResult(map[string]string{"error": "no incident store configured"}, true)
		}
		ids, err := m.Store.List()
		if err != nil {
			return textResult(map[string]string{"error": err.Error()}, true)
		}
		return textResult(map[string]any{"incidents": ids}, false)
	default:
		return marshalRPC(id, nil, &rpcError{Code: -32602, Message: "unknown tool: " + call.Name}), nil
	}
}

func (m *MCPServer) loadIncident(id string) (*model.Incident, error) {
	if m.Store == nil {
		return nil, fmt.Errorf("no incident store configured")
	}
	return m.Store.Load(id)
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}
