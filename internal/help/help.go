// Package help provides colorful CLI help output.
package help

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
)

// Color palette: green, dark purple, greyscale
var (
	// Green tones
	green = lipgloss.Color("35") // Green

	// Purple tones
	purple = lipgloss.Color("54") // Dark purple

	// Greyscale
	grey     = lipgloss.Color("245")
	greyDark = lipgloss.Color("242")
	white    = lipgloss.Color("252")
)

// Render functions that don't add extra whitespace
func renderGreen(s string) string {
	return lipgloss.NewStyle().Foreground(green).Render(s)
}

func renderPurple(s string) string {
	return lipgloss.NewStyle().Foreground(purple).Render(s)
}

func renderPurpleBold(s string) string {
	return lipgloss.NewStyle().Foreground(purple).Bold(true).Render(s)
}

func renderGreenBold(s string) string {
	return lipgloss.NewStyle().Foreground(green).Bold(true).Render(s)
}

func renderWhite(s string) string {
	return lipgloss.NewStyle().Foreground(white).Render(s)
}

func renderGrey(s string) string {
	return lipgloss.NewStyle().Foreground(grey).Render(s)
}

func renderGreyDark(s string) string {
	return lipgloss.NewStyle().Foreground(greyDark).Render(s)
}

func renderURL(s string) string {
	return lipgloss.NewStyle().Foreground(green).Underline(true).Render(s)
}

