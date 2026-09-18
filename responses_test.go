package main

import (
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesNormalize(t *testing.T) {
	req := pluginapi.ExecutorRequest{Format: "openai-response", Model: "test", Payload: []byte(`{"instructions":"be helpful","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"max_output_tokens":99}`)}
	p, err := normalizeChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 4 || len(p.Tools) != 1 || p.MaxTokens != 99 {
		t.Fatalf("%+v", p)
	}
	for _, input := range []string{`{"input":"hi","previous_response_id":"resp_x"}`, `{"input":[{"role":"user","content":[{"type":"input_image"}]}]}`, `{"input":"hi","tools":[{"type":"web_search"}]}`} {
		req.Payload = []byte(input)
		if _, err = normalizeChatRequest(req); err == nil {
			t.Fatal("expected rejection", input)
		}
	}
}
func TestResponsesExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"type":"text-delta","delta":"hello"}

data: {"type":"tool-call","toolCallId":"c2","toolName":"lookup","input":{"q":"hello"}}

data: {"type":"finish","finishReason":"tool-calls","usage":{"inputTokens":10,"outputTokens":2}}

`)
	}))
	defer server.Close()
	configMu.Lock()
	prev := apiBaseURL
	apiBaseURL = server.URL
	configMu.Unlock()
	defer func() { configMu.Lock(); apiBaseURL = prev; configMu.Unlock() }()
	req := pluginapi.ExecutorRequest{Format: "openai-response", Model: "test", StorageJSON: []byte(`{"apiKey":"test"}`), Payload: []byte(`{"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)}
	var body map[string]any
	if err := json.Unmarshal(executeCommandCode(req).Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "response" || len(body["output"].([]any)) != 2 {
		t.Fatalf("%v", body)
	}
	seq := 0
	closed := 0
	seen := map[string]int{}
	rid := ""
	produceResponses(req, func(raw []byte) error {
		var e map[string]any
		parts := strings.SplitN(string(raw), "\ndata: ", 2)
		if len(parts) != 2 || !strings.HasSuffix(parts[1], "\n\n") {
			t.Fatalf("invalid SSE frame: %q", raw)
		}
		if err := json.Unmarshal([]byte(strings.TrimSuffix(parts[1], "\n\n")), &e); err != nil {
			return err
		}
		if e["sequence_number"] != float64(seq) {
			t.Fatalf("sequence %v", e)
		}
		seq++
		kind := e["type"].(string)
		if parts[0] != "event: "+kind {
			t.Fatal("event type mismatch")
		}
		seen[kind]++
		if response, ok := e["response"].(map[string]any); ok {
			if rid == "" {
				rid = response["id"].(string)
			}
			if response["id"] != rid {
				t.Fatal("unstable response id")
			}
		}
		if kind == "response.completed" {
			response := e["response"].(map[string]any)
			if len(response["output"].([]any)) != 2 {
				t.Fatalf("%v", response)
			}
		}
		return nil
	}, func(msg string) {
		closed++
		if msg != "" {
			t.Error(msg)
		}
	})
	for _, kind := range []string{"response.created", "response.in_progress", "response.output_text.delta", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.completed"} {
		if seen[kind] != 1 {
			t.Error(kind, seen)
		}
	}
	if closed != 1 || seen["response.output_item.done"] != 2 {
		t.Fatal(closed, seen)
	}
}
func TestResponsesIncompleteAndUsage(t *testing.T) {
	var body map[string]any
	json.Unmarshal(buildExecutorCompletion(pluginapi.ExecutorRequest{Format: "openai-response"}, upstreamResult{Text: "x", FinishReason: "length", Usage: map[string]any{"inputTokens": 10, "outputTokens": 2, "cacheReadTokens": 4}}), &body)
	if body["status"] != "incomplete" {
		t.Fatal(body)
	}
	u := body["usage"].(map[string]any)
	if u["input_tokens"] != float64(10) || u["input_tokens_details"].(map[string]any)["cached_tokens"] != float64(4) {
		t.Fatal(u)
	}
}
