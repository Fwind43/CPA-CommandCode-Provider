package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// quotaLookup describes which Command Code credential a quota read should use.
// A requested lookup must never fall back to the first globbed auth file.
type quotaLookup struct {
	apiKey    string
	cacheKey  string
	filePath  string
	authIndex string
	requested bool
}

type quotaCacheEntry struct {
	body []byte
	at   time.Time
}

func quotaResetForTest() {
	quotaMu.Lock()
	quotaCache = map[string]quotaCacheEntry{}
	quotaAPIKey = ""
	quotaMu.Unlock()
}

func quotaKeyFingerprint(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%x", sum[:8])
}

func quotaAuthDirs() []string {
	configMu.RLock()
	configuredDir := quotaAuthDir
	configMu.RUnlock()
	candidates := []string{}
	if configuredDir != "" {
		candidates = append(candidates, configuredDir)
	}
	candidates = append(candidates, "/root/.cli-proxy-api", "./auths", "auths", "/CLIProxyAPI/auths")
	seen := map[string]bool{}
	out := []string{}
	for _, dir := range candidates {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

func quotaAPIKeyFromMaps(metadata map[string]any, attributes map[string]string) string {
	if metadata != nil {
		if key := firstString(metadata, "apiKey", "api_key", "commandCodeApiKey", "key", "token"); key != "" {
			return key
		}
	}
	if attributes != nil {
		for _, name := range []string{"api_key", "apiKey", "commandCodeApiKey", "key", "token"} {
			if value := strings.TrimSpace(attributes[name]); value != "" {
				return value
			}
		}
	}
	return ""
}

func quotaAuthIndexFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return ""
	}
	return firstString(parsed, "auth_index", "authIndex", "auth_id", "authId")
}

func quotaLookupFromAuthIndex(authIndex string) quotaLookup {
	authIndex = strings.TrimSpace(authIndex)
	lookup := quotaLookup{cacheKey: "glob-default"}
	if authIndex == "" {
		return lookup
	}
	lookup.authIndex = authIndex
	lookup.cacheKey = "auth:" + authIndex
	lookup.requested = true
	if key, path := quotaAPIKeyFromAuthIndex(authIndex); key != "" {
		lookup.apiKey = key
		lookup.filePath = path
	}
	return lookup
}

func quotaLookupFromManagement(req pluginapi.ManagementRequest, query url.Values) quotaLookup {
	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
		if idx := quotaAuthIndexFromBody(req.Body); idx != "" {
			return quotaLookupFromAuthIndex(idx)
		}
	}
	if idx := strings.TrimSpace(query.Get("auth_index")); idx != "" {
		return quotaLookupFromAuthIndex(idx)
	}
	return quotaLookup{cacheKey: "glob-default"}
}

func quotaLookupFromFetchRequest(req pluginapi.QuotaFetchRequest) quotaLookup {
	lookup := quotaLookup{cacheKey: "glob-default"}
	if idx := strings.TrimSpace(req.AuthIndex); idx != "" {
		lookup.authIndex = idx
		lookup.cacheKey = "auth:" + idx
		lookup.requested = true
	} else if id := strings.TrimSpace(req.AuthID); id != "" {
		lookup.cacheKey = "id:" + id
		lookup.requested = true
	}
	if key := apiKeyFromStorage(req.StorageJSON); key != "" {
		lookup.apiKey = key
		if lookup.cacheKey == "glob-default" {
			lookup.cacheKey = "key:" + quotaKeyFingerprint(key)
		}
		return lookup
	}
	if key := quotaAPIKeyFromMaps(req.Metadata, req.Attributes); key != "" {
		lookup.apiKey = key
		if lookup.cacheKey == "glob-default" {
			lookup.cacheKey = "key:" + quotaKeyFingerprint(key)
		}
		return lookup
	}
	if lookup.authIndex != "" {
		if key, path := quotaAPIKeyFromAuthIndex(lookup.authIndex); key != "" {
			lookup.apiKey = key
			lookup.filePath = path
		}
	}
	return lookup
}

func quotaAPIKeyFromAuthIndex(authIndex string) (string, string) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || strings.Contains(authIndex, "..") {
		return "", ""
	}
	dirs := quotaAuthDirs()
	for _, dir := range dirs {
		candidates := []string{filepath.Join(dir, authIndex)}
		if !strings.HasSuffix(strings.ToLower(authIndex), ".json") {
			candidates = append(candidates, filepath.Join(dir, authIndex+".json"))
		}
		for _, candidate := range candidates {
			if key := quotaAPIKeyFromFile(candidate); key != "" {
				return key, candidate
			}
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		sort.Strings(matches)
		for _, match := range matches {
			base := filepath.Base(match)
			stem := strings.TrimSuffix(base, filepath.Ext(base))
			if base == authIndex || stem == authIndex || strings.EqualFold(base, authIndex) || strings.EqualFold(stem, authIndex) {
				if key := quotaAPIKeyFromFile(match); key != "" {
					return key, match
				}
			}
		}
	}
	if n, errAtoi := strconv.Atoi(authIndex); errAtoi == nil && n >= 0 {
		for _, dir := range dirs {
			matches, errGlob := filepath.Glob(filepath.Join(dir, "commandcode*.json"))
			if errGlob != nil || len(matches) == 0 {
				matches, _ = filepath.Glob(filepath.Join(dir, "*command*code*.json"))
			}
			sort.Strings(matches)
			if n < len(matches) {
				if key := quotaAPIKeyFromFile(matches[n]); key != "" {
					return key, matches[n]
				}
			}
		}
	}
	return "", ""
}
