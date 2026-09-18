package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	providerKey       = "commandcode"
	defaultAPIBase    = "https://api.commandcode.ai"
	pluginName        = "commandcode"
	callbackSuffix    = "/v0/resource/plugins/commandcode/callback"
	defaultPublicBase = "http://127.0.0.1:8317"
)

var (
	configMu      sync.RWMutex
	apiBaseURL    = defaultAPIBase
	publicBaseURL = ""
	loginMu       sync.Mutex
	loginSessions = map[string]*loginSession{}
	httpClient    = &http.Client{Timeout: 10 * time.Minute}
)

type loginSession struct {
	State       string
	CreatedAt   time.Time
	Credential  map[string]any
	BridgeToken string
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[commandcode] "+format+"\n", args...)
}

func applyConfigYAML(request []byte) {
	var req lifecycleRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return
	}
	text := string(req.ConfigYAML)
	configMu.Lock()
	defer configMu.Unlock()
	if match := regexValue(text, "public[-_]base[-_]url"); match != "" {
		publicBaseURL = strings.TrimRight(match, "/")
	}
	if match := regexValue(text, "api[-_]base[-_]url"); match != "" {
		apiBaseURL = strings.TrimRight(match, "/")
	}
	for _, key := range []string{"quota_enabled", "quota-enabled", "quotaEnabled"} {
		if match := regexValue(text, key); match != "" {
			quotaEnabled = !quotaFalsy(match)
			break
		}
	}
	for _, key := range []string{"auth-dir", "auth_dir", "authDir"} {
		if match := regexValue(text, key); match != "" {
			quotaAuthDir = match
			break
		}
	}
	logf("config applied api_base=%s public_base=%s quota_enabled=%t", apiBaseURL, publicBaseURL, quotaEnabled)
}

func regexValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		value = strings.Trim(value, "\"'")
		if index := strings.Index(value, " #"); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
		return value
	}
	return ""
}

func commandCodeRegistration() registration {
	return registration{
		SchemaVersion: 1,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          "1.0.0",
			Author:           "custom",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "public_base_url",
					Type:        pluginapi.ConfigFieldType("string"),
					Description: "Public origin of this CLIProxyAPI instance (for example http://host:8317) used to build the Command Code browser login callback URL.",
				},
				{
					Name:        "api_base_url",
					Type:        pluginapi.ConfigFieldType("string"),
					Description: "Optional override for the Command Code API base URL. Defaults to https://api.commandcode.ai.",
				},
				{
					Name:        "quota_enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Serve the read-only Command Code quota page at /v0/resource/plugins/commandcode/quota (numbers only, no secrets). Defaults to true.",
				},
			},
		},
		Capabilities: registrationCapability{
			ModelRegistrar:        true,
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScope("static"),
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
			QuotaProvider:         true,
		},
	}
}

func publicModelID(id string) string {
 _, short, found := strings.Cut(id, "/")
 if !found { return id }
 for _, other := range goPlanModelIDs {
  _, candidate, hasPrefix := strings.Cut(other, "/")
  if !hasPrefix { candidate = other }
  if other != id && candidate == short { return id }
 }
 return short
}

func upstreamModelID(id string) string {
 for _, canonical := range goPlanModelIDs {
  if id == canonical { return canonical }
 }
 for _, canonical := range goPlanModelIDs {
  if id == publicModelID(canonical) { return canonical }
 }
 return id
}

func commandCodeModels() []pluginapi.ModelInfo {
	now := time.Now().Unix()
	models := make([]pluginapi.ModelInfo, 0, len(goPlanModelIDs))
	for _, id := range goPlanModelIDs {
        id = publicModelID(id)
		models = append(models, pluginapi.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             providerKey,
			Type:                "chat",
			DisplayName:         id,
			Name:                id,
			InputTokenLimit:     200000,
			OutputTokenLimit:    32768,
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedParameters: []string{"max_tokens", "temperature", "top_p", "stream", "stop"},
		})
	}
	return models
}

