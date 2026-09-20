package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"time"
)

func isResponsesFormat(format string) bool {
	return format == "openai-response" || format == "responses"
}

// Responses is stateless: callers must supply conversation history explicitly.
func normalizeResponses(req pluginapi.ExecutorRequest) (chatRequestPayload, error) {
	var r struct {
		Input        json.RawMessage  `json:"input"`
		Instructions string           `json:"instructions"`
		Previous     string           `json:"previous_response_id"`
		Conversation any              `json:"conversation"`
		Background   bool             `json:"background"`
		Tools        []map[string]any `json:"tools"`
		ToolChoice   any              `json:"tool_choice"`
		MaxTokens    int              `json:"max_output_tokens"`
	}
	var p chatRequestPayload
	if err := json.Unmarshal(req.Payload, &r); err != nil {
		return p, err
	}
	if r.Previous != "" || r.Conversation != nil || r.Background {
		return p, fmt.Errorf("Responses requires explicit input history; previous_response_id, conversation and background are unsupported")
	}
	if r.Instructions != "" {
		p.Messages = append(p.Messages, map[string]any{"role": "system", "content": r.Instructions})
	}
	var text string
	if json.Unmarshal(r.Input, &text) == nil {
		p.Messages = append(p.Messages, map[string]any{"role": "user", "content": text})
	} else {
		var items []map[string]any
		if err := json.Unmarshal(r.Input, &items); err != nil {
			return p, fmt.Errorf("input must be a string or item array")
		}
		for _, item := range items {
			switch item["type"] {
			case "additional_tools":
				// Tool declarations are metadata, not conversation messages.
				if _, err := additionalResponseTools(item); err != nil {
					return p, err
				}
			case "custom_tool_call":
				input, ok := item["input"].(string)
				if !ok {
					return p, fmt.Errorf("custom_tool_call requires string input")
				}
				args, _ := json.Marshal(map[string]any{"input": input})
				p.Messages = append(p.Messages, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": namespaceToolName(firstString(item, "namespace"), firstString(item, "name")), "arguments": string(args)}}}})
			case "function_call":
				p.Messages = append(p.Messages, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": namespaceToolName(firstString(item, "namespace"), firstString(item, "name")), "arguments": item["arguments"]}}}})
			case "function_call_output", "custom_tool_call_output":
				p.Messages = append(p.Messages, map[string]any{"role": "tool", "tool_call_id": item["call_id"], "content": item["output"]})
			case nil, "message":
				content := item["content"]
				if parts, ok := content.([]any); ok {
					for _, part := range parts {
						m, ok := part.(map[string]any)
						if !ok {
							return p, fmt.Errorf("invalid content part")
						}
						switch m["type"] {
						case "input_text", "output_text":
							m["type"] = "text"
						case "input_image":
							url, ok := m["image_url"].(string)
							if !ok || url == "" {
								return p, fmt.Errorf("input_image requires image_url; file_id is unsupported")
							}
							image := map[string]any{"url": url}
							if detail, exists := m["detail"]; exists {
								image["detail"] = detail
							}
							m["type"] = "image_url"
							m["image_url"] = image
						default:
							return p, fmt.Errorf("unsupported Responses content type: %v", m["type"])
						}
					}
				}
				role, _ := item["role"].(string)
				if role == "" {
					role = "user"
				}
				p.Messages = append(p.Messages, map[string]any{"role": role, "content": content})
			default:
				return p, fmt.Errorf("unsupported Responses input item: %v", item["type"])
			}
		}
	}
	tools, toolsErr := responseToolsForRequest(req.Payload)
	if toolsErr != nil {
		return p, toolsErr
	}
	p.Tools, toolsErr = flattenResponseTools(tools)
	if toolsErr != nil {
		return p, toolsErr
	}
	p.ToolChoice = r.ToolChoice
	if choice, ok := r.ToolChoice.(map[string]any); ok && (choice["type"] == "function" || choice["type"] == "custom") {
		p.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": namespaceToolName(firstString(choice, "namespace"), firstString(choice, "name"))}}
	}
	p.MaxTokens = r.MaxTokens
	p.Model = req.Model
	raw, _ := json.Marshal(p)
	req.Payload = raw
	req.Format = "chat-completions"
	return normalizeChatRequest(req)
}
func responseMessage(id, text, status string) map[string]any {
	return map[string]any{"id": id, "type": "message", "role": "assistant", "status": status, "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}
func responseTool(call map[string]any) map[string]any {
	fn, _ := call["function"].(map[string]any)
	return map[string]any{"id": "fc_" + fmt.Sprint(call["id"]), "type": "function_call", "call_id": call["id"], "name": fn["name"], "arguments": fn["arguments"], "status": "completed"}
}
func responseObject(id, model, status string, output []any, usage map[string]any) map[string]any {
	return map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "model": model, "status": status, "output": output, "usage": usage, "error": nil, "incomplete_details": nil, "parallel_tool_calls": true}
}
func responsesUsage(r upstreamResult) map[string]any {
	u := usagePayload(r.Usage, r.Text)
	if u["prompt_tokens_details"] == nil {
		u["prompt_tokens_details"] = map[string]any{"cached_tokens": 0}
	}
	return map[string]any{"input_tokens": u["prompt_tokens"], "output_tokens": u["completion_tokens"], "total_tokens": u["total_tokens"], "input_tokens_details": u["prompt_tokens_details"], "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
}
func buildExecutorCompletion(req pluginapi.ExecutorRequest, r upstreamResult) []byte {
	if !isResponsesFormat(req.Format) {
		return buildChatCompletion(req.Model, r)
	}
	out := []any{}
	if r.Text != "" {
		out = append(out, responseMessage("msg_"+randomState()[:24], r.Text, "completed"))
	}
	for _, c := range r.ToolCalls {
		out = append(out, responseToolForRequest(req, c))
	}
	body := responseObject("resp_"+randomState()[:24], req.Model, "completed", out, responsesUsage(r))
	markResponseLimit(body, r)
	raw, _ := json.Marshal(body)
	return raw
}
func markResponseLimit(body map[string]any, r upstreamResult) {
	if r.FinishReason == "length" || r.FinishReason == "max_tokens" {
		body["status"] = "incomplete"
		body["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
}
func produceResponses(req pluginapi.ExecutorRequest, emit func([]byte) error, closeStream func(string)) {
	seq := 0
	event := func(kind string, fields map[string]any) error {
		fields["type"] = kind
		fields["sequence_number"] = seq
		seq++
		raw, _ := json.Marshal(fields)
		return emit([]byte("event: " + kind + "\ndata: " + string(raw) + "\n\n"))
	}
	run := func() error {
		if apiKeyFromStorage(req.StorageJSON) == "" {
			return fmt.Errorf("missing Command Code apiKey in stored auth")
		}
		p, err := normalizeResponses(req)
		if err != nil {
			return err
		}
		id := "resp_" + randomState()[:24]
		out := []any{}
		for _, kind := range []string{"response.created", "response.in_progress"} {
			if err = event(kind, map[string]any{"response": responseObject(id, req.Model, "in_progress", []any{}, nil)}); err != nil {
				return err
			}
		}
		textID := "msg_" + randomState()[:24]
		textIndex := -1
		text := ""
		p.onToolCall = func(c map[string]any) error {
			item := responseToolForRequest(req, c)
			idx := len(out)
			out = append(out, item)
			added := map[string]any{}
			for k, v := range item {
				added[k] = v
			}
			field, eventBase := "arguments", "response.function_call_arguments"
			if item["type"] == "custom_tool_call" {
				field, eventBase = "input", "response.custom_tool_call_input"
			}
			added[field] = ""
			added["status"] = "in_progress"
			if err := event("response.output_item.added", map[string]any{"output_index": idx, "item": added}); err != nil {
				return err
			}
			for _, kind := range []string{eventBase + ".delta", eventBase + ".done"} {
				f := map[string]any{"output_index": idx, "item_id": item["id"]}
				if kind == eventBase+".delta" {
					f["delta"] = item[field]
				} else {
					f[field] = item[field]
				}
				if err := event(kind, f); err != nil {
					return err
				}
			}
			return event("response.output_item.done", map[string]any{"output_index": idx, "item": item})
		}
		r, err := callUpstream(context.Background(), apiKeyFromStorage(req.StorageJSON), req.Model, p, func(delta string) error {
			if textIndex < 0 {
				textIndex = len(out)
				out = append(out, responseMessage(textID, "", "in_progress"))
				item := responseMessage(textID, "", "in_progress")
				item["content"] = []any{}
				if err := event("response.output_item.added", map[string]any{"output_index": textIndex, "item": item}); err != nil {
					return err
				}
				if err := event("response.content_part.added", map[string]any{"output_index": textIndex, "item_id": textID, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}); err != nil {
					return err
				}
			}
			text += delta
			return event("response.output_text.delta", map[string]any{"output_index": textIndex, "item_id": textID, "content_index": 0, "delta": delta})
		})
		if err != nil {
			return err
		}
		if textIndex >= 0 {
			item := responseMessage(textID, text, "completed")
			out[textIndex] = item
			if err = event("response.output_text.done", map[string]any{"output_index": textIndex, "item_id": textID, "content_index": 0, "text": text}); err != nil {
				return err
			}
			if err = event("response.content_part.done", map[string]any{"output_index": textIndex, "item_id": textID, "content_index": 0, "part": item["content"].([]any)[0]}); err != nil {
				return err
			}
			if err = event("response.output_item.done", map[string]any{"output_index": textIndex, "item": item}); err != nil {
				return err
			}
		}
		body := responseObject(id, req.Model, "completed", out, responsesUsage(r))
		markResponseLimit(body, r)
		kind := "response.completed"
		if body["status"] == "incomplete" {
			kind = "response.incomplete"
		}
		return event(kind, map[string]any{"response": body})
	}
	if err := run(); err != nil {
		closeStream(err.Error())
		return
	}
	closeStream("")
}
