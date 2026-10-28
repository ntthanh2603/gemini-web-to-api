package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/models"

	"go.uber.org/zap"
)

type uploadedFile struct {
	ID   string
	Name string
}

const (
	uploadAuthorization = "Basic c2F2ZXM6cyNMdGhlNmxzd2F2b0RsN3J1d1U="
	uploadTimeout       = 2 * time.Minute
)

type uploadHTTPError struct {
	status     int
	body       string
	retryAfter string
}

func (e *uploadHTTPError) Error() string {
	return fmt.Sprintf("upload failed with status %d: %s", e.status, e.body)
}

// InputFilesFromAttachments decodes base64 API attachments into provider upload inputs.
func InputFilesFromAttachments(messages []models.Message) ([]InputFile, error) {
	var files []InputFile
	for _, msg := range messages {
		msgFiles, err := InputFilesFromAttachmentList(msg.Attachments)
		if err != nil {
			return nil, err
		}
		files = append(files, msgFiles...)
	}
	return files, nil
}

// InputFilesFromAttachmentList decodes base64 API attachments into provider upload inputs.
func InputFilesFromAttachmentList(attachments []models.Attachment) ([]InputFile, error) {
	files := make([]InputFile, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.Data) == "" {
			continue
		}
		data, err := decodeBase64Data(attachment.Data)
		if err != nil {
			return nil, fmt.Errorf("decode attachment %q: %w", attachment.Name, err)
		}
		files = append(files, InputFile{
			Name:     attachment.Name,
			MimeType: attachment.MimeType,
			Data:     data,
		})
	}
	return files, nil
}

func DecodeBase64Data(value string) ([]byte, error) {
	return decodeBase64Data(value)
}

func decodeBase64Data(value string) ([]byte, error) {
	cleaned := strings.TrimSpace(value)
	if data, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
		return data, nil
	}
	if data, err := base64.RawStdEncoding.DecodeString(cleaned); err == nil {
		return data, nil
	}
	if data, err := base64.URLEncoding.DecodeString(cleaned); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(cleaned)
}

func (c *Client) uploadRequestFiles(ctx context.Context, cfg *GenerateConfig, cookieHdr string) ([]uploadedFile, error) {
	total := len(cfg.Files) + len(cfg.InputFiles)
	if total == 0 {
		return nil, nil
	}

	out := make([]uploadedFile, 0, total)
	for _, path := range cfg.Files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read file %q: %w", path, err)
		}
		name := filepath.Base(path)
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		uploaded, err := c.uploadFile(ctx, name, mimeType, data, cookieHdr)
		if err != nil {
			return nil, err
		}
		out = append(out, uploaded)
	}

	for i, file := range cfg.InputFiles {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			name = fmt.Sprintf("input_%d%s", i+1, models.ExtensionForMimeType(file.MimeType))
		}
		mimeType := strings.TrimSpace(file.MimeType)
		if mimeType == "" {
			mimeType = http.DetectContentType(file.Data)
		}
		uploaded, err := c.uploadFile(ctx, name, mimeType, file.Data, cookieHdr)
		if err != nil {
			return nil, err
		}
		out = append(out, uploaded)
	}

	return out, nil
}

func (c *Client) uploadFile(ctx context.Context, filename, mimeType string, data []byte, cookieHdr string) (uploadedFile, error) {
	return c.uploadFileTo(ctx, EndpointUpload, filename, mimeType, data, cookieHdr)
}

func (c *Client) uploadFileTo(ctx context.Context, endpoint, filename, mimeType string, data []byte, cookieHdr string) (uploadedFile, error) {
	client := *c.httpClient.GetClient()
	client.Timeout = uploadTimeout
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return validateUploadURL(req.URL.String(), endpoint)
	}

	maxAttempts := c.maxRetries
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := c.performResumableUpload(ctx, &client, endpoint, filename, mimeType, data, cookieHdr)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableUploadError(err) || attempt == maxAttempts {
			break
		}
		delay := uploadRetryDelay(err, attempt)
		c.log.Warn("Retrying Gemini file upload",
			zap.String("filename", filename),
			zap.Int("attempt", attempt+1),
			zap.Duration("backoff", delay),
			zap.Error(err),
		)
		if err := waitForRetry(ctx, delay); err != nil {
			return uploadedFile{}, fmt.Errorf("upload %q canceled while waiting to retry: %w", filename, err)
		}
	}

	return uploadedFile{}, fmt.Errorf("upload %q failed after %d attempt(s): %w", filename, maxAttempts, lastErr)
}

