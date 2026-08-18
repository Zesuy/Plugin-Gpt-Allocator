package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

// CPA_ROUTE_ALLOCATOR_CPA_URL is optional; the default matches CPA's usual
// local management listener. The browser's Management Key is forwarded for
// this one status request and is never persisted by the plugin.
var cpaManagementURL = "http://127.0.0.1:8317"
var cpaManagementURLExplicit bool

type hostHTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type credentialRouteConflict struct {
	RouteSlotID       string   `json:"route_slot_id"`
	EnabledCount      int      `json:"enabled_count"`
	EnabledIdentities []string `json:"enabled_identities"`
}

// credentialStatusResult keeps the normal PublicState response shape while
// adding a small, non-persistent hint for the enable flow. The UI can ask the
// user whether to reassign instead of silently moving a credential.
type credentialStatusResult struct {
	model.PublicState
	RouteConflict *credentialRouteConflict `json:"route_conflict,omitempty"`
}

func init() {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("CPA_ROUTE_ALLOCATOR_CPA_URL")), "/"); value != "" {
		cpaManagementURL = value
		cpaManagementURLExplicit = true
	}
}

// cpaManagementBaseURL follows the address used to reach the plugin when the
// deployment did not explicitly configure CPA_ROUTE_ALLOCATOR_CPA_URL. This
// matters when CPA binds only to a LAN address (for example 192.168.x.x) and
// 127.0.0.1 is another service or not reachable from the plugin host.
func cpaManagementBaseURL(headers http.Header) string {
	if cpaManagementURLExplicit {
		return strings.TrimRight(cpaManagementURL, "/")
	}
	for _, header := range []string{"X-CPA-Route-Allocator-Origin", "Origin", "Referer"} {
		value := strings.TrimSpace(headerValue(headers, header))
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host
		}
	}
	for _, header := range []string{"X-Forwarded-Host", "Host"} {
		host := strings.TrimSpace(headerValue(headers, header))
		if host == "" {
			continue
		}
		if !strings.Contains(host, ":") {
			host += ":8317"
		}
		return "http://" + host
	}
	return strings.TrimRight(cpaManagementURL, "/")
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func (a *App) setCredentialStatus(body []byte, requestHeaders http.Header) (managementResponse, error) {
	var input credentialStatusInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Identity = strings.TrimSpace(input.Identity)
	if input.Identity == "" {
		return managementResponse{}, clientError("identity is required")
	}
	authorization := strings.TrimSpace(requestHeaders.Get("Authorization"))
	if authorization == "" {
		return managementResponse{}, clientError("management authorization is required")
	}
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	credentialIndex := -1
	for index := range value.Credentials {
		if value.Credentials[index].Identity == input.Identity {
			credentialIndex = index
			break
		}
	}
	if credentialIndex < 0 {
		return managementResponse{}, clientError("credential is not managed")
	}
	credential := value.Credentials[credentialIndex]
	payload, err := json.Marshal(map[string]any{"name": credential.AuthFile, "disabled": input.Disabled})
	if err != nil {
		return managementResponse{}, err
	}
	request := map[string]any{
		"method": http.MethodPatch,
		"url":    cpaManagementBaseURL(requestHeaders) + "/v0/management/auth-files/status",
		"headers": http.Header{
			"Authorization": []string{authorization},
			"Content-Type":  []string{"application/json"},
		},
		"body": payload,
	}
	raw, err := a.host.Call("host.http.do", request)
	if err != nil {
		return managementResponse{}, upstreamError("update CPA Auth status: " + err.Error())
	}
	var hostResult hostHTTPResponse
	if err := json.Unmarshal(raw, &hostResult); err != nil {
		return managementResponse{}, upstreamError("decode CPA Auth status response: " + err.Error())
	}
	if hostResult.StatusCode < 200 || hostResult.StatusCode >= 300 {
		detail := ""
		var apiError struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(hostResult.Body, &apiError) == nil {
			detail = strings.TrimSpace(apiError.Error)
		}
		if detail != "" {
			return managementResponse{}, upstreamError(fmt.Sprintf("CPA Auth status returned HTTP %d: %s", hostResult.StatusCode, detail))
		}
		return managementResponse{}, upstreamError(fmt.Sprintf("CPA Auth status returned HTTP %d", hostResult.StatusCode))
	}
	updated, err := a.store.Update(func(state *model.State) error {
		for index := range state.Credentials {
			if state.Credentials[index].Identity == input.Identity {
				state.Credentials[index].Disabled = input.Disabled
				state.Credentials[index].UpdatedAt = time.Now().UTC()
				return nil
			}
		}
		return clientError("credential disappeared while updating status")
	})
	if err != nil {
		return managementResponse{}, err
	}
	result := credentialStatusResult{PublicState: updated.Public()}
	if !input.Disabled {
		result.RouteConflict = findCredentialRouteConflict(updated, input.Identity)
	}
	return jsonResponse(http.StatusOK, result), nil
}

func findCredentialRouteConflict(value model.State, identity string) *credentialRouteConflict {
	var target *model.Credential
	for index := range value.Credentials {
		if value.Credentials[index].Identity == identity {
			target = &value.Credentials[index]
			break
		}
	}
	if target == nil || target.RouteSlotID == "" || target.Disabled {
		return nil
	}
	conflict := &credentialRouteConflict{RouteSlotID: target.RouteSlotID}
	for _, credential := range value.Credentials {
		if credential.Identity == identity || credential.Disabled || credential.RouteSlotID != target.RouteSlotID {
			continue
		}
		name := credential.DisplayName()
		if name == "" {
			name = credential.Identity
		}
		conflict.EnabledIdentities = append(conflict.EnabledIdentities, name)
	}
	conflict.EnabledCount = len(conflict.EnabledIdentities)
	if conflict.EnabledCount == 0 {
		return nil
	}
	return conflict
}
