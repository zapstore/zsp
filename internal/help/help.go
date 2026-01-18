// Package help provides intelligent, searchable help using README content.
package help

import (
	"embed"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

//go:embed README.md
var readmeFS embed.FS

// Section represents a searchable section of the README.
type Section struct {
	Title    string   // Section title (e.g., "GitHub Releases")
	Path     string   // Full path (e.g., "APK Sources > GitHub Releases")
	Content  string   // Raw markdown content
	Keywords []string // Extracted keywords for searching
	Level    int      // Heading level (2 = ##, 3 = ###)
}

// sections holds all parsed sections from README.
var sections []Section

// searchIndex holds all searchable strings mapped to section indices.
var searchIndex []searchItem

type searchItem struct {
	Text         string // Searchable text (title + keywords)
	SectionIndex int    // Index into sections slice
}

func init() {
	data, err := readmeFS.ReadFile("README.md")
	if err != nil {
		return
	}
	sections = parseREADME(string(data))
	buildSearchIndex()
}

// parseREADME splits README into searchable sections.
func parseREADME(content string) []Section {
	var result []Section
	lines := strings.Split(content, "\n")

	var currentSection *Section
	var currentContent strings.Builder
	var parentTitle string // Track parent section for path

	for _, line := range lines {
		// Check for heading
		if strings.HasPrefix(line, "## ") {
			// Save previous section
			if currentSection != nil {
				currentSection.Content = strings.TrimSpace(currentContent.String())
				currentSection.Keywords = extractKeywords(currentSection.Content)
				result = append(result, *currentSection)
			}

			title := strings.TrimPrefix(line, "## ")
			parentTitle = title
			currentSection = &Section{
				Title: title,
				Path:  title,
				Level: 2,
			}
			currentContent.Reset()
		} else if strings.HasPrefix(line, "### ") {
			// Save previous section
			if currentSection != nil {
				currentSection.Content = strings.TrimSpace(currentContent.String())
				currentSection.Keywords = extractKeywords(currentSection.Content)
				result = append(result, *currentSection)
			}

			title := strings.TrimPrefix(line, "### ")
			currentSection = &Section{
				Title: title,
				Path:  parentTitle + " › " + title,
				Level: 3,
			}
			currentContent.Reset()
		} else if currentSection != nil {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Save last section
	if currentSection != nil {
		currentSection.Content = strings.TrimSpace(currentContent.String())
		currentSection.Keywords = extractKeywords(currentSection.Content)
		result = append(result, *currentSection)
	}

	return result
}

// extractKeywords pulls out important terms from content.
func extractKeywords(content string) []string {
	// Important terms to extract
	keywordPatterns := []string{
		`github`, `gitlab`, `codeberg`, `gitea`, `forgejo`, `fdroid`, `f-droid`,
		`playstore`, `play store`, `google play`,
		`blossom`, `cdn`, `relay`, `nostr`,
		`nsec`, `npub`, `bunker`, `nip-07`, `nip-46`, `browser`,
		`yaml`, `config`, `configuration`,
		`signing`, `sign`, `publish`, `upload`,
		`apk`, `android`, `arm64`, `x86`,
		`metadata`, `icon`, `screenshot`, `image`,
		`release`, `version`, `changelog`, `commit`,
		`repository`, `source`, `local`, `web`, `scraping`,
		`ci`, `cd`, `github actions`, `automation`,
		`wizard`, `interactive`,
		`dry-run`, `check-apk`, `extract`,
		`match`, `pattern`, `regex`, `filter`,
		`variant`, `channel`, `beta`, `nightly`,
		`identity`, `x509`, `certificate`, `nip-ci`, `keystore`, `pkcs12`, `pem`, `jks`, `spkifp`, `kind 30509`,
	}

	lower := strings.ToLower(content)
	var found []string
	seen := make(map[string]bool)

	for _, kw := range keywordPatterns {
		if strings.Contains(lower, kw) && !seen[kw] {
			seen[kw] = true
			found = append(found, kw)
		}
	}

	return found
}

// buildSearchIndex creates the fuzzy search index.
func buildSearchIndex() {
	searchIndex = nil
	for i, sec := range sections {
		// Add title as searchable
		searchIndex = append(searchIndex, searchItem{
			Text:         strings.ToLower(sec.Title + " " + strings.Join(sec.Keywords, " ")),
			SectionIndex: i,
		})
	}
}

// searchResult holds a matched section with its score.
type searchResult struct {
	Section Section
	Score   int
}

// Search finds sections matching the query.
func Search(query string) []Section {
	if len(sections) == 0 || query == "" {
		return nil
	}

	query = strings.ToLower(query)
	queryTerms := strings.Fields(query)

	// Score each section based on how many query terms match
	var results []searchResult

	for _, sec := range sections {
		score := 0
		searchText := strings.ToLower(sec.Title + " " + sec.Path + " " + strings.Join(sec.Keywords, " ") + " " + sec.Content)

		for _, term := range queryTerms {
			// Exact match in title (high score)
			if strings.Contains(strings.ToLower(sec.Title), term) {
				score += 100
			}
			// Match in keywords (medium score)
			for _, kw := range sec.Keywords {
				if strings.Contains(kw, term) || strings.Contains(term, kw) {
					score += 50
					break
				}
			}
			// Match in content (lower score)
			if strings.Contains(searchText, term) {
				score += 10
			}
		}

		// Also do fuzzy matching on title
		titleMatches := fuzzy.Find(query, []string{sec.Title})
		if len(titleMatches) > 0 {
			score += titleMatches[0].Score
		}

		if score > 0 {
			results = append(results, searchResult{Section: sec, Score: score})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Return top results (max 5)
	var matched []Section
	for i, r := range results {
		if i >= 5 {
			break
		}
		matched = append(matched, r.Section)
	}

	return matched
}

// Render renders sections as beautiful terminal output.
func Render(secs []Section) string {
	if len(secs) == 0 {
		return ""
	}

	// Create glamour renderer for markdown
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		// Fallback to plain text
		var buf strings.Builder
		for _, sec := range secs {
			buf.WriteString(fmt.Sprintf("\n## %s\n\n%s\n", sec.Title, sec.Content))
		}
		return buf.String()
	}

	var buf strings.Builder

	// Header style
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		MarginBottom(1)

	pathStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true)

	divider := lipgloss.NewStyle().
		Foreground(lipgloss.Color("236")).
		Render(strings.Repeat("─", 60))

	for i, sec := range secs {
		if i > 0 {
			buf.WriteString("\n" + divider + "\n\n")
		}

		// Show path if it differs from title
		if sec.Path != sec.Title {
			buf.WriteString(pathStyle.Render(sec.Path) + "\n")
		}
		buf.WriteString(headerStyle.Render(sec.Title) + "\n\n")

		// Render markdown content
		rendered, err := renderer.Render(sec.Content)
		if err != nil {
			buf.WriteString(sec.Content)
		} else {
			buf.WriteString(rendered)
		}
	}

	return buf.String()
}

// ShowSearchHelp displays help for a search query.
func ShowSearchHelp(query string) {
	results := Search(query)

	if len(results) == 0 {
		noResultStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("208")).
			Bold(true)

		fmt.Println(noResultStyle.Render(fmt.Sprintf("\nNo results found for: %s", query)))
		fmt.Println()
		fmt.Println("Try searching for:")
		suggestions := []string{"github", "fdroid", "signing", "config", "metadata", "ci/cd", "web scraping"}
		for _, s := range suggestions {
			fmt.Printf("  zsp --help %s\n", s)
		}
		return
	}

	fmt.Print(Render(results))
}

// ListTopics shows all available help topics.
func ListTopics() {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212"))

	topicStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("251"))

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Available Help Topics"))
	fmt.Println()

	// Group by level-2 sections
	var currentParent string
	for _, sec := range sections {
		if sec.Level == 2 {
			currentParent = sec.Title
			fmt.Println(topicStyle.Render("  • " + sec.Title))
		} else if sec.Level == 3 && currentParent != "" {
			fmt.Println(dimStyle.Render("      └─ " + sec.Title))
		}
	}

	fmt.Println()
	fmt.Println(dimStyle.Render("Search with: zsp --help <topic>"))
	fmt.Println(dimStyle.Render("Examples:"))
	fmt.Println(dimStyle.Render("  zsp --help fdroid"))
	fmt.Println(dimStyle.Render("  zsp --help signing browser"))
	fmt.Println(dimStyle.Render("  zsp --help config release"))
}

// QuickReference returns the traditional --help output.
func QuickReference() string {
	return `zsp - Publish Android apps to Nostr relays used by Zapstore

USAGE
  zsp [config.yaml]              Config file (default: ./zapstore.yaml)
  zsp <app.apk> [-r <repo>]      Local APK with optional source repo
  zsp -r <repo>                  Fetch latest release from repo
  zsp <app.apk> --extract-apk    Extract APK metadata as JSON
  zsp                            Interactive wizard (no args, no config)
  zsp --wizard                   Interactive wizard (uses existing config as defaults)

FLAGS
  -r <url>        Repository URL (GitHub/GitLab/F-Droid)
  -s <url>        Release source URL (defaults to -r if not specified)
  -m <source>     Fetch metadata from source (repeatable: -m github -m fdroid)
  -y              Auto-confirm all prompts
  -h, --help      Show this help
  -v, --version   Print version

  --wizard        Run interactive wizard (uses existing config as defaults)
  --match <pattern>   Regex pattern to filter APK assets
  --commit <hash>     Git commit hash for reproducible builds
  --extract-apk   Extract APK metadata as JSON (local APK only)
  --check-apk     Verify config fetches and parses an arm64-v8a APK (exit 0=success)
  --skip-preview  Skip the browser preview prompt
  --port <port>   Custom port for browser preview/signing (default: 17007/17008)
  --overwrite-release  Bypass cache and re-publish even if release unchanged
  --overwrite-app      Re-fetch metadata even if app already exists on relays
  --legacy        Use legacy event format (default: true, use --legacy=false for new format)
  -n, --dry-run   Parse & build events, but don't upload/publish
  --quiet         Minimal output, no prompts (implies -y)
  --verbose       Debug output (show scores, API responses)
  --no-color      Disable colored output

IDENTITY (NIP-C1)
  --link-identity <file>        Publish cryptographic identity proof (kind 30509)
                                Supports: .p12, .pfx (PKCS12), .pem, .crt (PEM)
                                Links your signing key's SPKIFP to your Nostr pubkey
  --verify-identity <file>      Verify identity proof against certificate
                                Checks SPKIFP match, signature, expiry, and revocation
  --identity-expiry <duration>  Validity period (default: 1y). Examples: 1y, 6mo, 30d
  --identity-relays <url>       Relays for identity proofs (repeatable, overrides defaults)
                                Defaults: relay.primal.net, relay.damus.io, relay.zapstore.dev

  Example: zsp --link-identity signing.p12 --dry-run
  Example: zsp --link-identity signing.p12 --identity-expiry 2y
  Example: zsp --verify-identity signing.p12

ENVIRONMENT
  SIGN_WITH         Required. Signing method:
                      nsec1...      Direct signing with private key
                      npub1...      Output unsigned events (for external signing)
                      bunker://...  Remote signing via NIP-46
                      browser       Sign with browser extension (NIP-07)

  GITHUB_TOKEN      Optional. Avoid GitHub API rate limits
  RELAY_URLS        Custom relay URLs (default: wss://relay.zapstore.dev)
  BLOSSOM_URL       Custom CDN server (default: https://cdn.zapstore.dev)

SEARCH HELP
  zsp --help <topic>       Search help for a topic
  zsp --help topics        List all help topics
  zsp --help fdroid        Help about F-Droid sources
  zsp --help signing       Help about signing methods
  zsp --help config        Help about configuration

More info: https://github.com/zapstore/zsp
`
}

// HasSearchQuery checks if help args contain a search query.
func HasSearchQuery(args []string) bool {
	return len(args) > 0
}

// HandleHelp processes help command with optional search query.
func HandleHelp(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, QuickReference())
		return
	}

	query := strings.Join(args, " ")

	// Special case: "topics" lists all topics
	if strings.ToLower(query) == "topics" {
		ListTopics()
		return
	}

	ShowSearchHelp(query)
}

// highlightMatches adds highlighting to matched terms (unused for now, but available)
var highlightRe = regexp.MustCompile(`(?i)(github|gitlab|fdroid|signing|config|metadata)`)

func highlightMatches(text, query string) string {
	terms := strings.Fields(strings.ToLower(query))
	result := text

	highlightStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("226"))

	for _, term := range terms {
		re := regexp.MustCompile(`(?i)(` + regexp.QuoteMeta(term) + `)`)
		result = re.ReplaceAllStringFunc(result, func(match string) string {
			return highlightStyle.Render(match)
		})
	}

	return result
}

