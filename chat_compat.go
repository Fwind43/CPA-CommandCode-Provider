package main

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// chatContentParts preserves ordering and never silently discards input.
func chatContentParts(content any) ([]map[string]any, error) {
	if content == nil {
		return nil, nil
	}
	if text, ok := content.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "text", "text": text}}, nil
	}
	items, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("content must be a string, null or array")
	}
	var parts []map[string]any
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid content part")
		}
		switch firstString(part, "type") {
		case "text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("text part requires text")
			}
			parts = append(parts, map[string]any{"type": "text", "text": text})
		case "image_url":
			image, ok := part["image_url"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("image_url requires an object")
			}
			url := firstString(image, "url")
			if !strings.HasPrefix(url, "data:") {
				return nil, fmt.Errorf("upstream supports inline base64 images only; remote image URLs are not supported")
			}
			header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
			if !ok || !strings.HasSuffix(header, ";base64") {
				return nil, fmt.Errorf("image must be a base64 data URL")
			}
			media := strings.TrimSuffix(header, ";base64")
			if !strings.HasPrefix(media, "image/") {
				return nil, fmt.Errorf("invalid image media type")
			}
			if _, err := base64.StdEncoding.DecodeString(data); err != nil {
				return nil, fmt.Errorf("invalid image base64: %w", err)
			}
			parts = append(parts, map[string]any{"type": "image", "image": data, "mediaType": media})
		default:
			return nil, fmt.Errorf("unsupported content part: %s", firstString(part, "type"))
		}
	}
	return parts, nil
}
