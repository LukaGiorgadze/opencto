package grep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/opencto/opencto/internal/domain"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

const (
	OutputModeContent          = "content"
	OutputModeFilesWithMatches = "files_with_matches"
	OutputModeCount            = "count"

	defaultOutputMode = OutputModeFilesWithMatches
	defaultHeadLimit  = 250
)

var (
	ErrPatternRequired   = errors.New("pattern is required")
	ErrInvalidOutputMode = errors.New("invalid output_mode")
	ErrInvalidLimit      = errors.New("grep limits must be non-negative")
)

type Request struct {
	ProjectID          string
	Intent             string
	Pattern            string `json:"pattern"`
	Path               string `json:"path,omitempty"`
	Glob               string `json:"glob,omitempty"`
	Type               string `json:"type,omitempty"`
	OutputMode         string `json:"output_mode,omitempty"`
	After              int    `json:"-A,omitempty"`
	Before             int    `json:"-B,omitempty"`
	ContextAlias       int    `json:"-C,omitempty"`
	Context            int    `json:"context,omitempty"`
	CaseInsensitive    bool   `json:"-i,omitempty"`
	LineNumbers        bool   `json:"-n,omitempty"`
	Multiline          bool   `json:"multiline,omitempty"`
	HeadLimit          int    `json:"head_limit,omitempty"`
	Offset             int    `json:"offset,omitempty"`
	WorkingDir         string
	Timeout            time.Duration
	Environment        map[string]string
	FallbackCandidates []domain.ToolType
	Actions            []Action `json:"actions,omitempty"`

	lineNumbersSet bool
	headLimitSet   bool
}

type Action struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Glob            string `json:"glob,omitempty"`
	Type            string `json:"type,omitempty"`
	OutputMode      string `json:"output_mode,omitempty"`
	After           int    `json:"-A,omitempty"`
	Before          int    `json:"-B,omitempty"`
	ContextAlias    int    `json:"-C,omitempty"`
	Context         int    `json:"context,omitempty"`
	CaseInsensitive bool   `json:"-i,omitempty"`
	LineNumbers     bool   `json:"-n,omitempty"`
	Multiline       bool   `json:"multiline,omitempty"`
	HeadLimit       int    `json:"head_limit,omitempty"`
	Offset          int    `json:"offset,omitempty"`

	lineNumbersSet bool
	headLimitSet   bool
}

type Result struct {
	Pattern          string
	Path             string
	Stdout           string
	Stderr           string
	ExitCode         int
	WorkingDirectory string
	StartedAt        time.Time
	CompletedAt      time.Time
	Duration         time.Duration
	Actions          []ActionResult
}

