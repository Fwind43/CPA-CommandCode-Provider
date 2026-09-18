package main

// Command Code quota resource for the CLIProxyAPI management panel.
//
// Exposes a read-only page/JSON document under
// /v0/resource/plugins/commandcode/quota that shows the Command Code Go plan
// credits, the rolling 5h and weekly usage windows, and lifetime usage totals.
//
// Security notes:
//   - Plugin resources are NOT covered by management authentication, and this
//     instance is reachable from the public internet, so this file must only
//     ever render non-sensitive quota numbers. Never include the API key,
//     tokens, credential payloads, or raw upstream responses.
//   - Account identifiers coming from the upstream are masked before display.
//   - Upstream calls are cached (see quotaCacheTTL) and only refreshed on a
//     low-frequency schedule or via an explicit ?refresh=1 request, to avoid
//     tripping upstream rate limiting.
//   - Disable with plugins.configs.commandcode.quota_enabled=false.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	quotaCacheTTL    = 5 * time.Minute
	quotaHTTPTimeout = 20 * time.Second

	quotaCreditsPath = "/alpha/billing/credits"
	quotaUsagePath   = "/alpha/usage/summary"
	quotaWhoAmIPath  = "/alpha/whoami"
)

var (
	// quotaEnabled and quotaAuthDir are populated from the plugin config block
	// by applyConfigYAML (see impl.go) and guarded by configMu.
	quotaEnabled = true
	quotaAuthDir = ""

	quotaMu     sync.Mutex
	quotaCache  = map[string]quotaCacheEntry{}
	quotaAPIKey string
)

var quotaUTC8 = time.FixedZone("UTC+8", 8*60*60)

// quotaFalsy reports whether a config value should disable the feature. The
// feature is opt-out, so anything that is not an explicit false stays enabled.
func quotaFalsy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "no", "off", "disabled":
		return true
	}
	return false
}

// quotaResolveAPIKey returns the Command Code API key used for read-only
// upstream calls. The key never leaves this process: it is only used to build
// the Authorization header of the quota requests.
//
// A requested lookup (StorageJSON / auth_index / metadata) must never fall
// back to the first globbed auth file.
func quotaResolveAPIKey(lookup quotaLookup) (string, error) {
	if lookup.apiKey != "" {
		return lookup.apiKey, nil
	}
	if lookup.requested {
		if lookup.authIndex != "" {
			return "", fmt.Errorf("command code credential not found for auth_index %s", lookup.authIndex)
		}
		return "", fmt.Errorf("command code credential not found for requested auth")
	}

	quotaMu.Lock()
	cached := quotaAPIKey
	quotaMu.Unlock()
	if cached != "" {
		return cached, nil
	}

	searched := []string{}
	for _, dir := range quotaAuthDirs() {
		searched = append(searched, dir)
		matches, errGlob := filepath.Glob(filepath.Join(dir, "commandcode*.json"))
		if errGlob != nil || len(matches) == 0 {
			matches, _ = filepath.Glob(filepath.Join(dir, "*command*code*.json"))
		}
		sort.Strings(matches)
		for _, path := range matches {
			if key := quotaAPIKeyFromFile(path); key != "" {
				quotaMu.Lock()
				quotaAPIKey = key
				quotaMu.Unlock()
				return key, nil
			}
		}
	}
	return "", fmt.Errorf("command code credential not found (searched: %s)", strings.Join(searched, ", "))
}

func quotaAPIKeyFromFile(path string) string {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return ""
	}
	defer file.Close()
	body, errRead := io.ReadAll(io.LimitReader(file, 1<<16))
	if errRead != nil {
		return ""
	}
	return apiKeyFromStorage(body)
}

func quotaFetchJSON(apiKey, path string) (map[string]any, error) {
	configMu.RLock()
	base := apiBaseURL
	configMu.RUnlock()

	request, errRequest := http.NewRequest(http.MethodGet, base+path, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("x-command-code-version", cliVersion)
	request.Header.Set("x-cli-environment", "production")
	request.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: quotaHTTPTimeout}
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, errDo
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s status %d", path, response.StatusCode)
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("%s decode error", path)
	}
	return parsed, nil
}

