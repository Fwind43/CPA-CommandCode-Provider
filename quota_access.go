package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Destination comes only from trusted process configuration, never request headers.
var quotaAuthClient = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func quotaManagementURL() (string, error) {
	base := strings.TrimSpace(os.Getenv("COMMANDCODE_MANAGEMENT_BASE_URL"))
	if base == "" {
		base = "http://127.0.0.1:8317"
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", &url.Error{Op: "parse", URL: "management base", Err: http.ErrNotSupported}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v0/management/config"
	u.RawPath = ""
	return u.String(), nil
}

func quotaAuthorize(req pluginapi.ManagementRequest) bool {
	token := req.Headers.Get("Authorization")
	if !strings.HasPrefix(token, "Bearer ") || len(token) > 4096 || len(strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))) == 0 {
		return false
	}
	destination, err := quotaManagementURL()
	if err != nil {
		return false
	}
	check, err := http.NewRequest(http.MethodGet, destination, nil)
	if err != nil {
		return false
	}
	check.Header.Set("Authorization", token)
	response, err := quotaAuthClient.Do(check)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
