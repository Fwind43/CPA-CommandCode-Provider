package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamSSEIncrementalAndDone(t *testing.T) {
	for _, prefix := range []string{"data:", "data: "} {
		t.Run(prefix, func(t *testing.T) {
			release := make(chan struct{})
			delta := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, ": heartbeat\n\n%s{\"type\":\"text-delta\",\"text\":\"hello \"}\n\n", prefix)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				fmt.Fprintf(w, "%s{\"type\":\"text-delta\",\"text\":\"world\"}\n\n%s[DONE]\n\n", prefix, prefix)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			configMu.Lock()
			prev := apiBaseURL
			apiBaseURL = server.URL
			configMu.Unlock()
			defer func() { configMu.Lock(); apiBaseURL = prev; configMu.Unlock() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				result, err := callUpstream(ctx, "test", "test", chatRequestPayload{Messages: []map[string]any{{"role": "user", "content": "hi"}}}, func(s string) error { delta <- s; return nil })
				if err == nil && result.Text != "hello world" {
					err = fmt.Errorf("text = %q", result.Text)
				}
				done <- err
			}()
			select {
			case s := <-delta:
				if s != "hello " {
					t.Errorf("first delta = %q", s)
				}
			case <-ctx.Done():
				t.Fatal("first delta not delivered before upstream completion")
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("DONE did not terminate open connection")
			}
		})
	}
}

func TestStreamHostPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data:{\"type\":\"text-delta\",\"text\":\"hello \"}\n\ndata:[DONE]\n\n")
	}))
	defer server.Close()
	configMu.Lock()
	prev := apiBaseURL
	apiBaseURL = server.URL
	configMu.Unlock()
	oldHost := invokeHost
	defer func() { invokeHost = oldHost; configMu.Lock(); apiBaseURL = prev; configMu.Unlock() }()
	var text string
	var finished bool
	closes := 0
	invokeHost = func(method string, raw []byte) error {
		switch method {
		case pluginabi.MethodHostStreamEmit:
			var req streamEmitRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return err
			}
			if req.StreamID != "test-stream" {
				t.Errorf("stream ID = %s", req.StreamID)
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(req.Payload, &chunk); err != nil {
				return fmt.Errorf("expected bare JSON: %w", err)
			}
			for _, choice := range chunk.Choices {
				text += choice.Delta.Content
				if choice.FinishReason != nil {
					finished = *choice.FinishReason == "stop"
				}
			}
		case pluginabi.MethodHostStreamClose:
			var req streamCloseRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return err
			}
			if req.Error != "" {
				t.Errorf("stream closed with error: %s", req.Error)
			}
			closes++
		default:
			return fmt.Errorf("unexpected host method %s", method)
		}
		return nil
	}
	produceCommandCodeStream("test-stream", pluginapi.ExecutorRequest{Model: "test", StorageJSON: []byte(`{"apiKey":"test"}`), Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)})
	if text != "hello " || !finished || closes != 1 {
		t.Fatalf("text=%q finished=%v closes=%d", text, finished, closes)
	}
}

func TestStreamSystemRoles(t *testing.T) {
 server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  var body struct { Params struct { System string `json:"system"`; Messages []map[string]any `json:"messages"` } `json:"params"` }
  if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Error(err) }
  if body.Params.System != "instruction\n\ndeveloper instruction" { t.Errorf("system=%q", body.Params.System) }
  if len(body.Params.Messages) != 2 { t.Errorf("messages=%v", body.Params.Messages) }
  for _, m := range body.Params.Messages { if m["role"] != "user" && m["role"] != "assistant" { t.Errorf("invalid role=%v", m["role"]) } }
  fmt.Fprint(w, "data:[DONE]\n\n")
 }))
 defer server.Close()
 configMu.Lock(); prev := apiBaseURL; apiBaseURL = server.URL; configMu.Unlock()
 defer func(){configMu.Lock(); apiBaseURL = prev; configMu.Unlock()}()
 _, err := callUpstream(context.Background(), "test", "test", chatRequestPayload{Messages: []map[string]any{
 {"role":"system", "content":"instruction"},
 {"role":"developer", "content": []any{map[string]any{"type":"text","text":"developer instruction"}}},
 {"role":"user", "content":"hello"}, {"role":"assistant", "content":"hi"},
 }}, nil)
 if err != nil {t.Fatal(err)}
}

func TestStreamToolRoundTrip(t *testing.T) {
 server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  var body map[string]any
  if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil {t.Error(err)}
  params:=body["params"].(map[string]any)
  tools:=params["tools"].([]any)
  if len(tools)!=1 || tools[0].(map[string]any)["name"]!="lookup" {t.Errorf("tools=%v",tools)}
  messages:=params["messages"].([]any)
  if len(messages)!=3 {t.Errorf("messages=%v",messages)} else {
   call:=messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
   result:=messages[2].(map[string]any)
   part:=result["content"].([]any)[0].(map[string]any)
   if call["type"]!="tool-call" || result["role"]!="tool" || part["toolName"]!="lookup" || part["toolCallId"]!="c1" {t.Errorf("history=%v",messages)}
  }
  fmt.Fprint(w,`data: {"type":"tool-call","toolCallId":"c2","toolName":"lookup","input":{"q":"a b"}}

data: {"type":"finish","finishReason":"tool-calls"}

`)
 }))
 defer server.Close()
 configMu.Lock();prev:=apiBaseURL;apiBaseURL=server.URL;configMu.Unlock()
 defer func(){configMu.Lock();apiBaseURL=prev;configMu.Unlock()}()
 var payload chatRequestPayload
 err:=json.Unmarshal([]byte(`{"tools":[{"type":"function","function":{"name":"lookup","description":"lookup","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"result"}]}`),&payload)
 if err!=nil {t.Fatal(err)}
 emitted:=0
 payload.onToolCall=func(call map[string]any)error{emitted++;if call["index"]!=0 || call["id"]!="c2" {t.Errorf("delta=%v",call)};return nil}
 result,err:=callUpstream(context.Background(),"test","test",payload,nil)
 if err!=nil {t.Fatal(err)}
 if emitted!=1 || len(result.ToolCalls)!=1 || result.FinishReason!="tool_calls" {t.Fatalf("result=%+v emitted=%d",result,emitted)}
 var completion map[string]any
 if err=json.Unmarshal(buildChatCompletion("test",result),&completion);err!=nil {t.Fatal(err)}
 choice:=completion["choices"].([]any)[0].(map[string]any)
 if choice["message"].(map[string]any)["tool_calls"]==nil || choice["finish_reason"]!="tool_calls" {t.Fatalf("completion=%v",completion)}
}
