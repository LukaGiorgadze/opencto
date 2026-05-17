package activities

import (
	"time"
)

const (
	NextActionStatusTool      = "tool"
	NextActionStatusCompleted = "completed"
	NextActionStatusBlocked   = "blocked"
	NextActionStatusFailed    = "failed"
	NextActionStatusIgnored   = "ignored"

	defaultResponseSessionRefresh   = 4 * time.Second
	defaultResponseSessionTimeout   = 3 * time.Second
	defaultResponseSessionMaxAge    = 30 * time.Minute
	defaultToolHeartbeatGap         = 2 * time.Second
	defaultExecGrace                = 2 * time.Minute
	defaultExecTailBytes            = 16 << 10
	memoryEmbeddingQueryMaxChars    = 1600
	memoryEmbeddingQueryMaxMessages = 6
)
