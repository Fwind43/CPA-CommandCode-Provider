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

func TestResponsesCustomTools(t *testing.T) {
	for _, ns := range []string{"", "editor"} {
		for _, rawInput := range []bool{false, true} {
			t.Run(fmt.Sprintf("namespace=%s/raw=%t", ns, rawInput), func(t *testing.T) {
				tool := map[string]any{"type": "custom", "name": "patch", "format": map[string]any{"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}}
				var decl any = tool
				if ns != "" {
					decl = map[string]any{"type": "namespace", "name": ns, "tools": []any{tool}}
				}
				input := "*** Begin Patch\n\"hello\"\\world\n*** End Patch"
				history := []any{map[string]any{"type": "additional_tools", "tools": []any{decl}}, map[string]any{"type": "custom_tool_call", "name": "patch", "namespace": ns, "call_id": "old", "input": input}, map[string]any{"type": "custom_tool_call_output", "call_id": "old", "output": "ok"}}
				payload, _ := json.Marshal(map[string]any{"input": history, "tool_choice": map[string]any{"type": "custom", "name": "patch", "namespace": ns}})
				req := pluginapi.ExecutorRequest{Format: "responses", Model: "test", StorageJSON: []byte(`{"apiKey":"test"}`), Payload: payload}
				p, err := normalizeResponses(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(p)
				if len(p.Messages) != 2 || len(p.Tools) != 1 || !strings.Contains(string(raw), "Input format specification") {
					t.Fatalf("%s", raw)
				}
				calls := p.Messages[0]["tool_calls"].([]any)
				fn := calls[0].(map[string]any)["function"].(map[string]any)
				var wrapped map[string]string
				if err := json.Unmarshal([]byte(fn["arguments"].(string)), &wrapped); err != nil || wrapped["input"] != input {
					t.Fatal(fn, err)
				}
				if fn["name"] != namespaceToolName(ns, "patch") {
					t.Fatal(fn)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var upstreamInput any = map[string]any{"input": input}
					if rawInput {
						upstreamInput = input
					}
					data, _ := json.Marshal(map[string]any{"type": "tool-call", "toolCallId": "new", "toolName": namespaceToolName(ns, "patch"), "input": upstreamInput})
					fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"finish\",\"finishReason\":\"tool-calls\"}\n\n", data)
				}))
				defer server.Close()
				configMu.Lock()
				prev := apiBaseURL
				apiBaseURL = server.URL
				configMu.Unlock()
				defer func() { configMu.Lock(); apiBaseURL = prev; configMu.Unlock() }()
				check := func(item map[string]any) {
					if item["type"] != "custom_tool_call" || item["input"] != input || item["name"] != "patch" || item["call_id"] != "new" {
						t.Fatal(item)
					}
					if _, ok := item["arguments"]; ok {
						t.Fatal(item)
					}
					if ns != "" && item["namespace"] != ns {
						t.Fatal(item)
					}
				}
				var body map[string]any
				if err := json.Unmarshal(executeCommandCode(req).Payload, &body); err != nil {
					t.Fatal(err)
				}
				output, ok := body["output"].([]any)
				if !ok || len(output) != 1 {
					t.Fatalf("expected one custom tool call, got %v", body)
				}
				check(output[0].(map[string]any))
				seen := map[string]int{}
				produceResponses(req, func(raw []byte) error {
					var e map[string]any
					if err := json.Unmarshal([]byte(strings.TrimSpace(strings.SplitN(string(raw), "\ndata: ", 2)[1])), &e); err != nil {
						return err
					}
					kind := e["type"].(string)
					seen[kind]++
					if strings.Contains(kind, "function_call_arguments") {
						t.Fatal(kind)
					}
					if kind == "response.custom_tool_call_input.delta" && e["delta"] != input {
						t.Fatal(e)
					}
					if kind == "response.custom_tool_call_input.done" && e["input"] != input {
						t.Fatal(e)
					}
					if kind == "response.output_item.done" {
						check(e["item"].(map[string]any))
					}
					return nil
				}, func(msg string) {
					if msg != "" {
						t.Error(msg)
					}
				})
				for _, kind := range []string{"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "response.completed"} {
					if seen[kind] != 1 {
						t.Fatal(seen)
					}
				}
			})
		}
	}
}
