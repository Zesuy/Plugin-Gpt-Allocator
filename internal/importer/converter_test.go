package importer

import (
	"encoding/json"
	"testing"
)

func TestParseSub2APIAccounts(t *testing.T) {
	raw := json.RawMessage(`{
  "accounts": [
    {
      "name": "example",
      "platform": "openai",
      "type": "oauth",
      "priority": 1,
      "credentials": {
        "access_token": "header.payload.signature",
        "refresh_token": "refresh",
        "email": "alice@example.com",
        "account_id": "account-1",
        "workspace_id": "workspace-1"
      }
    }
  ]
}`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d credentials, want 1", len(got))
	}
	if got[0].Provider != "codex" || got[0].Email != "alice@example.com" {
		t.Fatalf("unexpected credential: %#v", got[0])
	}
	if got[0].WorkspaceID != "workspace-1" {
		t.Fatalf("workspace = %q", got[0].WorkspaceID)
	}
	if got[0].Auth["refresh_token"] != "refresh" {
		t.Fatalf("auth = %#v", got[0].Auth)
	}
}

func TestParseKeepsSameEmailDifferentWorkspace(t *testing.T) {
	raw := json.RawMessage(`[
  {"access_token":"one","email":"alice@example.com","account_id":"account","workspace_id":"one"},
  {"access_token":"two","email":"alice@example.com","account_id":"account","workspace_id":"two"}
]`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d credentials, want 2", len(got))
	}
	if got[0].Identity == got[1].Identity {
		t.Fatal("different workspaces produced the same identity")
	}
}

func TestParseDeduplicatesSameSecret(t *testing.T) {
	raw := json.RawMessage(`[
  {"access_token":"one","email":"alice@example.com"},
  {"access_token":"one","email":"alice@example.com"}
]`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d credentials, want 1", len(got))
	}
}
