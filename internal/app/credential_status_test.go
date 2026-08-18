package app

import (
	"testing"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

func TestFindCredentialRouteConflictOnlyCountsEnabledPeers(t *testing.T) {
	value := model.NewState()
	value.Credentials = []model.Credential{
		{Identity: "target", Email: "target@example.com", RouteSlotID: "cpa01"},
		{Identity: "enabled", Email: "enabled@example.com", RouteSlotID: "cpa01"},
		{Identity: "disabled", Email: "disabled@example.com", RouteSlotID: "cpa01", Disabled: true},
	}
	conflict := findCredentialRouteConflict(value, "target")
	if conflict == nil || conflict.RouteSlotID != "cpa01" || conflict.EnabledCount != 1 || len(conflict.EnabledIdentities) != 1 || conflict.EnabledIdentities[0] != "enabled@example.com" {
		t.Fatalf("unexpected route conflict: %#v", conflict)
	}
	value.Credentials[1].Disabled = true
	if got := findCredentialRouteConflict(value, "target"); got != nil {
		t.Fatalf("disabled peers should not produce a conflict: %#v", got)
	}
}
