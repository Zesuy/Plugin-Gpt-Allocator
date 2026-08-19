package app

import (
	"strings"
	"time"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/allocator"
	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

// assignManagedCredential recalculates one credential's route using the target
// group's shortage policy. The credential's previous assignment is removed
// from the working copy before allocation so reassignment does not count its
// own old Listener as occupied.
func (a *App) assignManagedCredential(identity, groupName string) (model.State, error) {
	current, err := a.store.Load()
	if err != nil {
		return model.State{}, err
	}
	targetGroup, ok := findGroup(current, groupName)
	if !ok {
		return model.State{}, clientError("group does not exist")
	}
	credentialIndex := -1
	for index := range current.Credentials {
		if current.Credentials[index].Identity == identity {
			credentialIndex = index
			break
		}
	}
	if credentialIndex < 0 {
		return model.State{}, clientError("credential does not exist")
	}

	files, err := a.listHostAuthFiles()
	if err != nil {
		return model.State{}, upstreamError("list CPA Auth files: " + err.Error())
	}
	byName := make(map[string]hostAuthFileEntry, len(files))
	for _, file := range files {
		byName[strings.ToLower(file.Name)] = file
	}
	original := current.Credentials[credentialIndex]
	auth, err := a.readExistingAuth(original.AuthFile, byName)
	if err != nil {
		return model.State{}, upstreamError("read CPA Auth " + original.AuthFile + ": " + err.Error())
	}
	if auth == nil {
		return model.State{}, conflictError("CPA Auth " + original.AuthFile + " is unavailable; refresh local credentials before reassigning")
	}

	working := cloneState(current)
	credential := &working.Credentials[credentialIndex]
	credential.RouteSlotID = ""
	credential.RouteStatus = ""
	now := time.Now().UTC()
	assignment, err := allocator.Assign(&working, targetGroup, now)
	if err != nil {
		return model.State{}, conflictError(err.Error())
	}
	credential.Group = targetGroup.Name
	credential.RouteSlotID = assignment.RouteSlotID
	credential.RouteStatus = assignment.RouteStatus
	credential.UpdatedAt = now
	if credential.RouteSlotID != "" {
		// A failed Selector preparation is persisted as pending so the CPA Auth
		// safely falls back without retaining a stale proxy_url.
		_ = a.prepareNewRoute(&working, credential)
	}
	applyManagedFields(auth, working, *credential, targetGroup)
	if err := a.saveHostAuth(credential.AuthFile, auth); err != nil {
		return model.State{}, upstreamError("save CPA Auth " + credential.AuthFile + ": " + err.Error())
	}

	updated, err := a.store.Update(func(state *model.State) error {
		*state = working
		return nil
	})
	if err != nil {
		return model.State{}, err
	}
	return updated, nil
}
