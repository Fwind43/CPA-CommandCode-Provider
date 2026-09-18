// Command Code provider plugin for CLIProxyAPI.
//
// Implements the CLIProxyAPI C ABI plugin contract: model provider, auth
// provider, OpenAI chat-completions executor, and a browser-navigable login
// callback resource. Build with:
//
//	go build -buildmode=c-shared -o commandcode.so .
package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar           bool                         `json:"model_registrar"`
	ModelProvider            bool                         `json:"model_provider"`
	AuthProvider             bool                         `json:"auth_provider"`
	FrontendAuthProvider     bool                         `json:"frontend_auth_provider"`
	Executor                 bool                         `json:"executor"`
	ExecutorModelScope       pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats     []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats    []string                     `json:"executor_output_formats,omitempty"`
	RequestTranslator        bool                         `json:"request_translator"`
	RequestNormalizer        bool                         `json:"request_normalizer"`
	ResponseTranslator       bool                         `json:"response_translator"`
	ResponseBeforeTranslator bool                         `json:"response_before_translator"`
	ResponseAfterTranslator  bool                         `json:"response_after_translator"`
	ThinkingApplier          bool                         `json:"thinking_applier"`
	UsagePlugin              bool                         `json:"usage_plugin"`
	CommandLinePlugin        bool                         `json:"command_line_plugin"`
	ManagementAPI            bool                         `json:"management_api"`
	QuotaProvider            bool                         `json:"quota_provider"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type streamRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

type streamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type streamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

var invokeHost = callHost

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func writeResponse(response *C.cliproxy_buffer, payload []byte) {
	if response == nil || len(payload) == 0 {
		return
	}
	ptr := C.malloc(C.size_t(len(payload)))
	if ptr == nil {
		return
	}
	C.memcpy(ptr, unsafe.Pointer(&payload[0]), C.size_t(len(payload)))
	response.ptr = ptr
	response.len = C.size_t(len(payload))
}

func callHost(method string, payload []byte) error {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}

	var response C.cliproxy_buffer
	status := C.call_host_api(cMethod, request, C.size_t(len(payload)), &response)
	var raw []byte
	if response.ptr != nil {
		raw = C.GoBytes(response.ptr, C.int(response.len))
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(raw) == 0 {
		if status != 0 {
			return fmt.Errorf("host call %s failed with status %d", method, int(status))
		}
		return fmt.Errorf("host call %s returned an empty response", method)
	}
	var reply envelope
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("decode host call %s response: %w", method, err)
	}
	if !reply.OK {
		if reply.Error != nil && reply.Error.Message != "" {
			return fmt.Errorf("host call %s: %s", method, reply.Error.Message)
		}
		return fmt.Errorf("host call %s failed with status %d", method, int(status))
	}
	if status != 0 {
		return fmt.Errorf("host call %s failed with status %d", method, int(status))
	}
	return nil
}

func okEnvelope(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		applyConfigYAML(request)
		return okEnvelope(commandCodeRegistration())
	case pluginabi.MethodPluginReconfigure:
		applyConfigYAML(request)
		return okEnvelope(commandCodeRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(map[string]any{})
	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: providerKey, Models: commandCodeModels()})
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: providerKey, Models: commandCodeModels()})
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerKey})
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(parseCommandCodeAuth(req))
	case pluginabi.MethodAuthLoginStart:
		var req pluginapi.AuthLoginStartRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(startCommandCodeLogin(req))
	case pluginabi.MethodAuthLoginPoll:
		var req pluginapi.AuthLoginPollRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(pollCommandCodeLogin(req))
	case pluginabi.MethodAuthRefresh:
		var req pluginapi.AuthRefreshRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(refreshCommandCodeAuth(req))
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerKey})
	case pluginabi.MethodExecutorExecute:
		var req pluginapi.ExecutorRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(executeCommandCode(req))
	case pluginabi.MethodExecutorExecuteStream:
		var req streamRPCRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		if req.StreamID == "" {
			return errorEnvelope("invalid_request", "stream_id is required"), nil
		}
		if errValidate := validateCommandCodeStream(req.ExecutorRequest); errValidate != nil {
			return errorEnvelope("invalid_request", errValidate.Error()), nil
		}
		go produceCommandCodeStream(req.StreamID, req.ExecutorRequest)
		return okEnvelope(streamResponse{})
	case pluginabi.MethodExecutorCountTokens:
		var req pluginapi.ExecutorRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(countCommandCodeTokens(req))
	case pluginabi.MethodExecutorHTTPRequest:
		return okEnvelope(pluginapi.ExecutorHTTPResponse{StatusCode: http.StatusNotImplemented, Body: []byte(`{"error":"not supported"}`)})
	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{SupportedProviders: []string{providerKey}, DisplayName: "Command Code", SupportsReset: false})
	case pluginabi.MethodQuotaFetch:
		var req pluginapi.QuotaFetchRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(commandCodeQuotaFetch(req))
	case pluginabi.MethodQuotaReset:
		return errorEnvelope("unsupported", "Command Code quota is read-only"), nil
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/callback",
				Menu:        "Command Code login",
				Description: "Receives the Command Code browser login redirect and stores the API key for the waiting session.",
			},
			{
				Path:        "/status",
				Menu:        "Command Code status",
				Description: "Shows pending Command Code login sessions and the configured public callback base URL.",
			},
			{
				Path:        "/quota",
				Menu:        "Command Code quota",
				Description: "Read-only Command Code Go plan quota: monthly credits, 5h and weekly window usage, lifetime calls and cost. Non-sensitive numbers only; cached for 5 minutes.",
			},
		}})
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return errorEnvelope("invalid_request", errDecode.Error()), nil
		}
		return okEnvelope(handleCommandCodeManagement(req))
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}
