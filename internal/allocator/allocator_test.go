package allocator

import (
	"errors"
	"testing"
	"time"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

func TestAssignUsesFreeLeastRecentlyAssignedSlot(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	value := model.NewState()
	value.RouteSlots = []model.RouteSlot{
		{ID: "newer", Pool: "default", ListenerURL: "socks5://127.0.0.1:2", LastAssignedAt: now.Add(-time.Hour)},
		{ID: "never", Pool: "default", ListenerURL: "socks5://127.0.0.1:1"},
	}
	got, err := Assign(&value, model.Group{ListenerPool: "default", ShortagePolicy: model.ShortageReject}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteSlotID != "never" || got.RouteStatus != model.RouteStatusAssigned {
		t.Fatalf("unexpected assignment: %#v", got)
	}
}

func TestAssignSharesLeastUsedSlot(t *testing.T) {
	now := time.Now().UTC()
	value := model.NewState()
	value.RouteSlots = []model.RouteSlot{
		{ID: "one", Pool: "default", ListenerURL: "socks5://127.0.0.1:1"},
		{ID: "two", Pool: "default", ListenerURL: "socks5://127.0.0.1:2"},
	}
	value.Credentials = []model.Credential{
		{Identity: "a", RouteSlotID: "one"},
		{Identity: "b", RouteSlotID: "one"},
		{Identity: "c", RouteSlotID: "two"},
	}
	got, err := Assign(&value, model.Group{ListenerPool: "default", ShortagePolicy: model.ShortageShareLeast}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteSlotID != "two" || got.RouteStatus != model.RouteStatusShared {
		t.Fatalf("unexpected assignment: %#v", got)
	}
}

func TestAssignTreatsDisabledCredentialsAsFreeCapacity(t *testing.T) {
	now := time.Now().UTC()
	value := model.NewState()
	value.RouteSlots = []model.RouteSlot{
		{ID: "dormant", Pool: "default", ListenerURL: "socks5://127.0.0.1:1"},
		{ID: "active", Pool: "default", ListenerURL: "socks5://127.0.0.1:2"},
	}
	value.Credentials = []model.Credential{
		{Identity: "disabled", RouteSlotID: "dormant", Disabled: true},
		{Identity: "enabled", RouteSlotID: "active"},
	}
	got, err := Assign(&value, model.Group{ListenerPool: "default", ShortagePolicy: model.ShortageReject}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteSlotID != "dormant" || got.RouteStatus != model.RouteStatusAssigned {
		t.Fatalf("disabled-only Listener was not treated as free: %#v", got)
	}
}

func TestAssignExcludingRotatesAwayFromCurrentSlot(t *testing.T) {
	now := time.Now().UTC()
	value := model.NewState()
	value.RouteSlots = []model.RouteSlot{
		{ID: "current", Pool: "default", ListenerURL: "socks5://127.0.0.1:1"},
		{ID: "next", Pool: "default", ListenerURL: "socks5://127.0.0.1:2", LastAssignedAt: now},
	}
	got, err := AssignExcluding(&value, model.Group{ListenerPool: "default", ShortagePolicy: model.ShortageReject}, now, "current")
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteSlotID != "next" || got.RouteStatus != model.RouteStatusAssigned {
		t.Fatalf("rotation selected unexpected Listener: %#v", got)
	}
}

func TestAssignHonorsDefaultAndRejectPolicies(t *testing.T) {
	value := model.NewState()
	defaultRoute, err := Assign(&value, model.Group{ListenerPool: "missing", ShortagePolicy: model.ShortageDefault}, time.Now())
	if err != nil || defaultRoute.RouteStatus != model.RouteStatusDefault {
		t.Fatalf("default assignment: %#v err=%v", defaultRoute, err)
	}
	_, err = Assign(&value, model.Group{ListenerPool: "missing", ShortagePolicy: model.ShortageReject}, time.Now())
	if !errors.Is(err, ErrNoRouteAvailable) {
		t.Fatalf("reject error = %v", err)
	}
}
