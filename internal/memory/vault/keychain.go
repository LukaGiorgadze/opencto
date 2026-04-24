package vault

import (
	"context"
	"errors"

	keyring "github.com/zalando/go-keyring"

	"github.com/opencto/opencto/internal/domain"
)

type KeychainStore struct {
	service string
}

func NewKeychainStore(service string) *KeychainStore {
	return &KeychainStore{service: service}
}

func (s *KeychainStore) PutSecret(ctx context.Context, ref domain.CredentialRef, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return keyring.Set(s.service, key(ref.ProjectID, ref.Provider, ref.Handle), value)
}

func (s *KeychainStore) GetSecret(ctx context.Context, projectID, provider, handle string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := keyring.Get(s.service, key(projectID, provider, handle))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", domain.ErrNotFound
	}
	return value, err
}

func (s *KeychainStore) DeleteSecret(ctx context.Context, projectID, provider, handle string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := keyring.Delete(s.service, key(projectID, provider, handle))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
