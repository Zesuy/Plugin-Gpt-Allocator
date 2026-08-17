package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/mihomo"
)

type deduplicateInput struct {
	RouteSlotIDs  []string `json:"route_slot_ids,omitempty"`
	MaxCandidates int      `json:"max_candidates,omitempty"`
}

type deduplicateSlot struct {
	RouteSlotID string `json:"route_slot_id"`
	Name        string `json:"name"`
	Selector    string `json:"selector"`
	Node        string `json:"current_node"`
	IP          string `json:"public_ip,omitempty"`
	Credentials int    `json:"credentials"`
}

type deduplicateCandidate struct {
	Node       string   `json:"node"`
	Alive      *bool    `json:"alive,omitempty"`
	MihomoMS   *int     `json:"mihomo_delay_ms,omitempty"`
	P95MS      *float64 `json:"request_p95_ms,omitempty"`
	PublicIP   string   `json:"public_ip,omitempty"`
	Validation string   `json:"validation"`
	Reason     string   `json:"reason,omitempty"`
}

type deduplicateCluster struct {
	IP             string                 `json:"public_ip"`
	Slots          []deduplicateSlot      `json:"slots"`
	SharedSelector bool                   `json:"shared_selector"`
	Candidates     []deduplicateCandidate `json:"candidates"`
}

type deduplicatePreview struct {
	CheckedAt time.Time            `json:"checked_at"`
	Clusters  []deduplicateCluster `json:"clusters"`
	Message   string               `json:"message,omitempty"`
}

type deduplicateChange struct {
	RouteSlotIDs []string `json:"route_slot_ids"`
	Selector     string   `json:"selector"`
	FromNode     string   `json:"from_node"`
	ToNode       string   `json:"to_node,omitempty"`
	FromIP       string   `json:"from_ip,omitempty"`
	ToIP         string   `json:"to_ip,omitempty"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason,omitempty"`
}

type deduplicateResult struct {
	CheckedAt time.Time           `json:"checked_at"`
	Changes   []deduplicateChange `json:"changes"`
	Message   string              `json:"message"`
}

func (a *App) deduplicatePreview(body []byte) (managementResponse, error) {
	input, err := decodeDeduplicateInput(body)
	if err != nil {
		return managementResponse{}, err
	}
	snapshot, err := a.readRouteDiagnostics()
	if err != nil {
		return managementResponse{}, err
	}
	preview := buildDeduplicatePreview(snapshot, input)
	if value, loadErr := a.store.Load(); loadErr == nil {
		counts := make(map[string]int)
		for _, credential := range value.Credentials {
			counts[credential.RouteSlotID]++
		}
		for clusterIndex := range preview.Clusters {
			for slotIndex := range preview.Clusters[clusterIndex].Slots {
				slot := &preview.Clusters[clusterIndex].Slots[slotIndex]
				slot.Credentials = counts[slot.RouteSlotID]
			}
		}
	}
	return jsonResponse(http.StatusOK, preview), nil
}

