package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const listenerProbeURL = "https://chatgpt.com/cdn-cgi/trace"

type listenerProbe struct {
	IP        string `json:"ip,omitempty"`
	Location  string `json:"location,omitempty"`
	Colo      string `json:"colo,omitempty"`
	TTFTMS    int64  `json:"ttft_ms,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func probeListener(ctx context.Context, listenerURL string) listenerProbe {
	result := listenerProbe{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	parsed, err := url.Parse(strings.TrimSpace(listenerURL))
	if err != nil || parsed.Host == "" {
		result.Error = "Listener 地址无效"
		return result
	}
	client, err := listenerHTTPClient(parsed)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, listenerProbeURL, nil)
	if err != nil {
		result.Error = "创建公网 IP 探测请求失败"
		return result
	}
	request.Header.Set("accept", "text/plain")
	firstByteAt := time.Time{}
	started := time.Now()
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		GotFirstResponseByte: func() { firstByteAt = time.Now() },
	}))
	response, err := client.Do(request)
	if err != nil {
		result.Error = shortProbeError(err)
		return result
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		result.Error = fmt.Sprintf("公网 IP 探测返回 HTTP %d", response.StatusCode)
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		result.Error = shortProbeError(err)
		return result
	}
	fields := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if strings.TrimSpace(fields["ip"]) == "" {
		result.Error = "公网 IP 探测没有返回 IP"
		return result
	}
	result.IP = fields["ip"]
	result.Location = fields["loc"]
	result.Colo = fields["colo"]
	if !firstByteAt.IsZero() {
		result.TTFTMS = firstByteAt.Sub(started).Milliseconds()
	}
	return result
}

func listenerHTTPClient(listener *url.URL) (*http.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	switch strings.ToLower(listener.Scheme) {
	case "http", "https":
		proxyURL := *listener
		transport.Proxy = http.ProxyURL(&proxyURL)
	case "socks5", "socks5h":
		dialer, err := proxy.SOCKS5("tcp", listener.Host, nil, proxy.Direct)
		if err != nil {
			return nil, errors.New("创建 SOCKS5 Listener 连接失败")
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			type ctxDialer interface {
				DialContext(context.Context, string, string) (net.Conn, error)
			}
			if contextual, ok := dialer.(ctxDialer); ok {
				return contextual.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		}
	default:
		return nil, errors.New("Listener 只支持 HTTP、HTTPS 或 SOCKS5")
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}, nil
}

func shortProbeError(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "deadline") {
		return "公网 IP 探测超时"
	}
	if strings.Contains(message, "eof") || strings.Contains(message, "reset") {
		return "Listener 连接中断"
	}
	return "公网 IP 探测失败"
}
