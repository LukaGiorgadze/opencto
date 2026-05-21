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

func TestEnsureOpenCTOBinaryCopiesCurrentExecutable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	openCTORoot := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := ensureOpenCTOBinary(root, openCTORoot, configPath); err != nil {
		t.Fatalf("ensure opencto binary: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, "bin", "opencto"))
	if err != nil {
		t.Fatalf("stat opencto binary: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected opencto binary to be a file")
	}
	if info.Size() == 0 {
		t.Fatal("expected opencto binary to be non-empty")
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected opencto binary to be executable, got mode %s", info.Mode().Perm())
	}
	marker, err := os.ReadFile(filepath.Join(root, "bin", installedOpenCTORootFilename))
	if err != nil {
		t.Fatalf("read OpenCTO root marker: %v", err)
	}
	if got := strings.TrimSpace(string(marker)); got != openCTORoot {
		t.Fatalf("expected OpenCTO root marker %q, got %q", openCTORoot, got)
	}
	marker, err = os.ReadFile(filepath.Join(root, "bin", installedOpenCTOConfigFilename))
	if err != nil {
		t.Fatalf("read OpenCTO config marker: %v", err)
	}
	if got := strings.TrimSpace(string(marker)); got != configPath {
		t.Fatalf("expected OpenCTO config marker %q, got %q", configPath, got)
	}
}
