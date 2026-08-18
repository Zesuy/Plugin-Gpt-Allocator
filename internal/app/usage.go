package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type usageRecord struct {
	Provider        string        `json:"Provider"`
	ExecutorType    string        `json:"ExecutorType"`
	AuthID          string        `json:"AuthID"`
	AuthIndex       string        `json:"AuthIndex"`
	AuthType        string        `json:"AuthType"`
	Model           string        `json:"Model"`
	Generate        bool          `json:"Generate"`
	RequestedAt     time.Time     `json:"RequestedAt"`
	Latency         time.Duration `json:"Latency"`
	TTFT            time.Duration `json:"TTFT"`
	Failed          bool          `json:"Failed"`
	Failure         usageFailure  `json:"Failure"`
	ResponseHeaders http.Header   `json:"ResponseHeaders"`
	Detail          usageDetail   `json:"Detail"`
}

type usageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

type usageDetail struct {
	OutputTokens int64 `json:"OutputTokens"`
}

type transportStats struct {
	Requests          int64
	Failed            int64
	TTFTMsTotal       int64
	GenerationMsTotal int64
	OutputTokens      int64
	LastAt            time.Time
	FailureStatuses   map[string]int64
}

func (s *transportStats) add(record usageRecord) {
	s.Requests++
	if record.Failed {
		s.Failed++
		if record.Failure.StatusCode > 0 {
			if s.FailureStatuses == nil {
				s.FailureStatuses = make(map[string]int64)
			}
			key := fmt.Sprintf("%d %s", record.Failure.StatusCode, http.StatusText(record.Failure.StatusCode))
			s.FailureStatuses[key]++
		}
	}
	latencyMs := record.Latency.Milliseconds()
	ttftMs := record.TTFT.Milliseconds()
	if ttftMs > 0 {
		s.TTFTMsTotal += ttftMs
	}
	generationMs := latencyMs - ttftMs
	if generationMs < 0 {
		generationMs = 0
	}
	s.GenerationMsTotal += generationMs
	s.OutputTokens += record.Detail.OutputTokens
	s.LastAt = time.Now().UTC()
}

type transportSummary struct {
	Requests            int64            `json:"requests"`
	Failed              int64            `json:"failed"`
	FailureRate         float64          `json:"failure_rate"`
	TTFTMsTotal         int64            `json:"ttft_ms_total"`
	GenerationMsTotal   int64            `json:"generation_ms_total"`
	AverageTTFTMs       *float64         `json:"average_ttft_ms,omitempty"`
	AverageGenerationMs *float64         `json:"average_generation_ms,omitempty"`
	OutputTokens        int64            `json:"output_tokens"`
	GenerationTokensPS  *float64         `json:"generation_tokens_per_second,omitempty"`
	LastAt              *time.Time       `json:"last_at,omitempty"`
	FailureStatusCounts map[string]int64 `json:"failure_status_counts,omitempty"`
}

func (s transportStats) summary() transportSummary {
	result := transportSummary{
		Requests: s.Requests, Failed: s.Failed, OutputTokens: s.OutputTokens,
		TTFTMsTotal: s.TTFTMsTotal, GenerationMsTotal: s.GenerationMsTotal,
	}
	if s.Requests > 0 {
		result.FailureRate = float64(s.Failed) / float64(s.Requests)
	}
	if s.TTFTMsTotal > 0 {
		value := float64(s.TTFTMsTotal) / float64(s.Requests)
		result.AverageTTFTMs = &value
	}
	if s.GenerationMsTotal > 0 {
		value := float64(s.GenerationMsTotal) / float64(s.Requests)
		result.AverageGenerationMs = &value
		if s.OutputTokens > 0 {
			speed := float64(s.OutputTokens) / (float64(s.GenerationMsTotal) / 1000)
			result.GenerationTokensPS = &speed
		}
	}
	if !s.LastAt.IsZero() {
		last := s.LastAt
		result.LastAt = &last
	}
	if len(s.FailureStatuses) > 0 {
		result.FailureStatusCounts = make(map[string]int64, len(s.FailureStatuses))
		for key, value := range s.FailureStatuses {
			result.FailureStatusCounts[key] = value
		}
	}
	return result
}

type usageStats struct {
	mu          sync.RWMutex
	sse         transportStats
	websocket   transportStats
	nonStream   transportStats
	otherStream transportStats
}

type usageStatsResponse struct {
	SSE         transportSummary `json:"sse"`
	Websocket   transportSummary `json:"websocket"`
	NonStream   transportSummary `json:"non_stream"`
	OtherStream transportSummary `json:"other_stream"`
	Quota       any              `json:"quota"`
	Source      string           `json:"source"`
}

func (s *usageStats) add(record usageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch classifyTransport(record) {
	case "sse":
		s.sse.add(record)
	case "websocket":
		s.websocket.add(record)
	case "non_stream":
		s.nonStream.add(record)
	default:
		s.otherStream.add(record)
	}
}

func (s *usageStats) snapshot() usageStatsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return usageStatsResponse{
		SSE:         s.sse.summary(),
		Websocket:   s.websocket.summary(),
		NonStream:   s.nonStream.summary(),
		OtherStream: s.otherStream.summary(),
		// CPA usage.handle provides token usage, but not a provider quota or
		// remaining balance. Keep this explicit instead of inventing a number.
		Quota:  nil,
		Source: "CPA usage.handle; process-local since last plugin start",
	}
}

func classifyTransport(record usageRecord) string {
	contentType := strings.ToLower(record.ResponseHeaders.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return "sse"
	}
	upgrade := strings.ToLower(record.ResponseHeaders.Get("Upgrade"))
	executor := strings.ToLower(record.ExecutorType)
	if strings.Contains(upgrade, "websocket") || strings.Contains(executor, "websocket") || strings.Contains(executor, "wsrelay") || strings.HasSuffix(executor, "ws") {
		return "websocket"
	}
	if record.TTFT <= 0 {
		return "non_stream"
	}
	return "other_stream"
}

func (a *App) handleUsage(raw []byte) error {
	var record usageRecord
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	a.usage.add(record)
	if value, err := a.store.Load(); err == nil {
		if slotID, node, ok := a.resolveUsageRoute(value, record); ok {
			a.nodeStats.add(slotID, node, classifyTransport(record), classifyNodeResult(record), record)
		}
	}
	return nil
}
