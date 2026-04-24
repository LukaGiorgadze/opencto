package vault

import (
	"context"

	"github.com/opencto/opencto/internal/domain"
)

type Store interface {
	PutSecret(context.Context, domain.CredentialRef, string) error
	GetSecret(context.Context, string, string, string) (string, error)
	DeleteSecret(context.Context, string, string, string) error
}
