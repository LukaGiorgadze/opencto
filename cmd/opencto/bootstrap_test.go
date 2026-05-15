package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWorkspaceDirsCreatesReservedDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	if err := ensureWorkspaceDirs(root); err != nil {
		t.Fatalf("ensure workspace dirs: %v", err)
	}

	for _, name := range []string{".state", ".db", "workflows", "workflow-runs"} {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", path)
		}
	}
}

func TestEnsureWorkspaceDirsRequiresWorkspaceRoot(t *testing.T) {
	t.Parallel()

	err := ensureWorkspaceDirs(" ")
	if err == nil {
		t.Fatal("expected missing workspace root error")
	}
	if !strings.Contains(err.Error(), "workspace root is required") {
		t.Fatalf("expected workspace root error, got %v", err)
	}
}
