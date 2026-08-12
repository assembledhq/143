package codereview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/assembledhq/143/internal/models"
)

const (
	visualEvidenceMaxImageBytes  = 10 << 20
	visualEvidenceMaxPixels      = 40_000_000
	visualEvidenceMaxRedirects   = 3
	visualEvidenceFetchAttempts  = 3
	visualEvidenceFetchTimeout   = 15 * time.Second
	visualEvidenceRetryBaseDelay = 200 * time.Millisecond
)

var visualEvidenceBlockedNetworks = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "100.64.0.0/10", "0.0.0.0/8", "::1/128",
		"fe80::/10", "fc00::/7", "ff00::/8",
	}
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("code review visual evidence: invalid blocked CIDR %q: %v", cidr, err))
		}
		networks = append(networks, network)
	}
	return networks
}()

type visualEvidenceDownloader struct {
	client    *http.Client
	retryWait func(context.Context, time.Duration) error
}

type visualEvidenceFetchResult struct {
	data          []byte
	byteSize      int64
	contentSHA256 string
	contentType   string
	width         int
	height        int
	status        models.CodeReviewVisualEvidenceFetchStatus
	failureReason string
	hostClass     string
	duration      time.Duration
}

type visualEvidenceAttemptResult struct {
	data          []byte
	status        models.CodeReviewVisualEvidenceFetchStatus
	failureReason string
	retryable     bool
}

