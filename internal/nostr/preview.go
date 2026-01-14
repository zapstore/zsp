package nostr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/zapstore/zsp/internal/apk"
	"github.com/zapstore/zsp/internal/config"
)

// DefaultPreviewPort is the default port for the HTML preview server.
const DefaultPreviewPort = 17008

// AssetPreviewData contains data for a single software asset.
type AssetPreviewData struct {
	SHA256          string
	FileSize        int64
	Filename        string
	CertFingerprint string
	MinSDK          int32
	TargetSDK       int32
	Platforms       []string // Platform identifiers for this specific asset
}

// PreviewImageData holds pre-downloaded image data for local serving.
type PreviewImageData struct {
	Data     []byte
	MimeType string
}

// PreviewData contains all data needed to render the preview.
type PreviewData struct {
	// Software Application
	AppName     string
	PackageID   string
	Summary     string
	Description string
	Website     string
	Repository  string
	License     string
	Tags        []string
	IconData    []byte             // Raw PNG icon data
	IconURL     string             // URL if using remote icon
	ImageURLs   []string           // Screenshot URLs (remote or will be replaced with local)
	ImageData   []PreviewImageData // Pre-downloaded screenshot data (served locally)
	Platforms   []string           // All platforms (union of all assets)

	// Software Release
	Version     string
	VersionCode int64
	Channel     string
	Changelog   string

	// Software Assets (multiple)
	Assets []AssetPreviewData

	// Publish targets (where it WILL be published)
	BlossomServer string
	RelayURLs     []string

	// Events for JSON view (optional - may be nil before signing)
	AppMetadataEvent    *nostr.Event
	ReleaseEvent        *nostr.Event
	SoftwareAssetEvents []*nostr.Event
}

// BuildPreviewDataFromAPK creates preview data from APK info and config (before signing).
// This is used for the pre-signing preview where events are not yet available.
func BuildPreviewDataFromAPK(apkInfo *apk.APKInfo, cfg *config.Config, changelog string, blossomURL string, relayURLs []string) *PreviewData {
	return BuildPreviewDataFromAPKs([]*apk.APKInfo{apkInfo}, cfg, changelog, blossomURL, relayURLs)
}

// BuildPreviewDataFromAPKs creates preview data from multiple APK infos and config (before signing).
// This is used for the pre-signing preview where events are not yet available.
func BuildPreviewDataFromAPKs(apkInfos []*apk.APKInfo, cfg *config.Config, changelog string, blossomURL string, relayURLs []string) *PreviewData {
	if len(apkInfos) == 0 {
		return nil
	}

	// Use first APK for app-level info
	firstAPK := apkInfos[0]

	// Determine app name
	name := cfg.Name
	if name == "" {
		name = firstAPK.Label
	}
	if name == "" {
		name = firstAPK.PackageID
	}

	// Build assets and collect all platforms
	var assets []AssetPreviewData
	platformSet := make(map[string]bool)

	for _, apkInfo := range apkInfos {
		// Convert architectures to platform identifiers for this asset
		assetPlatforms := make([]string, 0, len(apkInfo.Architectures))
		for _, arch := range apkInfo.Architectures {
			p := archToPlatform(arch)
			assetPlatforms = append(assetPlatforms, p)
			platformSet[p] = true
		}
		if len(assetPlatforms) == 0 {
			// Architecture-independent
			for _, p := range []string{"android-arm64-v8a", "android-armeabi-v7a", "android-x86", "android-x86_64"} {
				assetPlatforms = append(assetPlatforms, p)
				platformSet[p] = true
			}
		}

		assets = append(assets, AssetPreviewData{
			SHA256:          apkInfo.SHA256,
			FileSize:        apkInfo.FileSize,
			Filename:        apkInfo.FilePath,
			CertFingerprint: apkInfo.CertFingerprint,
			MinSDK:          apkInfo.MinSDK,
			TargetSDK:       apkInfo.TargetSDK,
			Platforms:       assetPlatforms,
		})
	}

	// Collect all platforms
	var platforms []string
	for p := range platformSet {
		platforms = append(platforms, p)
	}

	return &PreviewData{
		AppName:       name,
		PackageID:     firstAPK.PackageID,
		Summary:       cfg.Summary,
		Description:   cfg.Description,
		Website:       cfg.Website,
		Repository:    cfg.Repository,
		License:       cfg.License,
		Tags:          cfg.Tags,
		IconData:      firstAPK.Icon,
		ImageURLs:     cfg.Images,
		Platforms:     platforms,
		Version:       firstAPK.VersionName,
		VersionCode:   firstAPK.VersionCode,
		Channel:       "main",
		Changelog:     changelog,
		Assets:        assets,
		BlossomServer: blossomURL,
		RelayURLs:     relayURLs,
	}
}

