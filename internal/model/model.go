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
	LastAssignedAt time.Time `json:"last_assigned_at,omitempty"`
}

type Credential struct {
	Identity    string    `json:"identity"`
	AuthFile    string    `json:"auth_file"`
	Email       string    `json:"email"`
	Alias       string    `json:"alias,omitempty"`
	Provider    string    `json:"provider"`
	AccountID   string    `json:"account_id,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Group       string    `json:"group"`
	RouteSlotID string    `json:"route_slot_id,omitempty"`
	RouteStatus string    `json:"route_status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
