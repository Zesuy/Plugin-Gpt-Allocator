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
	CredentialStatusSync credentialStatusSyncInfo `json:"credential_status_sync"`
}

// stateWithCredentialStatusSync treats CPA's Auth list as the source of truth
// for enabled/disabled state. Route ownership remains allocator-managed and is
// deliberately left unchanged when a status was toggled outside this plugin.
func (a *App) stateWithCredentialStatusSync() (model.State, credentialStatusSyncInfo, error) {
	now := time.Now().UTC()
	info := credentialStatusSyncInfo{
		Source:   "CPA host.auth.list",
		SyncedAt: now.Format(time.RFC3339),
	}
	value, err := a.store.Load()
	if err != nil {
		return model.State{}, info, err
	}
	files, err := a.listHostAuthFiles()
	if err != nil {
		info.Error = "读取 CPA 凭据状态失败"
		return value, info, nil
	}
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
		return value, info, nil
	}
	updated, err := a.store.Update(func(state *model.State) error {
		applyCredentialStatuses(state, statuses, now)
		return nil
	})
	if err != nil {
		return model.State{}, info, err
	}
	return updated, info, nil
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