// BuildPreviewData creates preview data from APK info, config, and events.
// Use this after signing when events are available.
func BuildPreviewData(apkInfo *apk.APKInfo, cfg *config.Config, events *EventSet, changelog string, blossomURL string, relayURLs []string) *PreviewData {
	data := BuildPreviewDataFromAPK(apkInfo, cfg, changelog, blossomURL, relayURLs)
	if events != nil {
		data.AppMetadataEvent = events.AppMetadata
		data.ReleaseEvent = events.Release
		data.SoftwareAssetEvents = events.SoftwareAssets
	}
	return data
}

// BuildPreviewDataFromEvents creates preview data from multiple APKs, config, and events.
// Use this after signing when events are available.
func BuildPreviewDataFromEvents(apkInfos []*apk.APKInfo, cfg *config.Config, events *EventSet, changelog string, blossomURL string, relayURLs []string) *PreviewData {
	data := BuildPreviewDataFromAPKs(apkInfos, cfg, changelog, blossomURL, relayURLs)
	if events != nil {
		data.AppMetadataEvent = events.AppMetadata
		data.ReleaseEvent = events.Release
		data.SoftwareAssetEvents = events.SoftwareAssets
	}
	return data
}

// PreviewServer serves the HTML preview.
type PreviewServer struct {
	port        int
	server      *http.Server
	listener    net.Listener
	data        *PreviewData
	done        chan struct{}
	cliConfirm  chan struct{} // signals when confirmed from CLI
	changelog   string
	iconURL     string
	iconDataB64 string
}

// NewPreviewServer creates a preview server on the specified port.
// If port is 0, it uses the default port.
func NewPreviewServer(data *PreviewData, changelog, iconURL string, port int) *PreviewServer {
	if port == 0 {
		port = DefaultPreviewPort
	}

	// Pre-encode icon to base64 if available
	var iconDataB64 string
	if len(data.IconData) > 0 {
		iconDataB64 = base64.StdEncoding.EncodeToString(data.IconData)
	}

	return &PreviewServer{
		port:        port,
		data:        data,
		done:        make(chan struct{}),
		cliConfirm:  make(chan struct{}),
		changelog:   changelog,
		iconURL:     iconURL,
		iconDataB64: iconDataB64,
	}
}

// Start starts the preview server and opens the browser.
func (s *PreviewServer) Start() (string, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return "", fmt.Errorf("failed to start preview server: %w", err)
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/poll", s.handlePoll)
	mux.HandleFunc("/images/", s.handleImage) // Serve pre-downloaded images

	s.server = &http.Server{Handler: mux}
	go s.server.Serve(listener)

	url := fmt.Sprintf("http://localhost:%d/", s.port)

	// Open browser
	if err := openBrowser(url); err != nil {
		// Non-fatal: user can manually open the URL
		fmt.Printf("Could not open browser automatically. Please open: %s\n", url)
	}

	return url, nil
}

