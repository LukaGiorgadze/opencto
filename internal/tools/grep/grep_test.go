package grep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRequestUnmarshalTreatsNullableDefaultsAsUnset(t *testing.T) {
	t.Parallel()

	var req Request
	if err := json.Unmarshal([]byte(`{
		"pattern": "needle",
		"path": null,
		"glob": null,
		"type": null,
		"output_mode": null,
		"-A": null,
		"-B": null,
		"-C": null,
		"-i": null,
		"-n": null,
		"multiline": null,
		"context": null,
		"head_limit": null,
		"offset": null
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	normalized, err := normalizeRequest(req)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !normalized.LineNumbers {
		t.Fatalf("expected nullable -n to use the default true behavior")
	}
	if normalized.HeadLimit != defaultHeadLimit {
		t.Fatalf("expected nullable head_limit to use default %d, got %d", defaultHeadLimit, normalized.HeadLimit)
	}
}

func TestSecureWorkingDirDefaultsToOpenCTOInUserHome(t *testing.T) {
	t.Parallel()

	workingDir, err := secureWorkingDir("", "", false)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	want := filepath.Join(home, "opencto")
	if workingDir != want {
		t.Fatalf("expected %q, got %q", want, workingDir)
	}
}
