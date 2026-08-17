package importer

import "testing"

func TestAvailableAuthFileUsesFullEmailAndSuffix(t *testing.T) {
	reserved := map[string]struct{}{"alice@example.com.json": {}}
	got := AvailableAuthFile("Alice@Example.com", "codex", "identity", reserved)
	if got != "alice@example.com_1.json" {
		t.Fatalf("got %q", got)
	}
}
