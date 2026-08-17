package app

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

const (
	nodeStatsMaxSamples = 1000
	nodeStatsMaxAge     = 6 * time.Hour
)

type nodeResultClass string

const (
	nodeResultSuccess  nodeResultClass = "success"
	nodeResultUpstream nodeResultClass = "upstream_error"
	nodeResultNetwork  nodeResultClass = "network_error"
	nodeResultTimeout  nodeResultClass = "timeout"
	nodeResultUnknown  nodeResultClass = "unknown_error"
)

type nodeSample struct {
	At        time.Time
	LatencyMS int64
	TTFTMS    int64
	Class     nodeResultClass
}

type nodeBucketKey struct {
	SlotID    string
	Node      string
	Transport string
}

type nodeStatsStore struct {
	mu      sync.RWMutex
	buckets map[nodeBucketKey][]nodeSample
}

func (s *nodeStatsStore) add(slotID, node, transport string, class nodeResultClass, record usageRecord) {
	slotID = strings.TrimSpace(slotID)
	node = strings.TrimSpace(node)
	if slotID == "" || node == "" {
		return
	}
	if transport == "" {
		transport = "unknown"
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	sample := nodeSample{At: at.UTC(), LatencyMS: record.Latency.Milliseconds(), TTFTMS: record.TTFT.Milliseconds(), Class: class}
	key := nodeBucketKey{SlotID: slotID, Node: node, Transport: transport}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = make(map[nodeBucketKey][]nodeSample)
	}
	cutoff := time.Now().UTC().Add(-nodeStatsMaxAge)
	values := s.buckets[key]
	start := 0
	for start < len(values) && values[start].At.Before(cutoff) {
		start++
	}
	if start > 0 {
		values = values[start:]
	}
	values = append(values, sample)
	if len(values) > nodeStatsMaxSamples {
		values = values[len(values)-nodeStatsMaxSamples:]
	}
	s.buckets[key] = values
}

type nodeStatsSummary struct {
	Requests           int64            `json:"requests"`
	Success            int64            `json:"success"`
	UpstreamErrors     int64            `json:"upstream_errors"`
	NetworkErrors      int64            `json:"network_errors"`
	Timeouts           int64            `json:"timeouts"`
	UnknownErrors      int64            `json:"unknown_errors"`
	NetworkFailureRate float64          `json:"network_failure_rate"`
	AverageLatencyMS   *float64         `json:"average_latency_ms,omitempty"`
	P50LatencyMS       *float64         `json:"p50_latency_ms,omitempty"`
	P95LatencyMS       *float64         `json:"p95_latency_ms,omitempty"`
	AverageTTFTMS      *float64         `json:"average_ttft_ms,omitempty"`
	Congestion         string           `json:"congestion"`
	LastAt             *time.Time       `json:"last_at,omitempty"`
	FailureReasons     map[string]int64 `json:"failure_reasons,omitempty"`
}

type nodeStatsView struct {
	Node       string                      `json:"node"`
	All        nodeStatsSummary            `json:"all"`
	Transports map[string]nodeStatsSummary `json:"transports,omitempty"`
}

func (s *nodeStatsStore) snapshot(slotID, node string) nodeStatsView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := nodeStatsView{Node: node, Transports: make(map[string]nodeStatsSummary)}
	var all []nodeSample
	for key, values := range s.buckets {
		if key.SlotID != slotID || key.Node != node {
			continue
		}
		all = append(all, values...)
		view.Transports[key.Transport] = summarizeNodeSamples(values)
	}
	view.All = summarizeNodeSamples(all)
	return view
}