type ActionResult struct {
	Pattern  string
	Path     string
	Stdout   string
	Stderr   string
	ExitCode int
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

func NewTool() *SafeExecutor {
	return NewSafeExecutor(nil)
}

func Grep(req Request) (Result, error) {
	return NewSafeExecutor(nil).Run(context.Background(), req)
}

func Search(req Request) (Result, error) {
	return Grep(req)
}

func Execute(ctx context.Context, req Request) (Result, error) {
	return NewSafeExecutor(nil).Run(ctx, req)
}

func (e *SafeExecutor) Execute(ctx context.Context, req Request) (Result, error) {
	return e.Run(ctx, req)
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
	workingDir, err := shelltool.ResolveWorkingDir(req.WorkingDir)
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var stdout strings.Builder
	var stderr strings.Builder
	results := make([]ActionResult, 0, len(req.Actions))
	exitCode := 0

	for index, action := range req.Actions {
		result, err := e.runSingle(runCtx, requestForAction(req, action))
		appendActionOutput(&stdout, index, actionSummary(result.Pattern, result.Path), result.Stdout)
		appendActionOutput(&stderr, index, actionSummary(result.Pattern, result.Path), result.Stderr)
		exitCode = result.ExitCode
		results = append(results, ActionResult{
			Pattern:  result.Pattern,
			Path:     result.Path,
			Stdout:   result.Stdout,
			Stderr:   result.Stderr,
			ExitCode: result.ExitCode,
		})
		if err != nil {
			completedAt := time.Now()
			return Result{
				Stdout:           stdout.String(),
				Stderr:           stderr.String(),
				ExitCode:         exitCode,
				WorkingDirectory: workingDir,
				StartedAt:        startedAt,
				CompletedAt:      completedAt,
				Duration:         completedAt.Sub(startedAt),
				Actions:          results,
			}, fmt.Errorf("grep action %d: %w", index+1, err)
		}
	}

	completedAt := time.Now()
	return Result{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		ExitCode:         exitCode,
		WorkingDirectory: workingDir,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
		Actions:          results,
	}, nil
}

func (e *SafeExecutor) runSingle(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	normalized, err := normalizeRequest(req)
	if err != nil {
		return Result{}, err
	}

	startedAt := time.Now()

	workingDir, err := shelltool.ResolveWorkingDir(normalized.WorkingDir)
	if err != nil {
		return Result{}, err
	}
	searchPath, err := resolveSearchPath(workingDir, normalized.Path)
	if err != nil {
		return Result{}, err
	}
	normalized.Path = searchPath

	runCtx := ctx
	var cancel context.CancelFunc
	if normalized.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, normalized.Timeout)
		defer cancel()
	}

	args := ripgrepArgs(normalized)
	cmd := exec.CommandContext(runCtx, "rg", args...)
	cmd.Dir = workingDir
	cmd.Env = mergeEnv(normalized.Environment)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	completedAt := time.Now()
	code := exitCode(err)

	result := Result{
		Pattern:          normalized.Pattern,
		Path:             normalized.Path,
		Stdout:           limitOutput(stdout.String(), normalized.Offset, normalized.HeadLimit),
		Stderr:           stderr.String(),
		ExitCode:         code,
		WorkingDirectory: workingDir,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}

	e.logger.Info("grep executed",
		slog.String("project_id", normalized.ProjectID),
		slog.String("intent", normalized.Intent),
		slog.String("pattern", normalized.Pattern),
		slog.String("path", normalized.Path),
		slog.String("output_mode", normalized.OutputMode),
		slog.Any("args", args),
		slog.String("working_dir", workingDir),
		slog.Duration("duration", result.Duration),
		slog.Int("exit_code", result.ExitCode),
	)

	if err != nil && code != 1 {
		return result, err
	}

	return result, nil
}

func requestForAction(parent Request, action Action) Request {
	path := strings.TrimSpace(action.Path)
	if path == "" {
		path = parent.Path
	}
	return Request{
		ProjectID:          parent.ProjectID,
		Intent:             parent.Intent,
		Pattern:            action.Pattern,
		Path:               path,
		Glob:               action.Glob,
		Type:               action.Type,
		OutputMode:         action.OutputMode,
		After:              action.After,
		Before:             action.Before,
		ContextAlias:       action.ContextAlias,
		Context:            action.Context,
		CaseInsensitive:    action.CaseInsensitive,
		LineNumbers:        action.LineNumbers,
		Multiline:          action.Multiline,
		HeadLimit:          action.HeadLimit,
		Offset:             action.Offset,
		WorkingDir:         parent.WorkingDir,
		Timeout:            0,
		Environment:        parent.Environment,
		FallbackCandidates: append([]domain.ToolType(nil), parent.FallbackCandidates...),
		lineNumbersSet:     action.lineNumbersSet,
		headLimitSet:       action.headLimitSet,
	}
}

func (r *Request) UnmarshalJSON(data []byte) error {
	type alias Request
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if value, ok := raw["-n"]; ok && !isJSONNull(value) {
		decoded.lineNumbersSet = true
	}
	if value, ok := raw["head_limit"]; ok && !isJSONNull(value) {
		decoded.headLimitSet = true
	}
	*r = Request(decoded)
	return nil
}