// quotaFetchWithRetry resolves the credential, performs the request and retries
// once with a freshly read credential when the cached one was rejected.
func quotaFetchWithRetry(path string, lookup quotaLookup) (map[string]any, error) {
	apiKey, errKey := quotaResolveAPIKey(lookup)
	if errKey != nil {
		return nil, errKey
	}
	parsed, errFetch := quotaFetchJSON(apiKey, path)
	if errFetch != nil && strings.Contains(errFetch.Error(), "status 401") {
		fresh := lookup
		fresh.apiKey = ""
		if !lookup.requested {
			quotaMu.Lock()
			quotaAPIKey = ""
			quotaMu.Unlock()
		} else if lookup.authIndex != "" {
			if key, filePath := quotaAPIKeyFromAuthIndex(lookup.authIndex); key != "" {
				fresh.apiKey = key
				fresh.filePath = filePath
			}
		}
		if freshKey, errRe := quotaResolveAPIKey(fresh); errRe == nil && freshKey != apiKey {
			return quotaFetchJSON(freshKey, path)
		}
	}
	return parsed, errFetch
}

func nestedValues(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	if child, ok := values[key].(map[string]any); ok {
		return child
	}
	return nil
}

func floatFromAny(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch typed := values[key].(type) {
		case float64:
			return typed, true
		case float32:
			return float64(typed), true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case json.Number:
			if parsed, errParse := typed.Float64(); errParse == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func quotaRound(value float64) float64 {
	return float64(int64(value*1e6+0.5)) / 1e6
}

func quotaRoundAny(value any) any {
	if number, ok := value.(float64); ok {
		return quotaRound(number)
	}
	return value
}

// quotaWindowValues turns one upstream usage window into a display-ready map.
func quotaWindowValues(raw map[string]any) map[string]any {
	result := map[string]any{"available": raw != nil}
	if raw == nil {
		return result
	}
	used, usedOK := floatFromAny(raw, "used", "usedCredits", "used_credits")
	capValue, capOK := floatFromAny(raw, "cap", "limit", "capCredits", "cap_credits")
	if usedOK {
		result["used"] = quotaRound(used)
	}
	if capOK {
		result["cap"] = quotaRound(capValue)
	}
	if usedOK && capOK {
		result["remaining"] = quotaRound(capValue - used)
	}
	remainingPercent, percentOK := floatFromAny(raw, "remainingPercent", "remaining_percent")
	if !percentOK && usedOK && capOK && capValue > 0 {
		remainingPercent = (capValue - used) / capValue * 100
		percentOK = true
	}
	if percentOK {
		if remainingPercent < 0 {
			remainingPercent = 0
		}
		if remainingPercent > 100 {
			remainingPercent = 100
		}
		result["remainingPercent"] = quotaRound(remainingPercent)
	}
	for _, key := range []string{"resetAt", "reset_at", "resetsAt"} {
		if value, ok := raw[key].(string); ok {
			if reset, err := time.Parse(time.RFC3339, value); err == nil {
				result["reset_at"] = reset.UTC().Format(time.RFC3339)
				break
			}
		}
	}
	if exceeded, ok := raw["exceeded"].(bool); ok {
		result["exceeded"] = exceeded
	}
	if resetRaw, ok := floatFromAny(raw, "resetAt", "reset_at", "resetsAt"); ok && resetRaw > 0 {
		milliseconds := int64(resetRaw)
		if milliseconds < 1_000_000_000_000 {
			milliseconds *= 1000
		}
		reset := time.UnixMilli(milliseconds)
		until := time.Until(reset)
		result["reset_at"] = reset.UTC().Format(time.RFC3339)
		result["reset_at_utc8"] = reset.In(quotaUTC8).Format("2006-01-02 15:04:05 MST")
		result["reset_in"] = quotaHumanDuration(until)
		result["reset_in_seconds"] = int64(until.Seconds())
	}
	return result
}

func quotaHumanDuration(value time.Duration) string {
	if value <= 0 {
		return "expired"
	}
	total := int64(value.Seconds())
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	parts = append(parts, fmt.Sprintf("%dm", minutes))
	return strings.Join(parts, " ")
}

func maskIdentity(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if index := strings.Index(trimmed, "@"); index > 0 {
		local := []rune(trimmed[:index])
		domain := trimmed[index+1:]
		keep := 2
		if len(local) < keep {
			keep = len(local)
		}
		maskedDomain := "***"
		if dot := strings.LastIndex(domain, "."); dot > 0 {
			maskedDomain = "***" + domain[dot:]
		}
		return string(local[:keep]) + "***@" + maskedDomain
	}
	runes := []rune(trimmed)
	if len(runes) <= 2 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:2]) + "***"
}

