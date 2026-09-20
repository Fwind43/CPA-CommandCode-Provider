package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func namespaceToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return "ns_" + hex.EncodeToString(sum[:28])
}

// Adapt function and custom tools to the function-only upstream protocol.
func flattenResponseTools(tools []map[string]any) ([]map[string]any, error) {
	out := []map[string]any{}
	seen := map[string]bool{}
	add := func(tool map[string]any, namespace, description string) error {
		if tool["type"] != "function" && tool["type"] != "custom" {
			return fmt.Errorf("unsupported Responses tool type: %v", tool["type"])
		}
		name, _ := tool["name"].(string)
		if name == "" {
			return fmt.Errorf("function tool requires a name")
		}
		alias := namespaceToolName(namespace, name)
		if seen[alias] {
			return fmt.Errorf("duplicate tool name: %s", alias)
		}
		seen[alias] = true
		fn := map[string]any{}
		for k, v := range tool {
			if k != "type" {
				fn[k] = v
			}
		}
		if tool["type"] == "custom" {
			d, _ := tool["description"].(string)
			if format, ok := tool["format"]; ok {
				raw, _ := json.Marshal(format)
				d += "\nInput format specification (follow exactly): " + string(raw)
			}
			fn = map[string]any{"description": d + "\nPass the raw tool input verbatim in the input string.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}, "required": []string{"input"}, "additionalProperties": false}}
		}
		fn["name"] = alias
		if namespace != "" {
			d, _ := fn["description"].(string)
			fn["description"] = "Namespace " + namespace + ": " + description + "\nFunction " + name + ": " + d
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
		return nil
	}
	for _, tool := range tools {
		if tool["type"] != "namespace" {
			if err := add(tool, "", ""); err != nil {
				return nil, err
			}
			continue
		}
		ns, _ := tool["name"].(string)
		if ns == "" {
			return nil, fmt.Errorf("namespace requires a name")
		}
		children, ok := tool["tools"].([]any)
		if !ok {
			return nil, fmt.Errorf("namespace requires a tools array")
		}
		desc, _ := tool["description"].(string)
		for _, child := range children {
			fn, ok := child.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid namespace tool")
			}
			if err := add(fn, ns, desc); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func additionalResponseTools(item map[string]any) ([]map[string]any, error) {
	values, ok := item["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("additional_tools requires a tools array")
	}
	tools := make([]map[string]any, 0, len(values))
	for _, value := range values {
		tool, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid additional_tools tool")
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// Use the same declarations for upstream conversion and output name restoration.
func responseToolsForRequest(payload []byte) ([]map[string]any, error) {
	var r struct {
		Tools []map[string]any `json:"tools"`
		Input json.RawMessage  `json:"input"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	var items []map[string]any
	if json.Unmarshal(r.Input, &items) == nil {
		for _, item := range items {
			if item["type"] != "additional_tools" {
				continue
			}
			tools, err := additionalResponseTools(item)
			if err != nil {
				return nil, err
			}
			r.Tools = append(r.Tools, tools...)
		}
	}
	return r.Tools, nil
}

func responseToolForRequest(req pluginapi.ExecutorRequest, call map[string]any) map[string]any {
	item := responseTool(call)
	tools, err := responseToolsForRequest(req.Payload)
	if err != nil {
		return item
	}
	for _, tool := range tools {
		ns := ""
		children := []any{tool}
		if tool["type"] == "namespace" {
			ns, _ = tool["name"].(string)
			children, _ = tool["tools"].([]any)
		}
		for _, child := range children {
			fn, ok := child.(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if item["name"] == namespaceToolName(ns, name) {
				item["name"] = name
				if ns != "" {
					item["namespace"] = ns
				}
				if fn["type"] == "custom" {
					args, _ := item["arguments"].(string)
					var wrapped struct {
						Input *string `json:"input"`
					}
					if json.Unmarshal([]byte(args), &wrapped) == nil && wrapped.Input != nil {
						args = *wrapped.Input
					}
					item["type"], item["input"] = "custom_tool_call", args
					delete(item, "arguments")
				}
				return item
			}
		}
	}
	return item
}
