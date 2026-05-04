package write

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

var (
	ErrFilePathRequired = errors.New("file_path is required")
	ErrFileNotRead      = errors.New("file must be read before rewriting")
)

type Request struct {
	ProjectID string
	Intent    string
	FilePath  string `json:"file_path"`
	Content   string `json:"content"`
}

type Result struct {
	FilePath     string `json:"file_path"`
	BytesWritten int    `json:"bytes_written"`
	Overwritten  bool   `json:"overwritten"`
	StartedAt    time.Time
	CompletedAt  time.Time
	Duration     time.Duration
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type ReadTracker interface {
	HasRead(filePath string) bool
}

type SafeExecutor struct {
	logger      *slog.Logger
	readTracker ReadTracker
}

func NewSafeExecutor(logger *slog.Logger) *SafeExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &SafeExecutor{logger: logger}
}

func NewReadAwareSafeExecutor(logger *slog.Logger, readTracker ReadTracker) *SafeExecutor {
	executor := NewSafeExecutor(logger)
	executor.readTracker = readTracker
	return executor
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	startedAt := time.Now()

	filePath, err := resolveFilePath(req.FilePath)
	if err != nil {
		return Result{}, err
	}

	overwritten, err := fileExists(filePath)
	if err != nil {
		return Result{}, err
	}
	if overwritten && e.readTracker != nil && !e.readTracker.HasRead(filePath) {
		return Result{}, fmt.Errorf("%w: %s", ErrFileNotRead, filePath)
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return Result{}, fmt.Errorf("create parent directories: %w", err)
	}
	if err := os.WriteFile(filePath, []byte(req.Content), 0o644); err != nil {
		return Result{}, fmt.Errorf("write file: %w", err)
	}

	completedAt := time.Now()
	result := Result{
		FilePath:     filePath,
		BytesWritten: len(req.Content),
		Overwritten:  overwritten,
		StartedAt:    startedAt,
		CompletedAt:  completedAt,
		Duration:     completedAt.Sub(startedAt),
	}

	e.logger.Info("file written",
		slog.String("project_id", req.ProjectID),
		slog.String("intent", req.Intent),
		slog.String("file_path", filePath),
		slog.Int("bytes_written", result.BytesWritten),
		slog.Bool("overwritten", result.Overwritten),
		slog.Duration("duration", result.Duration),
	)

	return result, nil
}

func resolveFilePath(filePath string) (string, error) {
	path := filepath.Clean(strings.TrimSpace(filePath))
	if path == "." {
		return "", ErrFilePathRequired
	}
	resolvedPath, err := shelltool.ResolvePath("", path)
	if err != nil {
		return "", err
	}
	return resolvedPath, nil
}

func fileExists(filePath string) (bool, error) {
	info, err := os.Stat(filePath)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("write file: %s is a directory", filePath)
		}
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat file: %w", err)
}
