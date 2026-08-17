package app

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/zesuy/cpa-route-allocator/internal/state"
)

type fakeHost struct{}

func (fakeHost) Call(string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

func TestRegistrationUsesCurrentSchema(t *testing.T) {
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	raw, err := application.Handle("plugin.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got registration
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || !got.Capabilities.ManagementAPI {
		t.Fatalf("unexpected registration: %#v", got)
	}
}

func TestManagementStateRedactsMihomoSecret(t *testing.T) {
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	put := callManagement(t, application, http.MethodPut, managementPrefix+"/settings", map[string]any{
		"mihomo_controller_url": "http://127.0.0.1:9090",
		"mihomo_secret":         "secret",
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d body=%s", put.StatusCode, put.Body)
	}
	get := callManagement(t, application, http.MethodGet, managementPrefix+"/state", nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d body=%s", get.StatusCode, get.Body)
	}
	if string(get.Body) == "" || containsJSONKey(get.Body, "mihomo_secret") {
		t.Fatalf("state leaked mihomo secret: %s", get.Body)
	}
	if !containsJSONKey(get.Body, "mihomo_secret_set") {
		t.Fatalf("state did not report configured secret: %s", get.Body)
	}
}

func TestImportPreviewRejectsGroupChange(t *testing.T) {
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	group := map[string]any{
		"name":            "primary",
		"priority":        100,
		"websockets":      true,
		"listener_pool":   "default",
		"shortage_policy": "reject",
	}
	response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups", group)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("group status = %d body=%s", response.StatusCode, response.Body)
	}
	preview := callManagement(t, application, http.MethodPost, managementPrefix+"/import/preview", map[string]any{
		"group": "primary",
		"credential": map[string]any{
			"access_token": "token",
			"email":        "alice@example.com",
			"account_id":   "account-1",
		},
	})
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", preview.StatusCode, preview.Body)
	}
	var got importPreview
	if err := json.Unmarshal(preview.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Items[0].Email != "alice@example.com" || got.Items[0].Action != "create" {
		t.Fatalf("unexpected preview: %#v", got)
	}
}

func callManagement(t *testing.T, application *App, method, path string, body any) managementResponse {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := json.Marshal(managementRequest{Method: method, Path: path, Body: bodyBytes})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := application.Handle("management.handle", req)
	if err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func containsJSONKey(raw []byte, key string) bool {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	return findJSONKey(object, key)
}

func findJSONKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for current, nested := range typed {
			if current == key || findJSONKey(nested, key) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if findJSONKey(nested, key) {
				return true
			}
		}
	}
	return false
}
