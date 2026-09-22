package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const commandCodeModelsURL = "https://api.commandcode.ai/provider/v1/models"

type catalogModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextLength int64    `json:"context_length"`
	Endpoints     []string `json:"supported_endpoints"`
}

// The catalog is public, not an account entitlement check. Never send credentials.
// Serialize refreshes and retain the last good snapshot on any upstream failure.
type modelCatalog struct {
	mu          sync.Mutex
	client      *http.Client
	url         string
	models      []catalogModel
	nextRefresh time.Time
}

var commandCodeCatalog = &modelCatalog{
	client: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	url:    commandCodeModelsURL,
}

func fallbackCatalog() []catalogModel {
	out := make([]catalogModel, 0, len(goPlanModelIDs))
	for _, id := range goPlanModelIDs {
		out = append(out, catalogModel{ID: id, Name: id, ContextLength: 200000})
	}
	return out
}

func (c *modelCatalog) snapshot() []catalogModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !now.Before(c.nextRefresh) {
		models, err := c.fetch()
		if err == nil {
			c.models = models
			c.nextRefresh = now.Add(5 * time.Minute)
		} else {
			c.nextRefresh = now.Add(time.Minute)
		}
	}
	if len(c.models) == 0 {
		return fallbackCatalog()
	}
	return append([]catalogModel(nil), c.models...)
}

func (c *modelCatalog) fetch() ([]catalogModel, error) {
	req, err := http.NewRequest(http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog HTTP %d", resp.StatusCode)
	}
	const limit = 2 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("model catalog too large")
	}
	var data struct {
		Data []catalogModel `json:"data"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]catalogModel, 0, len(data.Data))
	for _, m := range data.Data {
		if m.ID == "" || strings.TrimSpace(m.ID) != m.ID || strings.ContainsAny(m.ID, "\r\n\t ") {
			return nil, fmt.Errorf("invalid model ID")
		}
		compatible := len(m.Endpoints) == 0
		for _, endpoint := range m.Endpoints {
			if endpoint == "/chat/completions" || endpoint == "/responses" {
				compatible = true
			}
		}
		if !compatible || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.ContextLength <= 0 {
			m.ContextLength = 200000
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty model catalog")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func catalogPublicID(model string, models []catalogModel) string {
	name := modelShortName(model)
	count := 0
	for _, candidate := range models {
		if modelShortName(candidate.ID) == name {
			count++
		}
	}
	if count == 1 {
		return name
	}
	return model
}

func modelShortName(id string) string {
	_, short, found := strings.Cut(id, "/")
	if found {
		return short
	}
	return id
}
