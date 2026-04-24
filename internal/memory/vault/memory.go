package vault

import (
	"context"
	"fmt"
	"sync"

	"github.com/opencto/opencto/internal/domain"
)

type MemoryStore struct {
	mu      sync.RWMutex
	secrets map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		secrets: make(map[string]string),
	}
}

func (s *MemoryStore) PutSecret(_ context.Context, ref domain.CredentialRef, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[key(ref.ProjectID, ref.Provider, ref.Handle)] = value
	return nil
}

func (s *MemoryStore) GetSecret(_ context.Context, projectID, provider, handle string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.secrets[key(projectID, provider, handle)]
	if !ok {
		return "", domain.ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) DeleteSecret(_ context.Context, projectID, provider, handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, key(projectID, provider, handle))
	return nil
}

func key(projectID, provider, handle string) string {
	return fmt.Sprintf("%s:%s:%s", projectID, provider, handle)
}
