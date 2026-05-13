package workflowbundle

import (
	"context"
	"strings"
	"testing"
)

func TestWriteBundleRejectsManifestFile(t *testing.T) {
	t.Parallel()

	err := WriteBundle(context.Background(), t.TempDir(), testManifest(), []File{{
		Path:    ManifestFilename,
		Content: "version: 1\n",
	}})
	if err == nil {
		t.Fatal("expected workflow.yml file write to be rejected")
	}
	if !strings.Contains(err.Error(), "under src") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateManifestRejectsNonPositiveRequiredDuration(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0s", "-1s"} {
		manifest := testManifest()
		manifest.Steps[0].StartToCloseTimeout = value
		if err := ValidateManifest(manifest); err == nil {
			t.Fatalf("expected start_to_close_timeout %q to be rejected", value)
		}
	}
}

func TestParseOptionalDurationRejectsNegativeDuration(t *testing.T) {
	t.Parallel()

	if _, err := ParseOptionalDuration("schedule_to_close_timeout", "-1s"); err == nil {
		t.Fatal("expected negative optional duration to be rejected")
	}
}

func testManifest() Manifest {
	return Manifest{
		Version: 1,
		Name:    "test workflow",
		Schedule: Schedule{
			Cron:          "0 9 * * *",
			OverlapPolicy: OverlapPolicySkip,
			CatchupWindow: "10m",
		},
		NotificationPolicy: NotificationPolicy{OnFailure: true},
		Steps: []Step{{
			ID:                  "step",
			Command:             "echo",
			Args:                []string{"ok"},
			StartToCloseTimeout: "1m",
		}},
	}
}
