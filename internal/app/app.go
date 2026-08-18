package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/allocator"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/importer"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/mihomo"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/state"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/ui"
)

const (
	PluginName    = "cpa-route-allocator"
	SchemaVersion = 3
)

// PluginVersion is overridden by release builds through -ldflags -X.
var PluginVersion = "0.1.0-dev"

const managementPrefix = "/v0/management/plugins/" + PluginName

type Host interface {
	Call(method string, payload any) (json.RawMessage, error)
}

type App struct {
	store            *state.Store
	host             Host
	usage            usageStats
	nodeStats        nodeStatsStore
	routeHistory     routeHistory
	authIndexMu      sync.RWMutex
	authFilesByIndex map[string]string
	authIndexCacheAt time.Time
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      metadata                 `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
	UsagePlugin   bool `json:"usage_plugin"`
}

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description,omitempty"`
}

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type settingsInput struct {
	MihomoControllerURL string  `json:"mihomo_controller_url"`
	MihomoSecret        *string `json:"mihomo_secret,omitempty"`
}

type importInput struct {
	Group      string          `json:"group"`
	Credential json.RawMessage `json:"credential"`
}

type importPreviewItem struct {
	Identity     string `json:"identity"`
	Email        string `json:"email"`
	Provider     string `json:"provider"`
	AccountID    string `json:"account_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	Action       string `json:"action"`
	CurrentGroup string `json:"current_group,omitempty"`
}

type importPreview struct {
	Group string              `json:"group"`
	Count int                 `json:"count"`
	Items []importPreviewItem `json:"items"`
}

type importMode string

const (
	importModeUpload importMode = "upload"
	importModeAPI    importMode = "api"
)

type importResult struct {
	Imported int                `json:"imported"`
	Updated  int                `json:"updated"`
	Items    []importResultItem `json:"items"`
}

type importResultItem struct {
	Identity    string `json:"identity"`
	Email       string `json:"email"`
	AuthFile    string `json:"auth_file"`
	Action      string `json:"action"`
	Group       string `json:"group"`
	RouteStatus string `json:"route_status"`
	Warning     string `json:"warning,omitempty"`
}

type pendingSave struct {
	name string
	auth map[string]any
}

type hostAuthFileEntry struct {
	AuthIndex     string `json:"auth_index"`
	Name          string `json:"name"`
	Email         string `json:"email,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Type          string `json:"type,omitempty"`
	Label         string `json:"label,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
	Unavailable   bool   `json:"unavailable,omitempty"`
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	RuntimeOnly   bool   `json:"runtime_only,omitempty"`
}

type hostAuthListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

type hostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index,omitempty"`
	Name      string          `json:"name,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type selectNodeInput struct {
	RouteSlotID string `json:"route_slot_id"`
	Node        string `json:"node"`
}

type aliasInput struct {
	Identity string `json:"identity"`
	Alias    string `json:"alias"`
}

type moveCredentialInput struct {
	Identity string `json:"identity"`
	Group    string `json:"group"`
}

type reassignCredentialInput struct {
	Identity string `json:"identity"`
}

type adoptCredentialInput struct {
	AuthIndex string `json:"auth_index"`
	Group     string `json:"group"`
}

type unmanageCredentialInput struct {
	Identity string `json:"identity"`
}

type credentialStatusInput struct {
	Identity string `json:"identity"`
	Disabled bool   `json:"disabled"`
}

type routeSlotDeleteInput struct {
	ID string `json:"id"`
}

type syncedRouteSlot struct {
	RouteSlotID   string                     `json:"route_slot_id"`
	Selector      string                     `json:"selector"`
	CurrentNode   string                     `json:"current_node,omitempty"`
	Nodes         []string                   `json:"nodes,omitempty"`
	NodeHealth    map[string]mihomo.Selector `json:"node_health,omitempty"`
	NodeStats     map[string]nodeStatsView   `json:"node_stats,omitempty"`
	ListenerProbe *listenerProbe             `json:"listener_probe,omitempty"`
	Error         string                     `json:"error,omitempty"`
}

func New(store *state.Store, host Host) *App {
	return &App{store: store, host: host, routeHistory: newRouteHistory()}
}

func (a *App) Handle(method string, raw []byte) (json.RawMessage, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return marshalResult(pluginRegistration())
	case "management.register":
		return marshalResult(managementRoutes())
	case "management.handle":
		return a.handleManagement(raw)
	case "usage.handle":
		if err := a.handleUsage(raw); err != nil {
			return nil, fmt.Errorf("decode usage record: %w", err)
		}
		return marshalResult(map[string]any{})
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: SchemaVersion,
		Metadata: metadata{
			Name:             PluginName,
			Version:          PluginVersion,
			Author:           "Zesuy",
			GitHubRepository: "https://github.com/Zesuy/Plugin-Gpt-Allocator",
			ConfigFields:     []configField{},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true, UsagePlugin: true},
	}
}

func managementRoutes() managementRegistration {
	return managementRegistration{
		Resources: []managementResource{{
			Path:        "/upload",
			Menu:        "Route Allocator",
			Description: "Import credentials and manage CPA-to-Mihomo route assignments.",
		}},
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/state", Description: "Read allocator state without secrets."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/settings", Description: "Update Mihomo controller settings."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/groups", Description: "List credential groups."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/groups", Description: "Create a credential group."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/groups", Description: "Create or update a credential group."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/groups/order", Description: "Reorder credential groups."},
			{Method: http.MethodDelete, Path: "/plugins/" + PluginName + "/groups", Description: "Delete an unused credential group."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/route-slots", Description: "List Listener and Selector mappings."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots", Description: "Create a Listener and Selector mapping."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/route-slots", Description: "Create or update a Listener and Selector mapping."},
			{Method: http.MethodDelete, Path: "/plugins/" + PluginName + "/route-slots", Description: "Delete an unused Listener and Selector mapping."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/mihomo/status", Description: "Check the configured Mihomo controller."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/network/public-ip", Description: "Read the plugin host's current public egress IP."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/stats", Description: "Read process-local usage summaries split by transport."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/mihomo/selectors", Description: "List Selector groups available from Mihomo."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots/sync", Description: "Refresh Selector nodes and current selections from Mihomo."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/route-slots/diagnostics", Description: "Read per-Listener and per-node diagnostics."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots/diagnostics", Description: "Refresh per-Listener and per-node diagnostics."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots/deduplicate/preview", Description: "Preview manual duplicate public-IP removal."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots/deduplicate", Description: "Apply manual duplicate public-IP removal with verification and rollback."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/route-slots/select", Description: "Manually switch a route slot Selector node."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/credentials/alias", Description: "Override the display alias for a credential."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/credentials/move", Description: "Move an existing credential to another group."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/credentials/reassign", Description: "Reassign a managed credential using its current group policy."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/credentials/status", Description: "Enable or disable a managed credential through CPA."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/credentials/quota", Description: "Check Codex quota for one managed credential through CPA."},
			{Method: http.MethodGet, Path: "/plugins/" + PluginName + "/credentials/local", Description: "List existing CPA Auth files that can be managed."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/credentials/adopt", Description: "Add an existing CPA Auth file to allocator management."},
			{Method: http.MethodDelete, Path: "/plugins/" + PluginName + "/credentials/managed", Description: "Stop managing a credential without deleting its CPA Auth file."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/import/preview", Description: "Preview credential conversion without writing CPA Auth files."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/upload", Description: "Upload credentials from the management page."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/import", Description: "Import credentials through the external Management API."},
		},
	}
}

func (a *App) handleManagement(raw []byte) (json.RawMessage, error) {
	var req managementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode management request: %w", err)
	}
	if strings.HasPrefix(req.Path, "/v0/resource/plugins/"+PluginName+"/") {
		return marshalResult(managementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"content-type": []string{"text/html; charset=utf-8"}, "cache-control": []string{"no-store"}},
			Body:       ui.IndexHTML,
		})
	}
	path := strings.TrimSuffix(req.Path, "/")
	var response managementResponse
	var err error
	switch {
	case req.Method == http.MethodGet && path == managementPrefix+"/state":
		response, err = a.getState()
	case req.Method == http.MethodPut && path == managementPrefix+"/settings":
		response, err = a.putSettings(req.Body)
	case req.Method == http.MethodGet && path == managementPrefix+"/groups":
		response, err = a.getGroups()
	case req.Method == http.MethodPost && path == managementPrefix+"/groups":
		response, err = a.createGroup(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/groups":
		response, err = a.putGroup(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/groups/order":
		response, err = a.reorderGroups(req.Body)
	case req.Method == http.MethodDelete && path == managementPrefix+"/groups":
		response, err = a.deleteGroup(req.Body)
	case req.Method == http.MethodGet && path == managementPrefix+"/route-slots":
		response, err = a.getRouteSlots()
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots":
		response, err = a.createRouteSlot(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/route-slots":
		response, err = a.putRouteSlot(req.Body)
	case req.Method == http.MethodDelete && path == managementPrefix+"/route-slots":
		response, err = a.deleteRouteSlot(req.Body)
	case req.Method == http.MethodGet && path == managementPrefix+"/mihomo/status":
		response, err = a.mihomoStatus()
	case req.Method == http.MethodGet && path == managementPrefix+"/network/public-ip":
		response, err = a.publicIP()
	case req.Method == http.MethodGet && path == managementPrefix+"/stats":
		response = jsonResponse(http.StatusOK, a.usage.snapshot())
	case req.Method == http.MethodGet && path == managementPrefix+"/mihomo/selectors":
		response, err = a.mihomoSelectors()
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots/sync":
		response, err = a.syncRouteSlots()
	case req.Method == http.MethodGet && path == managementPrefix+"/route-slots/diagnostics":
		response, err = a.syncRouteSlots()
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots/diagnostics":
		response, err = a.syncRouteSlots()
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots/deduplicate/preview":
		response, err = a.deduplicatePreview(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots/deduplicate":
		response, err = a.deduplicate(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/route-slots/select":
		response, err = a.selectRouteNode(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/credentials/alias":
		response, err = a.setCredentialAlias(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/credentials/move":
		response, err = a.moveCredential(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/credentials/reassign":
		response, err = a.reassignCredential(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/credentials/status":
		response, err = a.setCredentialStatus(req.Body, req.Headers)
	case req.Method == http.MethodPost && path == managementPrefix+"/credentials/quota":
		response, err = a.checkCredentialQuota(req.Body, req.Headers)
	case req.Method == http.MethodGet && path == managementPrefix+"/credentials/local":
		response, err = a.listLocalCredentials()
	case req.Method == http.MethodPost && path == managementPrefix+"/credentials/adopt":
		response, err = a.adoptLocalCredential(req.Body)
	case req.Method == http.MethodDelete && path == managementPrefix+"/credentials/managed":
		response, err = a.unmanageCredential(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/import/preview":
		response, err = a.previewImport(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/upload":
		response, err = a.executeImport(req.Body, importModeUpload)
	case req.Method == http.MethodPost && path == managementPrefix+"/import":
		response, err = a.executeImport(req.Body, importModeAPI)
	default:
		response = jsonError(http.StatusNotFound, "route not found")
	}
	if err != nil {
		response = jsonError(statusForError(err), err.Error())
	}
	return marshalResult(response)
}

func (a *App) getState() (managementResponse, error) {
	value, syncInfo, runtime, err := a.stateWithCredentialStatusSync()
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, stateResult{
		PublicState:          value.Public(),
		CredentialStatusSync: syncInfo,
		CredentialRuntime:    runtime,
	}), nil
}

func (a *App) putSettings(body []byte) (managementResponse, error) {
	var input settingsInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	if strings.TrimSpace(input.MihomoControllerURL) != "" {
		if _, err := mihomo.New(input.MihomoControllerURL, "", nil); err != nil {
			return managementResponse{}, clientError(err.Error())
		}
	}
	updated, err := a.store.Update(func(value *model.State) error {
		value.Settings.MihomoControllerURL = strings.TrimSpace(input.MihomoControllerURL)
		if input.MihomoSecret != nil {
			value.Settings.MihomoSecret = *input.MihomoSecret
		}
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) putRouteSlot(body []byte) (managementResponse, error) {
	input, err := decodeRouteSlot(body)
	if err != nil {
		return managementResponse{}, err
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for index := range value.RouteSlots {
			if value.RouteSlots[index].ID == input.ID {
				input.LastAssignedAt = value.RouteSlots[index].LastAssignedAt
				input.CurrentNode = value.RouteSlots[index].CurrentNode
				value.RouteSlots[index] = input
				return nil
			}
		}
		value.RouteSlots = append(value.RouteSlots, input)
		sort.Slice(value.RouteSlots, func(i, j int) bool { return value.RouteSlots[i].ID < value.RouteSlots[j].ID })
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) getRouteSlots() (managementResponse, error) {
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	slots := append([]model.RouteSlot(nil), value.RouteSlots...)
	if slots == nil {
		slots = []model.RouteSlot{}
	}
	return jsonResponse(http.StatusOK, map[string]any{"route_slots": slots}), nil
}

func (a *App) createRouteSlot(body []byte) (managementResponse, error) {
	input, err := decodeRouteSlot(body)
	if err != nil {
		return managementResponse{}, err
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for _, slot := range value.RouteSlots {
			if slot.ID == input.ID {
				return conflictError("route slot already exists")
			}
		}
		value.RouteSlots = append(value.RouteSlots, input)
		sort.Slice(value.RouteSlots, func(i, j int) bool { return value.RouteSlots[i].ID < value.RouteSlots[j].ID })
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusCreated, updated.Public()), nil
}

func (a *App) deleteRouteSlot(body []byte) (managementResponse, error) {
	var input routeSlotDeleteInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		return managementResponse{}, clientError("id is required")
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for _, credential := range value.Credentials {
			if credential.RouteSlotID == input.ID {
				return conflictError("route slot is still assigned to managed credentials")
			}
		}
		for index := range value.RouteSlots {
			if value.RouteSlots[index].ID == input.ID {
				value.RouteSlots = append(value.RouteSlots[:index], value.RouteSlots[index+1:]...)
				return nil
			}
		}
		return clientError("route slot does not exist")
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func decodeRouteSlot(body []byte) (model.RouteSlot, error) {
	var input model.RouteSlot
	if err := decodeBody(body, &input); err != nil {
		return model.RouteSlot{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.ListenerURL = strings.TrimSpace(input.ListenerURL)
	input.Selector = strings.TrimSpace(input.Selector)
	input.Pool = strings.TrimSpace(input.Pool)
	if input.ID == "" || input.ListenerURL == "" || input.Selector == "" || input.Pool == "" {
		return model.RouteSlot{}, clientError("id, listener_url, selector and pool are required")
	}
	if input.Name == "" {
		input.Name = input.ID
	}
	return input, nil
}

func (a *App) mihomoStatus() (managementResponse, error) {
	value, client, err := a.mihomoClient()
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := client.Version(ctx)
	if err != nil {
		return managementResponse{}, upstreamError(err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"controller_url": value.Settings.MihomoControllerURL,
		"version":        version.Version,
		"meta":           version.Meta,
		"premium":        version.Premium,
	}), nil
}

func (a *App) mihomoSelectors() (managementResponse, error) {
	_, client, err := a.mihomoClient()
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selectors, err := client.Selectors(ctx)
	if err != nil {
		return managementResponse{}, upstreamError(err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"selectors": selectors}), nil
}

func (a *App) syncRouteSlots() (managementResponse, error) {
	value, client, err := a.mihomoClient()
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	proxies, proxiesErr := client.Proxies(ctx)
	results := make([]syncedRouteSlot, 0, len(value.RouteSlots))
	currentNodes := make(map[string]string, len(value.RouteSlots))
	for _, slot := range value.RouteSlots {
		result := syncedRouteSlot{RouteSlotID: slot.ID, Selector: slot.Selector}
		var selector mihomo.Selector
		var selectorErr error
		if proxiesErr == nil {
			var ok bool
			selector, ok = proxies[slot.Selector]
			if !ok {
				selectorErr = fmt.Errorf("Selector %q was not found in Mihomo /proxies", slot.Selector)
			}
		} else {
			// Preserve the previous per-slot behavior when a controller/version
			// does not support a complete /proxies snapshot.
			selector, selectorErr = client.Selector(ctx, slot.Selector)
		}
		if selectorErr != nil {
			result.Error = selectorErr.Error()
		} else {
			result.CurrentNode = selector.Now
			result.Nodes = selector.All
			result.NodeHealth = make(map[string]mihomo.Selector)
			result.NodeStats = make(map[string]nodeStatsView)
			for _, node := range selector.All {
				if health, ok := proxies[node]; ok && (health.Alive != nil || len(health.History) > 0) {
					result.NodeHealth[node] = mihomo.Selector{
						Name:    health.Name,
						Type:    health.Type,
						Alive:   health.Alive,
						History: health.History,
					}
				}
				result.NodeStats[node] = a.nodeStats.snapshot(slot.ID, node)
			}
			currentNodes[slot.ID] = selector.Now
		}
		results = append(results, result)
	}
	var probeWait sync.WaitGroup
	for index := range results {
		if strings.TrimSpace(value.RouteSlots[index].ListenerURL) == "" {
			continue
		}
		probeWait.Add(1)
		go func(index int, listenerURL string) {
			defer probeWait.Done()
			probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
			defer probeCancel()
			probe := probeListener(probeCtx, listenerURL)
			results[index].ListenerProbe = &probe
		}(index, value.RouteSlots[index].ListenerURL)
	}
	probeWait.Wait()
	if len(currentNodes) > 0 {
		if _, err := a.store.Update(func(state *model.State) error {
			for index := range state.RouteSlots {
				if node, ok := currentNodes[state.RouteSlots[index].ID]; ok {
					if state.RouteSlots[index].CurrentNode != node {
						a.routeHistory.record(state.RouteSlots[index].ID, node, time.Now().UTC())
						state.RouteSlots[index].NodeChangedAt = time.Now().UTC()
					}
					state.RouteSlots[index].CurrentNode = node
				}
			}
			return nil
		}); err != nil {
			return managementResponse{}, err
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"route_slots": results}), nil
}

func (a *App) selectRouteNode(body []byte) (managementResponse, error) {
	var input selectNodeInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.RouteSlotID = strings.TrimSpace(input.RouteSlotID)
	input.Node = strings.TrimSpace(input.Node)
	if input.RouteSlotID == "" || input.Node == "" {
		return managementResponse{}, clientError("route_slot_id and node are required")
	}
	value, client, err := a.mihomoClient()
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	var selectedSlot *model.RouteSlot
	for index := range value.RouteSlots {
		if value.RouteSlots[index].ID == input.RouteSlotID {
			selectedSlot = &value.RouteSlots[index]
			break
		}
	}
	if selectedSlot == nil {
		return managementResponse{}, clientError("route slot does not exist")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selector, err := client.Selector(ctx, selectedSlot.Selector)
	if err != nil {
		return managementResponse{}, upstreamError(err.Error())
	}
	found := false
	for _, node := range selector.All {
		if node == input.Node {
			found = true
			break
		}
	}
	if !found {
		return managementResponse{}, clientError("node is not available in the configured Selector")
	}
	if err := client.Select(ctx, selectedSlot.Selector, input.Node); err != nil {
		return managementResponse{}, upstreamError(err.Error())
	}
	if _, err := a.store.Update(func(state *model.State) error {
		for index := range state.RouteSlots {
			if state.RouteSlots[index].ID == input.RouteSlotID {
				state.RouteSlots[index].CurrentNode = input.Node
				state.RouteSlots[index].NodeChangedAt = time.Now().UTC()
				return nil
			}
		}
		return clientError("route slot disappeared while updating state")
	}); err != nil {
		return managementResponse{}, err
	}
	a.routeHistory.record(input.RouteSlotID, input.Node, time.Now().UTC())
	return jsonResponse(http.StatusOK, map[string]any{
		"route_slot_id": input.RouteSlotID,
		"selector":      selectedSlot.Selector,
		"current_node":  input.Node,
	}), nil
}

func (a *App) setCredentialAlias(body []byte) (managementResponse, error) {
	var input aliasInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Identity = strings.TrimSpace(input.Identity)
	if input.Identity == "" {
		return managementResponse{}, clientError("identity is required")
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for index := range value.Credentials {
			if value.Credentials[index].Identity == input.Identity {
				value.Credentials[index].Alias = strings.TrimSpace(input.Alias)
				value.Credentials[index].UpdatedAt = time.Now().UTC()
				return nil
			}
		}
		return clientError("credential does not exist")
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) moveCredential(body []byte) (managementResponse, error) {
	var input moveCredentialInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Identity = strings.TrimSpace(input.Identity)
	input.Group = strings.TrimSpace(input.Group)
	if input.Identity == "" || input.Group == "" {
		return managementResponse{}, clientError("identity and group are required")
	}
	updated, err := a.assignManagedCredential(input.Identity, input.Group)
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) reassignCredential(body []byte) (managementResponse, error) {
	var input reassignCredentialInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Identity = strings.TrimSpace(input.Identity)
	if input.Identity == "" {
		return managementResponse{}, clientError("identity is required")
	}
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	group := ""
	for _, credential := range value.Credentials {
		if credential.Identity == input.Identity {
			group = credential.Group
			break
		}
	}
	if group == "" {
		return managementResponse{}, clientError("credential does not exist")
	}
	updated, err := a.assignManagedCredential(input.Identity, group)
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) mihomoClient() (model.State, *mihomo.Client, error) {
	value, err := a.store.Load()
	if err != nil {
		return model.State{}, nil, err
	}
	client, err := mihomo.New(value.Settings.MihomoControllerURL, value.Settings.MihomoSecret, nil)
	if err != nil {
		return model.State{}, nil, err
	}
	return value, client, nil
}

func (a *App) previewImport(body []byte) (managementResponse, error) {
	var input importInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Group = strings.TrimSpace(input.Group)
	if input.Group == "" {
		return managementResponse{}, clientError("group is required")
	}
	current, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	if !hasGroup(current, input.Group) {
		return managementResponse{}, clientError("group does not exist")
	}
	parsed, err := importer.Parse(input.Credential)
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	existing := make(map[string]model.Credential, len(current.Credentials))
	for _, item := range current.Credentials {
		existing[item.Identity] = item
	}
	preview := importPreview{Group: input.Group, Count: len(parsed), Items: make([]importPreviewItem, 0, len(parsed))}
	for _, item := range parsed {
		row := importPreviewItem{
			Identity:    item.Identity,
			Email:       item.Email,
			Provider:    item.Provider,
			AccountID:   item.AccountID,
			WorkspaceID: item.WorkspaceID,
			Action:      "create",
		}
		if known, ok := existing[item.Identity]; ok {
			row.Action = "update"
			row.CurrentGroup = known.Group
			if !strings.EqualFold(known.Group, input.Group) {
				row.Action = "reject_group_change"
			}
		}
		preview.Items = append(preview.Items, row)
	}
	return jsonResponse(http.StatusOK, preview), nil
}

func (a *App) executeImport(body []byte, mode importMode) (managementResponse, error) {
	var input importInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Group = strings.TrimSpace(input.Group)
	if input.Group == "" {
		return managementResponse{}, clientError("group is required")
	}
	current, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	requestedGroup, ok := findGroup(current, input.Group)
	if !ok {
		return managementResponse{}, clientError("group does not exist")
	}
	parsed, err := importer.Parse(input.Credential)
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	hostFiles, err := a.listHostAuthFiles()
	if err != nil {
		return managementResponse{}, upstreamError("list CPA Auth files: " + err.Error())
	}
	reserved := make(map[string]struct{}, len(hostFiles)+len(current.Credentials))
	filesByName := make(map[string]hostAuthFileEntry, len(hostFiles))
	for _, file := range hostFiles {
		name := strings.ToLower(strings.TrimSpace(file.Name))
		if name != "" {
			reserved[name] = struct{}{}
			filesByName[name] = file
		}
	}
	for _, credential := range current.Credentials {
		if credential.AuthFile != "" {
			reserved[strings.ToLower(credential.AuthFile)] = struct{}{}
		}
	}
	externalByIdentity := a.discoverExternalIdentities(hostFiles, filesByName)

	working := cloneState(current)
	identityIndex := make(map[string]int, len(working.Credentials))
	for index, credential := range working.Credentials {
		identityIndex[credential.Identity] = index
	}
	result := importResult{Items: make([]importResultItem, 0, len(parsed))}
	saves := make([]pendingSave, 0, len(parsed))
	now := time.Now().UTC()
	for _, item := range parsed {
		credentialIndex, exists := identityIndex[item.Identity]
		activeGroup := requestedGroup
		resultItem := importResultItem{Identity: item.Identity, Email: item.Email, Action: "created", Group: requestedGroup.Name}
		var credential *model.Credential
		var baseAuth map[string]any
		if exists {
			credential = &working.Credentials[credentialIndex]
			storedGroup, found := findGroup(working, credential.Group)
			if !found {
				return managementResponse{}, conflictError("existing credential references a missing group")
			}
			activeGroup = storedGroup
			resultItem.Action = "updated"
			resultItem.Group = storedGroup.Name
			if !strings.EqualFold(storedGroup.Name, requestedGroup.Name) {
				if mode == importModeUpload {
					return managementResponse{}, conflictError("credential already exists in group " + storedGroup.Name + "; move it from the panel instead")
				}
				resultItem.Warning = "submitted group ignored; existing group and route were kept"
			}
			baseAuth, err = a.readExistingAuth(credential.AuthFile, filesByName)
			if err != nil {
				return managementResponse{}, upstreamError("read existing CPA Auth " + credential.AuthFile + ": " + err.Error())
			}
			credential.Email = item.Email
			credential.Provider = item.Provider
			credential.AccountID = item.AccountID
			credential.WorkspaceID = item.WorkspaceID
			credential.UpdatedAt = now
			result.Updated++
		} else {
			assignment, assignErr := allocator.Assign(&working, requestedGroup, now)
			if assignErr != nil {
				return managementResponse{}, conflictError(assignErr.Error())
			}
			filename := externalByIdentity[item.Identity]
			if filename == "" {
				filename = importer.AvailableAuthFile(item.Email, item.Provider, item.Identity, reserved)
			} else {
				resultItem.Action = "adopted"
				baseAuth, err = a.readExistingAuth(filename, filesByName)
				if err != nil {
					return managementResponse{}, upstreamError("read existing CPA Auth " + filename + ": " + err.Error())
				}
			}
			working.Credentials = append(working.Credentials, model.Credential{
				Identity:    item.Identity,
				AuthFile:    filename,
				Email:       item.Email,
				Provider:    item.Provider,
				AccountID:   item.AccountID,
				WorkspaceID: item.WorkspaceID,
				Group:       requestedGroup.Name,
				RouteSlotID: assignment.RouteSlotID,
				RouteStatus: assignment.RouteStatus,
				CreatedAt:   now,
				UpdatedAt:   now,
			})
			credentialIndex = len(working.Credentials) - 1
			identityIndex[item.Identity] = credentialIndex
			credential = &working.Credentials[credentialIndex]
			if credential.RouteSlotID != "" {
				if routeErr := a.prepareNewRoute(&working, credential); routeErr != nil {
					resultItem.Warning = "route pending: " + routeErr.Error()
				}
			}
			result.Imported++
		}
		resultItem.AuthFile = credential.AuthFile
		resultItem.RouteStatus = credential.RouteStatus
		auth := mergeAuth(baseAuth, item.Auth)
		applyManagedFields(auth, working, *credential, activeGroup)
		saves = append(saves, pendingSave{name: credential.AuthFile, auth: auth})
		result.Items = append(result.Items, resultItem)
	}

	for _, save := range saves {
		if err := a.saveHostAuth(save.name, save.auth); err != nil {
			return managementResponse{}, upstreamError("save CPA Auth " + save.name + ": " + err.Error())
		}
	}
	if _, err := a.store.Update(func(value *model.State) error {
		*value = working
		return nil
	}); err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, result), nil
}

func (a *App) discoverExternalIdentities(files []hostAuthFileEntry, byName map[string]hostAuthFileEntry) map[string]string {
	identities := make(map[string]string)
	for _, file := range files {
		if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.AuthIndex) == "" {
			continue
		}
		auth, err := a.readExistingAuth(file.Name, byName)
		if err != nil || len(auth) == 0 {
			continue
		}
		raw, err := json.Marshal(auth)
		if err != nil {
			continue
		}
		parsed, err := importer.Parse(raw)
		if err != nil {
			continue
		}
		for _, item := range parsed {
			if _, exists := identities[item.Identity]; !exists {
				identities[item.Identity] = file.Name
			}
		}
	}
	return identities
}

func (a *App) prepareNewRoute(value *model.State, credential *model.Credential) error {
	var slot *model.RouteSlot
	for index := range value.RouteSlots {
		if value.RouteSlots[index].ID == credential.RouteSlotID {
			slot = &value.RouteSlots[index]
			break
		}
	}
	if slot == nil {
		credential.RouteStatus = model.RouteStatusPending
		return errors.New("assigned route slot no longer exists")
	}
	client, err := mihomo.New(value.Settings.MihomoControllerURL, value.Settings.MihomoSecret, nil)
	if err != nil {
		credential.RouteStatus = model.RouteStatusPending
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selector, err := client.Selector(ctx, slot.Selector)
	if err != nil {
		credential.RouteStatus = model.RouteStatusPending
		return err
	}
	if selector.Now != "" {
		slot.CurrentNode = selector.Now
	}
	if credential.RouteStatus == model.RouteStatusShared {
		if slot.CurrentNode == "" {
			credential.RouteStatus = model.RouteStatusPending
			return errors.New("Selector has no current node")
		}
		return nil
	}
	// Assigning a credential to a Listener must not implicitly rotate the
	// Listener's Mihomo Selector. Other credentials may already share this
	// Listener, so changing the node here would unexpectedly move all of them.
	// Inherit the Selector's current node and leave the route pending only when
	// Mihomo has not selected one yet.
	if strings.TrimSpace(selector.Now) == "" {
		credential.RouteStatus = model.RouteStatusPending
		return errors.New("Selector has no current node")
	}
	slot.CurrentNode = selector.Now
	return nil
}

func (a *App) listHostAuthFiles() ([]hostAuthFileEntry, error) {
	raw, err := a.host.Call("host.auth.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var response hostAuthListResponse
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
	}
	return response.Files, nil
}

func (a *App) readExistingAuth(filename string, files map[string]hostAuthFileEntry) (map[string]any, error) {
	file, ok := files[strings.ToLower(filename)]
	if !ok || file.AuthIndex == "" {
		return nil, nil
	}
	raw, err := a.host.Call("host.auth.get", map[string]any{"auth_index": file.AuthIndex})
	if err != nil {
		return nil, err
	}
	var response hostAuthGetResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var auth map[string]any
	if len(response.JSON) > 0 {
		if err := json.Unmarshal(response.JSON, &auth); err != nil {
			return nil, err
		}
	}
	return auth, nil
}

func (a *App) saveHostAuth(name string, auth map[string]any) error {
	_, err := a.host.Call("host.auth.save", map[string]any{"name": name, "json": auth})
	return err
}

func mergeAuth(base, incoming map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(incoming)+4)
	for key, value := range base {
		result[key] = value
	}
	for key, value := range incoming {
		result[key] = value
	}
	return result
}

func applyManagedFields(auth map[string]any, value model.State, credential model.Credential, group model.Group) {
	auth["priority"] = group.Priority
	auth["websockets"] = group.Websockets
	auth["allocator_identity"] = credential.Identity
	auth["allocator_group"] = credential.Group
	if credential.WorkspaceID != "" {
		auth["workspace_id"] = credential.WorkspaceID
	}
	if credential.RouteSlotID == "" || credential.RouteStatus == model.RouteStatusDefault || credential.RouteStatus == model.RouteStatusPending {
		delete(auth, "proxy_url")
		return
	}
	for _, slot := range value.RouteSlots {
		if slot.ID == credential.RouteSlotID {
			auth["proxy_url"] = slot.ListenerURL
			return
		}
	}
}

func cloneState(value model.State) model.State {
	value.Groups = append([]model.Group(nil), value.Groups...)
	value.RouteSlots = append([]model.RouteSlot(nil), value.RouteSlots...)
	value.Credentials = append([]model.Credential(nil), value.Credentials...)
	return value
}

func hasGroup(value model.State, name string) bool {
	_, ok := findGroup(value, name)
	return ok
}

func findGroup(value model.State, name string) (model.Group, bool) {
	for _, group := range value.Groups {
		if strings.EqualFold(group.Name, name) {
			return group, true
		}
	}
	return model.Group{}, false
}

func decodeBody(body []byte, dst any) error {
	if len(body) == 0 {
		return clientError("request body is required")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return clientError("request body must be valid JSON")
	}
	return nil
}

func jsonResponse(status int, value any) managementResponse {
	body, _ := json.Marshal(value)
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}, "cache-control": []string{"no-store"}},
		Body:       body,
	}
}

func jsonError(status int, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": message})
}

func marshalResult(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	return json.RawMessage(raw), err
}

type requestError struct {
	status  int
	message string
}

func (e requestError) Error() string { return e.message }

func clientError(message string) error {
	return requestError{status: http.StatusBadRequest, message: message}
}

func conflictError(message string) error {
	return requestError{status: http.StatusConflict, message: message}
}

func upstreamError(message string) error {
	return requestError{status: http.StatusBadGateway, message: message}
}

func statusForError(err error) int {
	var target requestError
	if errors.As(err, &target) {
		return target.status
	}
	return http.StatusInternalServerError
}
