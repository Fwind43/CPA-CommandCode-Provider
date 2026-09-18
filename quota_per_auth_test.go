package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	testKeyA = "aaaaaaaaaaaaaaaaaaaa"
	testKeyB = "bbbbbbbbbbbbbbbbbbbb"
)

func writeCommandCodeAuth(t *testing.T, dir, name, key string) {
	t.Helper()
	body := fmt.Sprintf(`{"apiKey":%q,"metadata":{"type":"commandcode"}}`, key)
	if errWrite := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func startQuotaTestServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	quotaResetForTest()
	configMu.Lock()
	quotaAuthDir = dir
	prevBase := apiBaseURL
	configMu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token := stringsTrimBearer(request.Header.Get("Authorization"))
		monthly := 0.0
		switch token {
		case testKeyA:
			monthly = 11
		case testKeyB:
			monthly = 22
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case quotaCreditsPath:
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"credits": map[string]any{
					"monthlyCredits":   monthly,
					"purchasedCredits": 0,
					"freeCredits":      0,
				},
				"windowLimits": map[string]any{
					"fiveHour": map[string]any{"remainingPercent": 90.0, "resetAt": "2026-09-17T12:00:00Z"},
					"weekly":   map[string]any{"remainingPercent": 80.0, "resetAt": "2026-09-20T12:00:00Z"},
				},
			})
		case quotaUsagePath:
			_ = json.NewEncoder(writer).Encode(map[string]any{"totalCount": 1, "totalCost": monthly})
		case quotaWhoAmIPath:
			_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]any{"email": token[:2] + "@example.com"}})
		default:
			http.NotFound(writer, request)
		}
	}))
	configMu.Lock()
	apiBaseURL = server.URL
	configMu.Unlock()
	t.Cleanup(func() {
		server.Close()
		configMu.Lock()
		apiBaseURL = prevBase
		quotaAuthDir = ""
		configMu.Unlock()
		quotaResetForTest()
	})
	return server
}

func stringsTrimBearer(value string) string {
	const prefix = "Bearer "
	if len(value) > len(prefix) && value[:len(prefix)] == prefix {
		return value[len(prefix):]
	}
	return value
}

func mustMonthly(t *testing.T, snapshot map[string]any) float64 {
	t.Helper()
	value, ok := quotaFloat(snapshot, "monthly_credits")
	if !ok {
		t.Fatalf("monthly_credits missing: %#v", snapshot)
	}
	return value
}

func TestQuotaAuthIndexFromBody(t *testing.T) {
	if got := quotaAuthIndexFromBody([]byte(`{"auth_index":"commandcode-b.json","plugin_id":"commandcode"}`)); got != "commandcode-b.json" {
		t.Fatalf("auth_index=%q", got)
	}
	if got := quotaAuthIndexFromBody([]byte(`{"authIndex":"x"}`)); got != "x" {
		t.Fatalf("authIndex=%q", got)
	}
}

func TestQuotaFetchUsesStorageJSONNotFirstGlob(t *testing.T) {
	dir := t.TempDir()
	writeCommandCodeAuth(t, dir, "commandcode-a.json", testKeyA)
	writeCommandCodeAuth(t, dir, "commandcode-b.json", testKeyB)
	startQuotaTestServer(t, dir)

	response := commandCodeQuotaFetch(pluginapi.QuotaFetchRequest{
		AuthIndex:   "commandcode-b.json",
		StorageJSON: []byte(`{"apiKey":"` + testKeyB + `","metadata":{"type":"commandcode"}}`),
	})
	if len(response.Summary) == 0 {
		t.Fatalf("empty summary: %#v", response)
	}
	found := 0.0
	ok := false
	for _, metric := range response.Summary {
		if metric.Key == "monthly_credits" {
			found = metric.Value
			ok = true
		}
	}
	if !ok || found != 22 {
		t.Fatalf("wanted B credits 22, got %#v", response.Summary)
	}
}

func TestManagementPostQuotaUsesAuthIndexNotGlob(t *testing.T) {
	mockQuotaManagementAccess(t)
	dir := t.TempDir()
	writeCommandCodeAuth(t, dir, "commandcode-a.json", testKeyA)
	writeCommandCodeAuth(t, dir, "commandcode-b.json", testKeyB)
	startQuotaTestServer(t, dir)

	got := handleCommandCodeManagement(pluginapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/plugins/commandcode/quota",
		Headers: http.Header{"Authorization": {"Bearer fixture-valid"}},
		Query:   url.Values{"format": []string{"json"}},
		Body:    []byte(`{"auth_index":"commandcode-b.json","plugin_id":"commandcode"}`),
	})
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.StatusCode, got.Body)
	}
	var snapshot map[string]any
	if errUnmarshal := json.Unmarshal(got.Body, &snapshot); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if mustMonthly(t, snapshot) != 22 {
		t.Fatalf("POST used glob/first auth: %#v", snapshot)
	}
	if snapshot["auth_index"] != "commandcode-b.json" {
		t.Fatalf("auth_index=%v", snapshot["auth_index"])
	}

	get := handleCommandCodeManagement(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/plugins/commandcode/quota",
		Headers: http.Header{"Authorization": {"Bearer fixture-valid"}},
		Query:   url.Values{"format": []string{"json"}},
	})
	var globbed map[string]any
	if errUnmarshal := json.Unmarshal(get.Body, &globbed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if mustMonthly(t, globbed) != 11 {
		t.Fatalf("GET glob should stay on first file A=11: %#v", globbed)
	}
}

func TestQuotaCacheIsolatedPerAuth(t *testing.T) {
	dir := t.TempDir()
	writeCommandCodeAuth(t, dir, "commandcode-a.json", testKeyA)
	writeCommandCodeAuth(t, dir, "commandcode-b.json", testKeyB)
	startQuotaTestServer(t, dir)

	lookupA := quotaLookupFromAuthIndex("commandcode-a.json")
	lookupB := quotaLookupFromAuthIndex("commandcode-b.json")
	snapA := quotaBuildSnapshotMap(true, lookupA)
	snapB := quotaBuildSnapshotMap(true, lookupB)
	if mustMonthly(t, snapA) != 11 || mustMonthly(t, snapB) != 22 {
		t.Fatalf("a=%v b=%v", snapA["monthly_credits"], snapB["monthly_credits"])
	}
	cachedA := quotaBuildSnapshotMap(false, lookupA)
	if mustMonthly(t, cachedA) != 11 {
		t.Fatalf("cache mixed A with B: %#v", cachedA)
	}
	if cachedA["cached"] != true {
		t.Fatalf("expected A cache hit: %#v", cachedA)
	}
	cachedB := quotaBuildSnapshotMap(false, lookupB)
	if mustMonthly(t, cachedB) != 22 || cachedB["cached"] != true {
		t.Fatalf("expected B cache hit: %#v", cachedB)
	}
}

func TestRequestedAuthDoesNotGlobFallback(t *testing.T) {
	dir := t.TempDir()
	writeCommandCodeAuth(t, dir, "commandcode-a.json", testKeyA)
	startQuotaTestServer(t, dir)

	snapshot := quotaBuildSnapshotMap(true, quotaLookupFromAuthIndex("commandcode-missing.json"))
	if snapshot["available"] != false {
		t.Fatalf("missing auth should not glob: %#v", snapshot)
	}
}