// RootHelp returns the top-level --help output.
func RootHelp() string {
	var b strings.Builder

	b.WriteString(ui.RenderLogo())
	b.WriteString(renderWhite("Publish Android apps to Nostr relays used by Zapstore") + "\n")

	b.WriteString(renderPurpleBold("USAGE") + "\n")
	b.WriteString("  " + renderGreen("zsp") + " <command> [options]\n\n")

	b.WriteString(renderPurpleBold("COMMANDS") + "\n")
	b.WriteString("  " + renderGreen("publish") + "     " + renderWhite("Publish APK releases to Nostr relays") + "\n")
	b.WriteString("  " + renderGreen("identity") + "    " + renderWhite("Manage cryptographic identity proofs (NIP-C1)") + "\n")
	b.WriteString("  " + renderGreen("apk") + "         " + renderWhite("APK utility commands (extract metadata)") + "\n\n")

	b.WriteString(renderPurpleBold("EXAMPLES") + "\n")
	writeExample(&b, "zsp publish --wizard", "Interactive wizard (recommended for first-time setup)")
	writeExample(&b, "zsp publish config.yaml", "Publish from config file")
	writeExample(&b, "zsp publish app.apk", "Publish local APK")
	writeExample(&b, "zsp publish -r github.com/org/repo", "Fetch and publish from GitHub (open source)")
	writeExample(&b, "zsp publish -s github.com/user/app", "Closed-source (releases only, no source code)")
	writeExample(&b, "zsp identity --link-key key.p12", "Link signing key to Nostr identity")
	b.WriteString("\n")

	b.WriteString(renderPurpleBold("ENVIRONMENT") + "\n")
	b.WriteString("  " + renderPurple("SIGN_WITH") + "       " + renderWhite("Signing method (nsec1..., npub1..., bunker://..., browser)") + "\n")
	b.WriteString("  " + renderPurple("GITHUB_TOKEN") + "    " + renderWhite("GitHub API token (optional, avoids rate limits)") + "\n")
	b.WriteString("  " + renderPurple("RELAY_URLS") + "      " + renderWhite("Custom relay URLs (default: wss://relay.zapstore.dev)") + "\n")
	b.WriteString("  " + renderPurple("BLOSSOM_URL") + "     " + renderWhite("Custom CDN server (default: https://cdn.zapstore.dev)") + "\n\n")

	b.WriteString(renderPurpleBold("GLOBAL FLAGS") + "\n")
	b.WriteString("  " + renderGreen("-h, --help") + "      " + renderWhite("Show help") + "\n")
	b.WriteString("  " + renderGreen("-v, --version") + "   " + renderWhite("Show version") + "\n")
	b.WriteString("  " + renderGreen("--verbose") + "       " + renderWhite("Debug output") + "\n")
	b.WriteString("  " + renderGreen("--no-color") + "      " + renderWhite("Disable colored output") + "\n\n")

	b.WriteString(renderPurpleBold("MORE INFO") + "\n")
	b.WriteString("  " + renderGreen("zsp publish --wizard") + "  " + renderWhite("Interactive wizard to determine best options") + "\n")
	b.WriteString("  " + renderGreen("zsp publish --help") + "    " + renderWhite("Detailed publish help") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --help") + "   " + renderWhite("Detailed identity help") + "\n")
	b.WriteString("  " + renderURL("https://github.com/zapstore/zsp") + "\n")

	return b.String()
}

// PublishHelp returns colorful help for the publish subcommand.
func PublishHelp() string {
	var b strings.Builder

	b.WriteString(ui.RenderLogo())
	b.WriteString(renderGreenBold("zsp publish") + " " + renderWhite("- Publish APK releases to Nostr relays") + "\n")
	b.WriteString("\n")

	b.WriteString(renderPurpleBold("USAGE") + "\n")
	b.WriteString("  " + renderGreen("zsp publish") + " [options] [config.yaml | app.apk]\n\n")

	b.WriteString(renderGreyDark("  With no arguments, runs the interactive wizard (unless zapstore.yaml exists).") + "\n")
	b.WriteString(renderGreyDark("  With a config file, publishes according to that configuration.") + "\n")
	b.WriteString(renderGreyDark("  With an APK file, publishes that APK directly.") + "\n\n")

	// Source flags
	b.WriteString(renderPurpleBold("SOURCE FLAGS") + "\n")
	writeFlag(&b, "-r <url>", "Source code repository URL (GitHub/GitLab/Codeberg/Gitea)")
	b.WriteString("                            " + renderGreyDark("Also fetches releases from here unless -s is specified") + "\n")
	writeFlag(&b, "-s <url>", "Release/download source URL (F-Droid, web page, etc)")
	b.WriteString("                            " + renderGreyDark("Use alone (no -r) for closed-source apps") + "\n")
	writeFlag(&b, "-m <source>", "Fetch metadata from source (repeatable: -m github -m fdroid)")
	b.WriteString("                            " + renderGreyDark("Fetched automatically for new releases") + "\n")
	writeFlag(&b, "--match <pattern>", "Regex pattern to filter APK assets (rarely needed)")
	b.WriteString("\n")

	// Release-specific flags (CLI only)
	b.WriteString(renderPurpleBold("RELEASE FLAGS") + "\n")
	writeFlag(&b, "--commit <hash>", "Git commit hash for reproducible builds")
	writeFlag(&b, "--channel <name>", "Release channel: main, beta, nightly, dev (default: main)")
	b.WriteString("\n")

	// Behavior flags
	b.WriteString(renderPurpleBold("BEHAVIOR FLAGS") + "\n")
	writeFlag(&b, "-y", "Skip confirmations (auto-yes)")
	writeFlag(&b, "-n, --dry-run", "Parse & build events, but don't upload/publish")
	writeFlag(&b, "--quiet", "Minimal output, no prompts (implies -y)")
	writeFlag(&b, "--wizard", "Run interactive wizard (uses existing config as defaults)")
	writeFlag(&b, "--skip-preview", "Skip the browser preview prompt")
	writeFlag(&b, "--port <port>", "Custom port for browser preview/signing")
	b.WriteString("\n")

	// Cache flags
	b.WriteString(renderPurpleBold("CACHE FLAGS") + "\n")
	writeFlag(&b, "--overwrite-release", "Bypass cache and re-publish even if release unchanged")
	writeFlag(&b, "--skip-metadata", "Skip fetching metadata from external sources")
	b.WriteString("                            " + renderGreyDark("Useful for apps with frequent releases") + "\n")
	b.WriteString("\n")

	// Other flags
	b.WriteString(renderPurpleBold("OTHER FLAGS") + "\n")
	writeFlag(&b, "--check", "Verify config fetches arm64-v8a APK (exit 0=success)")
	b.WriteString("                            " + renderGreyDark("Useful for CI/CD validation") + "\n")
	writeFlag(&b, "--legacy", "Use legacy event format (default: true)")
	writeFlag(&b, "--verbose", "Debug output")
	writeFlag(&b, "--no-color", "Disable colored output")
	writeFlag(&b, "-h, --help", "Show this help")
	b.WriteString("\n")

	// Examples section - comprehensive
	b.WriteString(renderPurpleBold("EXAMPLES") + "\n\n")

	b.WriteString(renderGreyDark("  # Interactive wizard - helps determine best options") + "\n")
	b.WriteString("  " + renderGreen("zsp publish --wizard") + "\n\n")

	b.WriteString(renderGreyDark("  # Publish from config file") + "\n")
	b.WriteString("  " + renderGreen("zsp publish zapstore.yaml") + "\n\n")

	b.WriteString(renderGreyDark("  # Publish local APK with repository metadata") + "\n")
	b.WriteString("  " + renderGreen("zsp publish app-release.apk -r github.com/user/app") + "\n\n")

	b.WriteString(renderGreyDark("  # Fetch latest release from GitHub and publish") + "\n")
	b.WriteString("  " + renderGreen("zsp publish -r github.com/AeonBTC/mempal") + "\n\n")

	b.WriteString(renderGreyDark("  # Closed-source app (releases on GitHub, but no source code)") + "\n")
	b.WriteString("  " + renderGreen("zsp publish -s github.com/user/app -m playstore") + "\n\n")

	b.WriteString(renderGreyDark("  # Open source: GitHub repo + F-Droid builds") + "\n")
	b.WriteString("  " + renderGreen("zsp publish -r github.com/user/app -s f-droid.org/packages/com.example") + "\n\n")

	b.WriteString(renderGreyDark("  # Dry run - preview events without publishing") + "\n")
	b.WriteString("  " + renderGreen("zsp publish zapstore.yaml --dry-run") + "\n\n")

	b.WriteString(renderGreyDark("  # CI/CD mode - no prompts, auto-confirm") + "\n")
	b.WriteString("  " + renderGreen("zsp publish -y zapstore.yaml") + "\n\n")

	b.WriteString(renderGreyDark("  # Force re-publish even if unchanged") + "\n")
	b.WriteString("  " + renderGreen("zsp publish zapstore.yaml --overwrite-release") + "\n\n")

	b.WriteString(renderGreyDark("  # Validate config fetches correct APK (CI/CD)") + "\n")
	b.WriteString("  " + renderGreen("zsp publish --check zapstore.yaml") + "\n\n")

	// Config section
	b.WriteString(renderPurpleBold("CONFIGURATION") + "\n")
	b.WriteString(renderGreyDark("  Config files are YAML. Minimal example:") + "\n\n")
	b.WriteString("  " + renderGreen("repository:") + " " + renderWhite("https://github.com/user/app") + "\n\n")
	b.WriteString(renderGreyDark("  Full example with all options:") + "\n\n")
	b.WriteString("  " + renderGreen("repository:") + "      " + renderWhite("https://github.com/user/app") + "\n")
	b.WriteString("  " + renderGreen("name:") + "            " + renderWhite("My App") + "\n")
	b.WriteString("  " + renderGreen("summary:") + "         " + renderWhite("A short description") + "\n")
	b.WriteString("  " + renderGreen("icon:") + "            " + renderWhite("./assets/icon.png") + "\n")
	b.WriteString("  " + renderGreen("images:") + "\n")
	b.WriteString("    " + renderGreen("-") + " " + renderWhite("./screenshots/1.png") + "\n")
	b.WriteString("  " + renderGreen("tags:") + "            " + renderWhite("[productivity, nostr]") + "\n")
	b.WriteString("  " + renderGreen("match:") + "           " + renderWhite("'.*arm64.*\\.apk$'") + "\n")
	b.WriteString("  " + renderGreen("release_notes:") + "   " + renderWhite("./CHANGELOG.md") + "\n\n")

	b.WriteString(renderGreyDark("  Default config file: ") + renderWhite("./zapstore.yaml") + "\n")

	return b.String()
}

// IdentityHelp returns colorful help for the identity subcommand.
func IdentityHelp() string {
	var b strings.Builder

	b.WriteString(ui.RenderLogo())
	b.WriteString(renderGreenBold("zsp identity") + " " + renderWhite("- Manage cryptographic identity proofs (NIP-C1)") + "\n")

	b.WriteString(renderPurpleBold("WHAT IS THIS?") + "\n")
	b.WriteString(renderWhite("  Links your Android signing key to your Nostr identity.") + "\n")
	b.WriteString(renderWhite("  This proves you control both the signing key and the Nostr pubkey.") + "\n")
	b.WriteString(renderWhite("  Users can verify that apps signed with your key are published by you.") + "\n\n")

	b.WriteString(renderPurpleBold("USAGE") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --link-key") + " <certificate>\n")
	b.WriteString("  " + renderGreen("zsp identity --verify") + " <certificate|apk>\n\n")

	// Commands
	b.WriteString(renderPurpleBold("COMMANDS") + "\n")
	writeFlag(&b, "--link-key <file>", "Publish cryptographic identity proof (kind 30509)")
	b.WriteString("                            " + renderGreyDark("Supported: .p12, .pfx (PKCS12), .pem, .crt (PEM)") + "\n")
	writeFlag(&b, "--verify <file>", "Verify identity proof against certificate or APK")
	b.WriteString("                            " + renderGreyDark("For APKs, extracts the signing certificate automatically") + "\n")
	b.WriteString("\n")

	// Options
	b.WriteString(renderPurpleBold("OPTIONS") + "\n")
	writeFlag(&b, "--link-key-expiry <duration>", "Validity period (default: 1y)")
	b.WriteString("                            " + renderGreyDark("Examples: 1y, 6mo, 30d, 720h") + "\n")
	writeFlag(&b, "--relays <url>", "Relays for identity proofs (repeatable)")
	b.WriteString("                            " + renderGreyDark("Defaults: relay.primal.net, relay.damus.io, relay.zapstore.dev") + "\n")
	b.WriteString("\n")

	// Other flags
	b.WriteString(renderPurpleBold("OTHER FLAGS") + "\n")
	writeFlag(&b, "-n, --dry-run", "Build event but don't publish")
	writeFlag(&b, "--verbose", "Debug output")
	writeFlag(&b, "--no-color", "Disable colored output")
	writeFlag(&b, "-h, --help", "Show this help")
	b.WriteString("\n")

	// Examples section - comprehensive
	b.WriteString(renderPurpleBold("EXAMPLES") + "\n\n")

	b.WriteString(renderGreyDark("  # Link your Android signing key to your Nostr identity") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --link-key release-key.p12") + "\n\n")

	b.WriteString(renderGreyDark("  # Link with 2-year expiry") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --link-key release-key.p12 --link-key-expiry 2y") + "\n\n")

	b.WriteString(renderGreyDark("  # Preview the event without publishing (dry run)") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --link-key release-key.p12 --dry-run") + "\n\n")

	b.WriteString(renderGreyDark("  # Use custom relays") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --link-key key.p12 --relays wss://my-relay.com") + "\n\n")

	b.WriteString(renderGreyDark("  # Verify that an APK's signing key is linked to a Nostr identity") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --verify downloaded-app.apk") + "\n\n")

	b.WriteString(renderGreyDark("  # Verify using your certificate file") + "\n")
	b.WriteString("  " + renderGreen("zsp identity --verify release-key.p12") + "\n\n")

	// Certificate formats
	b.WriteString(renderPurpleBold("CERTIFICATE FORMATS") + "\n")
	b.WriteString("  " + renderGreen("PKCS12 (.p12, .pfx)") + "   " + renderWhite("Android keystore format (requires password)") + "\n")
	b.WriteString("  " + renderGreen("PEM (.pem, .crt)") + "      " + renderWhite("Certificate + separate key file") + "\n\n")

	b.WriteString(renderGreyDark("  Note: JKS format is not directly supported. Convert with:") + "\n")
	b.WriteString("  " + renderGreen("keytool -importkeystore -srckeystore key.jks -destkeystore key.p12 \\") + "\n")
	b.WriteString("    " + renderGreen("-srcstoretype JKS -deststoretype PKCS12") + "\n\n")

	// How it works
	b.WriteString(renderPurpleBold("HOW IT WORKS") + "\n")
	b.WriteString(renderWhite("  1. Extracts SPKIFP (fingerprint) from your signing certificate") + "\n")
	b.WriteString(renderWhite("  2. Signs a message with your certificate's private key") + "\n")
	b.WriteString(renderWhite("  3. Creates a kind 30509 Nostr event with the proof") + "\n")
	b.WriteString(renderWhite("  4. Signs the event with your Nostr key") + "\n")
	b.WriteString(renderWhite("  5. Publishes to relays for others to verify") + "\n")

	return b.String()
}

// APKHelp returns colorful help for the apk subcommand.
func APKHelp() string {
	var b strings.Builder

	b.WriteString(ui.RenderLogo())
	b.WriteString(renderGreenBold("zsp apk") + " " + renderWhite("- APK utility commands") + "\n")

	b.WriteString(renderPurpleBold("USAGE") + "\n")
	b.WriteString("  " + renderGreen("zsp apk --extract") + " <file.apk>\n\n")

	// Commands
	b.WriteString(renderPurpleBold("COMMANDS") + "\n")
	writeFlag(&b, "--extract <file.apk>", "Extract APK metadata as JSON")
	b.WriteString("                            " + renderGreyDark("Also extracts the app icon to <name>_icon.png") + "\n")
	b.WriteString("\n")

	// Options
	b.WriteString(renderPurpleBold("OPTIONS") + "\n")
	writeFlag(&b, "--verbose", "Debug output")
	writeFlag(&b, "--no-color", "Disable colored output")
	writeFlag(&b, "-h, --help", "Show this help")
	b.WriteString("\n")

	// Examples
	b.WriteString(renderPurpleBold("EXAMPLES") + "\n\n")

	b.WriteString(renderGreyDark("  # Extract metadata from an APK") + "\n")
	b.WriteString("  " + renderGreen("zsp apk --extract myapp.apk") + "\n\n")

	// Output
	b.WriteString(renderPurpleBold("OUTPUT (--extract)") + "\n")
	b.WriteString(renderGreyDark("  {") + "\n")
	b.WriteString(renderGreyDark("    \"package_id\": \"com.example.app\",") + "\n")
	b.WriteString(renderGreyDark("    \"version_name\": \"1.2.3\",") + "\n")
	b.WriteString(renderGreyDark("    \"version_code\": 123,") + "\n")
	b.WriteString(renderGreyDark("    \"min_sdk\": 21,") + "\n")
	b.WriteString(renderGreyDark("    \"target_sdk\": 34,") + "\n")
	b.WriteString(renderGreyDark("    \"label\": \"My App\",") + "\n")
	b.WriteString(renderGreyDark("    \"architectures\": [\"arm64-v8a\", \"armeabi-v7a\"],") + "\n")
	b.WriteString(renderGreyDark("    \"cert_fingerprint\": \"AB:CD:EF:...\",") + "\n")
	b.WriteString(renderGreyDark("    \"sha256\": \"abc123...\",") + "\n")
	b.WriteString(renderGreyDark("    \"icon\": \"myapp_icon.png\"") + "\n")
	b.WriteString(renderGreyDark("  }") + "\n")

	return b.String()
}

// HandleHelp processes help for a command.
func HandleHelp(cmd cli.Command, args []string) {
	// Show command-specific help
	switch cmd {
	case cli.CommandPublish:
		fmt.Fprint(os.Stdout, PublishHelp())
	case cli.CommandIdentity:
		fmt.Fprint(os.Stdout, IdentityHelp())
	case cli.CommandAPK:
		fmt.Fprint(os.Stdout, APKHelp())
	default:
		fmt.Fprint(os.Stdout, RootHelp())
	}
}

// Helper to write a flag line
func writeFlag(b *strings.Builder, flag, desc string) {
	b.WriteString("  " + renderGreen(flag))
	// Pad to align descriptions (min 1 space)
	padding := 26 - len(flag)
	if padding < 1 {
		padding = 1
	}
	b.WriteString(strings.Repeat(" ", padding))
	b.WriteString(renderWhite(desc) + "\n")
}

// Helper to write an example line
func writeExample(b *strings.Builder, cmd, desc string) {
	b.WriteString("  " + renderGreen(cmd))
	// Pad to align descriptions
	padding := 38 - len(cmd)
	if padding > 0 {
		b.WriteString(strings.Repeat(" ", padding))
	}
	b.WriteString(renderGrey(desc) + "\n")
}