func randomState() string {
	buffer := make([]byte, 32)
	if _, errRead := rand.Read(buffer); errRead != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

// randomUUID returns a canonical UUIDv4. The upstream generate endpoint
// validates this field strictly and rejects anything else with
// `Invalid UUID at "threadId"`.
func randomUUID() string {
	buffer := make([]byte, 16)
	if _, errRead := rand.Read(buffer); errRead != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buffer[0:4], buffer[4:6], buffer[6:8], buffer[8:10], buffer[10:16])
}

func parseCommandCodeAuth(req pluginapi.AuthParseRequest) pluginapi.AuthParseResponse {
	var raw map[string]any
	if errUnmarshal := json.Unmarshal(req.RawJSON, &raw); errUnmarshal != nil {
		return pluginapi.AuthParseResponse{Handled: false}
	}
	apiKey := firstString(raw, "apiKey", "api_key", "commandCodeApiKey", "key", "token")
	if apiKey == "" {
		return pluginapi.AuthParseResponse{Handled: false}
	}
	fileName := strings.ToLower(req.FileName)
	rawText := strings.ToLower(string(req.RawJSON))
	marker := strings.Contains(fileName, "commandcode") || strings.Contains(fileName, "command-code") ||
		strings.Contains(rawText, "commandcode") || strings.Contains(rawText, "command-code")
	if !marker {
		return pluginapi.AuthParseResponse{Handled: false}
	}
	if len(apiKey) < 16 {
		return pluginapi.AuthParseResponse{Handled: false}
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: authDataFromCredential(raw, req.FileName)}
}