func (c *Client) performResumableUpload(ctx context.Context, client *http.Client, endpoint, filename, mimeType string, data []byte, cookieHdr string) (uploadedFile, error) {
	headers := c.uploadHeaders(cookieHdr, len(data))
	resp, err := doUploadRequest(ctx, client, http.MethodOptions, endpoint, nil, headers)
	if err != nil {
		return uploadedFile{}, fmt.Errorf("prepare upload: %w", err)
	}
	resp.Body.Close()

	startHeaders := headers.Clone()
	startHeaders.Set("Size", strconv.Itoa(len(data)))
	startBody := strings.NewReader("File name: " + filename)
	resp, err = doUploadRequest(ctx, client, http.MethodPost, endpoint, startBody, startHeaders)
	if err != nil {
		return uploadedFile{}, fmt.Errorf("start upload: %w", err)
	}
	uploadURL := strings.TrimSpace(resp.Header.Get("X-Goog-Upload-Url"))
	resp.Body.Close()
	if uploadURL == "" {
		return uploadedFile{}, fmt.Errorf("start upload: Gemini did not return an upload URL")
	}
	if err := validateUploadURL(uploadURL, endpoint); err != nil {
		return uploadedFile{}, err
	}

	resp, err = doUploadRequest(ctx, client, http.MethodOptions, uploadURL, nil, startHeaders)
	if err != nil {
		return uploadedFile{}, fmt.Errorf("prepare upload URL: %w", err)
	}
	resp.Body.Close()

	finalHeaders := startHeaders.Clone()
	finalHeaders.Set("Content-Type", mimeType)
	finalHeaders.Set("X-Goog-Upload-Command", "upload, finalize")
	finalHeaders.Set("X-Goog-Upload-Offset", "0")
	resp, err = doUploadRequest(ctx, client, http.MethodPost, uploadURL, bytes.NewReader(data), finalHeaders)
	if err != nil {
		return uploadedFile{}, fmt.Errorf("finalize upload: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return uploadedFile{}, fmt.Errorf("read upload response: %w", err)
	}
	id := strings.TrimSpace(string(respBody))
	if id == "" {
		return uploadedFile{}, fmt.Errorf("upload returned empty file id")
	}
	return uploadedFile{ID: id, Name: filename}, nil
}

func (c *Client) uploadHeaders(cookieHdr string, size int) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("Authorization", uploadAuthorization)
	headers.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	headers.Set("Origin", "https://gemini.google.com")
	headers.Set("Referer", "https://gemini.google.com/")
	headers.Set("Push-ID", c.pushIDOrDefault())
	headers.Set("X-Goog-Upload-Command", "start")
	headers.Set("X-Goog-Upload-Header-Content-Length", strconv.Itoa(size))
	headers.Set("X-Goog-Upload-Protocol", "resumable")
	headers.Set("X-Tenant-Id", "bard-storage")
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if cookieHdr != "" {
		headers.Set("Cookie", cookieHdr)
	}
	return headers
}

func doUploadRequest(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return nil, &uploadHTTPError{
		status:     resp.StatusCode,
		body:       strings.TrimSpace(string(responseBody)),
		retryAfter: resp.Header.Get("Retry-After"),
	}
}

func validateUploadURL(rawURL, endpoint string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("Gemini returned an invalid upload URL")
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() == "" {
		return fmt.Errorf("invalid configured upload endpoint")
	}

	trustedHost := strings.EqualFold(u.Hostname(), base.Hostname())
	if strings.EqualFold(base.Scheme, "https") {
		if u.Scheme != "https" {
			return fmt.Errorf("Gemini returned an insecure upload URL")
		}
	} else if !strings.EqualFold(u.Scheme, base.Scheme) {
		return fmt.Errorf("Gemini returned an upload URL with an unexpected scheme")
	}
	if !trustedHost {
		return fmt.Errorf("Gemini returned an upload URL on an untrusted host")
	}
	return nil
}

func isRetryableUploadError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *uploadHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.status {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func uploadRetryDelay(err error, attempt int) time.Duration {
	var httpErr *uploadHTTPError
	if errors.As(err, &httpErr) {
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(httpErr.retryAfter)); parseErr == nil && seconds >= 0 {
			return min(time.Duration(seconds)*time.Second, time.Minute)
		}
		if retryAt, parseErr := http.ParseTime(httpErr.retryAfter); parseErr == nil {
			return min(max(time.Until(retryAt), 0), time.Minute)
		}
	}
	return min(time.Duration(1<<uint(attempt-1))*time.Second, 30*time.Second)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) pushIDOrDefault() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pushID != "" {
		return c.pushID
	}
	return "feeds/mcudyrk2a4khkz"
}
