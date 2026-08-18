package app

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/allocator"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/importer"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

type localCredentialItem struct {
	AuthIndex   string `json:"auth_index"`
	AuthFile    string `json:"auth_file"`
	Identity    string `json:"identity"`
	Email       string `json:"email,omitempty"`
	Provider    string `json:"provider"`
	AccountID   string `json:"account_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Managed     bool   `json:"managed"`
	Group       string `json:"group,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

func (a *App) listLocalCredentials() (managementResponse, error) {
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	managed := make(map[string]model.Credential, len(value.Credentials))
	for _, credential := range value.Credentials {
		managed[credential.Identity] = credential
	}
	files, err := a.listHostAuthFiles()
	if err != nil {
		return managementResponse{}, upstreamError("list CPA Auth files: " + err.Error())
	}
	items := make([]localCredentialItem, 0, len(files))
	seen := make(map[string]struct{})
	for _, file := range files {
		if file.RuntimeOnly || strings.TrimSpace(file.AuthIndex) == "" {
			continue
		}
		response, err := a.readHostAuthByIndex(file.AuthIndex)
		if err != nil {
			continue
		}
		parsed, err := importer.Parse(response.JSON)
		if err != nil {
			continue
		}
		for _, credential := range parsed {
			key := credential.Identity + "\x00" + file.Name
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			item := localCredentialItem{
				AuthIndex:   file.AuthIndex,
				AuthFile:    file.Name,
				Identity:    credential.Identity,
				Email:       credential.Email,
				Provider:    credential.Provider,
				AccountID:   credential.AccountID,
				WorkspaceID: credential.WorkspaceID,
				Disabled:    file.Disabled,
			}
			if existing, ok := managed[credential.Identity]; ok {
				item.Managed = true
				item.Group = existing.Group
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left := strings.ToLower(firstNonEmptyString(items[i].Email, items[i].AuthFile))
		right := strings.ToLower(firstNonEmptyString(items[j].Email, items[j].AuthFile))
		return left < right
	})
	return jsonResponse(http.StatusOK, map[string]any{"credentials": items}), nil
}

func (a *App) adoptLocalCredential(body []byte) (managementResponse, error) {
	var input adoptCredentialInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.AuthIndex = strings.TrimSpace(input.AuthIndex)
	input.Group = strings.TrimSpace(input.Group)
	if input.AuthIndex == "" || input.Group == "" {
		return managementResponse{}, clientError("auth_index and group are required")
	}
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	group, ok := findGroup(value, input.Group)
	if !ok {
		return managementResponse{}, clientError("group does not exist")
	}
	hostAuth, err := a.readHostAuthByIndex(input.AuthIndex)
	if err != nil {
		return managementResponse{}, upstreamError("read CPA Auth: " + err.Error())
	}
	parsed, err := importer.Parse(hostAuth.JSON)
	if err != nil {
		return managementResponse{}, clientError("CPA Auth format is not supported by the allocator")
	}
	if len(parsed) != 1 {
		return managementResponse{}, conflictError("CPA Auth file contains multiple credentials; upload it through the import page instead")
	}
	item := parsed[0]
	for _, existing := range value.Credentials {
		if existing.Identity == item.Identity {
			return managementResponse{}, conflictError("credential is already managed")
		}
	}
	working := cloneState(value)
	now := time.Now().UTC()
	assignment, err := allocator.Assign(&working, group, now)
	if err != nil {
		return managementResponse{}, conflictError(err.Error())
	}
	authFile := strings.TrimSpace(hostAuth.Name)
	if authFile == "" {
		return managementResponse{}, conflictError("CPA Auth file name is unavailable")
	}
	working.Credentials = append(working.Credentials, model.Credential{
		Identity:    item.Identity,
		AuthFile:    authFile,
		Email:       item.Email,
		Provider:    item.Provider,
		AccountID:   item.AccountID,
		WorkspaceID: item.WorkspaceID,
		Group:       group.Name,
		RouteSlotID: assignment.RouteSlotID,
		RouteStatus: assignment.RouteStatus,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	credential := &working.Credentials[len(working.Credentials)-1]
	warning := ""
	if credential.RouteSlotID != "" {
		if routeErr := a.prepareNewRoute(&working, credential); routeErr != nil {
			warning = "route pending: " + routeErr.Error()
		}
	}
	var auth map[string]any
	if err := json.Unmarshal(hostAuth.JSON, &auth); err != nil {
		return managementResponse{}, clientError("CPA Auth file must contain one JSON object")
	}
	applyManagedFields(auth, working, *credential, group)
	if err := a.saveHostAuth(authFile, auth); err != nil {
		return managementResponse{}, upstreamError("save CPA Auth " + authFile + ": " + err.Error())
	}
	updated, err := a.store.Update(func(state *model.State) error {
		*state = working
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"credential": updated.Credentials[len(updated.Credentials)-1],
		"warning":    warning,
	}), nil
}

func (a *App) unmanageCredential(body []byte) (managementResponse, error) {
	var input unmanageCredentialInput
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
	files, err := a.listHostAuthFiles()
	if err != nil {
		return managementResponse{}, upstreamError("list CPA Auth files: " + err.Error())
	}
	byName := make(map[string]hostAuthFileEntry, len(files))
	for _, file := range files {
		byName[strings.ToLower(file.Name)] = file
	}
	auth, err := a.readExistingAuth(credential.AuthFile, byName)
	if err != nil {
		return managementResponse{}, upstreamError("read CPA Auth " + credential.AuthFile + ": " + err.Error())
	}
	if auth != nil {
		delete(auth, "allocator_identity")
		delete(auth, "allocator_group")
		delete(auth, "proxy_url")
		if err := a.saveHostAuth(credential.AuthFile, auth); err != nil {
			return managementResponse{}, upstreamError("save CPA Auth " + credential.AuthFile + ": " + err.Error())
		}
	}
	updated, err := a.store.Update(func(state *model.State) error {
		for index := range state.Credentials {
			if state.Credentials[index].Identity == input.Identity {
				state.Credentials = append(state.Credentials[:index], state.Credentials[index+1:]...)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) readHostAuthByIndex(authIndex string) (hostAuthGetResponse, error) {
	raw, err := a.host.Call("host.auth.get", map[string]any{"auth_index": authIndex})
	if err != nil {
		return hostAuthGetResponse{}, err
	}
	var response hostAuthGetResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return hostAuthGetResponse{}, err
	}
	return response, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
