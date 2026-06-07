package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const stateDirName = ".state"

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".opencto"), nil
}

func ResolveRoot(root string) (string, error) {
	return ResolveRootWithBase(root, "")
}

func ResolveRootWithBase(root, baseDir string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	return resolvePath(root, baseDir, "workspace root")
}

func ResolveStateDir(stateDir, workspaceRoot string) (string, error) {
	return ResolveStateDirWithBase(stateDir, workspaceRoot, "")
}

func ResolveStateDirWithBase(stateDir, workspaceRoot, baseDir string) (string, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir != "" {
		return resolvePath(stateDir, baseDir, "state dir")
	}

	root, err := ResolveRootWithBase(workspaceRoot, baseDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, stateDirName), nil
}

func resolvePath(path, baseDir, label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		path = filepath.Join(home, path[2:])
	} else {
		path = os.Expand(path, func(key string) string {
			switch key {
			case "HOME", "USERPROFILE":
				return home
			default:
				return os.Getenv(key)
			}
		})
	}
	if baseDir = strings.TrimSpace(baseDir); baseDir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return absPath, nil
}
