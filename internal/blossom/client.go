// Package blossom handles file uploads to Blossom servers.
package blossom

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostrpkg "github.com/zapstore/zsp/internal/nostr"
)

const (
	// DefaultServer is the default Blossom server URL.
	DefaultServer = "https://cdn.zapstore.dev"

	// AuthExpiration is how long the auth token is valid.
	AuthExpiration = 5 * time.Minute
)

// Client handles Blossom uploads.
type Client struct {
	serverURL  string
	httpClient *http.Client
}

// NewClient creates a new Blossom client.
func NewClient(serverURL string) *Client {
	if serverURL == "" {
		serverURL = DefaultServer
	}
	return &Client{
		serverURL:  serverURL,
		httpClient: newSecureHTTPClient(5 * time.Minute),
	}
}

// newSecureHTTPClient creates an HTTP client with security best practices.
func newSecureHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// UploadResult contains the result of an upload.
type UploadResult struct {
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Type    string `json:"type"`
	Existed bool   `json:"-"` // True if file already existed on server (not uploaded)
}

// ProgressFunc is called during upload to report progress.
type ProgressFunc func(uploaded, total int64)

// Exists checks if a file already exists on the server.
func (c *Client) Exists(ctx context.Context, sha256 string) (bool, error) {
	url := fmt.Sprintf("%s/%s", c.serverURL, sha256)

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// ExistsBatch checks if multiple files exist on the server in parallel.
// Returns a map of sha256 -> exists. Concurrency is limited to maxConcurrent.
func (c *Client) ExistsBatch(ctx context.Context, hashes []string, maxConcurrent int) map[string]bool {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	result := make(map[string]bool)
	if len(hashes) == 0 {
		return result
	}

	// Use a mutex to protect the result map
	var mu sync.Mutex

	// Semaphore channel to limit concurrency
	sem := make(chan struct{}, maxConcurrent)

	// WaitGroup to wait for all goroutines
	var wg sync.WaitGroup

	for _, hash := range hashes {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			exists, err := c.Exists(ctx, h)
			if err != nil {
				// On error, assume doesn't exist (will try to upload)
				exists = false
			}

			mu.Lock()
			result[h] = exists
			mu.Unlock()
		}(hash)
	}

	wg.Wait()
	return result
}

// Upload uploads a file to the Blossom server.
func (c *Client) Upload(ctx context.Context, filePath string, sha256 string, signer nostrpkg.Signer, onProgress ProgressFunc) (*UploadResult, error) {
	// Create and sign auth event
	authEvent := nostrpkg.BuildBlossomAuthEvent(sha256, signer.PublicKey(), time.Now().Add(AuthExpiration))
	if err := signer.Sign(ctx, authEvent); err != nil {
		return nil, fmt.Errorf("failed to sign auth event: %w", err)
	}
	return c.UploadWithAuth(ctx, filePath, sha256, authEvent, onProgress)
}

// UploadWithAuth uploads a file using a pre-signed auth event.
func (c *Client) UploadWithAuth(ctx context.Context, filePath string, sha256 string, authEvent *nostr.Event, onProgress ProgressFunc) (*UploadResult, error) {
	// Check if already exists
	exists, err := c.Exists(ctx, sha256)
	if err != nil {
		return nil, fmt.Errorf("failed to check existence: %w", err)
	}

	if exists {
		return &UploadResult{
			URL:     fmt.Sprintf("%s/%s", c.serverURL, sha256),
			SHA256:  sha256,
			Existed: true,
		}, nil
	}

	// Open file
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Encode auth event
	authJSON, err := json.Marshal(authEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal auth event: %w", err)
	}
	authHeader := "Nostr " + base64.StdEncoding.EncodeToString(authJSON)

	// Create upload request
	var reader io.Reader = f
	if onProgress != nil {
		reader = &progressReader{
			reader:     f,
			total:      fi.Size(),
			onProgress: onProgress,
		}
	}

	url := fmt.Sprintf("%s/upload", c.serverURL)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/vnd.android.package-archive")
	req.ContentLength = fi.Size()

	// Execute upload
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result UploadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Some servers don't return JSON, construct result manually
		result = UploadResult{
			URL:    fmt.Sprintf("%s/%s", c.serverURL, sha256),
			SHA256: sha256,
			Size:   fi.Size(),
		}
	}

	return &result, nil
}

// UploadBytes uploads raw bytes to the Blossom server.
func (c *Client) UploadBytes(ctx context.Context, data []byte, sha256 string, contentType string, signer nostrpkg.Signer) (*UploadResult, error) {
	// Create and sign auth event
	authEvent := nostrpkg.BuildBlossomAuthEvent(sha256, signer.PublicKey(), time.Now().Add(AuthExpiration))
	if err := signer.Sign(ctx, authEvent); err != nil {
		return nil, fmt.Errorf("failed to sign auth event: %w", err)
	}
	return c.UploadBytesWithAuth(ctx, data, sha256, contentType, authEvent)
}

// UploadBytesWithAuth uploads raw bytes using a pre-signed auth event.
func (c *Client) UploadBytesWithAuth(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event) (*UploadResult, error) {
	return c.uploadBytesWithAuth(ctx, data, sha256, contentType, authEvent, false)
}

// UploadBytesWithAuthPreChecked uploads raw bytes, using a pre-computed existence check result.
// If existed is true, returns immediately without uploading.
func (c *Client) UploadBytesWithAuthPreChecked(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event, existed bool) (*UploadResult, error) {
	if existed {
		return &UploadResult{
			URL:     fmt.Sprintf("%s/%s", c.serverURL, sha256),
			SHA256:  sha256,
			Size:    int64(len(data)),
			Existed: true,
		}, nil
	}
	return c.uploadBytesWithAuth(ctx, data, sha256, contentType, authEvent, true)
}

// uploadBytesWithAuth is the internal implementation.
func (c *Client) uploadBytesWithAuth(ctx context.Context, data []byte, sha256 string, contentType string, authEvent *nostr.Event, skipCheck bool) (*UploadResult, error) {
	// Check if already exists (unless skipCheck is true)
	if !skipCheck {
		exists, err := c.Exists(ctx, sha256)
		if err != nil {
			return nil, fmt.Errorf("failed to check existence: %w", err)
		}

		if exists {
			return &UploadResult{
				URL:     fmt.Sprintf("%s/%s", c.serverURL, sha256),
				SHA256:  sha256,
				Size:    int64(len(data)),
				Existed: true,
			}, nil
		}
	}

	// Encode auth event
	authJSON, err := json.Marshal(authEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal auth event: %w", err)
	}
	authHeader := "Nostr " + base64.StdEncoding.EncodeToString(authJSON)

	// Create upload request
	url := fmt.Sprintf("%s/upload", c.serverURL)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", authHeader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(data))

	// Execute upload
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return &UploadResult{
		URL:    fmt.Sprintf("%s/%s", c.serverURL, sha256),
		SHA256: sha256,
		Size:   int64(len(data)),
		Type:   contentType,
	}, nil
}

// ServerURL returns the configured server URL.
func (c *Client) ServerURL() string {
	return c.serverURL
}

// progressReader wraps a reader to track progress.
type progressReader struct {
	reader     io.Reader
	total      int64
	uploaded   int64
	onProgress ProgressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.uploaded += int64(n)
	if pr.onProgress != nil {
		pr.onProgress(pr.uploaded, pr.total)
	}
	return n, err
}

// Ensure nostr.Event is used (for auth event)
var _ = nostr.Event{}

