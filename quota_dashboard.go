package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed quota_dashboard.html
var quotaDashboardHTML string

type quotaAccount struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Username string `json:"username,omitempty"`
	path     string
}

// Expose only the account display name; never serialize paths or credentials.
func quotaAccounts() []quotaAccount {
	out := []quotaAccount{}
	seen := map[string]bool{}
	for _, dir := range quotaAuthDirs() {
		matches, _ := filepath.Glob(filepath.Join(dir, "*command*code*.json"))
		sort.Strings(matches)
		for _, path := range matches {
			absolute, err := filepath.Abs(path)
			if err != nil || seen[absolute] {
				continue
			}
			seen[absolute] = true
			sum := sha256.Sum256([]byte(absolute))
			id := fmt.Sprintf("%x", sum[:12])
			label := fmt.Sprintf("Command Code %02d", len(out)+1)
			username := ""
			if info, err := os.Stat(path); err == nil && info.Size() <= 1<<20 {
				if data, err := os.ReadFile(path); err == nil {
					var raw map[string]any
					if json.Unmarshal(data, &raw) == nil {
						username = firstString(raw, "userName", "username", "user_name", "email", "name")
					}
				}
			}
			if username != "" {
				label = username
			}
			out = append(out, quotaAccount{ID: id, Label: label, Username: username, path: path})
		}
	}
	return out
}

func quotaDashboardResponse(query url.Values) (pluginapi.ManagementResponse, bool) {
	response := pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Cache-Control": {"no-store"}, "Content-Type": {"application/json; charset=utf-8"}}}
	if query.Get("accounts") == "1" {
		response.Body, _ = json.Marshal(map[string]any{"accounts": quotaAccounts()})
		return response, true
	}
	if id := query.Get("account"); id != "" {
		for _, account := range quotaAccounts() {
			if account.ID != id {
				continue
			}
			lookup := quotaLookup{requested: true, cacheKey: "dashboard:" + id, filePath: account.path, apiKey: quotaAPIKeyFromFile(account.path)}
			snapshot := quotaBuildSnapshotMap(query.Get("refresh") == "1", lookup)
			delete(snapshot, "errors")
			delete(snapshot, "auth_index")
			response.Body, _ = json.Marshal(snapshot)
			return response, true
		}
		response.StatusCode = http.StatusNotFound
		response.Body = []byte(`{"error":"Account unavailable; reload the account list."}`)
		return response, true
	}
	if !strings.EqualFold(query.Get("format"), "json") && query.Get("auth_index") == "" {
		response.Headers.Set("Content-Type", "text/html; charset=utf-8")
		response.Body = []byte(quotaDashboardHTML)
		return response, true
	}
	return response, false
}