// quotaBuildSnapshotMap returns the quota document, serving it from the cache
// unless it is stale or the caller explicitly asked for a refresh.
func quotaBuildSnapshotMap(force bool, lookup quotaLookup) map[string]any {
	now := time.Now().UTC()
	cacheKey := lookup.cacheKey
	if cacheKey == "" {
		cacheKey = "glob-default"
	}

	quotaMu.Lock()
	entry, hit := quotaCache[cacheKey]
	quotaMu.Unlock()

	if !force && hit && entry.body != nil && now.Sub(entry.at) < quotaCacheTTL {
		var cached map[string]any
		if errUnmarshal := json.Unmarshal(entry.body, &cached); errUnmarshal == nil {
			cached["cached"] = true
			cached["cache_age_seconds"] = int(now.Sub(entry.at).Seconds())
			return cached
		}
	}

	snapshot := map[string]any{
		"provider":          providerKey,
		"available":         false,
		"fetched_at":        now.Format(time.RFC3339),
		"cached":            false,
		"cache_age_seconds": 0,
		"cache_ttl_seconds": int(quotaCacheTTL.Seconds()),
		"errors":            []string{},
	}
	if lookup.authIndex != "" {
		snapshot["auth_index"] = lookup.authIndex
	}
	errorsFound := []string{}

	credits, errCredits := quotaFetchWithRetry(quotaCreditsPath, lookup)
	if errCredits != nil {
		errorsFound = append(errorsFound, "credits: "+errCredits.Error())
	}
	usage, errUsage := quotaFetchWithRetry(quotaUsagePath, lookup)
	if errUsage != nil {
		errorsFound = append(errorsFound, "usage: "+errUsage.Error())
	}

	if credits != nil {
		if creditBlock := nestedValues(credits, "credits"); creditBlock != nil {
			for _, field := range []string{"monthlyCredits", "purchasedCredits", "freeCredits", "creditThreshold"} {
				if value, exists := creditBlock[field]; exists {
					snapshot[quotaSnakeCase(field)] = quotaRoundAny(value)
				}
			}
			if value, exists := creditBlock["belowThreshold"]; exists {
				snapshot["below_threshold"] = value
			}
		}
		if limits := nestedValues(credits, "windowLimits"); limits != nil {
			snapshot["five_hour"] = quotaWindowValues(nestedValues(limits, "fiveHour"))
			snapshot["weekly"] = quotaWindowValues(nestedValues(limits, "weekly"))
			if limited, exists := limits["limited"]; exists {
				snapshot["limited"] = limited
			}
		}
	}

	if usage != nil {
		usageView := map[string]any{}
		for _, field := range []string{"totalCount", "totalCost", "averageCost", "successRate", "completedCount", "failedCount", "totalTokensIn", "totalTokensOut", "totalTokens", "periodBasis"} {
			if value, exists := usage[field]; exists {
				usageView[field] = quotaRoundAny(value)
			}
		}
		if len(usageView) > 0 {
			snapshot["usage"] = usageView
		}
	}

	// The account label is a convenience only; it is always masked and is
	// skipped entirely when the lookup fails.
	if credits != nil || usage != nil {
		if who, errWho := quotaFetchWithRetry(quotaWhoAmIPath, lookup); errWho == nil {
			if user := nestedValues(who, "user"); user != nil {
				masked := maskIdentity(firstString(user, "email", "mail"))
				if masked == "" {
					masked = maskIdentity(firstString(user, "userName", "username"))
				}
				if masked != "" {
					snapshot["account_masked"] = masked
				}
			}
		}
	}

	if len(errorsFound) > 0 {
		snapshot["errors"] = errorsFound
	}
	snapshot["available"] = credits != nil || usage != nil

	if snapshot["available"] == true {
		if encoded, errMarshal := json.Marshal(snapshot); errMarshal == nil {
			quotaMu.Lock()
			quotaCache[cacheKey] = quotaCacheEntry{body: encoded, at: now}
			quotaMu.Unlock()
		}
	}
	return snapshot
}

