package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var publicIPTraceEndpoint = "https://chatgpt.com/cdn-cgi/trace"

type publicIPResult struct {
	IP        string `json:"ip"`
	Location  string `json:"location,omitempty"`
	Colo      string `json:"colo,omitempty"`
	CheckedAt string `json:"checked_at"`
}

// publicIP reads only the useful fields from Cloudflare's trace endpoint. It
// intentionally measures the plugin/CPA host's egress, not a Mihomo node.
func (a *App) publicIP() (managementResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicIPTraceEndpoint, nil)
	if err != nil {
		return managementResponse{}, upstreamError("create public IP request: " + err.Error())
	}
	req.Header.Set("accept", "text/plain")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return managementResponse{}, upstreamError("read public IP: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return managementResponse{}, upstreamError(fmt.Sprintf("public IP service returned HTTP %d", resp.StatusCode))
	}
	fields := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return managementResponse{}, upstreamError("read public IP response: " + err.Error())
	}
	if strings.TrimSpace(fields["ip"]) == "" {
		return managementResponse{}, upstreamError("public IP response did not contain an IP")
	}
	return jsonResponse(http.StatusOK, publicIPResult{
		IP:        fields["ip"],
		Location:  fields["loc"],
		Colo:      fields["colo"],
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}), nil
}
