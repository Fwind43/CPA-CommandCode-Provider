package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"testing"
)

func TestResponsesImageMapping(t *testing.T) {
	req := pluginapi.ExecutorRequest{Format: "openai-response", Model: "test", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"before"},{"type":"input_image","image_url":"data:image/png;base64,aGk=","detail":"high"},{"type":"input_text","text":"after"}]}]}`)}
	p, err := normalizeChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 1 {
		t.Fatalf("messages: %d", len(p.Messages))
	}
	parts, err := chatContentParts(p.Messages[0]["content"])
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0]["text"] != "before" || parts[1]["type"] != "image" || parts[1]["image"] != "aGk=" || parts[1]["mediaType"] != "image/png" || parts[2]["text"] != "after" {
		t.Fatalf("parts: %#v", parts)
	}
	for _, raw := range []string{`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file_x"}]}]}`, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":42}]}]}`} {
		req.Payload = []byte(raw)
		if _, err := normalizeChatRequest(req); err == nil {
			t.Fatal("expected image validation error")
		}
	}
}