func authDataFromCredential(raw map[string]any, fileName string) pluginapi.AuthData {
	apiKey := firstString(raw, "apiKey", "api_key", "key", "token")
	userID := firstString(raw, "userId", "user_id", "id")
	userName := firstString(raw, "userName", "user_name", "email", "name")
	keyName := firstString(raw, "keyName", "key_name")
	label := userName
	if label == "" {
		label = keyName
	}
	if label == "" {
		label = userID
	}
	if fileName == "" {
		fileName = "commandcode.json"
	}
	metadata := map[string]any{}
	if userID != "" {
		metadata["user_id"] = userID
	}
	if userName != "" {
		metadata["user_name"] = userName
	}
	if keyName != "" {
		metadata["key_name"] = keyName
	}
	storage := map[string]any{
		"apiKey":          apiKey,
		"userId":          userID,
		"userName":        userName,
		"keyName":         keyName,
		"authenticatedAt": time.Now().UTC().Format(time.RFC3339),
	}
	storageJSON, _ := json.Marshal(storage)
	id := userID
	if id == "" {
		id = keyName
	}
	if id == "" {
		id = fileName
	}
	return pluginapi.AuthData{
		Provider:    providerKey,
		ID:          id,
		FileName:    fileName,
		Label:       label,
		StorageJSON: storageJSON,
		Metadata:    metadata,
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if text, isString := value.(string); isString && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	for _, nested := range []string{"auth", "credentials", "data", "user", "commandCode", "commandcode"} {
		if child, ok := values[nested].(map[string]any); ok {
			if found := firstString(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

// rawString reads a string field while preserving surrounding whitespace. Upstream
// streaming deltas frequently carry the word separator as part of the token, so
// trimming here would glue words together in the client-visible output.
func rawString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if text, isString := value.(string); isString {
				return text
			}
		}
	}
	for _, nested := range []string{"data", "delta"} {
		if child, ok := values[nested].(map[string]any); ok {
			if found := rawString(child, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}

func startCommandCodeLogin(req pluginapi.AuthLoginStartRequest) pluginapi.AuthLoginStartResponse {
	state := randomState()
	bridgeToken := randomState()
	if state == "" || bridgeToken == "" {
		return pluginapi.AuthLoginStartResponse{Provider: providerKey, URL: "about:blank", State: "", ExpiresAt: time.Now(), Metadata: map[string]any{"error": "secure random unavailable"}}
	}
	now := time.Now()
	expires := now.Add(5 * time.Minute)
	loginMu.Lock()
	for key, old := range loginSessions {
		if now.Sub(old.CreatedAt) > 5*time.Minute {
			delete(loginSessions, key)
		}
	}
	loginSessions[state] = &loginSession{State: state, CreatedAt: now, BridgeToken: bridgeToken}
	loginMu.Unlock()
	loginURL := "http://127.0.0.1:5959/start?state=" + url.QueryEscape(state) + "&bridge_token=" + url.QueryEscape(bridgeToken)
	logf("login session started (expires in 5 minutes)")
	return pluginapi.AuthLoginStartResponse{Provider: providerKey, URL: loginURL, State: state, ExpiresAt: expires, Metadata: map[string]any{"callback": "http://127.0.0.1:5959/callback"}}
}

func pollCommandCodeLogin(req pluginapi.AuthLoginPollRequest) pluginapi.AuthLoginPollResponse {
	state := req.State
	if state == "" {
		if value, ok := req.Metadata["state"].(string); ok {
			state = value
		}
	}
	now := time.Now()
	loginMu.Lock()
	session := loginSessions[state]
	var credential map[string]any
	if session != nil {
		if now.Sub(session.CreatedAt) > 5*time.Minute {
			delete(loginSessions, state)
		} else {
			credential = session.Credential
			if credential != nil {
				delete(loginSessions, state)
			}
		}
	}
	loginMu.Unlock()
	if credential == nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatus("pending")}
	}
	auth := authDataFromCredential(credential, "commandcode-"+safeID(firstString(credential, "userId", "keyName"))+".json")
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatus("success"),
		Message: "Command Code login completed",
		Auth:    auth,
	}
}

func safeID(value string) string {
	if value == "" {
		return "default"
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, value)
	return cleaned
}

func refreshCommandCodeAuth(req pluginapi.AuthRefreshRequest) pluginapi.AuthRefreshResponse {
	var raw map[string]any
	if errUnmarshal := json.Unmarshal(req.StorageJSON, &raw); errUnmarshal != nil || raw == nil {
		return pluginapi.AuthRefreshResponse{NextRefreshAfter: time.Now().Add(24 * time.Hour)}
	}
	auth := authDataFromCredential(raw, "")
	auth.ID = req.AuthID
	return pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: time.Now().Add(7 * 24 * time.Hour)}
}

func handleCommandCodeManagement(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	path := req.Path
	query := url.Values{}
	if req.Query != nil {
		for key, values := range req.Query {
			query[key] = values
		}
	}
	if index := strings.Index(path, "?"); index >= 0 {
		if parsed, errParse := url.ParseQuery(path[index+1:]); errParse == nil {
			for key, values := range parsed {
				query[key] = values
			}
		}
		path = path[:index]
	}
	if strings.Contains(path, "/callback") {
		return handleCommandCodeCallback(query, req)
	}
	if strings.Contains(path, "/status") {
		return handleCommandCodeStatus()
	}
	if strings.Contains(path, "/quota") {
		return handleCommandCodeQuota(req, query)
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:       []byte("unknown command code resource: " + path),
	}
}

func handleCommandCodeCallback(query url.Values, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	// Resource routes are public and the host dispatches only GET requests to
	// them. A delivery therefore arrives either as a strict JSON body (in-process
	// callers) or as percent-encoded headers (GET bridge delivery). Query based
	// key import stays disabled, and every delivery still has to prove knowledge
	// of the live one-time state and bridge token.
	if query.Get("apiKey") != "" {
		return htmlResponse(http.StatusMethodNotAllowed, "Command Code login failed", "Manual or plaintext credential import is disabled.")
	}
	var credential map[string]any
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &credential); err != nil {
			return htmlResponse(http.StatusBadRequest, "Command Code login failed", "Malformed bridge payload.")
		}
	} else {
		credential = credentialFromHeaders(req)
		if firstString(credential, "apiKey") == "" {
			return htmlResponse(http.StatusMethodNotAllowed, "Command Code login failed", "Manual or plaintext credential import is disabled.")
		}
	}
	state := strings.TrimSpace(firstString(credential, "state"))
	bridgeToken := strings.TrimSpace(firstString(credential, "bridgeToken"))
	now := time.Now()
	loginMu.Lock()
	session, exists := loginSessions[state]
	// Do not consume a live session unless both unguessable values match. This
	// prevents arbitrary public requests from exhausting a user's login attempt.
	matched := exists && bridgeToken != "" && subtle.ConstantTimeCompare([]byte(bridgeToken), []byte(session.BridgeToken)) == 1
	if matched {
		delete(loginSessions, state)
	} // one attempt after authenticated bridge proof
	loginMu.Unlock()
	if state == "" || !matched || now.Sub(session.CreatedAt) > 5*time.Minute {
		return htmlResponse(http.StatusBadRequest, "Command Code login failed", "This login session is missing, expired, or already consumed.")
	}
	apiKey := firstString(credential, "apiKey")
	if apiKey == "" || len(apiKey) < 16 {
		return htmlResponse(http.StatusBadRequest, "Command Code login failed", "The official callback payload was incomplete.")
	}
	userInfo, errWho := fetchWhoAmI(apiKey)
	if errWho != nil {
		logf("bridge callback credential verification rejected")
		return htmlResponse(http.StatusUnauthorized, "Command Code login failed", "The credential could not be verified.")
	}
	for _, key := range []string{"userId", "userName", "keyName"} {
		if value := firstString(userInfo, key); value != "" {
			credential[key] = value
		}
	}
	credential["apiKey"] = apiKey
	credential["authenticatedAt"] = now.UTC().Format(time.RFC3339)
	loginMu.Lock()
	loginSessions[state] = &loginSession{State: state, CreatedAt: session.CreatedAt, Credential: credential, BridgeToken: session.BridgeToken}
	loginMu.Unlock()
	logf("bridge callback accepted")
	return htmlResponse(http.StatusOK, "Command Code login complete", "Authorization verified. Return to CLIProxyAPI.")
}

