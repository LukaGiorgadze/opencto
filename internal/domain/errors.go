package domain

import "errors"

var (
	ErrNotFound          = errors.New("not found")
	ErrApprovalRequired  = errors.New("approval required")
	ErrPolicyDenied      = errors.New("policy denied")
	ErrContradictionOpen = errors.New("pending contradiction blocks execution")
)
