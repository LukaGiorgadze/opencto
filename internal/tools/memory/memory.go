package memory

import "github.com/opencto/opencto/internal/domain"

const (
	ScopeProject = "project"
	ScopeGlobal  = "global"
	ScopeAll     = "all"
)

type RememberRequest struct {
	Content    string   `json:"content"`
	Scope      string   `json:"scope,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	Pinned     bool     `json:"pinned,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type RememberResult struct {
	Memory domain.Memory `json:"memory"`
}

type SearchRequest struct {
	Query string   `json:"query"`
	Scope string   `json:"scope,omitempty"`
	Limit int      `json:"limit,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

type SearchResult struct {
	Memories []domain.Memory `json:"memories"`
}

type ForgetRequest struct {
	MemoryID string `json:"memory_id"`
}

type ForgetResult struct {
	MemoryID string `json:"memory_id"`
	Deleted  bool   `json:"deleted"`
}
