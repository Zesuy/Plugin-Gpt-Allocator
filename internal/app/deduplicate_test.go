package app

import (
	"strings"
	"testing"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/mihomo"
)

func TestBuildDeduplicatePreviewKeepsOneAndMovesOnlyRemainingListeners(t *testing.T) {
	alive := true
	snapshot := map[string]syncedRouteSlot{
		"cpa01": dedupeTestSlot("cpa01", "selector-01", "node-a", "203.0.113.1", []string{"node-a", "used-node", "unknown-node"}, &alive),
		"cpa02": dedupeTestSlot("cpa02", "selector-02", "node-b", "203.0.113.1", []string{"node-b", "used-node", "unknown-node"}, &alive),
		"cpa03": dedupeTestSlot("cpa03", "selector-03", "used-node", "203.0.113.2", []string{"used-node"}, &alive),
	}

	preview := buildDeduplicatePreview(snapshot, deduplicateInput{})
	if len(preview.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(preview.Clusters))
	}
	cluster := preview.Clusters[0]
	if got := []string{cluster.Slots[0].RouteSlotID + ":" + cluster.Slots[0].Plan, cluster.Slots[1].RouteSlotID + ":" + cluster.Slots[1].Plan}; got[0] != "cpa01:keep" || got[1] != "cpa02:switch" {
		t.Fatalf("plans = %v, want [cpa01:keep cpa02:switch]", got)
	}
	if len(cluster.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cluster.Candidates))
	}
	if cluster.Candidates[0].RouteSlotID != "cpa02" || cluster.Candidates[0].Node != "unknown-node" || cluster.Candidates[0].InUse {
		t.Fatalf("first candidate = %#v, want unused node for cpa02", cluster.Candidates[0])
	}
	if cluster.Candidates[1].Node != "used-node" || !cluster.Candidates[1].InUse || cluster.Candidates[1].PublicIP != "203.0.113.2" {
		t.Fatalf("second candidate = %#v, want observed in-use node", cluster.Candidates[1])
	}
}

func TestBuildDeduplicatePreviewBlocksSharedSelectorIncludingOutsideCluster(t *testing.T) {
	snapshot := map[string]syncedRouteSlot{
		"cpa01": dedupeTestSlot("cpa01", "shared", "node-a", "203.0.113.1", []string{"node-a", "node-b"}, nil),
		"cpa02": dedupeTestSlot("cpa02", "selector-02", "node-a", "203.0.113.1", []string{"node-a", "node-b"}, nil),
		"cpa03": dedupeTestSlot("cpa03", "shared", "node-c", "203.0.113.3", []string{"node-c", "node-b"}, nil),
	}

	preview := buildDeduplicatePreview(snapshot, deduplicateInput{})
	cluster := preview.Clusters[0]
	if !cluster.SharedSelector || !strings.Contains(cluster.BlockedReason, "cpa01、cpa03") {
		t.Fatalf("shared-selector block = %q", cluster.BlockedReason)
	}
	for _, slot := range cluster.Slots {
		if slot.Plan != "blocked" {
			t.Fatalf("slot %s plan = %q, want blocked", slot.RouteSlotID, slot.Plan)
		}
	}
}

func TestPublicIPUniqueForUnitChecksEveryListener(t *testing.T) {
	snapshot := map[string]syncedRouteSlot{
		"cpa01": dedupeTestSlot("cpa01", "selector-01", "node-a", "203.0.113.1", nil, nil),
		"cpa02": dedupeTestSlot("cpa02", "selector-02", "node-b", "203.0.113.2", nil, nil),
		"cpa03": dedupeTestSlot("cpa03", "selector-03", "node-c", "203.0.113.3", nil, nil),
	}
	if !publicIPUniqueForUnit(snapshot, []string{"cpa02"}) {
		t.Fatal("unique target IP was rejected")
	}
	snapshot["cpa02"] = dedupeTestSlot("cpa02", "selector-02", "node-b", "203.0.113.1", nil, nil)
	if publicIPUniqueForUnit(snapshot, []string{"cpa02"}) {
		t.Fatal("target IP colliding with another Listener was accepted")
	}
	snapshot["cpa02"] = dedupeTestSlot("cpa02", "selector-02", "node-b", "", nil, nil)
	if publicIPUniqueForUnit(snapshot, []string{"cpa02"}) {
		t.Fatal("target without a verified IP was accepted")
	}
}

func TestCandidateNodesPrioritizesUnusedNodes(t *testing.T) {
	alive := true
	target := dedupeTestSlot("cpa02", "selector-02", "node-b", "203.0.113.1", []string{"node-b", "used-node", "unknown-node"}, &alive)
	snapshot := map[string]syncedRouteSlot{
		"cpa02": target,
		"cpa03": dedupeTestSlot("cpa03", "selector-03", "used-node", "203.0.113.2", nil, &alive),
	}
	candidates := candidateNodes(target, snapshot, 8)
	if len(candidates) != 2 || candidates[0] != "unknown-node" || candidates[1] != "used-node" {
		t.Fatalf("candidate order = %v, want [unknown-node used-node]", candidates)
	}
}

func dedupeTestSlot(id, selector, node, ip string, nodes []string, alive *bool) syncedRouteSlot {
	probe := &listenerProbe{IP: ip}
	return syncedRouteSlot{
		RouteSlotID:   id,
		Selector:      selector,
		CurrentNode:   node,
		Nodes:         nodes,
		ListenerProbe: probe,
		NodeHealth: map[string]mihomo.Selector{
			"unknown-node": {Alive: alive},
			"used-node":    {Alive: alive},
		},
		NodeStats: map[string]nodeStatsView{},
	}
}
