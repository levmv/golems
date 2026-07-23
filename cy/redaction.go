package main

import (
	"strings"
	"sync"

	"github.com/levmv/golems/cy/internal/state"
)

type secretMasker struct {
	mu      sync.RWMutex
	secrets []string
}

func newSecretMasker(store *state.Store, extra ...string) *secretMasker {
	masker := &secretMasker{}
	for _, provider := range loginProviderCatalog() {
		masker.Add(providerEnvToken(provider))
	}
	if store != nil {
		if keys, err := store.APIKeys(); err == nil {
			for _, key := range keys {
				masker.Add(key)
			}
		}
	}
	for _, secret := range extra {
		masker.Add(secret)
	}
	return masker
}

func (m *secretMasker) Add(secret string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, known := range m.secrets {
		if known == secret {
			return
		}
	}
	m.secrets = append(m.secrets, secret)
}

func (m *secretMasker) Redact(text string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, secret := range m.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
