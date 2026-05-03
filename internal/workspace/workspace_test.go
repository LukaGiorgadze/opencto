package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRootDefaultsToOpenCTOInUserHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}

	root, err := ResolveRoot("")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}

	want := filepath.Join(home, ".opencto")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestResolveRootExpandsHomeReferences(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}

	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "~/opencto", want: filepath.Join(home, "opencto")},
		{input: "$HOME/.opencto", want: filepath.Join(home, ".opencto")},
		{input: "$USERPROFILE/.opencto", want: filepath.Join(home, ".opencto")},
	} {
		root, err := ResolveRoot(tc.input)
		if err != nil {
			t.Fatalf("resolve %q: %v", tc.input, err)
		}
		if root != tc.want {
			t.Fatalf("expected %q for %q, got %q", tc.want, tc.input, root)
		}
	}
}

func TestDefaultStateRootUsesOpenCTOWorkingDir(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}

	root, err := DefaultStateRoot()
	if err != nil {
		t.Fatalf("resolve default state root: %v", err)
	}

	want := filepath.Join(home, ".opencto", ".state")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestResolveStateDirDefaultsToOpenCTOState(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}

	stateDir, err := ResolveStateDir("", "project-1")
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}

	want := filepath.Join(home, ".opencto", ".state")
	if stateDir != want {
		t.Fatalf("expected %q, got %q", want, stateDir)
	}
}

func TestResolveStateDirExpandsConfiguredPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}

	stateDir, err := ResolveStateDir("$HOME/.opencto/.state/custom", "project-1")
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}

	want := filepath.Join(home, ".opencto", ".state", "custom")
	if stateDir != want {
		t.Fatalf("expected %q, got %q", want, stateDir)
	}
}