func newVisualEvidenceDownloader() *visualEvidenceDownloader {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: visualEvidenceSafeDialControl,
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true, MaxIdleConns: visualEvidenceConcurrency,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}
	return &visualEvidenceDownloader{
		client: &http.Client{
			Timeout: visualEvidenceFetchTimeout, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		retryWait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

func (d *visualEvidenceDownloader) fetch(ctx context.Context, rawURL, token string) visualEvidenceFetchResult {
	startedAt := time.Now()
	hostClass := visualEvidenceHostClass(rawURL)
	fetchCtx, cancel := context.WithTimeout(ctx, visualEvidenceFetchTimeout)
	defer cancel()

	var attempt visualEvidenceAttemptResult
	for attemptNumber := 1; attemptNumber <= visualEvidenceFetchAttempts; attemptNumber++ {
		attempt = d.fetchAttempt(fetchCtx, rawURL, token)
		if !attempt.retryable || attemptNumber == visualEvidenceFetchAttempts {
			break
		}
		delay := visualEvidenceRetryBaseDelay << (attemptNumber - 1)
		if err := d.retryWait(fetchCtx, delay); err != nil {
			attempt = visualEvidenceAttemptResult{
				status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download was cancelled",
			}
			break
		}
	}
	result := visualEvidenceFetchResult{
		data: attempt.data, status: attempt.status, failureReason: attempt.failureReason,
		hostClass: hostClass, duration: time.Since(startedAt),
	}
	if result.status != models.CodeReviewVisualEvidenceFetchStatusAvailable {
		return result
	}
	result.byteSize = int64(len(result.data))
	result.contentSHA256 = sha256Bytes(result.data)
	contentType, width, height, err := inspectVisualEvidenceImage(result.data)
	if err != nil {
		result.data = nil
		result.status = models.CodeReviewVisualEvidenceFetchStatusUnsupported
		result.failureReason = "image format is unsupported or malformed"
		return result
	}
	if int64(width)*int64(height) > visualEvidenceMaxPixels {
		result.data = nil
		result.status = models.CodeReviewVisualEvidenceFetchStatusOverLimit
		result.failureReason = "image exceeds the 40 megapixel limit"
		result.contentType, result.width, result.height = contentType, width, height
		return result
	}
	result.contentType = contentType
	result.width = width
	result.height = height
	return result
}

func (d *visualEvidenceDownloader) fetchAttempt(ctx context.Context, rawURL, token string) visualEvidenceAttemptResult {
	currentURL := strings.TrimSpace(rawURL)
	for redirect := 0; ; redirect++ {
		parsed, err := validateVisualEvidenceURL(currentURL)
		if err != nil {
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnsupported, failureReason: "image URL is not eligible for secure download"}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnsupported, failureReason: "image URL is not eligible for secure download"}
		}
		request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp;q=0.9,*/*;q=0.1")
		request.Header.Set("User-Agent", "143-code-review-visual-evidence/1")
		if token != "" && visualEvidenceURLNeedsGitHubAuth(parsed.String()) {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := d.client.Do(request) // #nosec G704 -- production transport enforces public HTTPS targets at dial time
		if err != nil {
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download failed", retryable: true}
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			if closeErr := response.Body.Close(); closeErr != nil {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download failed", retryable: true}
			}
			if redirect >= visualEvidenceMaxRedirects {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image redirect limit exceeded"}
			}
			location := strings.TrimSpace(response.Header.Get("Location"))
			if location == "" {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image redirect is missing a destination"}
			}
			next, resolveErr := parsed.Parse(location)
			if resolveErr != nil {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image redirect destination is invalid"}
			}
			currentURL = next.String()
			continue
		}
		if response.StatusCode != http.StatusOK {
			if closeErr := response.Body.Close(); closeErr != nil {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download failed", retryable: true}
			}
			retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooEarly ||
				response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
			return visualEvidenceAttemptResult{
				status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image is unavailable", retryable: retryable,
			}
		}
		if response.ContentLength > visualEvidenceMaxImageBytes {
			if closeErr := response.Body.Close(); closeErr != nil {
				return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download failed", retryable: true}
			}
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusOverLimit, failureReason: "image exceeds the 10 MB limit"}
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, visualEvidenceMaxImageBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: "image download failed", retryable: true}
		}
		if len(data) > visualEvidenceMaxImageBytes {
			return visualEvidenceAttemptResult{status: models.CodeReviewVisualEvidenceFetchStatusOverLimit, failureReason: "image exceeds the 10 MB limit"}
		}
		return visualEvidenceAttemptResult{data: data, status: models.CodeReviewVisualEvidenceFetchStatusAvailable}
	}
}

func unavailableVisualEvidenceResult(reason string) visualEvidenceFetchResult {
	return visualEvidenceFetchResult{status: models.CodeReviewVisualEvidenceFetchStatusUnavailable, failureReason: reason, hostClass: "unknown"}
}

func validateVisualEvidenceURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("visual evidence URL must be an absolute HTTPS URL without user information")
	}
	if literal := net.ParseIP(parsed.Hostname()); literal != nil && isBlockedVisualEvidenceIP(literal) {
		return nil, fmt.Errorf("visual evidence URL host is not public")
	}
	return parsed, nil
}

func visualEvidenceURLNeedsGitHubAuth(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "github.com") && strings.HasPrefix(parsed.EscapedPath(), "/user-attachments/assets/")
}

func isBlockedVisualEvidenceIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
		return true
	}
	for _, network := range visualEvidenceBlockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func visualEvidenceSafeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("visual evidence SSRF guard: malformed dial address")
	}
	ip := net.ParseIP(host)
	if ip == nil || isBlockedVisualEvidenceIP(ip) {
		return fmt.Errorf("visual evidence SSRF guard: refusing non-public address")
	}
	return nil
}

func inspectVisualEvidenceImage(data []byte) (string, int, int, error) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		config, err := png.DecodeConfig(bytes.NewReader(data))
		return "image/png", config.Width, config.Height, err
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("\xff\xd8\xff")):
		config, err := jpeg.DecodeConfig(bytes.NewReader(data))
		return "image/jpeg", config.Width, config.Height, err
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		config, err := gif.DecodeConfig(bytes.NewReader(data))
		return "image/gif", config.Width, config.Height, err
	case len(data) >= 16 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		width, height, err := webPDimensions(data)
		return "image/webp", width, height, err
	default:
		return "", 0, 0, fmt.Errorf("unsupported image format")
	}
}

func webPDimensions(data []byte) (int, int, error) {
	if len(data) < 30 {
		return 0, 0, fmt.Errorf("truncated WebP image")
	}
	switch string(data[12:16]) {
	case "VP8 ":
		if !bytes.Equal(data[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0, fmt.Errorf("invalid VP8 frame header")
		}
		width := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return validateVisualEvidenceDimensions(width, height)
	case "VP8L":
		if data[20] != 0x2f {
			return 0, 0, fmt.Errorf("invalid VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		width := int(bits&0x3fff) + 1
		height := int((bits>>14)&0x3fff) + 1
		return validateVisualEvidenceDimensions(width, height)
	case "VP8X":
		width := 1 + int(data[24]) + int(data[25])<<8 + int(data[26])<<16
		height := 1 + int(data[27]) + int(data[28])<<8 + int(data[29])<<16
		return validateVisualEvidenceDimensions(width, height)
	default:
		return 0, 0, fmt.Errorf("unsupported WebP chunk")
	}
}

func validateVisualEvidenceDimensions(width, height int) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("image dimensions must be positive")
	}
	return width, height, nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}
