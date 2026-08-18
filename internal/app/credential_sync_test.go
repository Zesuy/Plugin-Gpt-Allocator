package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/state"
)

type credentialSyncHost struct {
	files []hostAuthFileEntry
	err   error
}

func (h credentialSyncHost) Call(method string, _ any) (json.RawMessage, error) {
	if method == "host.auth.list" {
		if h.err != nil {
			return nil, h.err
		}
		return json.Marshal(hostAuthListResponse{Files: h.files})
	}
	return json.RawMessage(`{}`), nil
}

func TestGetStateKeepsLastStatusWhenCPASyncFails(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	if _, err := store.Update(func(value *model.State) error {
		value.Credentials = []model.Credential{{Identity: "credential", AuthFile: "credential.json", Disabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	application := New(store, credentialSyncHost{err: errors.New("CPA unavailable")})
	response := callManagement(t, application, http.MethodGet, managementPrefix+"/state", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("state status = %d body=%s", response.StatusCode, response.Body)
	}
	var result stateResult
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.CredentialStatusSync.Error == "" || len(result.Credentials) != 1 || !result.Credentials[0].Disabled {
		t.Fatalf("unexpected failed sync result: %#v", result)
	}
}

func TestGetStateSynchronizesCredentialStatusFromCPA(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	if _, err := store.Update(func(value *model.State) error {
		value.Credentials = []model.Credential{
			{Identity: "disable-me", AuthFile: "disabled.json"},
			{Identity: "enable-me", AuthFile: "ENABLED.JSON", Disabled: true},
			{Identity: "missing", AuthFile: "missing.json", Disabled: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	application := New(store, credentialSyncHost{files: []hostAuthFileEntry{
		{Name: "disabled.json", AuthIndex: "auth-1", Disabled: true, Unavailable: true, Status: "error", StatusMessage: `{"detail":{"code":"deactivated_workspace"}}`},
		{Name: "enabled.json", AuthIndex: "auth-2", Disabled: false},
	}})
	response := callManagement(t, application, http.MethodGet, managementPrefix+"/state", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("state status = %d body=%s", response.StatusCode, response.Body)
	}
	var result stateResult
	if err := json.Unmarshal(response.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.CredentialStatusSync.Matched != 2 || result.CredentialStatusSync.Updated != 2 || result.CredentialStatusSync.Missing != 1 || result.CredentialStatusSync.Error != "" {
		t.Fatalf("unexpected sync info: %#v", result.CredentialStatusSync)
	}
	byIdentity := make(map[string]model.Credential, len(result.Credentials))
	for _, credential := range result.Credentials {
		byIdentity[credential.Identity] = credential
	}
	if !byIdentity["disable-me"].Disabled || byIdentity["enable-me"].Disabled || !byIdentity["missing"].Disabled {
		t.Fatalf("unexpected synchronized credentials: %#v", result.Credentials)
	}
	runtime, ok := result.CredentialRuntime["disable-me"]
	if !ok || runtime.Status != "error" || !runtime.Unavailable || runtime.StatusMessage != `{"detail":{"code":"deactivated_workspace"}}` {
		t.Fatalf("unexpected runtime status: %#v", result.CredentialRuntime)
	}
	if _, ok := result.CredentialRuntime["missing"]; ok {
		t.Fatalf("missing CPA credential received runtime state: %#v", result.CredentialRuntime)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Credentials[0].Disabled || loaded.Credentials[1].Disabled || !loaded.Credentials[2].Disabled {
		t.Fatalf("synchronized status was not persisted: %#v", loaded.Credentials)
	}
	persisted, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "deactivated_workspace") {
		t.Fatalf("runtime status was persisted: %s", persisted)
	}
}
