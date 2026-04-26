package domain

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrPolicyDenied = errors.New("policy denied")
)