func quotaSnakeCase(field string) string {
	switch field {
	case "monthlyCredits":
		return "monthly_credits"
	case "purchasedCredits":
		return "purchased_credits"
	case "freeCredits":
		return "free_credits"
	case "creditThreshold":
		return "credit_threshold"
	}
	return field
}

func quotaDisplay(value any) string {
	switch typed := value.(type) {
	case nil:
		return "-"
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "yes"
		}
		return "no"
	case string:
		if typed == "" {
			return "-"
		}
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func quotaWindowValue(snapshot map[string]any, window, field string) any {
	block, _ := snapshot[window].(map[string]any)
	if block == nil {
		return nil
	}
	return block[field]
}

func quotaUsageValue(snapshot map[string]any, field string) any {
	block, _ := snapshot["usage"].(map[string]any)
	if block == nil {
		return nil
	}
	return block[field]
}

// quotaRenderHTML renders the human-readable page. All dynamic values are
// escaped; only quota numbers, timestamps and the masked account label are
// ever placed on the page.
func quotaRenderHTML(snapshot map[string]any) string {
	available, _ := snapshot["available"].(bool)
	cached, _ := snapshot["cached"].(bool)

	var builder strings.Builder
	builder.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">")
	builder.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	builder.WriteString("<title>Command Code quota</title><style>")
	builder.WriteString("body{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;margin:2rem auto;max-width:54rem;padding:0 1rem;color:#1f2430;line-height:1.45}")
	builder.WriteString("h1{font-size:1.35rem;margin:0 0 .35rem}h2{font-size:1.02rem;margin:1.4rem 0 .35rem}")
	builder.WriteString("table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #e6e8ee;padding:.42rem .55rem;text-align:left;font-size:.92rem}")
	builder.WriteString("th{background:#f7f8fb;font-weight:600}code{color:#6b7280;font-size:.82rem}")
	builder.WriteString(".muted{color:#6b7280;font-size:.85rem}.err{color:#b42318;font-size:.9rem}a{color:#1d4ed8}")
	builder.WriteString("</style></head><body>")
	builder.WriteString("<h1>Command Code quota</h1>")

	fetchedAt := quotaDisplay(snapshot["fetched_at"])
	cacheNote := "live"
	if cached {
		cacheNote = "cached, age " + quotaDisplay(snapshot["cache_age_seconds"]) + "s"
	}
	builder.WriteString("<p class=\"muted\">Source: api.commandcode.ai/alpha (billing credits + usage summary) &middot; fetched ")
	builder.WriteString(html.EscapeString(fetchedAt))
	builder.WriteString(" UTC &middot; ")
	builder.WriteString(html.EscapeString(cacheNote))
	builder.WriteString(" &middot; <a href=\"?refresh=1\">refresh now</a> &middot; <a href=\"?format=json\">JSON</a></p>")

	if !available {
		builder.WriteString("<p class=\"err\">Command Code quota is temporarily unavailable.</p>")
		if errors, ok := snapshot["errors"].([]any); ok {
			for _, item := range errors {
				builder.WriteString("<p class=\"err\">" + html.EscapeString(quotaDisplay(item)) + "</p>")
			}
		} else if errors, ok := snapshot["errors"].([]string); ok {
			for _, item := range errors {
				builder.WriteString("<p class=\"err\">" + html.EscapeString(item) + "</p>")
			}
		}
		builder.WriteString("<p class=\"muted\">Retry later; upstream calls are cached for 5 minutes.</p></body></html>")
		return builder.String()
	}

	row := func(label, value, source string) {
		builder.WriteString("<tr><td>" + html.EscapeString(label) + "</td><td>" + html.EscapeString(value) + "</td><td><code>" + html.EscapeString(source) + "</code></td></tr>")
	}

	builder.WriteString("<h2>Credits</h2><table><tr><th>Metric</th><th>Value</th><th>Upstream field</th></tr>")
	row("Monthly credits (balance reported)", quotaDisplay(snapshot["monthly_credits"]), "credits.monthlyCredits")
	row("Purchased credits", quotaDisplay(snapshot["purchased_credits"]), "credits.purchasedCredits")
	row("Free credits", quotaDisplay(snapshot["free_credits"]), "credits.freeCredits")
	row("Credit threshold", quotaDisplay(snapshot["credit_threshold"]), "credits.creditThreshold")
	row("Below threshold", quotaDisplay(snapshot["below_threshold"]), "credits.belowThreshold")
	if masked := quotaDisplay(snapshot["account_masked"]); masked != "-" {
		row("Account (masked)", masked, "whoami.user.email (masked)")
	}
	builder.WriteString("</table>")

	windowBlock := func(title, key, prefix string) {
		builder.WriteString("<h2>" + html.EscapeString(title) + "</h2><table><tr><th>Metric</th><th>Value</th><th>Upstream field</th></tr>")
		row("Used", quotaDisplay(quotaWindowValue(snapshot, key, "used")), prefix+".used")
		row("Cap", quotaDisplay(quotaWindowValue(snapshot, key, "cap")), prefix+".cap")
		row("Remaining", quotaDisplay(quotaWindowValue(snapshot, key, "remaining")), "computed: cap - used")
		row("Exceeded", quotaDisplay(quotaWindowValue(snapshot, key, "exceeded")), prefix+".exceeded")
		row("Resets at (UTC+8)", quotaDisplay(quotaWindowValue(snapshot, key, "reset_at_utc8")), prefix+".resetAt")
		row("Resets in", quotaDisplay(quotaWindowValue(snapshot, key, "reset_in")), "computed from resetAt")
		builder.WriteString("</table>")
	}
	windowBlock("5 hour window", "five_hour", "windowLimits.fiveHour")
	windowBlock("Weekly window", "weekly", "windowLimits.weekly")

	builder.WriteString("<h2>Lifetime usage</h2><table><tr><th>Metric</th><th>Value</th><th>Upstream field</th></tr>")
	row("Total calls", quotaDisplay(quotaUsageValue(snapshot, "totalCount")), "usage.totalCount")
	row("Total cost (credits)", quotaDisplay(quotaUsageValue(snapshot, "totalCost")), "usage.totalCost")
	row("Average cost per call", quotaDisplay(quotaUsageValue(snapshot, "averageCost")), "usage.averageCost")
	row("Success rate (%)", quotaDisplay(quotaUsageValue(snapshot, "successRate")), "usage.successRate")
	row("Completed calls", quotaDisplay(quotaUsageValue(snapshot, "completedCount")), "usage.completedCount")
	row("Failed calls", quotaDisplay(quotaUsageValue(snapshot, "failedCount")), "usage.failedCount")
	row("Tokens in", quotaDisplay(quotaUsageValue(snapshot, "totalTokensIn")), "usage.totalTokensIn")
	row("Tokens out", quotaDisplay(quotaUsageValue(snapshot, "totalTokensOut")), "usage.totalTokensOut")
	row("Tokens total", quotaDisplay(quotaUsageValue(snapshot, "totalTokens")), "usage.totalTokens")
	row("Period basis", quotaDisplay(quotaUsageValue(snapshot, "periodBasis")), "usage.periodBasis")
	builder.WriteString("</table>")

	builder.WriteString("<p class=\"muted\">Read-only view. No credentials, tokens or raw upstream payloads are rendered. Values are cached for 5 minutes to limit upstream calls; use refresh to force a low-frequency manual update. Disable with plugins.configs.commandcode.quota_enabled=false.</p>")
	builder.WriteString("</body></html>")
	return builder.String()
}

