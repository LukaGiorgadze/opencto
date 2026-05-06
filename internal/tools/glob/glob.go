package glob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	exectool "github.com/opencto/opencto/internal/tools/exec"
)

var (
	ErrPatternRequired = errors.New("pattern is required")
	ErrPathRequired    = errors.New("path must be omitted or a valid directory path")
)

type Request struct {
	ProjectID string   `json:"-"`
	Intent    string   `json:"-"`
	Cwd       string   `json:"cwd,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Path      string   `json:"path,omitempty"`
	Actions   []Action `json:"actions,omitempty"`
	Timeout   time.Duration
}

type Action struct {
	Pattern string `json:"pattern"`
	Cwd     string `json:"cwd,omitempty"`
	Path    string `json:"path,omitempty"`
}

type Result struct {
	Pattern     string
	Root        string
	Matches     []string
	Actions     []ActionResult
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

type ActionResult struct {
	Pattern string
	Root    string
	Matches []string
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
	if len(req.Actions) > 0 {
		return e.runBatch(ctx, req)
	}
	return e.runSingle(ctx, req)
}

func (e *SafeExecutor) runBatch(ctx context.Context, req Request) (Result, error) {
	startedAt := time.Now()
	results := make([]ActionResult, 0, len(req.Actions))
	var matches []string

	for index, action := range req.Actions {
		result, err := e.runSingle(ctx, requestForAction(req, action))
		results = append(results, ActionResult{
			Pattern: result.Pattern,
			Root:    result.Root,
			Matches: append([]string(nil), result.Matches...),
		})
		matches = append(matches, result.Matches...)
		if err != nil {
			completedAt := time.Now()
			return Result{
				Pattern:     "batch",
				Matches:     matches,
				Actions:     results,
				StartedAt:   startedAt,
				CompletedAt: completedAt,
				Duration:    completedAt.Sub(startedAt),
			}, fmt.Errorf("glob action %d: %w", index+1, err)
		}
	}

	completedAt := time.Now()
	return Result{
		Pattern:     "batch",
		Matches:     matches,
		Actions:     results,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}, nil
}

func (e *SafeExecutor) runSingle(ctx context.Context, req Request) (Result, error) {
	pattern, root, rootIsFile, err := validateRequest(req)
	if err != nil {
		return Result{}, err
	}

	startedAt := time.Now()
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	matcher, err := newMatcher(pattern)
	if err != nil {
		return Result{}, fmt.Errorf("compile glob pattern: %w", err)
	}

	var matches []match
	if rootIsFile {
		var info os.FileInfo
		info, err = os.Stat(root)
		if err == nil && matcher(filepath.ToSlash(filepath.Base(root))) {
			matches = append(matches, match{path: root, modTime: info.ModTime()})
		}
	} else {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := runCtx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !matcher(filepath.ToSlash(rel)) {
				return nil
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}
			matches = append(matches, match{
				path:    path,
				modTime: info.ModTime(),
			})
			return nil
		})
	}
	completedAt := time.Now()

	result := Result{
		Pattern:     pattern,
		Root:        root,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}
	if err != nil {
		e.logger.Info("glob executed",
			slog.String("project_id", req.ProjectID),
			slog.String("intent", req.Intent),
			slog.String("pattern", pattern),
			slog.String("path", root),
			slog.Duration("duration", result.Duration),
			slog.String("error", err.Error()),
		)
		return result, fmt.Errorf("walk files: %w", err)
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].modTime.Equal(matches[j].modTime) {
			return matches[i].path < matches[j].path
		}
		return matches[i].modTime.After(matches[j].modTime)
	})

	paths := make([]string, 0, len(matches))
	for _, item := range matches {
		paths = append(paths, item.path)
	}
	result.Matches = paths

	e.logger.Info("glob executed",
		slog.String("project_id", req.ProjectID),
		slog.String("intent", req.Intent),
		slog.String("pattern", pattern),
		slog.String("path", root),
		slog.Duration("duration", result.Duration),
		slog.Int("matches", len(result.Matches)),
	)

	return result, nil
}

func requestForAction(parent Request, action Action) Request {
	cwd := strings.TrimSpace(action.Cwd)
	if cwd == "" {
		cwd = parent.Cwd
	}
	path := strings.TrimSpace(action.Path)
	if path == "" {
		path = parent.Path
	}
	return Request{
		ProjectID: parent.ProjectID,
		Intent:    parent.Intent,
		Cwd:       cwd,
		Pattern:   action.Pattern,
		Path:      path,
		Timeout:   parent.Timeout,
	}
}

func validateRequest(req Request) (string, string, bool, error) {
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return "", "", false, ErrPatternRequired
	}

	workingDir := strings.TrimSpace(req.Cwd)
	if strings.EqualFold(workingDir, "undefined") || strings.EqualFold(workingDir, "null") {
		return "", "", false, fmt.Errorf("%w: cwd %q", ErrPathRequired, workingDir)
	}
	var err error
	workingDir, err = exectool.ResolveWorkingDir(workingDir)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve cwd: %w", err)
	}

	root := strings.TrimSpace(req.Path)
	if root == "" {
		root = workingDir
	} else if strings.EqualFold(root, "undefined") || strings.EqualFold(root, "null") {
		return "", "", false, fmt.Errorf("%w: %q", ErrPathRequired, root)
	} else {
		root, err = exectool.ResolvePath(workingDir, root)
		if err != nil {
			return "", "", false, fmt.Errorf("resolve path: %w", err)
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", "", false, fmt.Errorf("stat path: %w", err)
	}
	return filepath.ToSlash(pattern), root, !info.IsDir(), nil
}

func newMatcher(pattern string) (func(string) bool, error) {
	expr, err := globRegexp(pattern)
	if err != nil {
		return nil, err
	}
	return func(path string) bool {
		return expr.MatchString(path)
	}, nil
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					builder.WriteString("(?:.*/)?")
					i++
					continue
				}
				builder.WriteString(".*")
				continue
			}
			builder.WriteString("[^/]*")
			i++
		case '?':
			builder.WriteString("[^/]")
			i++
		case '[':
			next, err := appendCharacterClass(&builder, pattern, i)
			if err != nil {
				return nil, err
			}
			i = next
		case '/':
			builder.WriteByte('/')
			i++
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

func appendCharacterClass(builder *strings.Builder, pattern string, start int) (int, error) {
	end := start + 1
	if end < len(pattern) && pattern[end] == '!' {
		end++
	}
	if end < len(pattern) && pattern[end] == ']' {
		end++
	}
	for end < len(pattern) && pattern[end] != ']' {
		end++
	}
	if end >= len(pattern) {
		return 0, fmt.Errorf("unterminated character class")
	}

	class := pattern[start+1 : end]
	if strings.Contains(class, "/") {
		return 0, fmt.Errorf("character class cannot contain path separator")
	}
	if strings.HasPrefix(class, "!") {
		class = "^" + class[1:]
	}
	builder.WriteByte('[')
	builder.WriteString(class)
	builder.WriteByte(']')
	return end + 1, nil
}

type match struct {
	path    string
	modTime time.Time
}