// handleImage serves pre-downloaded images by index.
func (s *PreviewServer) handleImage(w http.ResponseWriter, r *http.Request) {
	// Parse index from URL: /images/0, /images/1, etc.
	path := strings.TrimPrefix(r.URL.Path, "/images/")
	idx, err := strconv.Atoi(path)
	if err != nil || idx < 0 || idx >= len(s.data.ImageData) {
		http.NotFound(w, r)
		return
	}

	img := s.data.ImageData[idx]
	if img.MimeType != "" {
		w.Header().Set("Content-Type", img.MimeType)
	} else {
		w.Header().Set("Content-Type", "image/png")
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(img.Data)
}

// Close shuts down the preview server.
func (s *PreviewServer) Close() error {
	close(s.done)
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *PreviewServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(s.buildHTML()))
}

func (s *PreviewServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Events may be nil if preview is shown before signing
	if s.data.AppMetadataEvent == nil {
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Events will be generated after signing",
		})
		return
	}
	events := map[string]any{
		"softwareApplication": s.data.AppMetadataEvent,
		"softwareRelease":     s.data.ReleaseEvent,
		"softwareAssets":      s.data.SoftwareAssetEvents,
	}
	json.NewEncoder(w).Encode(events)
}

// handlePoll returns whether the preview was confirmed from CLI.
// Browser polls this to know when to close itself.
func (s *PreviewServer) handlePoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	select {
	case <-s.cliConfirm:
		// Confirmed from CLI, tell browser to close
		json.NewEncoder(w).Encode(map[string]any{"close": true})
	default:
		// Not yet confirmed
		json.NewEncoder(w).Encode(map[string]any{"close": false})
	}
}

// ConfirmFromCLI confirms the preview from the CLI, which signals the browser to close.
func (s *PreviewServer) ConfirmFromCLI() {
	close(s.cliConfirm)
}

