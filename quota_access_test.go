package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type accessTransport func(*http.Request) (*http.Response, error)

func (f accessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockQuotaManagementAccess(t *testing.T) {
	t.Helper()
	old := quotaAuthClient
	t.Cleanup(func() { quotaAuthClient = old })
	quotaAuthClient = &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:8317/v0/management/config" {
			t.Fatal("unsafe authentication destination")
		}
		status := http.StatusUnauthorized
		if r.Header.Get("Authorization") == "Bearer fixture-valid" {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
}

func TestDashboardAccess(t *testing.T) {
	old := quotaAuthClient
	defer func() { quotaAuthClient = old }()
	quotaAuthClient = &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:8317/v0/management/config" {
			t.Fatal("unsafe destination")
		}
		status := 401
		if r.Header.Get("Authorization") == "Bearer fixture-valid" {
			status = 200
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for _, token := range []string{"", "Bearer invalid"} {
		for _, query := range []string{"accounts=1", "format=json", "auth_index=0", "account=unknown", "refresh=1"} {
			q, _ := url.ParseQuery(query)
			r := handleCommandCodeQuota(pluginapi.ManagementRequest{Headers: http.Header{"Authorization": {token}}}, q)
			if r.StatusCode != 401 {
				t.Fatalf("query=%s status=%d", query, r.StatusCode)
			}
		}
	}
	if !quotaAuthorize(pluginapi.ManagementRequest{Headers: http.Header{"Authorization": {"Bearer fixture-valid"}}}) {
		t.Fatal("valid key denied")
	}
}

func TestQuotaManagementURL(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"", "http://127.0.0.1:8317/v0/management/config"},
		{"http://127.0.0.1:9321", "http://127.0.0.1:9321/v0/management/config"},
		{"https://example.invalid/cpa/", "https://example.invalid/cpa/v0/management/config"},
	} {
		t.Setenv("COMMANDCODE_MANAGEMENT_BASE_URL", tc.base)
		got, err := quotaManagementURL()
		if err != nil || got != tc.want {
			t.Fatalf("got %q err %v", got, err)
		}
	}
	for _, base := range []string{"file:///tmp/auth", "http://user:pass@example.invalid", "https://example.invalid?target=x"} {
		t.Setenv("COMMANDCODE_MANAGEMENT_BASE_URL", base)
		if _, err := quotaManagementURL(); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
