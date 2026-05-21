package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
	sqlitestore "github.com/opencto/opencto/internal/storage/sqlite"
)

const (
	installedOpenCTORootFilename   = "opencto.root"
	installedOpenCTOConfigFilename = "opencto.config"
)

func runBootstrap(ctx context.Context, cfg config.Config, project domain.Project, openCTORoot, configPath string, logger *slog.Logger) error {
	if err := ensureWorkspaceDirs(cfg.General.WorkspaceRoot); err != nil {
		return err
	}
	if err := ensureOpenCTOBinary(cfg.General.WorkspaceRoot, openCTORoot, configPath); err != nil {
		return err
	}

	store, dbPath, err := openRuntimeStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	return bootstrapRuntimeStore(ctx, store, dbPath, project, logger)
}

func ensureWorkspaceDirs(workspaceRoot string) error {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("workspace root is required")
	}

	for _, name := range []string{".state", ".db", "workflows", "workflow-runs"} {
		path := filepath.Join(workspaceRoot, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create workspace directory %s: %w", path, err)
		}
	}
	return nil
}

func ensureOpenCTOBinary(workspaceRoot, openCTORoot, configPath string) error {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("workspace root is required")
	}
	openCTORoot = strings.TrimSpace(openCTORoot)
	if openCTORoot == "" {
		return fmt.Errorf("OpenCTO root is required")
	}
	binDir := filepath.Join(workspaceRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create workspace bin directory %s: %w", binDir, err)
	}
	if err := writeInstalledPathMarker(binDir, installedOpenCTORootFilename, openCTORoot, "OpenCTO root"); err != nil {
		return err
	}
	if err := writeInstalledPathMarker(binDir, installedOpenCTOConfigFilename, configPath, "OpenCTO config"); err != nil {
		return err
	}
	target := filepath.Join(binDir, "opencto")
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve OpenCTO executable: %w", err)
	}
	if sameFile(source, target) {
		return nil
	}
	if err := copyExecutable(source, target); err != nil {
		return fmt.Errorf("install OpenCTO CLI: %w", err)
	}
	return nil
}

func writeInstalledPathMarker(binDir, filename, value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	target := filepath.Join(binDir, filename)
	if err := os.WriteFile(target, []byte(path+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s marker %s: %w", label, target, err)
	}
	return nil
}

func sameFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func copyExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source executable %s: %w", source, err)
	}
	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("stat source executable %s: %w", source, err)
	}
	if info.IsDir() {
		return fmt.Errorf("source executable %s is a directory", source)
	}

	tmp := target + ".tmp"
	output, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create target executable %s: %w", tmp, err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy executable: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close target executable %s: %w", tmp, closeErr)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("make target executable %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace target executable %s: %w", target, err)
	}
	return nil
}

func openRuntimeStore(ctx context.Context, cfg config.Config) (*sqlitestore.Store, string, error) {
	dbPath := storage.DefaultDBPath(cfg.General.WorkspaceRoot)
	store, err := sqlitestore.Open(ctx, dbPath)
	if err != nil {
		return nil, dbPath, fmt.Errorf("open sqlite store: %w", err)
	}
	return store, dbPath, nil
}

func bootstrapRuntimeStore(ctx context.Context, store *sqlitestore.Store, dbPath string, project domain.Project, logger *slog.Logger) error {
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate sqlite store: %w", err)
	}
	if err := store.EnsureProject(ctx, project); err != nil {
		return fmt.Errorf("ensure default project: %w", err)
	}
	if logger != nil {
		logger.Info("sqlite store ready", slog.String("path", dbPath), slog.String("project_id", project.ID))
	}
	return nil
}