// credentialFromHeaders reads a bridge delivery that arrived as GET headers. The
// bridge percent-encodes every value so metadata may carry arbitrary text while
// no raw CR/LF can reach the wire.
func credentialFromHeaders(req pluginapi.ManagementRequest) map[string]any {
	pairs := []struct {
		field  string
		header string
	}{
		{"apiKey", "X-CommandCode-Api-Key"},
		{"state", "X-CommandCode-State"},
		{"bridgeToken", "X-CommandCode-Bridge-Token"},
		{"userId", "X-CommandCode-User-Id"},
		{"userName", "X-CommandCode-User-Name"},
		{"keyName", "X-CommandCode-Key-Name"},
	}
	credential := map[string]any{}
	for _, pair := range pairs {
		values := req.Headers.Values(pair.header)
		if len(values) == 0 {
			continue
		}
		value := values[0]
		if decoded, errUnescape := url.QueryUnescape(value); errUnescape == nil {
			value = decoded
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		credential[pair.field] = value
	}
	return credential
}

func handleCommandCodeStatus() pluginapi.ManagementResponse {
	configMu.RLock()
	body, _ := json.Marshal(map[string]any{"provider": providerKey, "api_base_url": apiBaseURL, "models": len(goPlanModelIDs), "login_bridge": "loopback-required"})
	configMu.RUnlock()
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: body}
}

func htmlResponse(status int, title, message string) pluginapi.ManagementResponse {
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>" + title + "</title>" +
		"<style>body{font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem;text-align:center}</style></head>" +
		"<body><h1>" + html.EscapeString(title) + "</h1><p>" + html.EscapeString(message) + "</p></body></html>"
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	}
}

