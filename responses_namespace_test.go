package main

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"testing"
)

func TestResponsesNamespaceRoundTrip(t *testing.T) {
	req := pluginapi.ExecutorRequest{Format: "responses", Payload: []byte(`{"input":[{"type":"function_call","namespace":"mcp_a","name":"js","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}],"tools":[{"type":"namespace","name":"mcp_a","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]},{"type":"namespace","name":"mcp_b","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","namespace":"mcp_a","name":"js"}}`)}
	p, err := normalizeResponses(req)
	if err != nil {
		t.Fatal(err)
	}
	a := p.Tools[0]["function"].(map[string]any)["name"]
	b := p.Tools[1]["function"].(map[string]any)["name"]
	if a == b {
		t.Fatal("namespace collision")
	}
	calls := p.Messages[0]["tool_calls"].([]any)
	if calls[0].(map[string]any)["function"].(map[string]any)["name"] != a {
		t.Fatal("history alias mismatch")
	}
	if p.ToolChoice.(map[string]any)["function"].(map[string]any)["name"] != a {
		t.Fatal("choice alias mismatch")
	}
	call := map[string]any{"id": "c1", "function": map[string]any{"name": a, "arguments": "{}"}}
	item := responseToolForRequest(req, call)
	if item["namespace"] != "mcp_a" || item["name"] != "js" || item["call_id"] != "c1" {
		t.Fatal(item)
	}
	var body map[string]any
	if err = json.Unmarshal(buildExecutorCompletion(req, upstreamResult{ToolCalls: []map[string]any{call}}), &body); err != nil {
		t.Fatal(err)
	}
	item = body["output"].([]any)[0].(map[string]any)
	if item["namespace"] != "mcp_a" || item["name"] != "js" {
		t.Fatal(item)
	}
}

func TestResponsesNamespaceRejectUnsupported(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"namespace","name":"a","tools":[{"type":"web_search"}]}]`,
		`[{"type":"namespace","tools":[]}]`,
		`[{"type":"namespace","name":"a","tools":[{"type":"function","name":"js"},{"type":"function","name":"js"}]}]`,
	} {
		var tools []map[string]any
		json.Unmarshal([]byte(raw), &tools)
		if _, err := flattenResponseTools(tools); err == nil {
			t.Fatal("expected rejection", raw)
		}
	}
}
