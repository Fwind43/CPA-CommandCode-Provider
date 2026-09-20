package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestResponsesAdditionalToolsRoundTrip(t *testing.T) {
	req := pluginapi.ExecutorRequest{Format: "responses", Payload: []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp_a","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]},{"type":"function","name":"dynamic_fn","parameters":{"type":"object"}}]},{"type":"function_call","namespace":"mcp_a","name":"js","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}],"tools":[{"type":"namespace","name":"mcp_b","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","namespace":"mcp_a","name":"js"}}`)}
	p, err := normalizeResponses(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tools) != 3 || len(p.Messages) != 2 {
		t.Fatalf("tools=%d messages=%d", len(p.Tools), len(p.Messages))
	}
	alias := p.Tools[1]["function"].(map[string]any)["name"]
	if alias == p.Tools[0]["function"].(map[string]any)["name"] {
		t.Fatal("namespace collision")
	}
	calls := p.Messages[0]["tool_calls"].([]any)
	if calls[0].(map[string]any)["function"].(map[string]any)["name"] != alias {
		t.Fatal("history mismatch")
	}
	if p.ToolChoice.(map[string]any)["function"].(map[string]any)["name"] != alias {
		t.Fatal("choice mismatch")
	}
	call := map[string]any{"id": "c1", "function": map[string]any{"name": alias, "arguments": "{}"}}
	item := responseToolForRequest(req, call)
	if item["name"] != "js" || item["namespace"] != "mcp_a" || item["call_id"] != "c1" {
		t.Fatal(item)
	}
	var body map[string]any
	if err := json.Unmarshal(buildExecutorCompletion(req, upstreamResult{ToolCalls: []map[string]any{call}}), &body); err != nil {
		t.Fatal(err)
	}
	item = body["output"].([]any)[0].(map[string]any)
	if item["name"] != "js" || item["namespace"] != "mcp_a" {
		t.Fatal(item)
	}
}

func TestResponsesAdditionalToolsValidation(t *testing.T) {
	for _, tools := range []string{`null`, `{}`, `[1]`, `[{"type":"web_search"}]`, `[{"type":"namespace","name":"m","tools":[{"type":"web_search"}]}]`} {
		req := pluginapi.ExecutorRequest{Payload: []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":` + tools + `}]}`)}
		if _, err := normalizeResponses(req); err == nil {
			t.Fatalf("accepted invalid tools: %s", tools)
		}
	}
	req := pluginapi.ExecutorRequest{Payload: []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[]},{"role":"user","content":"hello"}]}`)}
	if p, err := normalizeResponses(req); err != nil || len(p.Messages) != 1 {
		t.Fatalf("empty declarations: %+v %v", p, err)
	}
}
