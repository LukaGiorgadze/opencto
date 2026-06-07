package channels

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

type AttachmentLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type ResolveOptions struct {
	WorkspaceRoot string
	Limits        AttachmentLimits
}

func ResolveReport(report domain.ReportMessage, options ResolveOptions) (domain.ReportMessage, error) {
	report.Text = strings.TrimSpace(report.Text)
	if len(report.Attachments) == 0 {
		return report, nil
	}
	if options.Limits.MaxFiles <= 0 {
		return domain.ReportMessage{}, fmt.Errorf("attachment uploads are disabled")
	}
	if len(report.Attachments) > options.Limits.MaxFiles {
		return domain.ReportMessage{}, fmt.Errorf("report has %d attachment(s), limit is %d", len(report.Attachments), options.Limits.MaxFiles)
	}

	total := int64(0)
	attachments := make([]domain.ReportAttachment, 0, len(report.Attachments))
	for _, attachment := range report.Attachments {
		resolved, err := resolveAttachment(options.WorkspaceRoot, attachment, options.Limits)
		if err != nil {
			return domain.ReportMessage{}, err
		}
		total += resolved.SizeBytes
		if options.Limits.MaxTotalBytes > 0 && total > options.Limits.MaxTotalBytes {
			return domain.ReportMessage{}, fmt.Errorf("report attachments exceed %d byte total limit", options.Limits.MaxTotalBytes)
		}
		attachments = append(attachments, resolved)
	}
	report.Attachments = attachments
	return report, nil
}

func resolvedRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("workspace root is required for relative attachments")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return resolved, nil
}

func resolveAttachment(root string, attachment domain.ReportAttachment, limits AttachmentLimits) (domain.ReportAttachment, error) {
	path := strings.TrimSpace(attachment.Path)
	if path == "" {
		return domain.ReportAttachment{}, fmt.Errorf("attachment path is required")
	}
	if !filepath.IsAbs(path) {
		rootPath, err := resolvedRoot(root)
		if err != nil {
			return domain.ReportAttachment{}, err
		}
		root = rootPath
		path = filepath.Join(root, path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return domain.ReportAttachment{}, fmt.Errorf("resolve attachment %q: %w", attachment.Path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return domain.ReportAttachment{}, fmt.Errorf("stat attachment %q: %w", attachment.Path, err)
	}
	if info.IsDir() {
		return domain.ReportAttachment{}, fmt.Errorf("attachment %q is a directory", attachment.Path)
	}
	if limits.MaxFileBytes > 0 && info.Size() > limits.MaxFileBytes {
		return domain.ReportAttachment{}, fmt.Errorf("attachment %q exceeds %d byte file limit", attachment.Path, limits.MaxFileBytes)
	}

	contentType := strings.TrimSpace(attachment.ContentType)
	if contentType == "" {
		contentType, err = detectContentType(resolved)
		if err != nil {
			return domain.ReportAttachment{}, err
		}
	}
	attachment.Path = resolved
	attachment.Filename = safeFilename(firstNonEmpty(attachment.Filename, filepath.Base(resolved)))
	attachment.ContentType = contentType
	attachment.SizeBytes = info.Size()
	return attachment, nil
}

func detectContentType(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open attachment %q: %w", path, err)
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read attachment %q: %w", path, err)
	}
	return http.DetectContentType(buf[:n]), nil
}

func safeFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	value = strings.Trim(value, ".-")
	if value == "" {
		return "attachment"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