func (a *App) deduplicate(body []byte) (managementResponse, error) {
	input, err := decodeDeduplicateInput(body)
	if err != nil {
		return managementResponse{}, err
	}
	initial, err := a.readRouteDiagnostics()
	if err != nil {
		return managementResponse{}, err
	}
	preview := buildDeduplicatePreview(initial, input)
	if len(preview.Clusters) == 0 {
		return jsonResponse(http.StatusOK, deduplicateResult{CheckedAt: time.Now().UTC(), Changes: []deduplicateChange{}, Message: "没有发现可处理的重复公网 IP"}), nil
	}
	value, client, err := a.mihomoClient()
	if err != nil {
		return managementResponse{}, clientError(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	changes := make([]deduplicateChange, 0)
	current := initial
	maxCandidates := input.MaxCandidates
	if maxCandidates <= 0 || maxCandidates > 8 {
		maxCandidates = 4
	}
	for _, cluster := range preview.Clusters {
		units := groupDuplicateSelectorUnits(cluster, current)
		for _, unit := range units {
			if len(unit.slots) == 0 {
				continue
			}
			slot := unit.slots[0]
			originalNode := current[slot.RouteSlotID].CurrentNode
			candidates := candidateNodes(current[slot.RouteSlotID], maxCandidates)
			accepted := false
			for _, candidate := range candidates {
				if candidate == originalNode {
					continue
				}
				if err := selectSelectorNode(ctx, client, current[slot.RouteSlotID].Selector, candidate); err != nil {
					changes = append(changes, deduplicateChange{RouteSlotIDs: unit.ids(), Selector: slot.Selector, FromNode: originalNode, ToNode: candidate, Action: "failed", Reason: "切换节点失败"})
					continue
				}
				refreshed, refreshErr := a.readRouteDiagnostics()
				if refreshErr == nil && duplicateResolvedForUnit(refreshed, unit.ids()) {
					current = refreshed
					changes = append(changes, deduplicateChange{RouteSlotIDs: unit.ids(), Selector: slot.Selector, FromNode: originalNode, ToNode: candidate, FromIP: cluster.IP, ToIP: refreshed[slot.RouteSlotID].ListenerProbe.IP, Action: "switched"})
					accepted = true
					break
				}
				_ = selectSelectorNode(ctx, client, slot.Selector, originalNode)
				changes = append(changes, deduplicateChange{RouteSlotIDs: unit.ids(), Selector: slot.Selector, FromNode: originalNode, ToNode: candidate, FromIP: cluster.IP, Action: "rolled_back", Reason: "候选节点公网 IP 仍重复或探测失败"})
			}
			if !accepted {
				changes = append(changes, deduplicateChange{RouteSlotIDs: unit.ids(), Selector: slot.Selector, FromNode: originalNode, Action: "unchanged", Reason: "没有验证到可用且不重复的候选节点"})
			}
		}
	}
	message := "去除重复执行完成"
	if len(changes) == 0 {
		message = "没有执行节点切换"
	}
	_ = value
	return jsonResponse(http.StatusOK, deduplicateResult{CheckedAt: time.Now().UTC(), Changes: changes, Message: message}), nil
}

func decodeDeduplicateInput(body []byte) (deduplicateInput, error) {
	var input deduplicateInput
	if err := decodeBody(body, &input); err != nil {
		return deduplicateInput{}, err
	}
	return input, nil
}

func (a *App) readRouteDiagnostics() (map[string]syncedRouteSlot, error) {
	response, err := a.syncRouteSlots()
	if err != nil {
		return nil, err
	}
	var payload struct {
		RouteSlots []syncedRouteSlot `json:"route_slots"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return nil, upstreamError("decode route diagnostics: " + err.Error())
	}
	result := make(map[string]syncedRouteSlot, len(payload.RouteSlots))
	for _, slot := range payload.RouteSlots {
		result[slot.RouteSlotID] = slot
	}
	return result, nil
}

func buildDeduplicatePreview(snapshot map[string]syncedRouteSlot, input deduplicateInput) deduplicatePreview {
	selected := make(map[string]struct{}, len(input.RouteSlotIDs))
	for _, id := range input.RouteSlotIDs {
		selected[strings.TrimSpace(id)] = struct{}{}
	}
	byIP := make(map[string][]syncedRouteSlot)
	for id, slot := range snapshot {
		if len(selected) > 0 {
			if _, ok := selected[id]; !ok {
				continue
			}
		}
		if slot.ListenerProbe == nil || strings.TrimSpace(slot.ListenerProbe.IP) == "" {
			continue
		}
		byIP[slot.ListenerProbe.IP] = append(byIP[slot.ListenerProbe.IP], slot)
	}
	preview := deduplicatePreview{CheckedAt: time.Now().UTC()}
	for ip, slots := range byIP {
		if len(slots) < 2 {
			continue
		}
		cluster := deduplicateCluster{IP: ip, Slots: make([]deduplicateSlot, 0, len(slots))}
		selectors := make(map[string]struct{})
		for _, slot := range slots {
			selectors[slot.Selector] = struct{}{}
			cluster.Slots = append(cluster.Slots, deduplicateSlot{RouteSlotID: slot.RouteSlotID, Name: slot.RouteSlotID, Selector: slot.Selector, Node: slot.CurrentNode, IP: ip})
		}
		cluster.SharedSelector = len(selectors) < len(slots)
		for _, slot := range slots {
			for _, candidate := range candidateNodes(slot, 4) {
				alive, delay, p95 := candidateMetrics(slot, candidate)
				cluster.Candidates = append(cluster.Candidates, deduplicateCandidate{Node: candidate, Alive: alive, MihomoMS: delay, P95MS: p95, Validation: "待验证", Reason: "需要通过 Listener 实际探测公网 IP"})
			}
		}
		preview.Clusters = append(preview.Clusters, cluster)
	}
	sort.Slice(preview.Clusters, func(i, j int) bool { return preview.Clusters[i].IP < preview.Clusters[j].IP })
	if len(preview.Clusters) == 0 {
		preview.Message = "没有发现重复公网 IP；请先刷新 Listener 诊断"
	} else {
		preview.Message = "候选节点的公网 IP 需要在执行时逐个验证"
	}
	return preview
}

type duplicateSelectorUnit struct {
	selector string
	slots    []syncedRouteSlot
}

func groupDuplicateSelectorUnits(cluster deduplicateCluster, snapshot map[string]syncedRouteSlot) []duplicateSelectorUnit {
	bySelector := make(map[string][]syncedRouteSlot)
	for _, item := range cluster.Slots {
		if slot, ok := snapshot[item.RouteSlotID]; ok {
			bySelector[item.Selector] = append(bySelector[item.Selector], slot)
		}
	}
	units := make([]duplicateSelectorUnit, 0, len(bySelector))
	for selector, slots := range bySelector {
		units = append(units, duplicateSelectorUnit{selector: selector, slots: slots})
	}
	return units
}

func (u duplicateSelectorUnit) ids() []string {
	ids := make([]string, 0, len(u.slots))
	for _, slot := range u.slots {
		ids = append(ids, slot.RouteSlotID)
	}
	return ids
}

func candidateNodes(slot syncedRouteSlot, limit int) []string {
	result := make([]string, 0, len(slot.Nodes))
	for _, node := range slot.Nodes {
		if node != "" && node != slot.CurrentNode {
			result = append(result, node)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

func candidateMetrics(slot syncedRouteSlot, node string) (*bool, *int, *float64) {
	health := slot.NodeHealth[node]
	stats := slot.NodeStats[node].All
	var delay *int
	if len(health.History) > 0 {
		value := health.History[len(health.History)-1].Delay
		delay = &value
	}
	return health.Alive, delay, stats.P95LatencyMS
}

func duplicateResolvedForUnit(snapshot map[string]syncedRouteSlot, ids []string) bool {
	seen := make(map[string]struct{})
	for _, id := range ids {
		slot, ok := snapshot[id]
		if !ok || slot.ListenerProbe == nil || slot.ListenerProbe.IP == "" {
			return false
		}
		if _, exists := seen[slot.ListenerProbe.IP]; exists {
			return false
		}
		seen[slot.ListenerProbe.IP] = struct{}{}
	}
	return true
}

func selectSelectorNode(ctx context.Context, client *mihomo.Client, selector, node string) error {
	if strings.TrimSpace(selector) == "" || strings.TrimSpace(node) == "" {
		return fmt.Errorf("selector and node are required")
	}
	return client.Select(ctx, selector, node)
}
