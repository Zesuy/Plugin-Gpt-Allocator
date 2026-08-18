package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Zesuy/Plugin-Gpt-Allocator/internal/model"
)

func TestStoreRoundTripAndRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := New(path)
	got, err := store.Update(func(value *model.State) error {
		value.Settings.MihomoControllerURL = "http://127.0.0.1:9090"
		value.Settings.MihomoSecret = "secret"
		value.Groups = append(value.Groups, model.Group{Name: "primary"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Public().Settings.MihomoSecretSet {
		t.Fatal("public settings did not report configured secret")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("state file is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.MihomoSecret != "secret" || len(loaded.Groups) != 1 {
		t.Fatalf("unexpected loaded state: %#v", loaded)
	}
}
