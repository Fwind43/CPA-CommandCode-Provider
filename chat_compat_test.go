package main

import (
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatStreamContract(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				params := body["params"].(map[string]any)
				if params["max_tokens"] != float64(45000) || params["temperature"] != float64(0) || params["reasoning_effort"] != "high" {
					t.Errorf("params: %#v", params)
				}
				if _, ok := params["maxOutputTokens"]; ok {
					t.Error("incorrect token key")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"reasoning-delta\",\"text\":\"thinking\"}\n\ndata: {\"type\":\"text-delta\",\"text\":\"answer\"}\n\ndata: {\"type\":\"finish\",\"finishReason\":\"stop\",\"totalUsage\":{\"inputTokens\":2,\"outputTokens\":3}}\n\n")
			}))
			defer srv.Close()
			configMu.Lock()
			old := apiBaseURL
			apiBaseURL = srv.URL
			configMu.Unlock()
			defer func() { configMu.Lock(); apiBaseURL = old; configMu.Unlock() }()
			saved := invokeHost
			defer func() { invokeHost = saved }()
			var chunks []map[string]any
			closes := 0
			invokeHost = func(method string, raw []byte) error {
				if method == pluginabi.MethodHostStreamClose {
					closes++
					return nil
				}
				if method != pluginabi.MethodHostStreamEmit {
					return fmt.Errorf("unexpected method %s", method)
				}
				var wrapper streamEmitRequest
				if err := json.Unmarshal(raw, &wrapper); err != nil {
					return err
				}
				var chunk map[string]any
				if err := json.Unmarshal(wrapper.Payload, &chunk); err != nil {
					return err
				}
				chunks = append(chunks, chunk)
				return nil
			}
			payload := fmt.Sprintf(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":45000,"temperature":0,"reasoning_effort":"high","stream_options":{"include_usage":%t}}`, include)
			produceCommandCodeStream("contract", pluginapi.ExecutorRequest{Model: "test", StorageJSON: []byte(`{"apiKey":"test"}`), Payload: []byte(payload)})
			want := 4
			if include {
				want++
			}
			if closes != 1 || len(chunks) != want {
				t.Fatalf("closes=%d chunks=%#v", closes, chunks)
			}
			for i, c := range chunks {
				if c["id"] != chunks[0]["id"] || c["created"] != chunks[0]["created"] {
					t.Fatal("identity changed")
				}
				usage, exists := c["usage"]
				if exists != include {
					t.Fatalf("usage presence at %d", i)
				}
				if include && i < len(chunks)-1 && usage != nil {
					t.Fatal("usage must be null until last chunk")
				}
			}
			delta := func(i int) map[string]any {
				return chunks[i]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
			}
			if delta(0)["role"] != "assistant" || delta(1)["reasoning_content"] != "thinking" || delta(2)["content"] != "answer" {
				t.Fatalf("bad deltas: %#v", chunks)
			}
			if include && len(chunks[len(chunks)-1]["choices"].([]any)) != 0 {
				t.Fatal("usage choices must be empty")
			}
		})
	}
}

func TestChatContentParts(t *testing.T) {
	var content any
	json.Unmarshal([]byte(`[{"type":"text","text":"before"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}},{"type":"text","text":"after"}]`), &content)
	parts, err := chatContentParts(content)
	if err != nil || len(parts) != 3 {
		t.Fatalf("%v %#v", err, parts)
	}
	if parts[1]["image"] != "aGk=" || parts[1]["mediaType"] != "image/png" || parts[2]["text"] != "after" {
		t.Fatal(parts)
	}
	for _, raw := range []string{`[{"type":"input_audio"}]`, `[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]`, `[{"type":"image_url","image_url":{"url":"data:image/png;base64,???"}}]`, `42`} {
		json.Unmarshal([]byte(raw), &content)
		if _, err := chatContentParts(content); err == nil {
			t.Fatalf("silently accepted %s", raw)
		}
	}
}