func fetchWhoAmI(apiKey string) (map[string]any, error) {
	configMu.RLock()
	base := apiBaseURL
	configMu.RUnlock()
	request, errRequest := http.NewRequest(http.MethodGet, base+"/alpha/whoami", nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("x-command-code-version", cliVersion)
	response, errDo := httpClient.Do(request)
	if errDo != nil {
		return nil, errDo
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whoami status %d", response.StatusCode)
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return parsed, nil
}

const (
	userAgent  = "command-code-cli/1.54.1"
	cliVersion = "1.54.1"
)

func executeCommandCode(req pluginapi.ExecutorRequest) pluginapi.ExecutorResponse {
	apiKey := apiKeyFromStorage(req.StorageJSON)
	if apiKey == "" {
		return errorExecutorResponse("missing Command Code apiKey in stored auth")
	}
	payload, errPayload := normalizeChatRequest(req)
	if errPayload != nil {
		return errorExecutorResponse(errPayload.Error())
	}
	result, errUpstream := callUpstream(context.Background(), apiKey, req.Model, payload, nil)
	if errUpstream != nil {
		return errorExecutorResponse(errUpstream.Error())
	}
	return pluginapi.ExecutorResponse{
		Payload: buildChatCompletion(req.Model, result),
		Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
	}
}

func validateCommandCodeStream(req pluginapi.ExecutorRequest) error {
	if apiKeyFromStorage(req.StorageJSON) == "" {
		return fmt.Errorf("missing Command Code apiKey in stored auth")
	}
	_, err := normalizeChatRequest(req)
	return err
}

func produceCommandCodeStream(streamID string, req pluginapi.ExecutorRequest) {
	closeStream := func(message string) {
		payload, _ := json.Marshal(streamCloseRequest{StreamID: streamID, Error: message})
		if err := invokeHost(pluginabi.MethodHostStreamClose, payload); err != nil {
			logf("stream close failed stream=%s model=%s err=%v", streamID, req.Model, err)
		}
	}
	emit := func(payload []byte) error {
		raw, errMarshal := json.Marshal(streamEmitRequest{StreamID: streamID, Payload: payload})
		if errMarshal != nil {
			return errMarshal
		}
		return invokeHost(pluginabi.MethodHostStreamEmit, raw)
	}

	apiKey := apiKeyFromStorage(req.StorageJSON)
	payload, errPayload := normalizeChatRequest(req)
	if errPayload != nil {
		closeStream(errPayload.Error())
		return
	}
	payload.onToolCall = func(call map[string]any) error { return emit(sseChunk(buildChunk(req.Model, map[string]any{"tool_calls": []map[string]any{call}}, nil))) }
	result, errUpstream := callUpstream(context.Background(), apiKey, req.Model, payload, func(text string) error {
		return emit(sseChunk(buildChunk(req.Model, map[string]any{"content": text}, nil)))
	})
	if errUpstream != nil {
		logf("stream upstream error model=%s err=%v", req.Model, errUpstream)
		closeStream(errUpstream.Error())
		return
	}
	finish := "stop"
	if result.FinishReason != "" {
		finish = result.FinishReason
	}
	if err := emit(sseChunk(buildChunk(req.Model, map[string]any{}, finish))); err != nil {
		closeStream(err.Error())
		return
	}
	if result.Usage != nil {
		if err := emit(sseChunk(buildUsageChunk(req.Model, result.Usage))); err != nil {
			closeStream(err.Error())
			return
		}
	}
	closeStream("")
}

func countCommandCodeTokens(req pluginapi.ExecutorRequest) pluginapi.ExecutorResponse {
	total := len(req.Payload)/4 + 1
	body, _ := json.Marshal(map[string]any{"total_tokens": total, "totalTokens": total})
	return pluginapi.ExecutorResponse{Payload: body, Headers: http.Header{"Content-Type": []string{"application/json"}}}
}

func errorExecutorResponse(message string) pluginapi.ExecutorResponse {
	return pluginapi.ExecutorResponse{
		Payload: buildErrorCompletion("", message),
		Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
	}
}

func apiKeyFromStorage(storage []byte) string {
	if len(storage) == 0 {
		return ""
	}
	var raw map[string]any
	if errUnmarshal := json.Unmarshal(storage, &raw); errUnmarshal != nil {
		return ""
	}
	return firstString(raw, "apiKey", "api_key", "commandCodeApiKey", "key", "token")
}

type chatRequestPayload struct {
 Tools []map[string]any `json:"tools"`
 ToolChoice any `json:"tool_choice"`
 onToolCall func(map[string]any) error

	Model       string           `json:"model"`
	Messages    []map[string]any `json:"messages"`
	MaxTokens   int              `json:"max_tokens"`
	MaxComplete int              `json:"max_completion_tokens"`
	Stream      bool             `json:"stream"`
}

func normalizeChatRequest(req pluginapi.ExecutorRequest) (chatRequestPayload, error) {
	var payload chatRequestPayload
	if errUnmarshal := json.Unmarshal(req.Payload, &payload); errUnmarshal != nil {
		return payload, fmt.Errorf("invalid chat-completions payload: %w", errUnmarshal)
	}
	if payload.Model == "" {
		payload.Model = req.Model
	}
	if len(payload.Messages) == 0 {
		return payload, fmt.Errorf("payload has no messages")
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = payload.MaxComplete
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = 8192
	}
	if payload.MaxTokens > 32768 {
		payload.MaxTokens = 32768
	}
	return payload, nil
}

type upstreamResult struct {
 ToolCalls []map[string]any

	Text         string
	Reasoning    string
	FinishReason string
	Usage        map[string]any
}

func callUpstream(ctx context.Context, apiKey, model string, payload chatRequestPayload, onDelta func(string) error) (upstreamResult, error) {
 model = upstreamModelID(model)
 upstreamMessages := make([]map[string]any, 0, len(payload.Messages))
 var systemInstructions []string
 toolNames := map[string]string{}
 for _, message := range payload.Messages {
  role := firstString(message, "role")
  if role == "" { role = "user" }
  text := messageText(message)
  if role == "system" || role == "developer" {
   systemInstructions = append(systemInstructions, text); continue
  }
  parts := []map[string]any{}
  if role == "tool" {
   id := firstString(message, "tool_call_id")
   name := toolNames[id]
   if name == "" { return upstreamResult{}, fmt.Errorf("tool result has no matching tool call: %s", id) }
   parts = append(parts, map[string]any{"type":"tool-result", "toolCallId":id, "toolName":name, "output":map[string]any{"type":"text", "value":text}})
  } else {
   // Tool calls are structured content, never a text fallback.
   if message["content"] != nil && text != "" { parts = append(parts, map[string]any{"type":"text", "text":text}) }
   if calls, ok := message["tool_calls"].([]any); ok {
    for _, raw := range calls {
     call, ok := raw.(map[string]any); if !ok { return upstreamResult{}, fmt.Errorf("invalid tool call") }
     fn, ok := call["function"].(map[string]any); if !ok { return upstreamResult{}, fmt.Errorf("invalid tool function") }
     id, name := firstString(call,"id"), firstString(fn,"name")
     var input any
     if err := json.Unmarshal([]byte(rawString(fn,"arguments")), &input); err != nil { return upstreamResult{}, fmt.Errorf("invalid arguments for %s: %w",name,err) }
     toolNames[id] = name
     parts = append(parts,map[string]any{"type":"tool-call","toolCallId":id,"toolName":name,"input":input})
    }
   }
  }
  if len(parts)>0 { upstreamMessages = append(upstreamMessages,map[string]any{"role":role,"content":parts}) }
 }
 tools := []map[string]any{}
 for _, tool := range payload.Tools {
  fn, ok := tool["function"].(map[string]any)
  if !ok || firstString(tool,"type") != "function" { return upstreamResult{},fmt.Errorf("unsupported tool type") }
  schema := fn["parameters"]
  if schema == nil { schema = map[string]any{"type":"object","properties":map[string]any{}} }
  tools = append(tools,map[string]any{"name":fn["name"],"description":fn["description"],"input_schema":schema})
 }
 if payload.ToolChoice == "none" { tools = []map[string]any{} }
	if len(upstreamMessages) == 0 {
		return upstreamResult{}, fmt.Errorf("no text content in chat messages")
	}
	body := map[string]any{
		"config": map[string]any{
			"workingDir":    "/tmp",
			"date":          time.Now().Format("2006-01-02"),
			"environment":   "linux",
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory":         nil,
		"permissionMode": "standard",
		"threadId":       randomUUID(),
		"params": map[string]any{
			"model":           model,
			"canonicalID":     model,
			"messages":        upstreamMessages,
			"tools":           tools,
			"maxOutputTokens": payload.MaxTokens,
		},
	}
	if len(systemInstructions) > 0 {
		body["params"].(map[string]any)["system"] = strings.Join(systemInstructions, "\n\n")
	}
	encoded, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return upstreamResult{}, errMarshal
	}
	configMu.RLock()
	base := apiBaseURL
	configMu.RUnlock()
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, base+"/alpha/generate", bytes.NewReader(encoded))
	if errRequest != nil {
		return upstreamResult{}, errRequest
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("x-command-code-version", cliVersion)
	request.Header.Set("x-cli-environment", "production")
	response, errDo := httpClient.Do(request)
	if errDo != nil {
		return upstreamResult{}, errDo
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return upstreamResult{}, fmt.Errorf("upstream status %d: %s", response.StatusCode, truncate(string(detail), 400))
	}
	result := upstreamResult{}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "[DONE]" {
			break
		}
		var event map[string]any
		if errUnmarshal := json.Unmarshal([]byte(line), &event); errUnmarshal != nil {
			continue
		}
		eventType := firstString(event, "type")
		switch eventType {
		case "text-delta", "reasoning-delta", "reasoning":
			text := rawString(event, "text", "delta", "content")
			if text == "" {
				text = firstString(event, "text", "delta", "content")
			}
			if text == "" {
				continue
			}
			if eventType == "reasoning-delta" {
				result.Reasoning += text
				continue
			}
			result.Text += text
			if onDelta != nil {
				if errDelta := onDelta(text); errDelta != nil {
					return result, errDelta
				}
			}
  case "tool-call":
   if executed, _ := event["providerExecuted"].(bool); executed { continue }
   input, ok := event["input"]; if !ok { input = event["args"] }
   arguments := ""
   if value, ok := input.(string); ok { arguments = value } else { encoded, err := json.Marshal(input); if err != nil { return result, err }; arguments = string(encoded) }
   if !json.Valid([]byte(arguments)) { return result, fmt.Errorf("invalid upstream tool arguments") }
   call := map[string]any{"id":firstString(event,"toolCallId"),"type":"function","function":map[string]any{"name":firstString(event,"toolName"),"arguments":arguments}}
   if call["id"] == "" { return result, fmt.Errorf("missing upstream tool call id") }
   result.ToolCalls = append(result.ToolCalls,call)
   if payload.onToolCall != nil {
    delta := map[string]any{"index":len(result.ToolCalls)-1,"id":call["id"],"type":"function","function":call["function"]}
    if err := payload.onToolCall(delta); err != nil { return result,err }
   }
		case "finish":
			if value := firstString(event, "finishReason", "finish_reason"); value != "" {
				result.FinishReason = mapFinishReason(value)
			}
			if usage, ok := event["totalUsage"].(map[string]any); ok {
				result.Usage = usage
			} else if usage, ok := event["usage"].(map[string]any); ok {
				result.Usage = usage
			}
		case "error":
			message := firstString(event, "message", "error", "detail")
			if message == "" {
				message = "upstream error event"
			}
			return result, fmt.Errorf("%s", message)
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return result, errScan
	}
	if strings.TrimSpace(result.Text) == "" && result.Reasoning != "" {
		result.Text = result.Reasoning
	}
	if len(result.ToolCalls)>0 { result.FinishReason = "tool_calls" }
	return result, nil
}

func mapFinishReason(value string) string {
	switch value {
	case "stop", "end_turn", "stop_sequence", "complete":
		return "stop"
	case "length", "max_tokens", "max_output_tokens":
		return "length"
	case "tool_calls", "tool_use", "tool-calls":
		return "tool_calls"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

func messageText(message map[string]any) string {
	if text, ok := message["content"].(string); ok {
		return text
	}
	if parts, ok := message["content"].([]any); ok {
		var builder strings.Builder
		for _, part := range parts {
			if item, isMap := part.(map[string]any); isMap {
				if text := firstString(item, "text", "content"); text != "" {
					builder.WriteString(text)
				}
			} else if text, isString := part.(string); isString {
				builder.WriteString(text)
			}
		}
		return builder.String()
	}
	if message["content"] == nil {
		if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
			encoded, _ := json.Marshal(toolCalls)
			return string(encoded)
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func completionID() string {
	return "chatcmpl-" + randomState()[:24]
}

func buildChatCompletion(model string, result upstreamResult) []byte {
	message := map[string]any{"role": "assistant", "content": result.Text}
 if len(result.ToolCalls)>0 { message["tool_calls"] = result.ToolCalls; if result.Text == "" { message["content"] = nil } }
	if result.Reasoning != "" {
		message["reasoning_content"] = result.Reasoning
	}
	body := map[string]any{
		"id":      completionID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": mapFinishReason(result.FinishReason),
		}},
		"usage": usagePayload(result.Usage, result.Text),
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func buildChunk(model string, delta map[string]any, finish any) []byte {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = finish
	}
	body := map[string]any{
		"id":      completionID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func buildUsageChunk(model string, usage map[string]any) []byte {
	body := map[string]any{
		"id":      completionID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{},
		"usage":   usagePayload(usage, ""),
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func buildErrorCompletion(model, message string) []byte {
	if model == "" {
		model = providerKey
	}
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    "commandcode_error",
		},
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func sseChunk(payload []byte) []byte {
	// The host wraps each executor stream chunk with the SSE "data: " prefix and a
	// trailing blank line, so the plugin must emit the raw JSON payload only.
	return payload
}

func usagePayload(usage map[string]any, text string) map[string]any {
	prompt := intFromAny(usage, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens")
	completion := intFromAny(usage, "outputTokens", "output_tokens", "completionTokens", "completion_tokens")
	total := intFromAny(usage, "totalTokens", "total_tokens")
	if completion == 0 && text != "" {
		completion = len(text)/4 + 1
	}
	if prompt == 0 && completion == 0 {
		total = 0
	} else if total == 0 {
		total = prompt + completion
	}
	payload := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	// Cache hits are reported as a subset of the input tokens, so they are
	// surfaced as prompt_tokens_details and never added to prompt_tokens.
	details := map[string]any{}
	if cached := cachedInputTokens(usage); cached > 0 {
		details["cached_tokens"] = cached
	}
	if creation := cacheCreationInputTokens(usage); creation > 0 {
		details["cache_creation_tokens"] = creation
	}
	if len(details) > 0 {
		payload["prompt_tokens_details"] = details
	}
	return payload
}

// cacheDetailContainers lists the upstream usage keys that may nest cache
// counters, covering both the camelCase agent protocol and OpenAI style names.
var cacheDetailContainers = []string{
	"inputTokenDetails",
	"input_tokens_details",
	"promptTokensDetails",
	"prompt_tokens_details",
}

func cachedInputTokens(usage map[string]any) int {
	if value := intFromContainers(usage, []string{
		"cacheReadTokens", "cache_read_tokens", "cachedTokens", "cached_tokens",
		"cacheReadInputTokens", "cache_read_input_tokens",
		"promptCacheHitTokens", "prompt_cache_hit_tokens",
	}); value > 0 {
		return value
	}
	return intFromAnyPositive(usage,
		"cacheReadTokens", "cache_read_tokens", "cachedInputTokens", "cached_tokens",
		"cache_read_input_tokens", "promptCacheHitTokens", "prompt_cache_hit_tokens")
}

func cacheCreationInputTokens(usage map[string]any) int {
	if value := intFromContainers(usage, []string{
		"cacheCreationTokens", "cache_creation_tokens",
		"cacheWriteTokens", "cache_write_tokens",
	}); value > 0 {
		return value
	}
	return intFromAny(usage, "cacheCreationTokens", "cache_creation_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens")
}

func intFromContainers(usage map[string]any, keys []string) int {
	for _, container := range cacheDetailContainers {
		nested, ok := usage[container].(map[string]any)
		if !ok {
			continue
		}
		if value := intFromAnyPositive(nested, keys...); value > 0 {
			return value
		}
	}
	return 0
}

// intFromAnyPositive returns the first key whose value is present and greater
// than zero, so a zero-valued alias (for example cachedInputTokens: 0) cannot
// mask a populated sibling such as cache_read_tokens.
func intFromAnyPositive(values map[string]any, keys ...string) int {
	if values == nil {
		return 0
	}
	for _, key := range keys {
		if value := intFromAny(values, key); value > 0 {
			return value
		}
	}
	return 0
}

func intFromAny(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if values == nil {
			return 0
		}
		if value, ok := values[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int(typed)
			case int:
				return typed
			case json.Number:
				parsed, errParse := typed.Int64()
				if errParse == nil {
					return int(parsed)
				}
			}
		}
	}
	return 0
}