func handleCommandCodeQuota(req pluginapi.ManagementRequest, query url.Values) pluginapi.ManagementResponse {
	configMu.RLock()
	enabled := quotaEnabled
	configMu.RUnlock()
	if !enabled {
		return htmlResponse(http.StatusNotFound, "Command Code quota", "Quota display is disabled (plugins.configs.commandcode.quota_enabled: false).")
	}

	if len(query) > 0 || len(req.Body) > 0 {
		if !quotaAuthorize(req) {
			return pluginapi.ManagementResponse{StatusCode: http.StatusUnauthorized, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: []byte(`{"error":"Management authentication required"}`)}
		}
	}

	if response, handled := quotaDashboardResponse(query); handled {
		return response
	}

	refresh := strings.ToLower(strings.TrimSpace(query.Get("refresh")))
	force := refresh == "1" || refresh == "true" || refresh == "yes"
	snapshot := quotaBuildSnapshotMap(force, quotaLookupFromManagement(req, query))

	if strings.EqualFold(strings.TrimSpace(query.Get("format")), "json") {
		body, errMarshal := json.Marshal(snapshot)
		if errMarshal != nil {
			return pluginapi.ManagementResponse{
				StatusCode: http.StatusInternalServerError,
				Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
				Body:       []byte(`{"error":"failed to encode quota document"}`),
			}
		}
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":  []string{"application/json; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			Body: body,
		}
	}

	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(quotaRenderHTML(snapshot)),
	}
}

