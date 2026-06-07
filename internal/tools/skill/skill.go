package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/opencto/opencto/internal/skills"
)

var ErrSkillIDRequired = errors.New("skill_id is required")

type Request struct {
	SkillID string `json:"skill_id"`
}

type Result struct {
	SkillID     string `json:"skill_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	BytesRead   int    `json:"bytes_read"`
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type SafeExecutor struct {
	Roots []string
}

func NewSafeExecutor(roots ...string) *SafeExecutor {
	return &SafeExecutor{Roots: roots}
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	id := strings.TrimSpace(req.SkillID)
	if id == "" {
		return Result{}, ErrSkillIDRequired
	}
	skill, err := skills.LoadFromRoots(ctx, id, e.Roots...)
	if err != nil {
		return Result{}, fmt.Errorf("load skill: %w", err)
	}
	return Result{
		SkillID:     skill.ID,
		Name:        skill.Name,
		Description: skill.Description,
		Path:        skill.Path,
		Content:     skill.Content,
		BytesRead:   len(skill.Content),
	}, nil
}