func summarizeNodeSamples(values []nodeSample) nodeStatsSummary {
	result := nodeStatsSummary{Congestion: "data_insufficient"}
	if len(values) == 0 {
		return result
	}
	latencies := make([]float64, 0, len(values))
	ttfts := make([]float64, 0, len(values))
	var latencyTotal, ttftTotal int64
	var lastAt time.Time
	for _, sample := range values {
		result.Requests++
		switch sample.Class {
		case nodeResultSuccess:
			result.Success++
		case nodeResultUpstream:
			result.UpstreamErrors++
		case nodeResultNetwork:
			result.NetworkErrors++
		case nodeResultTimeout:
			result.Timeouts++
		default:
			result.UnknownErrors++
		}
		if sample.LatencyMS > 0 {
			latency := float64(sample.LatencyMS)
			latencies = append(latencies, latency)
			latencyTotal += sample.LatencyMS
		}
		if sample.TTFTMS > 0 {
			ttft := float64(sample.TTFTMS)
			ttfts = append(ttfts, ttft)
			ttftTotal += sample.TTFTMS
		}
		if sample.At.After(lastAt) {
			lastAt = sample.At
		}
	}
	if result.Requests > 0 {
		result.NetworkFailureRate = float64(result.NetworkErrors+result.Timeouts) / float64(result.Requests)
	}
	if len(latencies) > 0 {
		average := float64(latencyTotal) / float64(len(latencies))
		result.AverageLatencyMS = &average
		sort.Float64s(latencies)
		p50 := percentile(latencies, 0.50)
		p95 := percentile(latencies, 0.95)
		result.P50LatencyMS = &p50
		result.P95LatencyMS = &p95
	}
	if len(ttfts) > 0 {
		average := float64(ttftTotal) / float64(len(ttfts))
		result.AverageTTFTMS = &average
	}
	if !lastAt.IsZero() {
		result.LastAt = &lastAt
	}
	result.Congestion = congestionLabel(result, latencies)
	return result
}

func percentile(values []float64, ratio float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * ratio)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func congestionLabel(summary nodeStatsSummary, latencies []float64) string {
	if summary.Requests < 5 {
		return "data_insufficient"
	}
	if summary.NetworkFailureRate >= 0.20 {
		return "network_unstable"
	}
	if len(latencies) >= 5 && summary.P50LatencyMS != nil && summary.P95LatencyMS != nil && *summary.P95LatencyMS >= 2*(*summary.P50LatencyMS) && *summary.P95LatencyMS >= 1500 {
		return "congestion_suspected"
	}
	return "normal"
}

func classifyNodeResult(record usageRecord) nodeResultClass {
	if !record.Failed {
		return nodeResultSuccess
	}
	if record.Failure.StatusCode >= 400 && record.Failure.StatusCode <= 599 {
		return nodeResultUpstream
	}
	body := strings.ToLower(strings.TrimSpace(record.Failure.Body))
	if strings.Contains(body, "timeout") || strings.Contains(body, "deadline") {
		return nodeResultTimeout
	}
	for _, marker := range []string{"eof", "connection reset", "connection refused", "broken pipe", "tls", "proxy", "dial ", "network"} {
		if strings.Contains(body, marker) {
			return nodeResultNetwork
		}
	}
	return nodeResultUnknown
}

type routeNodeChange struct {
	At   time.Time
	Node string
}

type routeHistory struct {
	mu   sync.RWMutex
	byID map[string][]routeNodeChange
}

func newRouteHistory() routeHistory {
	return routeHistory{byID: make(map[string][]routeNodeChange)}
}

func (h *routeHistory) record(slotID, node string, at time.Time) {
	slotID = strings.TrimSpace(slotID)
	node = strings.TrimSpace(node)
	if slotID == "" || node == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	values := append(h.byID[slotID], routeNodeChange{At: at.UTC(), Node: node})
	if len(values) > 100 {
		values = values[len(values)-100:]
	}
	h.byID[slotID] = values
}

func (h *routeHistory) nodeAt(slotID string, at time.Time, fallback string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	values := h.byID[slotID]
	node := fallback
	for _, change := range values {
		if at.IsZero() || !change.At.After(at) {
			node = change.Node
		}
	}
	return node
}

func (a *App) resolveUsageRoute(value model.State, record usageRecord) (string, string, bool) {
	var credential *model.Credential
	for index := range value.Credentials {
		item := &value.Credentials[index]
		if (record.AuthIndex != "" && strings.EqualFold(item.AuthFile, record.AuthIndex)) || (record.AuthID != "" && item.Identity == record.AuthID) {
			credential = item
			break
		}
	}
	if credential == nil {
		return "", "", false
	}
	for _, slot := range value.RouteSlots {
		if slot.ID != credential.RouteSlotID {
			continue
		}
		node := a.routeHistory.nodeAt(slot.ID, record.RequestedAt, slot.CurrentNode)
		return slot.ID, node, node != ""
	}
	return "", "", false
}
