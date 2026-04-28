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

	want := filepath.Join(home, "opencto")
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

	for _, input := range []string{"~/opencto", "$HOME/opencto", "$USERPROFILE/opencto"} {
		root, err := ResolveRoot(input)
		if err != nil {
			t.Fatalf("resolve %q: %v", input, err)
		}
		want := filepath.Join(home, "opencto")
		if root != want {
			t.Fatalf("expected %q for %q, got %q", want, input, root)
		}
	}
}