func quotaFloat(snapshot map[string]any, key string) (float64, bool) {
	value, ok := snapshot[key]
	if !ok {
		return 0, false
	}
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	}
	return 0, false
}

func commandCodeQuotaFetch(req pluginapi.QuotaFetchRequest) pluginapi.QuotaFetchResponse {
	snapshot := quotaBuildSnapshotMap(true, quotaLookupFromFetchRequest(req))
	response := pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Command Code"}}
	for _, item := range []struct{ key, label, format, currency string }{
		{"monthly_credits", "Monthly credits", "currency", "USD"},
		{"purchased_credits", "Purchased credits", "currency", "USD"},
		{"free_credits", "Free credits", "currency", "USD"},
	} {
		if value, ok := quotaFloat(snapshot, item.key); ok {
			response.Summary = append(response.Summary, pluginapi.QuotaMetric{Key: item.key, Label: item.label, Value: value, Format: item.format, Currency: item.currency})
		}
	}
	if usage, ok := snapshot["usage"].(map[string]any); ok {
		for _, item := range []struct{ key, label, format, currency string }{
			{"totalCost", "Lifetime usage", "currency", "USD"},
			{"totalCount", "Requests", "number", ""},
		} {
			if value, exists := quotaFloat(usage, item.key); exists {
				response.Summary = append(response.Summary, pluginapi.QuotaMetric{Key: item.key, Label: item.label, Value: value, Format: item.format, Currency: item.currency})
			}
		}
	}
	group := pluginapi.QuotaGroup{DisplayName: "Usage limits"}
	for _, item := range []struct{ key, label, window string }{{"five_hour", "Five hour", "5h"}, {"weekly", "Weekly", "7d"}} {
		window, ok := snapshot[item.key].(map[string]any)
		if !ok {
			continue
		}
		remaining, ok := quotaFloat(window, "remainingPercent")
		if !ok {
			continue
		}
		bucket := pluginapi.QuotaBucket{Window: item.window, RemainingFraction: remaining / 100, Description: item.label}
		if reset, ok := window["resetAt"].(string); ok {
			bucket.ResetTime = reset
		}
		group.Buckets = append(group.Buckets, bucket)
	}
	if len(group.Buckets) > 0 {
		response.Groups = append(response.Groups, group)
	}
	return response
}
