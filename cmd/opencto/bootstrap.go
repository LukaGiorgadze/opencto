package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
	sqlitestore "github.com/opencto/opencto/internal/storage/sqlite"
)

func runBootstrap(ctx context.Context, cfg config.Config, project domain.Project, logger *slog.Logger, ensureBundledSkills bool) error {
	if err := ensureWorkspaceDirs(cfg.General.WorkspaceRoot); err != nil {
		return err
	}
	if ensureBundledSkills {
		if _, err := ensureWorkspaceSkills(cfg.General.WorkspaceRoot); err != nil {
			return err
		}
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