func (s *PreviewServer) buildHTML() string {
	d := s.data

	// Icon handling
	iconHTML := ""
	if s.iconDataB64 != "" {
		iconHTML = fmt.Sprintf(`<img src="data:image/png;base64,%s" alt="App Icon">`, s.iconDataB64)
	} else if s.iconURL != "" {
		iconHTML = fmt.Sprintf(`<img src="%s" alt="App Icon">`, html.EscapeString(s.iconURL))
	}

	// Tags HTML
	tagsHTML := ""
	if len(d.Tags) > 0 {
		var tagSpans []string
		for _, tag := range d.Tags {
			tagSpans = append(tagSpans, fmt.Sprintf(`<span class="tag">%s</span>`, html.EscapeString(tag)))
		}
		tagsHTML = fmt.Sprintf(`<div class="tags-container">%s</div>`, strings.Join(tagSpans, " "))
	}

	// Screenshots HTML - use local URLs for pre-downloaded images
	screenshotsHTML := ""
	if len(d.ImageData) > 0 {
		// Use locally served pre-downloaded images
		var imgs []string
		for i := range d.ImageData {
			imgs = append(imgs, fmt.Sprintf(`<img src="/images/%d" alt="Screenshot">`, i))
		}
		screenshotsHTML = fmt.Sprintf(`<div class="screenshots">%s</div>`, strings.Join(imgs, ""))
	} else if len(d.ImageURLs) > 0 {
		// Fallback to remote URLs
		var imgs []string
		for _, url := range d.ImageURLs {
			imgs = append(imgs, fmt.Sprintf(`<img src="%s" alt="Screenshot">`, html.EscapeString(url)))
		}
		screenshotsHTML = fmt.Sprintf(`<div class="screenshots">%s</div>`, strings.Join(imgs, ""))
	}

	// Build assets HTML
	assetsHTML := ""
	for i, asset := range d.Assets {
		assetNum := ""
		if len(d.Assets) > 1 {
			assetNum = fmt.Sprintf(" %d", i+1)
		}
		assetsHTML += fmt.Sprintf(`
    <div class="section">
      <h2>Software Asset%s</h2>
      <div class="asset-grid">
        <div class="asset-item">
          <div class="label">SHA256</div>
          <div class="value">%s</div>
        </div>
        <div class="asset-item">
          <div class="label">File Size</div>
          <div class="value">%s</div>
        </div>
        <div class="asset-item">
          <div class="label">Min SDK</div>
          <div class="value">%s</div>
        </div>
        <div class="asset-item">
          <div class="label">Target SDK</div>
          <div class="value">%s</div>
        </div>
        <div class="asset-item" style="grid-column: 1 / -1;">
          <div class="label">APK Certificate Hash</div>
          <div class="value">%s</div>
        </div>
      </div>
    </div>`,
			assetNum,
			html.EscapeString(asset.SHA256),
			formatBytes(asset.FileSize),
			strconv.Itoa(int(asset.MinSDK)),
			strconv.Itoa(int(asset.TargetSDK)),
			html.EscapeString(asset.CertFingerprint),
		)
	}

	// Relay URLs HTML
	relayURLsHTML := ""
	if len(d.RelayURLs) > 0 {
		var urls []string
		for _, url := range d.RelayURLs {
			urls = append(urls, fmt.Sprintf(`<span class="url">%s</span>`, html.EscapeString(url)))
		}
		relayURLsHTML = strings.Join(urls, "<br>")
	}

	// Changelog - convert to HTML, build section conditionally
	releaseSectionHTML := ""
	if s.changelog != "" {
		changelogHTML := simpleMarkdownToHTML(s.changelog)
		releaseSectionHTML = fmt.Sprintf(`
    <div class="section">
      <h2>Release</h2>
      <div class="version-badge">
        <span class="ver">v%s</span>
        <span class="code">(%d)</span>
        <span class="channel-badge">%s</span>
      </div>
      <div class="changelog">%s</div>
    </div>`,
			html.EscapeString(d.Version),
			d.VersionCode,
			html.EscapeString(d.Channel),
			changelogHTML,
		)
	} else {
		// Show release section without changelog container
		releaseSectionHTML = fmt.Sprintf(`
    <div class="section">
      <h2>Release</h2>
      <div class="version-badge">
        <span class="ver">v%s</span>
        <span class="code">(%d)</span>
        <span class="channel-badge">%s</span>
      </div>
    </div>`,
			html.EscapeString(d.Version),
			d.VersionCode,
			html.EscapeString(d.Channel),
		)
	}

	// Description - convert markdown to HTML
	descriptionHTML := ""
	if d.Description != "" {
		descriptionHTML = fmt.Sprintf(`<div class="description">%s</div>`, simpleMarkdownToHTML(d.Description))
	}

	// App info rows
	appInfoHTML := ""
	websiteRow := buildInfoRow("Website", d.Website, true)
	repoRow := buildInfoRow("Repository", d.Repository, true)
	licenseRow := buildInfoRow("License", d.License, false)
	if websiteRow != "" || repoRow != "" || licenseRow != "" {
		appInfoHTML = fmt.Sprintf(`<div class="info-grid">%s%s%s</div>`, websiteRow, repoRow, licenseRow)
	}

	return fmt.Sprintf(previewHTML,
		// Title
		html.EscapeString(d.AppName),
		// App section
		iconHTML,
		html.EscapeString(d.AppName),
		html.EscapeString(d.PackageID),
		html.EscapeString(d.Summary),
		descriptionHTML,
		appInfoHTML,
		tagsHTML,
		screenshotsHTML,
		// Release section (built conditionally)
		releaseSectionHTML,
		// Assets section (built dynamically)
		assetsHTML,
		// Publish targets
		html.EscapeString(d.BlossomServer),
		relayURLsHTML,
	)
}

