package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

const (
	DefaultMaxImageBytes              int64 = 10 << 20
	DefaultMaxTotalImageBytes         int64 = 12 << 20
	DefaultMaxImagesPerEvent                = 4
	DefaultMaxImageCandidatesPerEvent       = 8
	DefaultMaxImageURLFetchesPerEvent       = 4
	DefaultFetchTimeout                     = 30 * time.Second
	DefaultMaxRedirects                     = 3
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type ImageStatus string

const (
	ImageStatusOK       ImageStatus = "ok"
	ImageStatusSkipped  ImageStatus = "skipped"
	ImageStatusNotImage ImageStatus = "not_image"
)

type ImageResolverConfig struct {
	MaxBytes            int64
	FetchTimeout        time.Duration
	MaxRedirects        int
	HTTPClient          *http.Client
	AllowPrivateNetwork bool
}

type ImageResolver struct {
	maxBytes            int64
	fetchTimeout        time.Duration
	maxRedirects        int
	httpClient          *http.Client
	allowPrivateNetwork bool
}

type ImageResolution struct {
	Status              ImageStatus
	Image               ResolvedImage
	Reason              string
	DeclaredContentType string
	DetectedContentType string
}

type ResolvedImage struct {
	Data                []byte
	ContentType         string
	Source              string
	DeclaredContentType string
	DetectedContentType string
	ContentTypeMismatch bool
}

func DefaultImageResolverConfig() ImageResolverConfig {
	return ImageResolverConfig{
		MaxBytes:     DefaultMaxImageBytes,
		FetchTimeout: DefaultFetchTimeout,
		MaxRedirects: DefaultMaxRedirects,
	}
}

func NewImageResolver(config ImageResolverConfig) *ImageResolver {
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxImageBytes
	}
	if config.FetchTimeout <= 0 {
		config.FetchTimeout = DefaultFetchTimeout
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = DefaultMaxRedirects
	}
	return &ImageResolver{
		maxBytes:            config.MaxBytes,
		fetchTimeout:        config.FetchTimeout,
		maxRedirects:        config.MaxRedirects,
		httpClient:          config.HTTPClient,
		allowPrivateNetwork: config.AllowPrivateNetwork,
	}
}

func (r *ImageResolver) Resolve(ctx context.Context, attachment domain.EventAttachment) ImageResolution {
	if r == nil {
		r = NewImageResolver(DefaultImageResolverConfig())
	}
	if ctx == nil {
		ctx = context.Background()
	}

	declared := normalizedContentType(attachment.ContentType)
	localPath := strings.TrimSpace(attachment.LocalPath)
	if localPath != "" {
		if !localImageCandidate(attachment, declared) {
			return ImageResolution{Status: ImageStatusNotImage, DeclaredContentType: declared}
		}
		return r.resolveLocal(ctx, attachment, declared, filepath.Clean(localPath))
	}
	if shouldFetchAttachmentURL(attachment, declared) {
		return r.resolveURL(ctx, attachment, declared)
	}
	return ImageResolution{Status: ImageStatusNotImage, DeclaredContentType: declared}
}

func (r *ImageResolver) resolveLocal(ctx context.Context, attachment domain.EventAttachment, declared, path string) ImageResolution {
	data, reason, err := readLimitedFile(ctx, path, r.maxBytes)
	if err != nil {
		return skippedImage(declared, "", fmt.Sprintf("read local image: %v", err))
	}
	if reason != "" {
		return skippedImage(declared, "", reason)
	}
	return validatedImageResolution(attachment, declared, "local file", data, localImageCandidate(attachment, declared))
}

func (r *ImageResolver) resolveURL(ctx context.Context, attachment domain.EventAttachment, declared string) ImageResolution {
	if attachment.SizeBytes > 0 && attachment.SizeBytes > r.maxBytes {
		return skippedImage(declared, "", fmt.Sprintf("image exceeds %d byte limit", r.maxBytes))
	}
	fetchCtx, cancel := context.WithTimeout(ctx, r.fetchTimeout)
	defer cancel()

	data, responseType, err := r.fetchURL(fetchCtx, strings.TrimSpace(attachment.URL))
	if err != nil {
		return skippedImage(declared, "", fmt.Sprintf("download image: %v", err))
	}
	if declared == "" {
		declared = normalizedContentType(responseType)
	}
	return validatedImageResolution(attachment, declared, "url", data, true)
}

func (r *ImageResolver) fetchURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	if _, err := validateFetchURL(rawURL, r.allowPrivateNetwork); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "OpenCTO/1.0")

	var baseClient *http.Client
	if r.httpClient != nil {
		baseClient = r.httpClient
	} else {
		baseClient = defaultFetchClient(r.allowPrivateNetwork)
	}
	client := *baseClient
	client.Timeout = 0
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= r.maxRedirects {
			return fmt.Errorf("too many redirects")
		}
		if _, err := validateFetchURL(req.URL.String(), r.allowPrivateNetwork); err != nil {
			return err
		}
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("status %s", resp.Status)
	}
	if resp.ContentLength > r.maxBytes {
		return nil, "", fmt.Errorf("image exceeds %d byte limit", r.maxBytes)
	}
	data, err := readLimited(resp.Body, r.maxBytes)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func readLimitedFile(ctx context.Context, path string, maxBytes int64) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Sprintf("image exceeds %d byte limit", maxBytes), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	data, err := readLimited(file, maxBytes)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return data, "", nil
}

