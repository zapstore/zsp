package source

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/zapstore/zsp/internal/config"
)

// PlayStore provides metadata fetching from Google Play Store.
type PlayStore struct {
	packageID string
	client    *http.Client
}

// NewPlayStore creates a new Play Store metadata fetcher.
func NewPlayStore(packageID string) *PlayStore {
	return &PlayStore{
		packageID: packageID,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// PlayStoreMetadata contains metadata scraped from the Play Store.
type PlayStoreMetadata struct {
	Name        string
	Description string
	IconURL     string
	ImageURLs   []string
}

// FetchMetadata fetches app metadata from the Google Play Store.
func (p *PlayStore) FetchMetadata(ctx context.Context) (*PlayStoreMetadata, error) {
	url := fmt.Sprintf("https://play.google.com/store/apps/details?id=%s&hl=en_US", p.packageID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Set a realistic User-Agent to avoid being blocked
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Play Store page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("app %s not found on Play Store", p.packageID)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Play Store returned status %d", resp.StatusCode)
	}

	// Parse the HTML
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	meta := &PlayStoreMetadata{}

	// Extract app name from itemprop="name"
	doc.Find("[itemprop=\"name\"]").First().Each(func(i int, s *goquery.Selection) {
		meta.Name = strings.TrimSpace(s.Text())
	})

	// Extract description from data-g-id="description"
	doc.Find("[data-g-id=\"description\"]").First().Each(func(i int, s *goquery.Selection) {
		html, err := s.Html()
		if err == nil {
			meta.Description = htmlToMarkdown(html)
		}
	})

	// Extract icon URL from img[itemprop="image"]
	doc.Find("img[itemprop=\"image\"]").First().Each(func(i int, s *goquery.Selection) {
		if src, exists := s.Attr("src"); exists {
			meta.IconURL = updateImageDimensions(src)
		}
	})

	// Extract screenshot URLs from img[data-screenshot-index]
	doc.Find("img[data-screenshot-index]").Each(func(i int, s *goquery.Selection) {
		if src, exists := s.Attr("src"); exists {
			src = strings.TrimSpace(src)
			if src != "" {
				meta.ImageURLs = append(meta.ImageURLs, updateImageDimensions(src))
			}
		}
	})

	return meta, nil
}

// PackageID returns the package ID.
func (p *PlayStore) PackageID() string {
	return p.packageID
}

// updateImageDimensions updates the image URL to request high-resolution images.
// Play Store image URLs have dimension parameters like =w240-h480, we replace with larger sizes.
func updateImageDimensions(url string) string {
	// Parse the URL and update the dimension parameter
	// URLs look like: https://play-lh.googleusercontent.com/xxx=w240-h480
	// We want to change it to =w5120-h2880 for high resolution
	if strings.Contains(url, "googleusercontent.com") {
		// Find the = parameter and replace dimensions
		parts := strings.Split(url, "=")
		if len(parts) >= 2 {
			basePath := parts[0]
			return basePath + "=w5120-h2880"
		}
	}
	return url
}

// htmlToMarkdown converts simple HTML to Markdown.
// This is a basic implementation handling common elements.
func htmlToMarkdown(html string) string {
	// Remove excessive whitespace
	html = strings.TrimSpace(html)

	// Convert <br> tags to newlines
	html = regexp.MustCompile(`<br\s*/?>|</br>`).ReplaceAllString(html, "\n")

	// Convert bold tags
	html = regexp.MustCompile(`<b>|<strong>`).ReplaceAllString(html, "**")
	html = regexp.MustCompile(`</b>|</strong>`).ReplaceAllString(html, "**")

	// Convert italic tags
	html = regexp.MustCompile(`<i>|<em>`).ReplaceAllString(html, "_")
	html = regexp.MustCompile(`</i>|</em>`).ReplaceAllString(html, "_")

	// Convert lists
	html = regexp.MustCompile(`<li>`).ReplaceAllString(html, "- ")
	html = regexp.MustCompile(`</li>`).ReplaceAllString(html, "\n")
	html = regexp.MustCompile(`<ul>|</ul>|<ol>|</ol>`).ReplaceAllString(html, "\n")

	// Convert paragraphs to double newlines
	html = regexp.MustCompile(`<p>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`</p>`).ReplaceAllString(html, "\n\n")

	// Convert links: <a href="url">text</a> -> [text](url)
	linkRegex := regexp.MustCompile(`<a[^>]*href=["']([^"']+)["'][^>]*>([^<]*)</a>`)
	html = linkRegex.ReplaceAllString(html, "[$2]($1)")

	// Remove any remaining HTML tags
	html = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, "")

	// Decode common HTML entities
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")
	html = strings.ReplaceAll(html, "&nbsp;", " ")

	// Clean up excessive newlines
	html = regexp.MustCompile(`\n{3,}`).ReplaceAllString(html, "\n\n")
	html = strings.TrimSpace(html)

	return html
}

// GetPlayStorePackageID extracts the package ID from a Play Store URL.
func GetPlayStorePackageID(url string) string {
	// Handle: https://play.google.com/store/apps/details?id=com.example.app
	lower := strings.ToLower(url)
	if strings.Contains(lower, "play.google.com") {
		// Extract id parameter
		if idx := strings.Index(lower, "id="); idx != -1 {
			id := url[idx+3:]
			// Remove any trailing parameters
			if ampIdx := strings.Index(id, "&"); ampIdx != -1 {
				id = id[:ampIdx]
			}
			return id
		}
	}
	return ""
}

// DetectPlayStore checks if a URL is a Play Store URL.
func DetectPlayStore(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, "play.google.com/store/apps")
}

// FetchPlayStoreMetadata is a convenience function to fetch metadata by package ID.
func FetchPlayStoreMetadata(ctx context.Context, packageID string) (*AppMetadata, error) {
	ps := NewPlayStore(packageID)
	psMeta, err := ps.FetchMetadata(ctx)
	if err != nil {
		return nil, err
	}

	meta := &AppMetadata{
		Name:        psMeta.Name,
		Description: psMeta.Description,
	}

	// Note: IconURL is returned separately since it needs to be downloaded
	// and uploaded to blossom, which is handled by the caller
	if len(psMeta.ImageURLs) > 0 {
		meta.ImageURLs = psMeta.ImageURLs
	}

	return meta, nil
}

// Type returns the source type for Play Store.
func (p *PlayStore) Type() config.SourceType {
	return config.SourcePlayStore
}

