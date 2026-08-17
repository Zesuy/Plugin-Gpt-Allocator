package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientReadsAndSwitchesSelector(t *testing.T) {
	var selected string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/version":
			_ = json.NewEncoder(writer).Encode(Version{Version: "1.19.0"})
		case request.Method == http.MethodGet && request.URL.Path == "/proxies/🤖 CPA-01":
			_ = json.NewEncoder(writer).Encode(Selector{Name: "🤖 CPA-01", Type: "Selector", Now: "JP-01", All: []string{"JP-01", "SG-01"}})
		case request.Method == http.MethodGet && request.URL.Path == "/proxies":
			_ = json.NewEncoder(writer).Encode(map[string]any{"proxies": map[string]any{
				"DIRECT":   Selector{Name: "DIRECT", Type: "Direct"},
				"🤖 CPA-01": Selector{Name: "🤖 CPA-01", Type: "Selector", Now: "JP-01", All: []string{"JP-01", "SG-01"}},
			}})
		case request.Method == http.MethodPut && request.URL.Path == "/proxies/🤖 CPA-01":
			var input map[string]string
			_ = json.NewDecoder(request.Body).Decode(&input)
			selected = input["name"]
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.Version(context.Background())
	if err != nil || version.Version != "1.19.0" {
		t.Fatalf("version=%#v err=%v", version, err)
	}
	selector, err := client.Selector(context.Background(), "🤖 CPA-01")
	if err != nil || selector.Now != "JP-01" || len(selector.All) != 2 {
		t.Fatalf("selector=%#v err=%v", selector, err)
	}
	selectors, err := client.Selectors(context.Background())
	if err != nil || len(selectors) != 1 || selectors[0].Name != "🤖 CPA-01" {
		t.Fatalf("selectors=%#v err=%v", selectors, err)
	}
	if err := client.Select(context.Background(), "🤖 CPA-01", "SG-01"); err != nil {
		t.Fatal(err)
	}
	if selected != "SG-01" {
		t.Fatalf("selected = %q", selected)
	}
}

func TestNewRejectsUnsafeControllerURL(t *testing.T) {
	for _, value := range []string{"", "192.168.1.1:9090", "ftp://example.com", "http://user:pass@example.com"} {
		if _, err := New(value, "", nil); err == nil {
			t.Fatalf("New(%q) succeeded", value)
		}
	}
}
