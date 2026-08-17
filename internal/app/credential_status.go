package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

// CPA_ROUTE_ALLOCATOR_CPA_URL is optional; the default matches CPA's usual
// local management listener. The browser's Management Key is forwarded for
// this one status request and is never persisted by the plugin.
var cpaManagementURL = "http://127.0.0.1:8317"

type hostHTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func init() {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("CPA_ROUTE_ALLOCATOR_CPA_URL")), "/"); value != "" {
		cpaManagementURL = value
	}
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
		"url":    strings.TrimRight(cpaManagementURL, "/") + "/v0/management/auth-files/status",
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
	var result hostHTTPResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return managementResponse{}, upstreamError("decode CPA Auth status response: " + err.Error())
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return managementResponse{}, upstreamError(fmt.Sprintf("CPA Auth status returned HTTP %d", result.StatusCode))
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
	return jsonResponse(http.StatusOK, updated.Public()), nil
}
