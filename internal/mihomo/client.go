package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxResponseSize = 2 << 20

type Client struct {
	baseURL *url.URL
	secret  string
	http    *http.Client
}

type Version struct {
	Meta    bool   `json:"meta"`
	Premium bool   `json:"premium"`
	Version string `json:"version"`
}

type Selector struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Now     string         `json:"now"`
	All     []string       `json:"all"`
	Alive   *bool          `json:"alive,omitempty"`
	History []HistoryEntry `json:"history,omitempty"`
}

// HistoryEntry is one delay probe recorded by Mihomo for a proxy.
// Mihomo uses delay=0/5000 for failed or timed-out probes depending on version.
// Keep the raw values so the UI can show the controller's own history faithfully.
type HistoryEntry struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

type proxiesResponse struct {
	Proxies map[string]Selector `json:"proxies"`
}

func New(controllerURL, secret string, httpClient *http.Client) (*Client, error) {
	controllerURL = strings.TrimRight(strings.TrimSpace(controllerURL), "/")
	if controllerURL == "" {
		return nil, errors.New("Mihomo controller URL is not configured")
	}
	parsed, err := url.Parse(controllerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Mihomo controller URL must be an absolute http or https URL")
	}
	if parsed.User != nil {
		return nil, errors.New("Mihomo controller URL must not contain user information")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: parsed, secret: strings.TrimSpace(secret), http: httpClient}, nil
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var result Version
	if err := c.doJSON(ctx, http.MethodGet, "/version", nil, &result); err != nil {
		return Version{}, err
	}
	return result, nil
}

func (c *Client) Selector(ctx context.Context, name string) (Selector, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Selector{}, errors.New("selector is required")
	}
	var result Selector
	if err := c.doJSON(ctx, http.MethodGet, "/proxies/"+url.PathEscape(name), nil, &result); err != nil {
		return Selector{}, err
	}
	return result, nil
}

// Proxies returns the complete snapshot from Mihomo's /proxies endpoint. It is
// used during route synchronization so node-level alive/history data is kept,
// instead of only returning the Selector metadata.
func (c *Client) Proxies(ctx context.Context) (map[string]Selector, error) {
	var response proxiesResponse
	if err := c.doJSON(ctx, http.MethodGet, "/proxies", nil, &response); err != nil {
		return nil, err
	}
	return response.Proxies, nil
}

func (c *Client) Selectors(ctx context.Context) ([]Selector, error) {
	proxies, err := c.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	selectors := make([]Selector, 0)
	for name, proxy := range proxies {
		if !strings.EqualFold(strings.TrimSpace(proxy.Type), "Selector") {
			continue
		}
		if strings.TrimSpace(proxy.Name) == "" {
			proxy.Name = name
		}
		selectors = append(selectors, proxy)
	}
	sort.Slice(selectors, func(i, j int) bool {
		return strings.ToLower(selectors[i].Name) < strings.ToLower(selectors[j].Name)
	})
	return selectors, nil
}

func (c *Client) Select(ctx context.Context, selector, node string) error {
	selector = strings.TrimSpace(selector)
	node = strings.TrimSpace(node)
	if selector == "" || node == "" {
		return errors.New("selector and node are required")
	}
	return c.doJSON(ctx, http.MethodPut, "/proxies/"+url.PathEscape(selector), map[string]string{"name": node}, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	endpoint := *c.baseURL
	escapedPath := strings.TrimRight(c.baseURL.EscapedPath(), "/") + path
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return fmt.Errorf("build Mihomo request path: %w", err)
	}
	endpoint.Path = decodedPath
	endpoint.RawPath = escapedPath
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode Mihomo request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create Mihomo request: %w", err)
	}
	req.Header.Set("accept", "application/json")
	if input != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.secret != "" {
		req.Header.Set("authorization", "Bearer "+c.secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("Mihomo controller closed the connection; check the IP, HTTP/HTTPS scheme, and Secret")
		}
		return fmt.Errorf("call Mihomo controller: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxResponseSize)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, limited)
		return fmt.Errorf("Mihomo controller returned HTTP %d", resp.StatusCode)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("decode Mihomo response: %w", err)
	}
	return nil
}
