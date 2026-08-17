package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

const codexQuotaURL = "https://chatgpt.com/backend-api/wham/usage"

type quotaInput struct {
	Identity string `json:"identity"`
}

type quotaCheckResult struct {
	Identity string               `json:"identity"`
	Quota    *model.QuotaSnapshot `json:"quota,omitempty"`
}

type cpaAPICallResponse struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func (a *App) checkCredentialQuota(body []byte, headers http.Header) (managementResponse, error) {
	var input quotaInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Identity = strings.TrimSpace(input.Identity)
	if input.Identity == "" {
		return managementResponse{}, clientError("identity is required")
	}
	authorization := strings.TrimSpace(headers.Get("Authorization"))
	if authorization == "" {
		return managementResponse{}, clientError("management authorization is required")
	}
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	var credential model.Credential
	found := false
	for _, item := range value.Credentials {
		if item.Identity == input.Identity {
			credential, found = item, true
			break
		}
	}
	if !found {
		return managementResponse{}, clientError("credential is not managed")
	}

	now := time.Now().UTC()
	snapshot := &model.QuotaSnapshot{Provider: credential.Provider, CheckedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if !strings.EqualFold(credential.Provider, "codex") && credential.Provider != "" {
		snapshot.Error = "当前自用版只检查 Codex 额度"
	} else {
		authIndex := credential.AuthFile
		if files, listErr := a.listHostAuthFiles(); listErr == nil {
			for _, file := range files {
				if strings.EqualFold(file.Name, credential.AuthFile) && strings.TrimSpace(file.AuthIndex) != "" {
					authIndex = file.AuthIndex
					break
				}
			}
		}
		requestHeaders := map[string]string{
			"Authorization": "Bearer $TOKEN$",
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
		}
		if strings.TrimSpace(credential.AccountID) != "" {
			requestHeaders["Chatgpt-Account-Id"] = credential.AccountID
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"auth_index": authIndex,
			"method":     http.MethodGet,
			"url":        codexQuotaURL,
			"header":     requestHeaders,
		})
		if marshalErr != nil {
			return managementResponse{}, marshalErr
		}
		raw, callErr := a.host.Call("host.http.do", map[string]any{
			"method": http.MethodPost,
			"url":    cpaManagementBaseURL(headers) + "/v0/management/api-call",
			"headers": http.Header{
				"Authorization": []string{authorization},
				"Content-Type":  []string{"application/json"},
			},
			"body": payload,
		})
		if callErr != nil {
			snapshot.Error = "调用 CPA 额度接口失败"
		} else {
			var response hostHTTPResponse
			if decodeErr := json.Unmarshal(raw, &response); decodeErr != nil {
				snapshot.Error = "读取 CPA 额度响应失败"
			} else {
				var apiResponse cpaAPICallResponse
				if apiErr := json.Unmarshal(response.Body, &apiResponse); apiErr != nil {
					snapshot.Error = "读取 CPA 额度响应失败"
				} else if apiResponse.StatusCode < 200 || apiResponse.StatusCode >= 300 {
					snapshot.Error = codexQuotaHTTPError(apiResponse.StatusCode, []byte(apiResponse.Body))
				} else if parseErr := parseCodexQuota([]byte(apiResponse.Body), snapshot); parseErr != nil {
					snapshot.Error = "解析 Codex 额度响应失败"
				}
			}
		}
	}
	updated, err := a.store.Update(func(state *model.State) error {
		for index := range state.Credentials {
			if state.Credentials[index].Identity == input.Identity {
				state.Credentials[index].Quota = snapshot
				state.Credentials[index].UpdatedAt = now
				return nil
			}
		}
		return clientError("credential disappeared while checking quota")
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, quotaCheckResult{Identity: input.Identity, Quota: updated.Credentials[findCredentialIndex(updated, input.Identity)].Quota}), nil
}

func findCredentialIndex(value model.State, identity string) int {
	for index, credential := range value.Credentials {
		if credential.Identity == identity {
			return index
		}
	}
	return 0
}

func parseCodexQuota(body []byte, snapshot *model.QuotaSnapshot) error {
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	rateLimit, _ := document["rate_limit"].(map[string]any)
	if rateLimit == nil {
		return fmt.Errorf("rate_limit is missing")
	}
	rowsBefore := len(snapshot.Rows)
	appendCodexQuotaRows(snapshot, "", rateLimit)
	if codeReview, _ := document["code_review_rate_limit"].(map[string]any); codeReview != nil {
		appendCodexQuotaRows(snapshot, "code review", codeReview)
	}
	if additional, _ := document["additional_rate_limits"].([]any); additional != nil {
		for _, raw := range additional {
			item, _ := raw.(map[string]any)
			if item == nil {
				continue
			}
			name := firstString(item["limit_name"], item["limitName"], item["metered_feature"], item["meteredFeature"])
			limits, _ := item["rate_limit"].(map[string]any)
			if name != "" && limits != nil {
				appendCodexQuotaRows(snapshot, name, limits)
			}
		}
	}
	if len(snapshot.Rows) == rowsBefore {
		return fmt.Errorf("no quota windows")
	}
	return nil
}

func appendCodexQuotaRows(snapshot *model.QuotaSnapshot, prefix string, rateLimit map[string]any) {
	for _, name := range []string{"primary_window", "secondary_window"} {
		window, _ := rateLimit[name].(map[string]any)
		if window == nil {
			continue
		}
		windowName := strings.TrimSuffix(strings.TrimSuffix(name, "_window"), "_")
		if prefix != "" {
			windowName = prefix + " · " + windowName
		}
		row := model.QuotaRow{Window: windowName}
		if used, ok := number(window["used_percent"]); ok {
			row.UsedPercent = &used
			remaining := 100 - used
			if remaining < 0 {
				remaining = 0
			}
			row.RemainingPercent = &remaining
		}
		if reset, ok := integer(window["reset_at"]); ok {
			row.ResetAt = &reset
		}
		if seconds, ok := integer(window["limit_window_seconds"]); ok {
			row.LimitWindowSeconds = &seconds
		}
		if seconds, ok := integer(window["reset_after_seconds"]); ok {
			row.ResetAfterSeconds = &seconds
		}
		if allowed, ok := window["allowed"].(bool); ok {
			row.Allowed = &allowed
		}
		if reached, ok := window["limit_reached"].(bool); ok {
			row.LimitReached = &reached
		}
		snapshot.Rows = append(snapshot.Rows, row)
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func codexQuotaHTTPError(status int, body []byte) string {
	var document struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &document)
	switch strings.ToLower(strings.TrimSpace(document.Error.Code)) {
	case "token_invalidated", "invalid_api_key", "invalid_token":
		return "凭据登录已失效，请重新登录"
	}
	if status == http.StatusUnauthorized {
		return "凭据鉴权失败，请重新登录"
	}
	if status == http.StatusTooManyRequests {
		return "额度服务请求过于频繁，请稍后重试"
	}
	return fmt.Sprintf("Codex 额度接口返回 HTTP %d", status)
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func integer(value any) (int64, bool) {
	n, ok := number(value)
	return int64(n), ok
}
