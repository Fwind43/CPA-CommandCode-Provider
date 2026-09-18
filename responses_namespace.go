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

// Flatten only function namespaces. Never silently drop unsupported tool kinds.
func flattenResponseTools(tools []map[string]any) ([]map[string]any, error) {
	out := []map[string]any{}
	seen := map[string]bool{}
	add := func(tool map[string]any, namespace, description string) error {
		if tool["type"] != "function" {
			return fmt.Errorf("only function tools are supported inside namespaces (got %v)", tool["type"])
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

func responseToolForRequest(req pluginapi.ExecutorRequest, call map[string]any) map[string]any {
	item := responseTool(call)
	var r struct {
		Tools []map[string]any `json:"tools"`
	}
	if json.Unmarshal(req.Payload, &r) != nil {
		return item
	}
	for _, tool := range r.Tools {
		if tool["type"] != "namespace" {
			continue
		}
		ns, _ := tool["name"].(string)
		children, _ := tool["tools"].([]any)
		for _, child := range children {
			fn, ok := child.(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if item["name"] == namespaceToolName(ns, name) {
				item["name"], item["namespace"] = name, ns
				return item
			}
		}
	}
	return item
}
