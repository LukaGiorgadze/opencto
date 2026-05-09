package memory

import "github.com/opencto/opencto/internal/domain"

const (
	ScopeThread  = "thread"
	ScopeChannel = "channel"
	ScopeProject = "project"
	ScopeUser    = "user"
	ScopeGlobal  = "global"
	ScopeAll     = "all"
)

type ProposeAddRequest struct {
	Content    string   `json:"content"`
	Scope      string   `json:"scope,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	Pinned     bool     `json:"pinned,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

type ProposeAddResult struct {
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

type ListRequest struct {
	Scope string   `json:"scope,omitempty"`
	Kind  string   `json:"kind,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

type ListResult struct {
	Memories []domain.Memory `json:"memories"`
}

type ProposeUpdateRequest struct {
	MemoryID       string   `json:"memory_id"`
	Content        string   `json:"content,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	TagsMode       string   `json:"tags_mode,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	ConfidenceMode string   `json:"confidence_mode,omitempty"`
	Confidence     float64  `json:"confidence,omitempty"`
	PinnedMode     string   `json:"pinned_mode,omitempty"`
	Pinned         bool     `json:"pinned,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type ProposeUpdateResult struct {
	Memory  domain.Memory `json:"memory"`
	Updated bool          `json:"updated"`
}

type ProposeForgetRequest struct {
	MemoryIDs []string `json:"memory_ids,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Scope     string   `json:"scope,omitempty"`
}

type ProposeForgetResult struct {
	MemoryIDs         []string `json:"memory_ids,omitempty"`
	Deleted           bool     `json:"deleted"`
	DeletedCount      int      `json:"deleted_count"`
	DeletedMemoryIDs  []string `json:"deleted_memory_ids,omitempty"`
	NotFoundMemoryIDs []string `json:"not_found_memory_ids,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	Scope             string   `json:"scope,omitempty"`
}
