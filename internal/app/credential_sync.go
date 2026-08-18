package app

import (
	"strings"
	"time"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

type credentialStatusSyncInfo struct {
	Source   string `json:"source"`
	SyncedAt string `json:"synced_at"`
	Matched  int    `json:"matched"`
	Updated  int    `json:"updated"`
	Missing  int    `json:"missing"`
	Error    string `json:"error,omitempty"`
}

type stateResult struct {
	model.PublicState
	CredentialStatusSync credentialStatusSyncInfo           `json:"credential_status_sync"`
	CredentialRuntime    map[string]credentialRuntimeStatus `json:"credential_runtime,omitempty"`
}

// credentialRuntimeStatus mirrors CPA's live Auth state. It is returned to the
// UI but deliberately never persisted in allocator state, because CPA updates
// it as real model requests succeed or fail.
type credentialRuntimeStatus struct {
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Unavailable   bool   `json:"unavailable,omitempty"`
}

// stateWithCredentialStatusSync treats CPA's Auth list as the source of truth
// for enabled/disabled state. Route ownership remains allocator-managed and is
// deliberately left unchanged when a status was toggled outside this plugin.
func (a *App) stateWithCredentialStatusSync() (model.State, credentialStatusSyncInfo, map[string]credentialRuntimeStatus, error) {
	now := time.Now().UTC()
	info := credentialStatusSyncInfo{
		Source:   "CPA host.auth.list",
		SyncedAt: now.Format(time.RFC3339),
	}
	value, err := a.store.Load()
	if err != nil {
		return model.State{}, info, nil, err
	}
	files, err := a.listHostAuthFiles()
	if err != nil {
		info.Error = "读取 CPA 凭据状态失败"
		return value, info, nil, nil
	}
	runtime := credentialRuntimeStatuses(value, files)
	statuses := make(map[string]bool, len(files))
	for _, file := range files {
		name := strings.ToLower(strings.TrimSpace(file.Name))
		if name != "" {
			statuses[name] = file.Disabled
		}
	}
	working := cloneState(value)
	info.Matched, info.Updated, info.Missing = applyCredentialStatuses(&working, statuses, now)
	if info.Updated == 0 {
		return value, info, runtime, nil
	}
	updated, err := a.store.Update(func(state *model.State) error {
		applyCredentialStatuses(state, statuses, now)
		return nil
	})
	if err != nil {
		return model.State{}, info, nil, err
	}
	return updated, info, runtime, nil
}

func credentialRuntimeStatuses(value model.State, files []hostAuthFileEntry) map[string]credentialRuntimeStatus {
	byName := make(map[string]hostAuthFileEntry, len(files))
	for _, file := range files {
		name := strings.ToLower(strings.TrimSpace(file.Name))
		if name != "" {
			byName[name] = file
		}
	}
	runtime := make(map[string]credentialRuntimeStatus)
	for _, credential := range value.Credentials {
		file, ok := byName[strings.ToLower(strings.TrimSpace(credential.AuthFile))]
		if !ok {
			continue
		}
		runtime[credential.Identity] = credentialRuntimeStatus{
			Status:        strings.TrimSpace(file.Status),
			StatusMessage: strings.TrimSpace(file.StatusMessage),
			Unavailable:   file.Unavailable,
		}
	}
	return runtime
}

func applyCredentialStatuses(value *model.State, statuses map[string]bool, now time.Time) (matched, updated, missing int) {
	for index := range value.Credentials {
		credential := &value.Credentials[index]
		disabled, ok := statuses[strings.ToLower(strings.TrimSpace(credential.AuthFile))]
		if !ok {
			missing++
			continue
		}
		matched++
		if credential.Disabled == disabled {
			continue
		}
		credential.Disabled = disabled
		credential.UpdatedAt = now
		updated++
	}
	return matched, updated, missing
}