func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, io.LimitReader(reader, maxBytes+1)); err != nil {
		return nil, err
	}
	if int64(buffer.Len()) > maxBytes {
		return nil, fmt.Errorf("image exceeds %d byte limit", maxBytes)
	}
	return buffer.Bytes(), nil
}

func validatedImageResolution(attachment domain.EventAttachment, declared, source string, data []byte, reportUnsupported bool) ImageResolution {
	detected, err := detectSupportedImageContentType(data)
	if err != nil {
		if reportUnsupported {
			return skippedImage(declared, "", "unsupported or invalid image content")
		}
		return ImageResolution{Status: ImageStatusNotImage, DeclaredContentType: declared}
	}
	return ImageResolution{
		Status:              ImageStatusOK,
		DeclaredContentType: declared,
		DetectedContentType: detected,
		Image: ResolvedImage{
			Data:                append([]byte(nil), data...),
			ContentType:         detected,
			Source:              source,
			DeclaredContentType: declared,
			DetectedContentType: detected,
			ContentTypeMismatch: declared != "" && declared != detected,
		},
	}
}

func skippedImage(declared, detected, reason string) ImageResolution {
	return ImageResolution{
		Status:              ImageStatusSkipped,
		Reason:              strings.TrimSpace(reason),
		DeclaredContentType: declared,
		DetectedContentType: detected,
	}
}

func normalizedContentType(value string) string {
	value = strings.TrimSpace(strings.Split(value, ";")[0])
	return strings.ToLower(value)
}

func detectSupportedImageContentType(data []byte) (string, error) {
	sniffed := sniffSupportedImageContentType(data)
	if sniffed == "" {
		return "", fmt.Errorf("unsupported image content")
	}
	if sniffed == "image/webp" {
		if !validWebP(data) {
			return "", fmt.Errorf("invalid webp image")
		}
		return sniffed, nil
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	switch format {
	case "png":
		return "image/png", nil
	case "jpeg":
		return "image/jpeg", nil
	case "gif":
		return "image/gif", nil
	default:
		return "", fmt.Errorf("unsupported image format %q", format)
	}
}

func sniffSupportedImageContentType(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

func validWebP(data []byte) bool {
	if len(data) < 20 || !bytes.Equal(data[:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WEBP")) {
		return false
	}
	riffSize := int64(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize+8 > int64(len(data)) {
		return false
	}
	chunkSize := int64(binary.LittleEndian.Uint32(data[16:20]))
	if chunkSize < 0 || 20+chunkSize > int64(len(data)) {
		return false
	}
	switch {
	case bytes.Equal(data[12:16], []byte("VP8 ")):
		return chunkSize >= 10
	case bytes.Equal(data[12:16], []byte("VP8L")):
		return chunkSize >= 5
	case bytes.Equal(data[12:16], []byte("VP8X")):
		return chunkSize >= 10
	default:
		return false
	}
}

func shouldFetchAttachmentURL(attachment domain.EventAttachment, declared string) bool {
	if strings.TrimSpace(attachment.URL) == "" {
		return false
	}
	if declared == "" || declared == "application/octet-stream" {
		return true
	}
	if strings.HasPrefix(declared, "image/") {
		return true
	}
	return imageLikeName(attachment.Filename) || imageLikeURL(attachment.URL)
}

func localImageCandidate(attachment domain.EventAttachment, declared string) bool {
	if declared == "" || declared == "application/octet-stream" {
		return true
	}
	if strings.HasPrefix(declared, "image/") {
		return true
	}
	return imageLikeName(attachment.Filename) || imageLikeName(attachment.LocalPath)
}

func MaybeImageAttachment(attachment domain.EventAttachment) bool {
	declared := normalizedContentType(attachment.ContentType)
	if declared == "" || declared == "application/octet-stream" || strings.HasPrefix(declared, "image/") {
		return true
	}
	return imageLikeName(attachment.Filename) || imageLikeName(attachment.LocalPath) || imageLikeURL(attachment.URL)
}

func imageLikeName(value string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(value))) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".heic", ".heif", ".avif":
		return true
	default:
		return false
	}
}

func imageLikeURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return imageLikeName(parsed.Path)
}

func validateFetchURL(rawURL string, allowPrivateNetwork bool) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("url userinfo is not allowed")
	}
	host := parsed.Hostname()
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("url host is required")
	}
	if !allowPrivateNetwork && unsafeHost(host) {
		return nil, fmt.Errorf("private or local hosts are not allowed")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("invalid url port")
		}
	}
	return parsed, nil
}

func defaultFetchClient(allowPrivateNetwork bool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           safeDialContext(allowPrivateNetwork),
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func safeDialContext(allowPrivateNetwork bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, address := range addresses {
			if !allowPrivateNetwork && unsafeIP(address.IP) {
				lastErr = fmt.Errorf("private or local hosts are not allowed")
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no resolved addresses")
	}
}

func unsafeHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return unsafeIP(ip)
}

func unsafeIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

func ImageURLsFromText(text string, max int) []string {
	if max <= 0 {
		return nil
	}
	matches := urlPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	urls := make([]string, 0, min(max, len(matches)))
	for _, match := range matches {
		candidate := strings.TrimRight(match, ".,;:!?)]}")
		if _, err := validateFetchURL(candidate, true); err != nil {
			continue
		}
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		urls = append(urls, candidate)
		if len(urls) == max {
			break
		}
	}
	return urls
}