func (a *Action) UnmarshalJSON(data []byte) error {
	type alias Action
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if value, ok := raw["-n"]; ok && !isJSONNull(value) {
		decoded.lineNumbersSet = true
	}
	if value, ok := raw["head_limit"]; ok && !isJSONNull(value) {
		decoded.headLimitSet = true
	}
	*a = Action(decoded)
	return nil
}

func isJSONNull(value json.RawMessage) bool {
	return strings.EqualFold(strings.TrimSpace(string(value)), "null")
}

func normalizeRequest(req Request) (Request, error) {
	if req.Pattern == "" {
		return Request{}, ErrPatternRequired
	}
	if req.After < 0 || req.Before < 0 || req.ContextAlias < 0 || req.Context < 0 || req.HeadLimit < 0 || req.Offset < 0 {
		return Request{}, ErrInvalidLimit
	}

	req.OutputMode = strings.TrimSpace(req.OutputMode)
	if req.OutputMode == "" {
		req.OutputMode = defaultOutputMode
	}
	switch req.OutputMode {
	case OutputModeContent, OutputModeFilesWithMatches, OutputModeCount:
	default:
		return Request{}, fmt.Errorf("%w: %s", ErrInvalidOutputMode, req.OutputMode)
	}

	req.Glob = strings.TrimSpace(req.Glob)
	req.Type = strings.TrimSpace(req.Type)
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		req.Path = "."
	}
	if req.ContextAlias > 0 && req.Context == 0 {
		req.Context = req.ContextAlias
	}
	if !req.lineNumbersSet {
		req.LineNumbers = true
	}
	if !req.headLimitSet && req.HeadLimit == 0 {
		req.HeadLimit = defaultHeadLimit
	}
	return req, nil
}

func ripgrepArgs(req Request) []string {
	args := []string{"--color", "never"}
	if req.CaseInsensitive {
		args = append(args, "-i")
	}
	if req.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if req.Glob != "" {
		args = append(args, "--glob", req.Glob)
	}
	if req.Type != "" {
		args = append(args, "--type", req.Type)
	}

	switch req.OutputMode {
	case OutputModeContent:
		args = append(args, "--with-filename")
		if req.LineNumbers {
			args = append(args, "--line-number")
		}
		if req.Context > 0 {
			args = append(args, "-C", strconv.Itoa(req.Context))
		}
		if req.Before > 0 {
			args = append(args, "-B", strconv.Itoa(req.Before))
		}
		if req.After > 0 {
			args = append(args, "-A", strconv.Itoa(req.After))
		}
	case OutputModeCount:
		args = append(args, "--count-matches")
	default:
		args = append(args, "--files-with-matches")
	}

	args = append(args, "--", req.Pattern, req.Path)
	return args
}

func resolveSearchPath(workingDir, searchPath string) (string, error) {
	path, err := shelltool.ResolvePath(workingDir, searchPath)
	if err != nil {
		return "", fmt.Errorf("resolve search path: %w", err)
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func appendActionOutput(builder *strings.Builder, index int, summary string, output string) {
	if strings.TrimSpace(output) == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	_, _ = fmt.Fprintf(builder, "action %d: %s\n", index+1, summary)
	builder.WriteString(output)
	if !strings.HasSuffix(output, "\n") {
		builder.WriteString("\n")
	}
}

func actionSummary(pattern, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return strings.TrimSpace(pattern)
	}
	return strings.TrimSpace(pattern) + " in " + path
}

func limitOutput(output string, offset int, headLimit int) string {
	lines := splitOutput(output)
	if offset >= len(lines) {
		return ""
	}
	lines = lines[offset:]
	if headLimit > 0 && headLimit < len(lines) {
		lines = lines[:headLimit]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func splitOutput(output string) []string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}

func mergeEnv(overrides map[string]string) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			current[parts[0]] = parts[1]
		}
	}
	for key, value := range overrides {
		current[key] = value
	}
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+current[key])
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}

	return 1
}
