package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestDashboardAccountsAndIsolation(t *testing.T) {
	dir := t.TempDir()
	writeCommandCodeAuth(t, dir, "commandcode-a.json", testKeyA)
	writeCommandCodeAuth(t, dir, "commandcode-b.json", testKeyB)
	startQuotaTestServer(t, dir)
	accounts := quotaAccounts()
	if len(accounts) != 2 {
		t.Fatalf("accounts=%d", len(accounts))
	}
	response, handled := quotaDashboardResponse(url.Values{"accounts": {"1"}})
	if !handled || response.StatusCode != 200 {
		t.Fatal("list failed")
	}
	for _, secret := range []string{testKeyA, testKeyB, dir, "commandcode-a.json"} {
		if strings.Contains(string(response.Body), secret) {
			t.Fatal("sensitive list field leaked")
		}
	}
	for repeat := 0; repeat < 2; repeat++ {
		for i, a := range accounts {
			response, _ = quotaDashboardResponse(url.Values{"account": {a.ID}, "format": {"json"}})
			var data map[string]any
			if err := json.Unmarshal(response.Body, &data); err != nil {
				t.Fatal(err)
			}
			if got := mustMonthly(t, data); got != float64((i+1)*11) {
				t.Fatalf("wrong account balance %v", got)
			}
			if _, ok := data["errors"]; ok {
				t.Fatal("raw errors leaked")
			}
		}
	}
	response, _ = quotaDashboardResponse(url.Values{"account": {"unknown"}})
	if response.StatusCode != 404 {
		t.Fatal("unknown account must not fall back")
	}
	response, _ = quotaDashboardResponse(url.Values{})
	if !strings.Contains(string(response.Body), `id="grid"`) {
		t.Fatal("missing dashboard")
	}
}

func TestDashboardWindowNormalization(t *testing.T) {
	w := quotaWindowValues(map[string]any{"used": 25.0, "cap": 100.0, "resetAt": "2026-09-20T12:00:00Z"})
	if w["remainingPercent"] != 75.0 || w["reset_at"] != "2026-09-20T12:00:00Z" {
		t.Fatalf("window=%v", w)
	}
}
