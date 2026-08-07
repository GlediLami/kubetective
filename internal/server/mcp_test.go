package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/record"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

func mcpTestServer(t *testing.T) *MCPServer {
	return &MCPServer{
		Inv:   &scriptedInvestigator{res: testResult()},
		Store: record.NewStore(t.TempDir()),
		Replay: func(_ context.Context, _ string, _ model.ResourceRef) (*api.InvestigationResult, error) {
			return testResult(), nil
		},
	}
}
func call(t *testing.T, m *MCPServer, msg string) map[string]any {
	t.Helper()
	out, err := m.HandleMessage([]byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("response %q: %v", out, err)
	}
	return r
}

func TestMCPInitialize(t *testing.T) {
	r := call(t, mcpTestServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test"}}}`)
	if r["result"] == nil || r["error"] != nil {
		t.Fatalf("initialize = %+v", r)
	}
	res := r["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "kubedoctor" {
		t.Errorf("serverInfo = %v", info)
	}
}

func TestMCPToolsList(t *testing.T) {
	r := call(t, mcpTestServer(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := r["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"investigate", "replay", "list_incidents", "read_incident", "action_preview"} {
		if !names[want] {
			t.Errorf("tool %q missing", want)
		}
	}
}

func TestMCPToolCallInvestigate(t *testing.T) {
	r := call(t, mcpTestServer(t), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"investigate","arguments":{"target":"deployment/checkout","namespace":"prod"}}}`)
	content := r["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	var res struct {
		Incident struct {
			Status string `json:"status"`
			Target struct {
				Name string `json:"name"`
			} `json:"target"`
		} `json:"incident"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("tool text = %q: %v", text, err)
	}
	if res.Incident.Status != "OOMKILLED" || res.Incident.Target.Name != "checkout" {
		t.Errorf("tool result = %+v", res)
	}
}

func TestMCPToolCallErrors(t *testing.T) {
	// Unknown tool → protocol error -32602.
	r := call(t, mcpTestServer(t), `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if r["error"] == nil {
		t.Fatalf("unknown tool must error, got %+v", r)
	}
	// Bad target → isError result, not protocol error.
	r = call(t, mcpTestServer(t), `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"investigate","arguments":{"target":"bogus"}}}`)
	res := r["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("bad target: isError = %v", res["isError"])
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "error") {
		t.Errorf("bad target text = %q", text)
	}
}

func TestMCPNotificationsAndUnknownMethod(t *testing.T) {
	m := mcpTestServer(t)
	// Notifications → no response.
	out, err := m.HandleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil || out != nil {
		t.Fatalf("notification response = %q, %v (want nil)", out, err)
	}
	// Unknown method → -32601.
	r := call(t, m, `{"jsonrpc":"2.0","id":6,"method":"bogus"}`)
	if r["error"].(map[string]any)["code"] != float64(-32601) {
		t.Errorf("unknown method = %+v", r["error"])
	}
	// Malformed JSON → parse error.
	out, _ = m.HandleMessage([]byte(`{not json`))
	var perr map[string]any
	if err := json.Unmarshal(out, &perr); err != nil {
		t.Fatal(err)
	}
	if perr["error"].(map[string]any)["code"] != float64(-32700) {
		t.Errorf("parse error = %+v", perr["error"])
	}
}

func TestMCPListIncidents(t *testing.T) {
	m := mcpTestServer(t)
	inc := &model.Incident{ID: "inc-9", Meta: model.IncidentMeta{Target: "deployment/prod/x"}}
	if _, err := m.Store.Save(inc); err != nil {
		t.Fatal(err)
	}
	r := call(t, m, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_incidents"}}`)
	text := r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "inc-9") {
		t.Errorf("list = %q", text)
	}
}

func TestMCPReadAndPreviewIncident(t *testing.T) {
	m := mcpTestServer(t)
	inc := &model.Incident{ID: "inc-7", Meta: model.IncidentMeta{Target: "deployment/prod/checkout"}}
	if _, err := m.Store.Save(inc); err != nil {
		t.Fatal(err)
	}
	// read_incident
	r := call(t, m, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"read_incident","arguments":{"incident_id":"inc-7"}}}`)
	text := r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "inc-7") {
		t.Errorf("read = %q", text)
	}
	// action_preview
	r = call(t, m, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"action_preview","arguments":{"incident_id":"inc-7"}}}`)
	text = r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "actions") {
		t.Errorf("preview = %q", text)
	}
}
