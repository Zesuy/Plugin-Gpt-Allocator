package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNodeStatsExposeTTFTWithoutTotalLatency(t *testing.T) {
	store := nodeStatsStore{}
	for index := int64(1); index <= 5; index++ {
		store.add("listener-1", "node-a", "websocket", nodeResultSuccess, usageRecord{
			RequestedAt: time.Now().UTC(),
			Latency:     time.Duration(10*index) * time.Second,
			TTFT:        time.Duration(index*100) * time.Millisecond,
		})
	}

	view := store.snapshot("listener-1", "node-a").All
	if view.AverageTTFTMS == nil || view.P50TTFTMS == nil || view.P95TTFTMS == nil {
		t.Fatalf("TTFT summary is incomplete: %#v", view)
	}
	if *view.P50TTFTMS != 300 || *view.P95TTFTMS != 400 {
		t.Fatalf("unexpected TTFT percentiles: p50=%v p95=%v", *view.P50TTFTMS, *view.P95TTFTMS)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "latency_ms") {
		t.Fatalf("node stats still expose total latency: %s", raw)
	}
}
