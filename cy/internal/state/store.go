package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/levmv/golems/cy/internal/session"
)

type Config struct {
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	RecentModels    []string       `json:"recent_models,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	ModelContexts   map[string]int `json:"model_contexts,omitempty"`
}

const maxRecentModels = 20

type authData struct {
	APIKeys map[string]string `json:"api_keys,omitempty"`
}

type Store struct {
	mu         sync.Mutex
	dir        string
	configPath string
	authPath   string
}

func Open(home string) (*Store, error) {
	dir, err := session.ResolveHome(home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create Cy home: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("set Cy home mode: %w", err)
	}
	store := &Store{
		dir:        dir,
		configPath: filepath.Join(dir, "config.json"),
		authPath:   filepath.Join(dir, "auth.json"),
	}
	if err := inspectStoreFile(store.configPath, "config"); err != nil {
		return nil, err
	}
	if err := inspectStoreFile(store.authPath, "auth"); err != nil {
		return nil, err
	}
	if _, err := store.Config(); err != nil {
		return nil, err
	}
	if _, err := store.APIKeys(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Config() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadConfig()
}

func (s *Store) APIKeys() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth, err := s.loadAuth()
	if err != nil {
		return nil, err
	}
	return auth.APIKeys, nil
}

func (s *Store) APIKey(provider string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth, err := s.loadAuth()
	if err != nil {
		return "", false, err
	}
	key, ok := auth.APIKeys[normalizeProvider(provider)]
	return key, ok && key != "", nil
}

func (s *Store) SetAPIKey(provider, key string) error {
	provider = normalizeProvider(provider)
	key = strings.TrimSpace(key)
	if provider == "" || key == "" {
		return errors.New("provider and API key are required")
	}
	return s.updateAuth(func(auth *authData) {
		auth.APIKeys[provider] = key
	})
}

func (s *Store) DeleteAPIKey(provider string) error {
	return s.updateAuth(func(auth *authData) {
		delete(auth.APIKeys, normalizeProvider(provider))
	})
}

func (s *Store) SetDefaultModelSelection(model, reasoningEffort string) error {
	model = strings.TrimSpace(model)
	return s.updateConfig(func(config *Config) {
		previous := config.Model
		config.Model = model
		config.ReasoningEffort = strings.ToLower(strings.TrimSpace(reasoningEffort))
		config.RecentModels = recentModels(config.RecentModels, previous, model)
	})
}

func (s *Store) SetDefaultProfile(profile string) error {
	return s.updateConfig(func(config *Config) { config.Profile = strings.TrimSpace(profile) })
}

func (s *Store) ModelContext(uri string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfig()
	if err != nil {
		return 0, false, err
	}
	window, ok := config.ModelContexts[normalizeModelURI(uri)]
	return window, ok && window > 0, nil
}

func (s *Store) SetModelContext(uri string, window int) error {
	uri = normalizeModelURI(uri)
	if uri == "" || window <= 0 {
		return errors.New("model URI and positive context window are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfig()
	if err != nil {
		return err
	}
	if config.ModelContexts[uri] == window {
		return nil
	}
	if config.ModelContexts == nil {
		config.ModelContexts = make(map[string]int)
	}
	config.ModelContexts[uri] = window
	return s.saveJSON(s.configPath, "config", config)
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) updateConfig(change func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.loadConfig()
	if err != nil {
		return err
	}
	change(&config)
	return s.saveJSON(s.configPath, "config", config)
}

func (s *Store) updateAuth(change func(*authData)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth, err := s.loadAuth()
	if err != nil {
		return err
	}
	change(&auth)
	return s.saveJSON(s.authPath, "auth", auth)
}

func (s *Store) loadConfig() (Config, error) {
	var config Config
	if err := loadJSON(s.configPath, "config", &config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s *Store) loadAuth() (authData, error) {
	auth := authData{APIKeys: make(map[string]string)}
	if err := loadJSON(s.authPath, "auth", &auth); err != nil {
		return authData{}, err
	}
	if auth.APIKeys == nil {
		auth.APIKeys = make(map[string]string)
	}
	return auth, nil
}

func loadJSON(path, label string, target any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	return nil
}

func (s *Store) saveJSON(path, label string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	file, err := os.CreateTemp(s.dir, "."+label+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create %s: %w", label, err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set temporary %s mode: %w", label, err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", label, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", label, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", label, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish %s: %w", label, err)
	}
	return nil
}

func inspectStoreFile(path, label string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s store must be a regular file", label)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set %s mode: %w", label, err)
	}
	return nil
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func normalizeModelURI(uri string) string {
	return strings.ToLower(strings.TrimSpace(uri))
}

func recentModels(models []string, previous, current string) []string {
	recent := make([]string, 0, min(maxRecentModels, len(models)+1))
	seen := make(map[string]struct{}, cap(recent)+1)
	if current = normalizeModelURI(current); current != "" {
		seen[current] = struct{}{}
	}
	add := func(model string) {
		model = strings.TrimSpace(model)
		key := normalizeModelURI(model)
		if key == "" || len(recent) == maxRecentModels {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		recent = append(recent, model)
	}
	add(previous)
	for _, model := range models {
		add(model)
	}
	return recent
}
