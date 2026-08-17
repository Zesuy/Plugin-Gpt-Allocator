package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/allocator"
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
	AuthIndex string `json:"auth_index"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
}

type hostAuthListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

type hostAuthGetResponse struct {
	JSON json.RawMessage `json:"json"`
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
			ConfigFields:     []configField{},
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
	case req.Method == http.MethodPut && path == managementPrefix+"/groups":
		response, err = a.putGroup(req.Body)
	case req.Method == http.MethodPut && path == managementPrefix+"/route-slots":
		response, err = a.putRouteSlot(req.Body)
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
			filename := importer.AvailableAuthFile(item.Email, item.Provider, item.Identity, reserved)
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
	if credential.RouteSlotID == "" || credential.RouteStatus == model.RouteStatusDefault {
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
