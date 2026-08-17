package allocator

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

var ErrNoRouteAvailable = errors.New("no route is available for this group")

type Assignment struct {
	RouteSlotID string
	RouteStatus string
	ListenerURL string
}

func Assign(value *model.State, group model.Group, now time.Time) (Assignment, error) {
	usage := make(map[string]int)
	for _, credential := range value.Credentials {
		if credential.RouteSlotID != "" {
			usage[credential.RouteSlotID]++
		}
	}
	type candidate struct {
		index int
		uses  int
	}
	var candidates []candidate
	for index, slot := range value.RouteSlots {
		if strings.EqualFold(slot.Pool, group.ListenerPool) {
			candidates = append(candidates, candidate{index: index, uses: usage[slot.ID]})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].uses != candidates[j].uses {
			return candidates[i].uses < candidates[j].uses
		}
		left := value.RouteSlots[candidates[i].index].LastAssignedAt
		right := value.RouteSlots[candidates[j].index].LastAssignedAt
		if left.IsZero() != right.IsZero() {
			return left.IsZero()
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return value.RouteSlots[candidates[i].index].ID < value.RouteSlots[candidates[j].index].ID
	})

	if len(candidates) == 0 {
		if group.ShortagePolicy == model.ShortageDefault {
			return Assignment{RouteStatus: model.RouteStatusDefault}, nil
		}
		return Assignment{}, ErrNoRouteAvailable
	}
	selected := candidates[0]
	if selected.uses > 0 {
		switch group.ShortagePolicy {
		case model.ShortageDefault:
			return Assignment{RouteStatus: model.RouteStatusDefault}, nil
		case model.ShortageReject:
			return Assignment{}, ErrNoRouteAvailable
		case model.ShortageShareLeast:
			// The least-used candidate selected above is intentionally shared.
		default:
			return Assignment{}, ErrNoRouteAvailable
		}
	}
	slot := &value.RouteSlots[selected.index]
	slot.LastAssignedAt = now.UTC()
	status := model.RouteStatusAssigned
	if selected.uses > 0 {
		status = model.RouteStatusShared
	}
	return Assignment{RouteSlotID: slot.ID, RouteStatus: status, ListenerURL: slot.ListenerURL}, nil
}
