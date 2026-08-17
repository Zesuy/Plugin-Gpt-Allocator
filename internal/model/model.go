package model

import "time"

const CurrentStateVersion = 1

const (
	RouteStatusAssigned = "assigned"
	RouteStatusShared   = "shared"
	RouteStatusDefault  = "default_route"
	RouteStatusPending  = "pending"
)

type ShortagePolicy string

const (
	ShortageShareLeast ShortagePolicy = "share_least"
	ShortageReject     ShortagePolicy = "reject"
	ShortageDefault    ShortagePolicy = "default_route"
)

func (p ShortagePolicy) Valid() bool {
	switch p {
	case ShortageShareLeast, ShortageReject, ShortageDefault:
		return true
	default:
		return false
	}
}

type Settings struct {
	MihomoControllerURL string `json:"mihomo_controller_url,omitempty"`
	MihomoSecret        string `json:"mihomo_secret,omitempty"`
}

type Group struct {
	Name           string         `json:"name"`
	Priority       int            `json:"priority"`
	Websockets     bool           `json:"websockets"`
	ListenerPool   string         `json:"listener_pool"`
	ShortagePolicy ShortagePolicy `json:"shortage_policy"`
}

type RouteSlot struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ListenerURL    string    `json:"listener_url"`
	Selector       string    `json:"selector"`
	Pool           string    `json:"pool"`
	CurrentNode    string    `json:"current_node,omitempty"`
	NodeChangedAt  time.Time `json:"node_changed_at,omitempty"`
	LastAssignedAt time.Time `json:"last_assigned_at,omitempty"`
}

type Credential struct {
	Identity    string         `json:"identity"`
	AuthFile    string         `json:"auth_file"`
	Disabled    bool           `json:"disabled"`
	Email       string         `json:"email"`
	Alias       string         `json:"alias,omitempty"`
	Provider    string         `json:"provider"`
	AccountID   string         `json:"account_id,omitempty"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Group       string         `json:"group"`
	RouteSlotID string         `json:"route_slot_id,omitempty"`
	RouteStatus string         `json:"route_status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Quota       *QuotaSnapshot `json:"quota,omitempty"`
}

// QuotaSnapshot is deliberately provider-neutral at the storage boundary. A
// provider can expose more than one independent window, so rows are kept
// separate instead of being collapsed into a single remaining number.
type QuotaSnapshot struct {
	Provider  string     `json:"provider"`
	CheckedAt time.Time  `json:"checked_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	Rows      []QuotaRow `json:"rows,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type QuotaRow struct {
	Window             string   `json:"window"`
	UsedPercent        *float64 `json:"used_percent,omitempty"`
	RemainingPercent   *float64 `json:"remaining_percent,omitempty"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds,omitempty"`
	ResetAt            *int64   `json:"reset_at,omitempty"`
	ResetAfterSeconds  *int64   `json:"reset_after_seconds,omitempty"`
	Allowed            *bool    `json:"allowed,omitempty"`
	LimitReached       *bool    `json:"limit_reached,omitempty"`
}

func (c Credential) DisplayName() string {
	if c.Alias != "" {
		return c.Alias
	}
	return c.Email
}

type State struct {
	Version     int          `json:"version"`
	Settings    Settings     `json:"settings"`
	Groups      []Group      `json:"groups"`
	RouteSlots  []RouteSlot  `json:"route_slots"`
	Credentials []Credential `json:"credentials"`
}

func NewState() State {
	return State{Version: CurrentStateVersion}
}

type PublicSettings struct {
	MihomoControllerURL string `json:"mihomo_controller_url,omitempty"`
	MihomoSecretSet     bool   `json:"mihomo_secret_set"`
}

type PublicState struct {
	Version     int            `json:"version"`
	Settings    PublicSettings `json:"settings"`
	Groups      []Group        `json:"groups"`
	RouteSlots  []RouteSlot    `json:"route_slots"`
	Credentials []Credential   `json:"credentials"`
}

func (s State) Public() PublicState {
	return PublicState{
		Version: s.Version,
		Settings: PublicSettings{
			MihomoControllerURL: s.Settings.MihomoControllerURL,
			MihomoSecretSet:     s.Settings.MihomoSecret != "",
		},
		Groups:      append([]Group(nil), s.Groups...),
		RouteSlots:  append([]RouteSlot(nil), s.RouteSlots...),
		Credentials: append([]Credential(nil), s.Credentials...),
	}
}
