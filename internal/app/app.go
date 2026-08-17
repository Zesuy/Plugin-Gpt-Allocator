package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/zesuy/cpa-route-allocator/internal/importer"
	"github.com/zesuy/cpa-route-allocator/internal/model"
	"github.com/zesuy/cpa-route-allocator/internal/state"
	"github.com/zesuy/cpa-route-allocator/internal/ui"
)

const (
	PluginName    = "cpa-route-allocator"
	PluginVersion = "0.1.0-dev"
	SchemaVersion = 3
)

const managementPrefix = "/v0/management/plugins/" + PluginName

type Host interface {
	Call(method string, payload any) (json.RawMessage, error)
}

type App struct {
	store *state.Store
	host  Host
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

func New(store *state.Store, host Host) *App {
	return &App{store: store, host: host}
}

func (a *App) Handle(method string, raw []byte) (json.RawMessage, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return marshalResult(pluginRegistration())
	case "management.register":
		return marshalResult(managementRoutes())
	case "management.handle":
		return a.handleManagement(raw)
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
			Author:           "zesuy",
			GitHubRepository: "https://github.com/zesuy/cpa-route-allocator",
			ConfigFields: []configField{{
				Name:        "state_path",
				Type:        "string",
				Description: "Optional allocator state file path. CPA_ROUTE_ALLOCATOR_STATE_PATH takes precedence in v0.1.",
			}},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
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
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/groups", Description: "Create or update a credential group."},
			{Method: http.MethodPut, Path: "/plugins/" + PluginName + "/route-slots", Description: "Create or update a Listener and Selector mapping."},
			{Method: http.MethodPost, Path: "/plugins/" + PluginName + "/import/preview", Description: "Preview credential conversion without writing CPA Auth files."},
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
	case req.Method == http.MethodPut && path == managementPrefix+"/groups":
		response, err = a.putGroup(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/route-slots":
		response, err = a.putRouteSlot(req.Body)
	case req.Method == http.MethodPost && path == managementPrefix+"/import/preview":
		response, err = a.previewImport(req.Body)
	default:
		response = jsonError(http.StatusNotFound, "route not found")
	}
	if err != nil {
		response = jsonError(statusForError(err), err.Error())
	}
	return marshalResult(response)
}

func (a *App) getState() (managementResponse, error) {
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, value.Public()), nil
}

func (a *App) putSettings(body []byte) (managementResponse, error) {
	var input settingsInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
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

func (a *App) putGroup(body []byte) (managementResponse, error) {
	var input model.Group
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.ListenerPool = strings.TrimSpace(input.ListenerPool)
	if input.Name == "" {
		return managementResponse{}, clientError("group name is required")
	}
	if input.ShortagePolicy == "" {
		input.ShortagePolicy = model.ShortageReject
	}
	if !input.ShortagePolicy.Valid() {
		return managementResponse{}, clientError("invalid shortage_policy")
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for index := range value.Groups {
			if strings.EqualFold(value.Groups[index].Name, input.Name) {
				value.Groups[index] = input
				return nil
			}
		}
		value.Groups = append(value.Groups, input)
		sort.Slice(value.Groups, func(i, j int) bool {
			return strings.ToLower(value.Groups[i].Name) < strings.ToLower(value.Groups[j].Name)
		})
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) putRouteSlot(body []byte) (managementResponse, error) {
	var input model.RouteSlot
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.ListenerURL = strings.TrimSpace(input.ListenerURL)
	input.Selector = strings.TrimSpace(input.Selector)
	input.Pool = strings.TrimSpace(input.Pool)
	if input.ID == "" || input.ListenerURL == "" || input.Selector == "" || input.Pool == "" {
		return managementResponse{}, clientError("id, listener_url, selector and pool are required")
	}
	if input.Name == "" {
		input.Name = input.ID
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

func hasGroup(value model.State, name string) bool {
	for _, group := range value.Groups {
		if strings.EqualFold(group.Name, name) {
			return true
		}
	}
	return false
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

type requestError struct{ message string }

func (e requestError) Error() string { return e.message }

func clientError(message string) error { return requestError{message: message} }

func statusForError(err error) int {
	var target requestError
	if errors.As(err, &target) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
