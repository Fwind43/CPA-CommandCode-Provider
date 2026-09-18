package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func decodeEnvelopeResult(t *testing.T, raw []byte, target any) {
	t.Helper()
	var outer struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !outer.OK {
		t.Fatalf("envelope not OK: %s", raw)
	}
	if err := json.Unmarshal(outer.Result, target); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}

func TestRegistrationAdvertisesNativeQuotaProvider(t *testing.T) {
	registration := commandCodeRegistration()
	if !registration.Capabilities.QuotaProvider {
		t.Fatal("quota_provider capability is false")
	}
}

func TestNativeQuotaDescribeDispatch(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodQuotaDescribe, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got pluginapi.QuotaDescribeResponse
	decodeEnvelopeResult(t, raw, &got)
	if len(got.SupportedProviders) != 1 || got.SupportedProviders[0] != providerKey {
		t.Fatalf("supported providers = %#v", got.SupportedProviders)
	}
	if got.SupportsReset {
		t.Fatal("read-only quota unexpectedly supports reset")
	}
}

func TestQuotaRegistersAuthenticatedDashboardShell(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got managementRegistrationResponse
	decodeEnvelopeResult(t, raw, &got)
	found := false
	for _, resource := range got.Resources {
		if resource.Path == "/quota" {
			found = true
		}
	}
	if !found {
		t.Fatal("quota iframe resource missing")
	}
	shell := handleCommandCodeManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/quota"})
	if shell.StatusCode != http.StatusOK || !strings.Contains(string(shell.Body), `id="grid"`) {
		t.Fatal("dashboard shell missing")
	}
	for _, query := range []map[string][]string{{"accounts": {"1"}}, {"format": {"json"}}} {
		data := handleCommandCodeManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/quota", Query: query})
		if data.StatusCode != http.StatusUnauthorized {
			t.Fatal("public quota data must require management authentication")
		}
	}
}

func TestManagementHandlerDispatchesQuotaJSON(t *testing.T) {
	mockQuotaManagementAccess(t)
	request, err := json.Marshal(pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/plugins/commandcode/quota",
		Headers: http.Header{"Authorization": {"Bearer fixture-valid"}},
		Query:   map[string][]string{"format": {"json"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod(pluginabi.MethodManagementHandle, request)
	if err != nil {
		t.Fatal(err)
	}
	var got pluginapi.ManagementResponse
	decodeEnvelopeResult(t, raw, &got)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.StatusCode, got.Body)
	}
	if got.Headers.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", got.Headers.Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("invalid JSON body: %v; body=%s", err, got.Body)
	}
	if _, exists := body["available"]; !exists {
		t.Fatalf("quota JSON lacks available: %s", got.Body)
	}
}

func TestUnknownManagementPathReturns404(t *testing.T) {
	got := handleCommandCodeManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/plugins/commandcode/not-quota"})
	if got.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", got.StatusCode)
	}
}