// simpleMarkdownToHTML converts basic markdown to HTML.
func simpleMarkdownToHTML(text string) string {
	// Escape HTML first
	text = html.EscapeString(text)

	// Convert headers (### Header -> <h3>Header</h3>)
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Handle code blocks
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				result = append(result, "<pre><code>")
			} else {
				result = append(result, "</code></pre>")
			}
			continue
		}

		if inCodeBlock {
			result = append(result, line)
			continue
		}

		// Headers
		if strings.HasPrefix(trimmed, "### ") {
			result = append(result, "<h4>"+strings.TrimPrefix(trimmed, "### ")+"</h4>")
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			result = append(result, "<h3>"+strings.TrimPrefix(trimmed, "## ")+"</h3>")
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			result = append(result, "<h2>"+strings.TrimPrefix(trimmed, "# ")+"</h2>")
			continue
		}

		// List items (markdown style and bullet character)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "• ") {
			content := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* "), "• ")
			result = append(result, "<li>"+content+"</li>")
			continue
		}

		// Empty lines become paragraph breaks (double <br> for visual spacing)
		if trimmed == "" {
			result = append(result, "<br><br>")
			continue
		}

		// Regular text - apply inline formatting and add line break
		line = applyInlineMarkdown(line)
		result = append(result, line+"<br>")
	}

	// Join without newlines since we're using <br> tags
	output := strings.Join(result, "")
	// Clean up trailing <br> tags
	output = strings.TrimSuffix(output, "<br>")
	// Clean up excessive <br> tags (more than 2 consecutive)
	output = regexp.MustCompile(`(<br>){3,}`).ReplaceAllString(output, "<br><br>")
	return output
}

// applyInlineMarkdown applies inline markdown formatting (bold, italic, code, links).
func applyInlineMarkdown(text string) string {
	// Bold: **text** or __text__
	text = replacePattern(text, `\*\*([^*]+)\*\*`, "<strong>$1</strong>")
	text = replacePattern(text, `__([^_]+)__`, "<strong>$1</strong>")

	// Italic: *text* or _text_
	text = replacePattern(text, `\*([^*]+)\*`, "<em>$1</em>")
	text = replacePattern(text, `_([^_]+)_`, "<em>$1</em>")

	// Inline code: `text`
	text = replacePattern(text, "`([^`]+)`", "<code>$1</code>")

	// Links: [text](url)
	text = replacePattern(text, `\[([^\]]+)\]\(([^)]+)\)`, `<a href="$2" target="_blank">$1</a>`)

	return text
}

// replacePattern applies a regex replacement.
func replacePattern(text, pattern, replacement string) string {
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllString(text, replacement)
}

func buildInfoRow(label, value string, isLink bool) string {
	if value == "" {
		return ""
	}
	if isLink && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
		return fmt.Sprintf(`<div class="info-row"><span class="label">%s</span><a href="%s" target="_blank">%s</a></div>`,
			label, html.EscapeString(value), html.EscapeString(value))
	}
	return fmt.Sprintf(`<div class="info-row"><span class="label">%s</span><span>%s</span></div>`,
		label, html.EscapeString(value))
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

const previewHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s - Release Preview</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    
    body {
      font-family: 'SF Pro Text', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #1a1a1e;
      color: #e0e0e4;
      min-height: 100vh;
      line-height: 1.6;
    }
    
    .container {
      max-width: 900px;
      margin: 0 auto;
      padding: 32px 24px;
    }
    
    .header-banner {
      padding: 16px 0;
      margin-bottom: 24px;
    }
    
    .header-banner h1 {
      font-size: 1.25rem;
      font-weight: 600;
      color: #9080a0;
    }
    
    .section {
      background: #232328;
      border: 1px solid #3a3a42;
      border-radius: 8px;
      padding: 24px;
      margin-bottom: 20px;
    }
    
    .section h2 {
      font-size: 1.2rem;
      font-weight: 600;
      color: #9080a0;
      border-bottom: 1px solid #3a3a42;
      padding-bottom: 12px;
      margin-bottom: 16px;
      display: flex;
      align-items: center;
      gap: 10px;
    }
    
    .section h2::before {
      content: '';
      display: block;
      width: 4px;
      height: 20px;
      background: #4a3a5c;
      border-radius: 2px;
    }
    
    .app-header {
      display: flex;
      align-items: flex-start;
      gap: 24px;
      margin-bottom: 20px;
    }
    
    .app-header img {
      width: 96px;
      height: 96px;
      border-radius: 16px;
      box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3);
      flex-shrink: 0;
    }
    
    .app-header .app-title {
      flex: 1;
    }
    
    .app-header h1 {
      font-size: 2rem;
      font-weight: 700;
      color: #fff;
      margin-bottom: 4px;
    }
    
    .app-header .package-id {
      font-family: 'SF Mono', 'Menlo', 'Monaco', monospace;
      font-size: 0.9rem;
      color: #9080a0;
      background: rgba(74, 58, 92, 0.3);
      padding: 4px 10px;
      border-radius: 4px;
      display: inline-block;
    }
    
    .app-header .summary {
      color: #a0a0a8;
      margin-top: 8px;
      font-size: 1.05rem;
    }
    
    .info-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
      gap: 16px;
      margin-top: 20px;
    }
    
    .info-row {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }
    
    .info-row .label {
      font-size: 0.8rem;
      color: #8a8a94;
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }
    
    .info-row a {
      color: #9080a0;
      text-decoration: none;
      word-break: break-all;
    }
    
    .info-row a:hover {
      text-decoration: underline;
      color: #a898b8;
    }
    
    .tags-container {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 16px;
    }
    
    .tag {
      background: rgba(74, 58, 92, 0.3);
      border: 1px solid rgba(74, 58, 92, 0.5);
      color: #9080a0;
      padding: 4px 12px;
      border-radius: 4px;
      font-size: 0.85rem;
    }
    
    .description {
      color: #c8c8d0;
      line-height: 1.7;
      margin-top: 16px;
    }
    .description h2, .description h3, .description h4 {
      color: #9080a0;
      margin: 16px 0 8px 0;
    }
    .description h2:first-child, .description h3:first-child, .description h4:first-child {
      margin-top: 0;
    }
    .description a { color: #9080a0; }
    .description code {
      background: rgba(74, 58, 92, 0.3);
      padding: 2px 6px;
      border-radius: 4px;
      font-family: 'SF Mono', monospace;
      font-size: 0.9em;
    }
    .description pre {
      background: #1a1a1e;
      padding: 12px;
      border-radius: 6px;
      overflow-x: auto;
      margin: 12px 0;
      border: 1px solid #3a3a42;
    }
    .description li {
      margin-left: 20px;
      margin-bottom: 4px;
    }
    
    .screenshots {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
      gap: 16px;
      margin-top: 20px;
    }
    
    .screenshots img {
      width: 100%%;
      border-radius: 6px;
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
    }
    
    .version-badge {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      background: rgba(74, 58, 92, 0.3);
      border: 1px solid rgba(74, 58, 92, 0.5);
      padding: 8px 16px;
      border-radius: 6px;
    }
    
    .version-badge .ver {
      font-size: 1.2rem;
      font-weight: 600;
      color: #fff;
    }
    
    .version-badge .code {
      font-size: 0.8rem;
      color: #8a8a94;
    }
    
    .channel-badge {
      background: #4a3a5c;
      color: #e0e0e4;
      padding: 2px 8px;
      border-radius: 4px;
      font-size: 0.75rem;
      text-transform: uppercase;
      font-weight: 600;
    }
    
    .changelog {
      background: #1a1a1e;
      padding: 16px;
      border-radius: 6px;
      border: 1px solid #3a3a42;
      color: #c8c8d0;
      max-height: 300px;
      overflow-y: auto;
      font-size: 0.95rem;
      margin-top: 16px;
    }
    .changelog h2, .changelog h3, .changelog h4 {
      color: #9080a0;
      margin: 12px 0 6px 0;
    }
    .changelog li {
      margin-left: 20px;
      margin-bottom: 4px;
    }
    
    .asset-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
      gap: 16px;
    }
    
    .asset-item {
      background: #1a1a1e;
      padding: 16px;
      border-radius: 6px;
      border: 1px solid #3a3a42;
    }
    
    .asset-item .label {
      font-size: 0.75rem;
      color: #8a8a94;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      margin-bottom: 4px;
    }
    
    .asset-item .value {
      font-family: 'SF Mono', monospace;
      font-size: 0.9rem;
      color: #e0e0e4;
      word-break: break-all;
    }
    
    .url {
      color: #9080a0;
      text-decoration: none;
      font-family: 'SF Mono', monospace;
      font-size: 0.85rem;
    }
    
    .url:hover {
      text-decoration: underline;
    }
    
    .actions {
      display: flex;
      gap: 16px;
      margin-top: 32px;
      padding-top: 24px;
      border-top: 1px solid #3a3a42;
    }
    
    .terminal-hint {
      text-align: center;
      color: #9080a0;
      font-size: 1.1rem;
      padding: 16px 24px;
      background: rgba(74, 58, 92, 0.2);
      border: 1px solid #3a3a42;
      border-radius: 8px;
    }
    
    .terminal-hint kbd {
      background: #3a3a42;
      padding: 4px 10px;
      border-radius: 4px;
      font-family: monospace;
      font-size: 0.95rem;
      border: 1px solid #4a4a52;
      color: #e0e0e4;
    }
    
    .status {
      text-align: center;
      padding: 24px;
      border-radius: 6px;
      margin-top: 16px;
      display: none;
    }
    
    .status.success {
      display: block;
      background: rgba(74, 58, 92, 0.3);
      border: 1px solid #4a3a5c;
      color: #9080a0;
    }
    
    .status.error {
      display: block;
      background: rgba(140, 50, 50, 0.2);
      border: 1px solid #8c3232;
      color: #e0a0a0;
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header-banner">
      <h1>Zapstore Publisher - Release Preview</h1>
    </div>
    
    <div class="section">
      <h2>Application</h2>
      <div class="app-header">
        %s
        <div class="app-title">
          <h1>%s</h1>
          <span class="package-id">%s</span>
          <p class="summary">%s</p>
        </div>
      </div>
      %s
      %s
      %s
      %s
    </div>
    
    %s
    
    %s
    
    <div class="section">
      <h2>Publish To</h2>
      <div class="asset-grid">
        <div class="asset-item" style="grid-column: 1 / -1;">
          <div class="label">Blossom CDN</div>
          <div class="value">%s</div>
        </div>
        <div class="asset-item" style="grid-column: 1 / -1;">
          <div class="label">Nostr Relays</div>
          <div class="value">%s</div>
        </div>
      </div>
    </div>
    
    <div class="actions">
      <div class="terminal-hint">Press <kbd>Enter</kbd> in terminal to continue, or <kbd>Ctrl+C</kbd> to cancel</div>
    </div>
    
    <div id="status" class="status"></div>
  </div>
  
  <script>
    // Poll for terminal confirmation or server shutdown
    async function pollCLI() {
      while (true) {
        try {
          const resp = await fetch('/api/poll');
          const data = await resp.json();
          if (data.close) {
            const status = document.getElementById('status');
            status.className = 'status success';
            status.innerHTML = '✓ Confirmed from terminal! Closing...';
            setTimeout(() => window.close(), 300);
            return;
          }
        } catch (e) {
          // Server closed (Ctrl+C in terminal)
          window.close();
          return;
        }
        await new Promise(r => setTimeout(r, 500));
      }
    }
    
    pollCLI();
  </script>
</body>
</html>`
