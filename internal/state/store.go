package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zesuy/cpa-route-allocator/internal/model"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Load() (model.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) Update(fn func(*model.State) error) (model.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.loadLocked()
	if err != nil {
		return model.State{}, err
	}
	if err := fn(&current); err != nil {
		return model.State{}, err
	}
	current.Version = model.CurrentStateVersion
	if err := s.saveLocked(current); err != nil {
		return model.State{}, err
	}
	return current, nil
}

func (s *Store) loadLocked() (model.State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return model.NewState(), nil
	}
	if err != nil {
		return model.State{}, fmt.Errorf("read allocator state: %w", err)
	}
	var result model.State
	if err := json.Unmarshal(data, &result); err != nil {
		return model.State{}, fmt.Errorf("decode allocator state: %w", err)
	}
	if result.Version != model.CurrentStateVersion {
		return model.State{}, fmt.Errorf("unsupported allocator state version %d", result.Version)
	}
	return result, nil
}

func (s *Store) saveLocked(value model.State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create allocator state directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode allocator state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".allocator-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create allocator state temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect allocator state temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write allocator state temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync allocator state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close allocator state temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace allocator state: %w", err)
	}
	return nil
}
