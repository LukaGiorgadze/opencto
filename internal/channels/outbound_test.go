package channels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestResolveReportAttachmentWithinWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "screenshots", "page.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	report, err := ResolveReport(domain.ReportMessage{
		Text: "done",
		Attachments: []domain.ReportAttachment{{
			Path:     "screenshots/page.png",
			Filename: "../Page Shot.png",
		}},
	}, ResolveOptions{
		WorkspaceRoot: root,
		Limits: AttachmentLimits{
			MaxFiles:      1,
			MaxFileBytes:  1024,
			MaxTotalBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("resolve report: %v", err)
	}

	attachment := report.Attachments[0]
	wantPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval path: %v", err)
	}
	if attachment.Path != wantPath {
		t.Fatalf("expected resolved path %q, got %q", wantPath, attachment.Path)
	}
	if attachment.Filename != "Page-Shot.png" {
		t.Fatalf("expected sanitized filename, got %q", attachment.Filename)
	}
	if !strings.HasPrefix(attachment.ContentType, "image/png") {
		t.Fatalf("expected image/png content type, got %q", attachment.ContentType)
	}
	if attachment.SizeBytes == 0 {
		t.Fatalf("expected size to be set")
	}
}

func TestResolveReportAllowsSymlinkOutsideWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	report, err := ResolveReport(domain.ReportMessage{
		Attachments: []domain.ReportAttachment{{Path: link}},
	}, ResolveOptions{
		WorkspaceRoot: root,
		Limits:        AttachmentLimits{MaxFiles: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024},
	})
	if err != nil {
		t.Fatalf("resolve report: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("eval outside path: %v", err)
	}
	if report.Attachments[0].Path != wantPath {
		t.Fatalf("expected symlink target %q, got %q", wantPath, report.Attachments[0].Path)
	}
}

func TestResolveReportAllowsAbsoluteAttachmentWithoutWorkspaceRoot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	report, err := ResolveReport(domain.ReportMessage{
		Attachments: []domain.ReportAttachment{{Path: path}},
	}, ResolveOptions{
		Limits: AttachmentLimits{MaxFiles: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024},
	})
	if err != nil {
		t.Fatalf("resolve report: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval path: %v", err)
	}
	if report.Attachments[0].Path != wantPath {
		t.Fatalf("expected absolute path %q, got %q", wantPath, report.Attachments[0].Path)
	}
}

func TestResolveReportRejectsAttachmentLimits(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	if err := os.WriteFile(path, []byte("large"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	_, err := ResolveReport(domain.ReportMessage{
		Attachments: []domain.ReportAttachment{{Path: path}},
	}, ResolveOptions{
		WorkspaceRoot: root,
		Limits:        AttachmentLimits{MaxFiles: 1, MaxFileBytes: 4, MaxTotalBytes: 1024},
	})
	if err == nil || !strings.Contains(err.Error(), "file limit") {
		t.Fatalf("expected file limit error, got %v", err)
	}
}
