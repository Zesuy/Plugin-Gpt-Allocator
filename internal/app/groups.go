package app

import (
	"net/http"
	"sort"
	"strings"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

type groupMutationInput struct {
	OriginalName   string               `json:"original_name,omitempty"`
	Name           string               `json:"name"`
	Priority       int                  `json:"priority"`
	Websockets     bool                 `json:"websockets"`
	ListenerPool   string               `json:"listener_pool"`
	ShortagePolicy model.ShortagePolicy `json:"shortage_policy"`
}

type groupDeleteInput struct {
	Name string `json:"name"`
}

func (a *App) getGroups() (managementResponse, error) {
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	groups := append([]model.Group(nil), value.Groups...)
	if groups == nil {
		groups = []model.Group{}
	}
	return jsonResponse(http.StatusOK, map[string]any{"groups": groups}), nil
}

func (a *App) createGroup(body []byte) (managementResponse, error) {
	input, group, err := decodeGroup(body)
	if err != nil {
		return managementResponse{}, err
	}
	_ = input
	updated, err := a.store.Update(func(value *model.State) error {
		if _, exists := findGroup(*value, group.Name); exists {
			return conflictError("group already exists")
		}
		value.Groups = append(value.Groups, group)
		sortGroups(value.Groups)
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusCreated, updated.Public()), nil
}

func (a *App) putGroup(body []byte) (managementResponse, error) {
	input, group, err := decodeGroup(body)
	if err != nil {
		return managementResponse{}, err
	}
	value, err := a.store.Load()
	if err != nil {
		return managementResponse{}, err
	}
	originalName := strings.TrimSpace(input.OriginalName)
	if originalName == "" {
		originalName = group.Name
	}
	groupIndex := -1
	for index := range value.Groups {
		if strings.EqualFold(value.Groups[index].Name, originalName) {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		// Keep the v0.1 PUT-as-upsert contract for older pages and API clients.
		response, err := a.createGroup(body)
		if err == nil {
			response.StatusCode = http.StatusOK
		}
		return response, err
	}
	for index := range value.Groups {
		if index != groupIndex && strings.EqualFold(value.Groups[index].Name, group.Name) {
			return managementResponse{}, conflictError("another group already uses this name")
		}
	}
	working := cloneState(value)
	working.Groups[groupIndex] = group
	for index := range working.Credentials {
		if strings.EqualFold(working.Credentials[index].Group, originalName) {
			working.Credentials[index].Group = group.Name
		}
	}
	sortGroups(working.Groups)
	if err := a.persistGroupManagedFields(working, group.Name); err != nil {
		return managementResponse{}, err
	}
	updated, err := a.store.Update(func(state *model.State) error {
		*state = working
		return nil
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func (a *App) deleteGroup(body []byte) (managementResponse, error) {
	var input groupDeleteInput
	if err := decodeBody(body, &input); err != nil {
		return managementResponse{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return managementResponse{}, clientError("group name is required")
	}
	updated, err := a.store.Update(func(value *model.State) error {
		for _, credential := range value.Credentials {
			if strings.EqualFold(credential.Group, input.Name) {
				return conflictError("group still contains managed credentials")
			}
		}
		for index := range value.Groups {
			if strings.EqualFold(value.Groups[index].Name, input.Name) {
				value.Groups = append(value.Groups[:index], value.Groups[index+1:]...)
				return nil
			}
		}
		return clientError("group does not exist")
	})
	if err != nil {
		return managementResponse{}, err
	}
	return jsonResponse(http.StatusOK, updated.Public()), nil
}

func decodeGroup(body []byte) (groupMutationInput, model.Group, error) {
	var input groupMutationInput
	if err := decodeBody(body, &input); err != nil {
		return groupMutationInput{}, model.Group{}, err
	}
	group := model.Group{
		Name:           strings.TrimSpace(input.Name),
		Priority:       input.Priority,
		Websockets:     input.Websockets,
		ListenerPool:   strings.TrimSpace(input.ListenerPool),
		ShortagePolicy: input.ShortagePolicy,
	}
	if group.Name == "" {
		return groupMutationInput{}, model.Group{}, clientError("group name is required")
	}
	if group.ListenerPool == "" {
		return groupMutationInput{}, model.Group{}, clientError("listener_pool is required")
	}
	if group.ShortagePolicy == "" {
		group.ShortagePolicy = model.ShortageReject
	}
	if !group.ShortagePolicy.Valid() {
		return groupMutationInput{}, model.Group{}, clientError("invalid shortage_policy")
	}
	return input, group, nil
}

func sortGroups(groups []model.Group) {
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})
}

func (a *App) persistGroupManagedFields(value model.State, groupName string) error {
	var credentials []model.Credential
	for _, credential := range value.Credentials {
		if strings.EqualFold(credential.Group, groupName) {
			credentials = append(credentials, credential)
		}
	}
	if len(credentials) == 0 {
		return nil
	}
	group, _ := findGroup(value, groupName)
	files, err := a.listHostAuthFiles()
	if err != nil {
		return upstreamError("list CPA Auth files: " + err.Error())
	}
	byName := make(map[string]hostAuthFileEntry, len(files))
	for _, file := range files {
		byName[strings.ToLower(file.Name)] = file
	}
	for _, credential := range credentials {
		auth, err := a.readExistingAuth(credential.AuthFile, byName)
		if err != nil {
			return upstreamError("read CPA Auth " + credential.AuthFile + ": " + err.Error())
		}
		if auth == nil {
			return conflictError("CPA Auth file is missing: " + credential.AuthFile)
		}
		applyManagedFields(auth, value, credential, group)
		if err := a.saveHostAuth(credential.AuthFile, auth); err != nil {
			return upstreamError("save CPA Auth " + credential.AuthFile + ": " + err.Error())
		}
	}
	return nil
}
