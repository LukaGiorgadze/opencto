package edit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	exectool "github.com/opencto/opencto/internal/tools/exec"
)

var (
	ErrFilePathRequired   = errors.New("file_path is required")
	ErrOldStringRequired  = errors.New("old_string is required")
	ErrStringsMatch       = errors.New("new_string must be different from old_string")
	ErrOldStringNotFound  = errors.New("old_string was not found")
	ErrOldStringNotUnique = errors.New("old_string is not unique")
	ErrFileNotRead        = errors.New("file must be read before editing")
)

type Request struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type Result struct {
	FilePath     string `json:"file_path"`
	Replacements int    `json:"replacements"`
	BytesWritten int    `json:"bytes_written"`
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type ReadTracker interface {
	HasRead(filePath string) bool
}

type Tool struct {
	readTracker ReadTracker
}

func NewTool() *Tool {
	return &Tool{}
}

func NewReadAwareTool(readTracker ReadTracker) *Tool {
	return &Tool{readTracker: readTracker}
}

func Edit(req Request) (Result, error) {
	return NewTool().Run(context.Background(), req)
}

func Apply(req Request) (Result, error) {
	return Edit(req)
}

func Execute(ctx context.Context, req Request) (Result, error) {
	return NewTool().Run(ctx, req)
}

func (t *Tool) Execute(ctx context.Context, req Request) (Result, error) {
	return t.Run(ctx, req)
}

func (t *Tool) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	normalized, err := validateRequest(req)
	if err != nil {
		return Result{}, err
	}
	if t != nil && t.readTracker != nil && !t.readTracker.HasRead(normalized.FilePath) {
		return Result{}, fmt.Errorf("%w: %s", ErrFileNotRead, normalized.FilePath)
	}

	info, err := os.Stat(normalized.FilePath)
	if err != nil {
		return Result{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("edit file: %s is a directory", normalized.FilePath)
	}

	content, err := os.ReadFile(normalized.FilePath)
	if err != nil {
		return Result{}, fmt.Errorf("read file: %w", err)
	}
	current := string(content)
	replacements := strings.Count(current, normalized.OldString)
	if replacements == 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrOldStringNotFound, normalized.FilePath)
	}
	if !normalized.ReplaceAll && replacements > 1 {
		return Result{}, fmt.Errorf("%w: found %d occurrences", ErrOldStringNotUnique, replacements)
	}
	if !normalized.ReplaceAll {
		replacements = 1
	}

	next := strings.Replace(current, normalized.OldString, normalized.NewString, replacements)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(normalized.FilePath, []byte(next), info.Mode().Perm()); err != nil {
		return Result{}, fmt.Errorf("write file: %w", err)
	}

	return Result{
		FilePath:     normalized.FilePath,
		Replacements: replacements,
		BytesWritten: len(next),
	}, nil
}

func validateRequest(req Request) (Request, error) {
	req.FilePath = filepath.Clean(strings.TrimSpace(req.FilePath))
	if req.FilePath == "." {
		return Request{}, ErrFilePathRequired
	}
	filePath, err := exectool.ResolvePath("", req.FilePath)
	if err != nil {
		return Request{}, err
	}
	req.FilePath = filePath
	if req.OldString == "" {
		return Request{}, ErrOldStringRequired
	}
	if req.OldString == req.NewString {
		return Request{}, ErrStringsMatch
	}
	return req, nil
}
