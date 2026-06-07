package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestImageResolverLocalImageSniffsContent(t *testing.T) {
	t.Parallel()

	testPNG := validPNG(t)
	path := writeMediaTestFile(t, "blob.bin", testPNG)
	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: 1024})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "not-really.txt",
		ContentType: "text/plain",
		LocalPath:   path,
	})

	if result.Status != ImageStatusNotImage {
		t.Fatalf("expected non-image when neither MIME nor name suggests image, got %#v", result)
	}

	result = resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.txt",
		ContentType: "image/jpeg",
		LocalPath:   path,
	})
	if result.Status != ImageStatusOK {
		t.Fatalf("expected image, got %#v", result)
	}
	if result.Image.ContentType != "image/png" {
		t.Fatalf("expected sniffed image/png, got %s", result.Image.ContentType)
	}
	if !result.Image.ContentTypeMismatch {
		t.Fatalf("expected MIME mismatch to be recorded")
	}
}

func TestImageResolverURLImageSniffsContent(t *testing.T) {
	t.Parallel()

	testPNG := validPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(server.Close)

	resolver := NewImageResolver(ImageResolverConfig{
		MaxBytes:            1024,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "remote.jpg",
		ContentType: "image/jpeg",
		URL:         server.URL + "/photo",
	})

	if result.Status != ImageStatusOK {
		t.Fatalf("expected image, got %#v", result)
	}
	if result.Image.ContentType != "image/png" {
		t.Fatalf("expected sniffed image/png, got %s", result.Image.ContentType)
	}
	if !result.Image.ContentTypeMismatch {
		t.Fatalf("expected declared/detected MIME mismatch")
	}
}

func TestImageResolverSkipsUnsupportedImage(t *testing.T) {
	t.Parallel()

	path := writeMediaTestFile(t, "diagram.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: 1024})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "diagram.svg",
		ContentType: "image/svg+xml",
		LocalPath:   path,
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "unsupported") {
		t.Fatalf("expected unsupported image skip, got %#v", result)
	}
}

func TestImageResolverSkipsOversizedLocalImage(t *testing.T) {
	t.Parallel()

	testPNG := validPNG(t)
	path := writeMediaTestFile(t, "photo.png", append(testPNG, []byte("large")...))
	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: int64(len(testPNG) - 1)})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.png",
		ContentType: "image/png",
		LocalPath:   path,
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "byte limit") {
		t.Fatalf("expected byte limit skip, got %#v", result)
	}
}

func TestImageResolverSkipsFailedDownload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	resolver := NewImageResolver(ImageResolverConfig{
		MaxBytes:            1024,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.png",
		ContentType: "image/png",
		URL:         server.URL + "/missing.png",
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "404") {
		t.Fatalf("expected failed download skip, got %#v", result)
	}
}

func TestImageResolverRejectsUnsafeURLByDefault(t *testing.T) {
	t.Parallel()

	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: 1024})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.png",
		ContentType: "image/png",
		URL:         "http://127.0.0.1/photo.png",
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "private or local") {
		t.Fatalf("expected unsafe URL skip, got %#v", result)
	}
}

func TestDefaultFetchClientDoesNotUseEnvironmentProxy(t *testing.T) {
	t.Parallel()

	client := defaultFetchClient(false)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected http transport, got %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("default image fetches should not use environment proxies")
	}
}

func TestImageResolverEnforcesRedirectLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again.png", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	resolver := NewImageResolver(ImageResolverConfig{
		MaxBytes:            1024,
		MaxRedirects:        1,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.png",
		ContentType: "image/png",
		URL:         server.URL + "/start.png",
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "too many redirects") {
		t.Fatalf("expected redirect skip, got %#v", result)
	}
}

func TestImageResolverIgnoresNonImageAttachment(t *testing.T) {
	t.Parallel()

	path := writeMediaTestFile(t, "voice.wav", []byte("audio"))
	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: 1})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "voice.wav",
		ContentType: "audio/wav",
		LocalPath:   path,
	})

	if result.Status != ImageStatusNotImage {
		t.Fatalf("expected non-image attachment, got %#v", result)
	}
}

func TestImageResolverRejectsTruncatedImage(t *testing.T) {
	t.Parallel()

	path := writeMediaTestFile(t, "photo.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	resolver := NewImageResolver(ImageResolverConfig{MaxBytes: 1024})
	result := resolver.Resolve(context.Background(), domain.EventAttachment{
		Filename:    "photo.png",
		ContentType: "image/png",
		LocalPath:   path,
	})

	if result.Status != ImageStatusSkipped || !strings.Contains(result.Reason, "unsupported or invalid") {
		t.Fatalf("expected invalid image skip, got %#v", result)
	}
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func writeMediaTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return path
}
