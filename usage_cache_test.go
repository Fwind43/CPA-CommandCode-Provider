package main

import (
	"encoding/json"
	"testing"
)

func TestUsagePayloadSurfacesCacheReadTokens(t *testing.T) {
	cases := []struct {
		name     string
		usage    map[string]any
		expected int
	}{
		{
			name: "agent protocol nested inputTokenDetails",
			usage: map[string]any{
				"inputTokens":       float64(135754),
				"outputTokens":      float64(9),
				"totalTokens":       float64(135763),
				"inputTokenDetails": map[string]any{"noCacheTokens": float64(126794), "cacheReadTokens": float64(8960)},
			},
			expected: 8960,
		},
		{
			name: "openai style nested prompt_tokens_details",
			usage: map[string]any{
				"prompt_tokens":         float64(100),
				"completion_tokens":     float64(5),
				"prompt_tokens_details": map[string]any{"cached_tokens": float64(64)},
			},
			expected: 64,
		},
		{
			name: "cli stats flat cache_read_tokens",
			usage: map[string]any{
				"input_tokens":       float64(4509),
				"output_tokens":      float64(392),
				"cache_read_tokens":  float64(4352),
				"cachedInputTokens":  float64(0),
				"cacheReadTokens":    float64(0),
			},
			expected: 4352,
		},
		{
			name: "no cache information stays absent",
			usage: map[string]any{
				"inputTokens":  float64(10),
				"outputTokens": float64(2),
			},
			expected: 0,
		},
	}
	for _, tc := range cases {
		payload := usagePayload(tc.usage, "")
		encoded, _ := json.Marshal(payload)
		details, _ := payload["prompt_tokens_details"].(map[string]any)
		got := 0
		if details != nil {
			if v, ok := details["cached_tokens"].(int); ok {
				got = v
			}
		}
		if got != tc.expected {
			t.Fatalf("%s: cached_tokens=%d want %d payload=%s", tc.name, got, tc.expected, encoded)
		}
		if tc.expected == 0 {
			if _, present := payload["prompt_tokens_details"]; present {
				t.Fatalf("%s: prompt_tokens_details should be omitted, payload=%s", tc.name, encoded)
			}
		}
	}
}

func TestUsagePayloadKeepsInputTokensInclusiveOfCache(t *testing.T) {
	payload := usagePayload(map[string]any{
		"inputTokens":       float64(135754),
		"outputTokens":      float64(9),
		"totalTokens":       float64(135763),
		"inputTokenDetails": map[string]any{"noCacheTokens": float64(126794), "cacheReadTokens": float64(8960)},
	}, "")
	if payload["prompt_tokens"] != 135754 {
		t.Fatalf("prompt_tokens must stay 135754 (subset semantics), got %v", payload["prompt_tokens"])
	}
	if payload["total_tokens"] != 135763 {
		t.Fatalf("total_tokens must stay 135763, got %v", payload["total_tokens"])
	}
}
