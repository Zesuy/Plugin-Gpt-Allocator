package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zesuy/cpa-route-allocator/internal/model"
	"github.com/zesuy/cpa-route-allocator/internal/state"
)

type fakeHost struct{}

func (fakeHost) Call(string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

type recordingHost struct {
	saved map[string]map[string]any
}

type quotaHost struct{}

func (quotaHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case "host.auth.list":
		return json.RawMessage(`{"files":[{"name":"alice@example.com.json","auth_index":"auth-1"}]}`), nil
	case "host.http.do":
		body, _ := json.Marshal(map[string]any{"status_code": http.StatusOK, "body": `{"rate_limit":{"primary_window":{"used_percent":5}}}`})
		return json.Marshal(map[string]any{"StatusCode": http.StatusOK, "Body": body})
	default:
		return json.RawMessage(`{}`), nil
	}
}

func newRecordingHost() *recordingHost { return &recordingHost{saved: make(map[string]map[string]any)} }

func (h *recordingHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case "host.auth.list":
		files := make([]map[string]any, 0, len(h.saved))
		for name := range h.saved {
			files = append(files, map[string]any{"name": name, "auth_index": name})
		}
		return json.Marshal(map[string]any{"files": files})
	case "host.auth.get":
		request := payload.(map[string]any)
		name := request["auth_index"].(string)
		return json.Marshal(map[string]any{"auth_index": name, "name": name, "json": h.saved[name]})
	case "host.auth.save":
		request := payload.(map[string]any)
		name := request["name"].(string)
		auth := request["json"].(map[string]any)
		h.saved[name] = auth
		return json.Marshal(map[string]any{"name": name, "path": "/auth/" + name})
	case "host.http.do":
		return json.Marshal(map[string]any{"StatusCode": http.StatusNoContent})
	default:
		return json.RawMessage(`{}`), nil
	}
}

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
	if got.SchemaVersion != SchemaVersion || !got.Capabilities.ManagementAPI || !got.Capabilities.UsagePlugin {
		t.Fatalf("unexpected registration: %#v", got)
	}
}

func TestPublicIPParsesTraceResponse(t *testing.T) {
	previous := publicIPTraceEndpoint
	publicIPTraceEndpoint = "https://trace.test/cdn-cgi/trace"
	defer func() { publicIPTraceEndpoint = previous }()

	// Route the test endpoint through a local server by temporarily using its
	// URL; the handler verifies that only parsed fields are returned.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, "fl=1\nip=203.0.113.7\nloc=US\ncolo=SJC\nhttp=http/2\n")
	}))
	defer server.Close()
	publicIPTraceEndpoint = server.URL + "/cdn-cgi/trace"

	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	response := callManagement(t, application, http.MethodGet, managementPrefix+"/network/public-ip", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public IP status = %d body=%s", response.StatusCode, response.Body)
	}
	var got publicIPResult
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.IP != "203.0.113.7" || got.Location != "US" || got.Colo != "SJC" || got.CheckedAt == "" {
		t.Fatalf("unexpected public IP result: %#v", got)
	}
}

func TestUsageHandleSeparatesSSEAndWebsocketStats(t *testing.T) {
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	for _, record := range []map[string]any{
		{"ExecutorType": "CodexExecutor", "Latency": int64(1500 * 1_000_000), "TTFT": int64(300 * 1_000_000), "Detail": map[string]any{"OutputTokens": 120}, "ResponseHeaders": map[string][]string{"Content-Type": {"text/event-stream"}}},
		{"ExecutorType": "WebSocketExecutor", "Latency": int64(1000 * 1_000_000), "TTFT": int64(200 * 1_000_000), "Detail": map[string]any{"OutputTokens": 80}, "ResponseHeaders": map[string][]string{"Upgrade": {"websocket"}}},
	} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := application.Handle("usage.handle", raw); err != nil {
			t.Fatal(err)
		}
	}
	response := callManagement(t, application, http.MethodGet, managementPrefix+"/stats", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d body=%s", response.StatusCode, response.Body)
	}
	var got usageStatsResponse
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.SSE.Requests != 1 || got.SSE.OutputTokens != 120 || got.Websocket.Requests != 1 || got.Websocket.OutputTokens != 80 {
		t.Fatalf("unexpected transport stats: %#v", got)
	}
}

