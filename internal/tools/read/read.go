package read

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

const defaultLineLimit = 2000

var (
	ErrFilePathRequired = errors.New("file_path is required")
	ErrOffsetInvalid    = errors.New("offset must be zero or greater")
	ErrLimitInvalid     = errors.New("limit must be zero or greater")
)

type Request struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Pages    string `json:"pages,omitempty"`
}

type Result struct {
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	LinesRead  int    `json:"lines_read"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
	BytesRead  int    `json:"bytes_read"`
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type SafeExecutor struct {
	logger *slog.Logger
}

func NewSafeExecutor(logger *slog.Logger) *SafeExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &SafeExecutor{logger: logger}
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
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

	info, err := os.Stat(normalized.FilePath)
	if err != nil {
		return Result{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("read file: %s is a directory", normalized.FilePath)
	}

	content, err := os.ReadFile(normalized.FilePath)
	if err != nil {
		return Result{}, fmt.Errorf("read file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	formatted, linesRead, totalLines, truncated := formatContent(string(content), normalized.Offset, normalized.Limit)
	result := Result{
		FilePath:   normalized.FilePath,
		Content:    formatted,
		Offset:     normalized.Offset,
		Limit:      normalized.Limit,
		LinesRead:  linesRead,
		TotalLines: totalLines,
		Truncated:  truncated,
		BytesRead:  len(content),
	}

	e.logger.Info("file read",
		slog.String("file_path", result.FilePath),
		slog.Int("offset", result.Offset),
		slog.Int("limit", result.Limit),
		slog.Int("lines_read", result.LinesRead),
		slog.Int("total_lines", result.TotalLines),
		slog.Bool("truncated", result.Truncated),
		slog.Int("bytes_read", result.BytesRead),
	)

	return result, nil
}

func validateRequest(req Request) (Request, error) {
	req.FilePath = filepath.Clean(strings.TrimSpace(req.FilePath))
	if req.FilePath == "." {
		return Request{}, ErrFilePathRequired
	}
	filePath, err := shelltool.ResolvePath("", req.FilePath)
	if err != nil {
		return Request{}, err
	}
	req.FilePath = filePath
	if req.Offset < 0 {
		return Request{}, fmt.Errorf("%w: %d", ErrOffsetInvalid, req.Offset)
	}
	if req.Limit < 0 {
		return Request{}, fmt.Errorf("%w: %d", ErrLimitInvalid, req.Limit)
	}
	if req.Limit == 0 {
		req.Limit = defaultLineLimit
	}
	req.Pages = strings.TrimSpace(req.Pages)
	return req, nil
}

func formatContent(content string, offset, limit int) (string, int, int, bool) {
	lines := splitLines(content)
	totalLines := len(lines)
	if offset >= totalLines {
		return "", 0, totalLines, false
	}

	end := offset + limit
	if end > totalLines {
		end = totalLines
	}

	var builder strings.Builder
	for i, line := range lines[offset:end] {
		_, _ = fmt.Fprintf(&builder, "%6d\t", offset+i+1)
		builder.WriteString(line)
	}

	linesRead := end - offset
	return builder.String(), linesRead, totalLines, end < totalLines
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}

	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