func TestParseCodexQuotaKeepsIndependentWindows(t *testing.T) {
	snapshot := &model.QuotaSnapshot{}
	err := parseCodexQuota([]byte(`{"rate_limit":{"primary_window":{"used_percent":12,"reset_at":1700000000},"secondary_window":{"used_percent":84,"reset_after_seconds":3600,"limit_reached":true}}}`), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 2 || snapshot.Rows[0].Window != "primary" || snapshot.Rows[1].Window != "secondary" {
		t.Fatalf("unexpected quota rows: %#v", snapshot.Rows)
	}
	if snapshot.Rows[0].RemainingPercent == nil || *snapshot.Rows[0].RemainingPercent != 88 {
		t.Fatalf("unexpected primary remaining percentage: %#v", snapshot.Rows[0])
	}
	if snapshot.Rows[1].ResetAfterSeconds == nil || *snapshot.Rows[1].ResetAfterSeconds != 3600 || snapshot.Rows[1].LimitReached == nil || !*snapshot.Rows[1].LimitReached {
		t.Fatalf("unexpected secondary window: %#v", snapshot.Rows[1])
	}
}

func TestCredentialQuotaUsesCPAAPICall(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	if _, err := store.Update(func(value *model.State) error {
		value.Credentials = []model.Credential{{Identity: "identity-1", AuthFile: "alice@example.com.json", Provider: "codex"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	application := New(store, quotaHost{})
	response := callManagementWithHeaders(t, application, http.MethodPost, managementPrefix+"/credentials/quota", map[string]any{"identity": "identity-1"}, http.Header{"Authorization": []string{"Bearer key"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("quota status = %d body=%s", response.StatusCode, response.Body)
	}
	var got quotaCheckResult
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Quota == nil || len(got.Quota.Rows) != 1 || got.Quota.Rows[0].UsedPercent == nil || *got.Quota.Rows[0].UsedPercent != 5 {
		t.Fatalf("unexpected quota result: %#v", got)
	}
}

func TestSyncRouteSlotsIncludesMihomoNodeHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/proxies" {
			http.NotFound(writer, request)
			return
		}
		_, _ = fmt.Fprint(writer, `{"proxies":{"selector":{"name":"selector","type":"Selector","now":"node-a","all":["node-a"]},"node-a":{"name":"node-a","type":"Http","alive":true,"history":[{"time":"2026-08-18T00:00:00Z","delay":123}]}}}`)
	}))
	defer server.Close()
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	if response := callManagement(t, application, http.MethodPut, managementPrefix+"/settings", map[string]any{"mihomo_controller_url": server.URL}); response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", response.StatusCode, response.Body)
	}
	if response := callManagement(t, application, http.MethodPut, managementPrefix+"/route-slots", map[string]any{"id": "slot-1", "listener_url": "socks5://127.0.0.1:21001", "selector": "selector", "pool": "default"}); response.StatusCode != http.StatusOK {
		t.Fatalf("route slot status = %d body=%s", response.StatusCode, response.Body)
	}
	response := callManagement(t, application, http.MethodPost, managementPrefix+"/route-slots/sync", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", response.StatusCode, response.Body)
	}
	var got struct {
		RouteSlots []syncedRouteSlot `json:"route_slots"`
	}
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	health := got.RouteSlots[0].NodeHealth["node-a"]
	if health.Alive == nil || !*health.Alive || len(health.History) != 1 || health.History[0].Delay != 123 {
		t.Fatalf("unexpected node health: %#v", health)
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

func TestImportPreviewCreatesCredentialPlan(t *testing.T) {
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

func TestUploadCreatesAndAPIUpdateKeepsGroupAndRoute(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	application := New(store, host)
	mihomoServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/proxies/cpa-01" && request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"name":"cpa-01","type":"Selector","now":"JP-01","all":["JP-01","SG-01"]}`))
			return
		}
		if request.URL.Path == "/proxies/cpa-01" && request.Method == http.MethodPut {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(writer, request)
	}))
	defer mihomoServer.Close()
	response := callManagement(t, application, http.MethodPut, managementPrefix+"/settings", map[string]any{
		"mihomo_controller_url": mihomoServer.URL,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", response.StatusCode, response.Body)
	}
	for _, name := range []string{"primary", "backup"} {
		response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups", map[string]any{
			"name":            name,
			"priority":        100,
			"websockets":      true,
			"listener_pool":   "default",
			"shortage_policy": "reject",
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("group status = %d body=%s", response.StatusCode, response.Body)
		}
	}
	response = callManagement(t, application, http.MethodPut, managementPrefix+"/route-slots", map[string]any{
		"id":           "slot-1",
		"listener_url": "socks5://127.0.0.1:21001",
		"selector":     "cpa-01",
		"pool":         "default",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("slot status = %d body=%s", response.StatusCode, response.Body)
	}
	credential := map[string]any{
		"access_token": "token-one",
		"email":        "alice@example.com",
		"account_id":   "account-1",
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/upload", map[string]any{
		"group": "primary", "credential": credential,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", response.StatusCode, response.Body)
	}
	saved := host.saved["alice@example.com.json"]
	if saved["proxy_url"] != "socks5://127.0.0.1:21001" || saved["websockets"] != true {
		t.Fatalf("unexpected saved auth: %#v", saved)
	}

	credential["access_token"] = "token-two"
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/upload", map[string]any{
		"group": "backup", "credential": credential,
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("manual group change status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/import", map[string]any{
		"group": "backup", "credential": credential,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("api update status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Credentials) != 1 || loaded.Credentials[0].Group != "primary" || loaded.Credentials[0].RouteSlotID != "slot-1" {
		t.Fatalf("existing assignment changed: %#v", loaded.Credentials)
	}
	if host.saved["alice@example.com.json"]["access_token"] != "token-two" {
		t.Fatalf("credential token was not updated: %#v", host.saved["alice@example.com.json"])
	}
}

func TestUploadAdoptsExistingCPAAuthFile(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	host.saved["alice@example.com.json"] = map[string]any{
		"type": "codex", "access_token": "old-token", "email": "alice@example.com", "account_id": "account-1",
	}
	application := New(store, host)
	response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups", map[string]any{
		"name": "primary", "listener_pool": "default", "shortage_policy": "reject",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("group status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPut, managementPrefix+"/route-slots", map[string]any{
		"id": "slot-1", "listener_url": "socks5://127.0.0.1:21001", "selector": "cpa-01", "pool": "default",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("slot status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/upload", map[string]any{
		"group": "primary", "credential": map[string]any{
			"access_token": "new-token", "email": "alice@example.com", "account_id": "account-1",
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", response.StatusCode, response.Body)
	}
	if host.saved["alice@example.com_1.json"] != nil || host.saved["alice@example.com.json"]["access_token"] != "new-token" {
		t.Fatalf("existing auth was not adopted: %#v", host.saved)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Credentials) != 1 || loaded.Credentials[0].AuthFile != "alice@example.com.json" {
		t.Fatalf("unexpected adopted state: %#v", loaded.Credentials)
	}
}

func TestAliasAndMoveCredential(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	application := New(store, host)
	for _, name := range []string{"primary", "backup"} {
		response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups", map[string]any{
			"name": name, "priority": 20, "listener_pool": "default", "shortage_policy": "default_route",
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("group status = %d body=%s", response.StatusCode, response.Body)
		}
	}
	response := callManagement(t, application, http.MethodPost, managementPrefix+"/upload", map[string]any{
		"group": "primary", "credential": map[string]any{
			"access_token": "token", "email": "alice@example.com", "account_id": "account-1",
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", response.StatusCode, response.Body)
	}
	identity := ""
	loaded, _ := store.Load()
	identity = loaded.Credentials[0].Identity
	response = callManagement(t, application, http.MethodPut, managementPrefix+"/credentials/alias", map[string]any{
		"identity": identity, "alias": "主号",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("alias status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/credentials/move", map[string]any{
		"identity": identity, "group": "backup",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("move status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Credentials[0].Alias != "主号" || loaded.Credentials[0].Group != "backup" {
		t.Fatalf("alias or group was not updated: %#v", loaded.Credentials[0])
	}
}

func TestCredentialMoveReassignsByGroupPolicy(t *testing.T) {
	mihomoServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/proxies/target-selector" && request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"name":"target-selector","type":"Selector","now":"node-a","all":["node-a"]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer mihomoServer.Close()

	for _, test := range []struct {
		name          string
		policy        model.ShortagePolicy
		wantStatus    int
		wantGroup     string
		wantSlot      string
		wantRoute     string
		wantProxy     any
		wantProxyKept bool
	}{
		{name: "share least uses occupied listener", policy: model.ShortageShareLeast, wantStatus: http.StatusOK, wantGroup: "target", wantSlot: "target-slot", wantRoute: model.RouteStatusShared, wantProxy: "socks5://127.0.0.1:22002"},
		{name: "default route removes stale proxy", policy: model.ShortageDefault, wantStatus: http.StatusOK, wantGroup: "target", wantRoute: model.RouteStatusDefault},
		{name: "reject preserves prior assignment", policy: model.ShortageReject, wantStatus: http.StatusConflict, wantGroup: "source", wantSlot: "source-slot", wantRoute: model.RouteStatusAssigned, wantProxy: "socks5://127.0.0.1:22001", wantProxyKept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := state.New(filepath.Join(t.TempDir(), "state.json"))
			host := newRecordingHost()
			host.saved["alice@example.com.json"] = map[string]any{"type": "codex", "access_token": "token", "proxy_url": "socks5://127.0.0.1:22001"}
			application := New(store, host)
			if _, err := store.Update(func(value *model.State) error {
				value.Settings.MihomoControllerURL = mihomoServer.URL
				value.Groups = []model.Group{
					{Name: "source", ListenerPool: "source", ShortagePolicy: model.ShortageReject},
					{Name: "target", ListenerPool: "target", ShortagePolicy: test.policy},
				}
				value.RouteSlots = []model.RouteSlot{
					{ID: "source-slot", ListenerURL: "socks5://127.0.0.1:22001", Selector: "source-selector", Pool: "source"},
					{ID: "target-slot", ListenerURL: "socks5://127.0.0.1:22002", Selector: "target-selector", Pool: "target"},
				}
				value.Credentials = []model.Credential{
					{Identity: "alice", AuthFile: "alice@example.com.json", Email: "alice@example.com", Group: "source", RouteSlotID: "source-slot", RouteStatus: model.RouteStatusAssigned},
					{Identity: "bob", AuthFile: "bob@example.com.json", Email: "bob@example.com", Group: "target", RouteSlotID: "target-slot", RouteStatus: model.RouteStatusAssigned},
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			response := callManagement(t, application, http.MethodPost, managementPrefix+"/credentials/move", map[string]any{"identity": "alice", "group": "target"})
			if response.StatusCode != test.wantStatus {
				t.Fatalf("move status = %d body=%s", response.StatusCode, response.Body)
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.Credentials[0]
			if got.Group != test.wantGroup || got.RouteSlotID != test.wantSlot || got.RouteStatus != test.wantRoute {
				t.Fatalf("unexpected assignment: %#v", got)
			}
			proxy, hasProxy := host.saved["alice@example.com.json"]["proxy_url"]
			if test.wantProxyKept || test.wantProxy != nil {
				if !hasProxy || proxy != test.wantProxy {
					t.Fatalf("proxy_url = %#v present=%v, auth=%#v", proxy, hasProxy, host.saved["alice@example.com.json"])
				}
			} else if hasProxy {
				t.Fatalf("default route retained stale proxy_url: %#v", host.saved["alice@example.com.json"])
			}
		})
	}
}

func TestCredentialReassignIgnoresItsOwnPreviousSlotUsage(t *testing.T) {
	mihomoServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/proxies/only-selector" && request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"name":"only-selector","type":"Selector","now":"node-a","all":["node-a"]}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer mihomoServer.Close()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	host.saved["alice@example.com.json"] = map[string]any{"type": "codex", "access_token": "token", "proxy_url": "socks5://127.0.0.1:23001"}
	application := New(store, host)
	if _, err := store.Update(func(value *model.State) error {
		value.Settings.MihomoControllerURL = mihomoServer.URL
		value.Groups = []model.Group{{Name: "primary", ListenerPool: "default", ShortagePolicy: model.ShortageReject}}
		value.RouteSlots = []model.RouteSlot{{ID: "only-slot", ListenerURL: "socks5://127.0.0.1:23001", Selector: "only-selector", Pool: "default"}}
		value.Credentials = []model.Credential{{Identity: "alice", AuthFile: "alice@example.com.json", Email: "alice@example.com", Group: "primary", RouteSlotID: "only-slot", RouteStatus: model.RouteStatusAssigned}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := callManagement(t, application, http.MethodPost, managementPrefix+"/credentials/reassign", map[string]any{"identity": "alice"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reassign status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Credentials[0]; got.RouteSlotID != "only-slot" || got.RouteStatus != model.RouteStatusAssigned {
		t.Fatalf("reassign counted its own old slot as occupied: %#v", got)
	}
}

func TestCredentialStatusDelegatesToCPA(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	application := New(store, host)
	response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups", map[string]any{
		"name": "primary", "listener_pool": "default", "shortage_policy": "default_route",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("group status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/upload", map[string]any{
		"group": "primary", "credential": map[string]any{"access_token": "token", "email": "alice@example.com", "account_id": "account-1"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Credentials) != 1 {
		t.Fatalf("credential was not created: err=%v state=%#v", err, loaded)
	}
	identity := loaded.Credentials[0].Identity
	response = callManagementWithHeaders(t, application, http.MethodPut, managementPrefix+"/credentials/status", map[string]any{"identity": identity, "disabled": true}, http.Header{"Authorization": []string{"Bearer management-key"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err = store.Load()
	if err != nil || !loaded.Credentials[0].Disabled {
		t.Fatalf("credential was not disabled: err=%v state=%#v", err, loaded)
	}
}

func TestManagementCRUDAndAdoptLocalCredential(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	host := newRecordingHost()
	host.saved["local.json"] = map[string]any{
		"type": "codex", "access_token": "local-token", "email": "local@example.com", "account_id": "account-local",
	}
	application := New(store, host)

	group := map[string]any{"name": "local", "priority": 50, "websockets": true, "listener_pool": "default", "shortage_policy": "default_route"}
	response := callManagement(t, application, http.MethodPost, managementPrefix+"/groups", group)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create group status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodGet, managementPrefix+"/groups", nil)
	if response.StatusCode != http.StatusOK || !containsJSONKey(response.Body, "groups") {
		t.Fatalf("list groups status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/route-slots", map[string]any{
		"id": "local-slot", "listener_url": "socks5://127.0.0.1:21002", "selector": "cpa-local", "pool": "default",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create route slot status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodPut, managementPrefix+"/route-slots", map[string]any{
		"id": "local-slot", "listener_url": "socks5://127.0.0.1:21003", "selector": "cpa-local-2", "pool": "default",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update route slot status = %d body=%s", response.StatusCode, response.Body)
	}

	response = callManagement(t, application, http.MethodGet, managementPrefix+"/credentials/local", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(response.Body), "local@example.com") {
		t.Fatalf("list local credentials status = %d body=%s", response.StatusCode, response.Body)
	}
	var local struct {
		Credentials []localCredentialItem `json:"credentials"`
	}
	if err := json.Unmarshal(response.Body, &local); err != nil || len(local.Credentials) != 1 {
		t.Fatalf("unexpected local credentials: err=%v body=%s", err, response.Body)
	}
	response = callManagement(t, application, http.MethodPost, managementPrefix+"/credentials/adopt", map[string]any{
		"auth_index": local.Credentials[0].AuthIndex, "group": "local",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("adopt credential status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodDelete, managementPrefix+"/groups", map[string]any{"name": "local"})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("delete non-empty group status = %d body=%s", response.StatusCode, response.Body)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Credentials) != 1 {
		t.Fatalf("adopt did not create managed credential: err=%v state=%#v", err, loaded)
	}
	response = callManagement(t, application, http.MethodDelete, managementPrefix+"/credentials/managed", map[string]any{"identity": loaded.Credentials[0].Identity})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unmanage credential status = %d body=%s", response.StatusCode, response.Body)
	}
	if host.saved["local.json"]["access_token"] != "local-token" {
		t.Fatalf("unmanage removed the CPA Auth credential: %#v", host.saved["local.json"])
	}
	response = callManagement(t, application, http.MethodDelete, managementPrefix+"/groups", map[string]any{"name": "local"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete empty group status = %d body=%s", response.StatusCode, response.Body)
	}
	response = callManagement(t, application, http.MethodDelete, managementPrefix+"/route-slots", map[string]any{"id": "local-slot"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete route slot status = %d body=%s", response.StatusCode, response.Body)
	}
}

func TestGroupOrderCanBeReordered(t *testing.T) {
	application := New(state.New(filepath.Join(t.TempDir(), "state.json")), fakeHost{})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		response := callManagement(t, application, http.MethodPost, managementPrefix+"/groups", map[string]any{
			"name": name, "priority": 10, "listener_pool": "default", "shortage_policy": "reject",
		})
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create group %q status = %d body=%s", name, response.StatusCode, response.Body)
		}
	}
	response := callManagement(t, application, http.MethodPut, managementPrefix+"/groups/order", map[string]any{
		"names": []string{"gamma", "alpha", "beta"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reorder groups status = %d body=%s", response.StatusCode, response.Body)
	}
	var got struct {
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 3 || got.Groups[0].Name != "gamma" || got.Groups[1].Name != "alpha" || got.Groups[2].Name != "beta" {
		t.Fatalf("unexpected group order: %#v", got.Groups)
	}
	response = callManagement(t, application, http.MethodPut, managementPrefix+"/groups/order", map[string]any{
		"names": []string{"gamma", "gamma", "beta"},
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate group order status = %d body=%s", response.StatusCode, response.Body)
	}
}

func callManagement(t *testing.T, application *App, method, path string, body any) managementResponse {
	return callManagementWithHeaders(t, application, method, path, body, nil)
}

func callManagementWithHeaders(t *testing.T, application *App, method, path string, body any, headers http.Header) managementResponse {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := json.Marshal(managementRequest{Method: method, Path: path, Headers: headers, Body: bodyBytes})
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
