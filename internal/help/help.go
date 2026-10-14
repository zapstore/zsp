// Package help provides CLI help output.
package help

import (
	"fmt"
	"os"
	"strings"

	"github.com/zapstore/zsp/internal/cli"
	"github.com/zapstore/zsp/internal/ui"
)

func renderBold(s string) string {
	return ui.Bold(s)
}

func renderAccent(s string) string {
	return ui.Success(s)
}

func renderWhite(s string) string {
	return ui.Title(s)
}

func renderGrey(s string) string {
	return ui.Info(s)
}

func renderGreyDark(s string) string {
	return ui.Dim(s)
}

func renderURL(s string) string {
	return ui.Success(s)
}

// RootHelp returns the top-level --help output.
func RootHelp() string {
	var b strings.Builder

	b.WriteString(renderBold("zsp") + " " + renderWhite("— Zapstore Android app publishing tool") + "\n")

	b.WriteString(renderBold("USAGE") + "\n")
	b.WriteString("  " + renderAccent("zsp") + " <command> [options]\n\n")

	b.WriteString(renderBold("COMMANDS") + "\n")
	b.WriteString("  " + renderAccent("check") + "       " + renderWhite("Verify the APK and show listing, proof, and next step (read-only)") + "\n")
	b.WriteString("  " + renderAccent("publish") + "     " + renderWhite("Publish APK releases to Nostr relays") + "\n")
	b.WriteString("  " + renderAccent("skill") + "       " + renderWhite("Print the agent skill for writing zapstore.yaml") + "\n\n")

	b.WriteString(renderBold("EXAMPLES") + "\n")
	writeExample(&b, "zsp", "Start the interactive app setup wizard")
	writeExample(&b, "zsp check", "Check zapstore.yaml: listing, proof, next step")
	writeExample(&b, "zsp check app-release.apk", "Check one local APK")
	writeExample(&b, "zsp publish", "Publish from zapstore.yaml")
	writeExample(&b, "zsp publish -r github.com/org/repo", "Override repository in zapstore.yaml")
	writeExample(&b, "zsp publish -s ./app-release.apk", "Override release_source in zapstore.yaml")
	b.WriteString("\n")

	b.WriteString(renderBold("ENVIRONMENT") + "\n")
	b.WriteString("  " + renderAccent("SIGN_WITH") + "       " + renderWhite("Signing method (nsec1..., hex, bunker://)") + "\n")
	b.WriteString("  " + renderAccent("GITHUB_TOKEN") + "    " + renderWhite("GitHub API token (optional, avoids rate limits)") + "\n")
	b.WriteString("  " + renderAccent("RELAYS") + "          " + renderWhite("WebSocket URLs or a Unix socket path (default: wss://relay.zapstore.dev)") + "\n")
	b.WriteString("  " + renderAccent("BLOSSOM_URL") + "     " + renderWhite("Custom CDN server (default: https://cdn.zapstore.dev)") + "\n")
	b.WriteString("  " + renderAccent("APK_CACHE") + "      " + renderWhite("Copy each accepted APK here, named by its SHA-256") + "\n")
	b.WriteString("  " + renderAccent("HOTBOX_SOCKET") + "   " + renderWhite("Hotbox socket; with HOTBOX_PIN, publish creates the C1 proof") + "\n\n")

	b.WriteString(renderBold("GLOBAL FLAGS") + "\n")
	b.WriteString("  " + renderAccent("-h, --help") + "      " + renderWhite("Show help") + "\n")
	b.WriteString("  " + renderAccent("-v, --version") + "   " + renderWhite("Show version") + "\n")
	b.WriteString("  " + renderAccent("--verbose") + "       " + renderWhite("Debug output") + "\n")
	b.WriteString("  " + renderAccent("--no-color") + "      " + renderWhite("Disable colored output") + "\n")
	b.WriteString("  " + renderAccent("--json") + "          " + renderWhite("JSON output (default when stdout is not a terminal)") + "\n\n")

	b.WriteString(renderBold("EXIT CODES") + "\n")
	b.WriteString("  " + renderAccent("0") + "   Success\n")
	b.WriteString("  " + renderAccent("1") + "   Error\n")
	b.WriteString("  " + renderAccent("130") + " Cancelled (Ctrl+C)\n\n")

	b.WriteString(renderBold("MORE INFO") + "\n")
	b.WriteString("  " + renderAccent("zsp") + "                         " + renderWhite("Developer onboarding") + "\n")
	b.WriteString("  " + renderAccent("zsp publish --help") + "    " + renderWhite("Detailed publish help") + "\n")
	b.WriteString("  " + renderURL("https://github.com/zapstore/zsp") + "\n")

	return b.String()
}

// PublishHelp returns colorful help for the publish subcommand.
func PublishHelp() string {
	var b strings.Builder

	b.WriteString(renderBold("zsp publish") + " " + renderWhite("— Publish APK releases to Nostr relays") + "\n")
	b.WriteString("\n")

	b.WriteString(renderBold("USAGE") + "\n")
	b.WriteString("  " + renderAccent("zsp publish") + " [options] [zapstore.yaml]\n\n")

	b.WriteString(renderGreyDark("  With no argument, loads zapstore.yaml from the current directory.") + "\n")
	b.WriteString(renderGreyDark("  A config path loads that file instead.") + "\n")
	b.WriteString(renderGreyDark("  A local APK belongs in release_source. Source flags override the loaded file.") + "\n\n")

	// Source flags
	b.WriteString(renderBold("SOURCE FLAGS") + "\n")
	writeFlag(&b, "-r <url>", "Source code repository URL (GitHub/GitLab/Codeberg/Gitea)")
	b.WriteString("                            " + renderGreyDark("Also fetches releases from here unless -s is specified") + "\n")
	writeFlag(&b, "-s <url>", "Release/download source URL (F-Droid, web page, etc)")
	b.WriteString("                            " + renderGreyDark("Use alone (no -r) for closed-source apps") + "\n")
	writeFlag(&b, "-m <source>", "Fetch metadata from source (repeatable: -m fastlane -m github)")
	b.WriteString("                            " + renderGreyDark("Fastlane is tried automatically for GitHub/GitLab/Codeberg repositories") + "\n")
	writeFlag(&b, "--match <pattern>", "Regex pattern to filter APK assets (rarely needed)")
	writeFlag(&b, "--release-filter <pattern>", "Regex pattern to filter source releases")
	writeFlag(&b, "--apk-hash <hash>", "Select one verified APK when several candidates match")
	b.WriteString("\n")

	// Release-specific flags (CLI only)
	b.WriteString(renderBold("RELEASE FLAGS") + "\n")
	writeFlag(&b, "--channel <channel>", "Publish to a release channel (default: main)")
	writeFlag(&b, "--commit <hash>", "Git commit hash for reproducible builds")
	b.WriteString("\n")

	// Behavior flags
	b.WriteString(renderBold("BEHAVIOR FLAGS") + "\n")
	writeFlag(&b, "-q, --quiet", "No progress output and no preview")
	writeFlag(&b, "--skip-preview", "Skip the browser preview")
	writeFlag(&b, "--port <port>", "Custom port for browser preview")
	writeFlag(&b, "--no-compress", "Preserve original icon and screenshot bytes")
	writeFlag(&b, "--skip-app-event", "Publish only release events, skip kind 32267 app metadata")
	b.WriteString("\n")

	// Source behavior flags
	b.WriteString(renderBold("SOURCE BEHAVIOR FLAGS") + "\n")
	writeFlag(&b, "--prerelease-channel <channel>", "Fetch releases from a prerelease channel")
	b.WriteString("\n")

	b.WriteString(renderBold("VALIDATION FLAGS") + "\n")
	writeFlag(&b, "--overwrite-release", "Allow replacing an equal version_code; never allows a downgrade")
	writeFlag(&b, "--overwrite-app-event", "Publish kind 32267 even if the application event is unchanged")
	writeFlag(&b, "--app-created-at-release", "Use release date for kind 32267 created_at")
	writeFlag(&b, "--skip-metadata", "Skip fetching metadata from external sources")
	b.WriteString("\n")

	// Other flags
	b.WriteString(renderBold("OTHER FLAGS") + "\n")
	writeFlag(&b, "--json", "JSON output (default when stdout is not a terminal)")
	writeFlag(&b, "--verbose", "Debug output")
	writeFlag(&b, "--no-color", "Disable colored output")
	writeFlag(&b, "-h, --help", "Show this help")
	b.WriteString("\n")

	// Examples section - comprehensive
	b.WriteString(renderBold("EXAMPLES") + "\n\n")

	b.WriteString(renderGreyDark("  # Publish from config file") + "\n")
	b.WriteString("  " + renderAccent("zsp publish zapstore.yaml") + "\n\n")

	b.WriteString(renderGreyDark("  # Publish the APK named by release_source") + "\n")
	b.WriteString("  " + renderAccent("zsp publish") + "\n\n")

	b.WriteString(renderGreyDark("  # Override the repository in zapstore.yaml") + "\n")
	b.WriteString("  " + renderAccent("zsp publish -r github.com/AeonBTC/mempal") + "\n\n")

	b.WriteString(renderGreyDark("  # Override release_source with a local APK") + "\n")
	b.WriteString("  " + renderAccent("zsp publish -s ./app-release.apk") + "\n\n")

	b.WriteString(renderGreyDark("  # Override repository and release source") + "\n")
	b.WriteString("  " + renderAccent("zsp publish -r github.com/user/app -s f-droid.org/packages/com.example") + "\n\n")

	b.WriteString(renderGreyDark("  # Select one APK when several candidates verify") + "\n")
	b.WriteString("  " + renderAccent("zsp publish zapstore.yaml --apk-hash <hash>") + "\n\n")

	b.WriteString(renderGreyDark("  # Replace an existing release with the same Android version code") + "\n")
	b.WriteString("  " + renderAccent("zsp publish zapstore.yaml --overwrite-release") + "\n\n")

	b.WriteString(renderGreyDark("  # Publish from CI/CD") + "\n")
	b.WriteString("  " + renderAccent("zsp publish -q") + "\n\n")

	// Config section
	b.WriteString(renderBold("CONFIGURATION") + "\n")
	b.WriteString(renderGreyDark("  Config files are YAML. Minimal example:") + "\n\n")
	b.WriteString("  " + renderAccent("repository:") + "     " + renderWhite("https://github.com/user/app") + "\n")
	b.WriteString("  " + renderAccent("release_source:") + " " + renderWhite("./app-release.apk") + "\n\n")
	b.WriteString(renderGreyDark("  Full example with all options:") + "\n\n")
	b.WriteString("  " + renderAccent("repository:") + "      " + renderWhite("https://github.com/user/app") + "\n")
	b.WriteString("  " + renderAccent("name:") + "            " + renderWhite("My App") + "\n")
	b.WriteString("  " + renderAccent("summary:") + "         " + renderWhite("A short description") + "\n")
	b.WriteString("  " + renderAccent("icon:") + "            " + renderWhite("./assets/icon.png") + "\n")
	b.WriteString("  " + renderAccent("images:") + "\n")
	b.WriteString("    " + renderAccent("-") + " " + renderWhite("./screenshots/1.png") + "\n")
	b.WriteString("  " + renderAccent("tags:") + "            " + renderWhite("[productivity, nostr]") + "\n")
	b.WriteString("  " + renderAccent("match:") + "           " + renderWhite("'.*arm64.*\\.apk$'") + "\n")
	b.WriteString("  " + renderAccent("release_notes:") + "   " + renderWhite("./CHANGELOG.md") + "\n\n")

	b.WriteString(renderGreyDark("  Default config file: ") + renderWhite("./zapstore.yaml") + "\n\n")

	b.WriteString(renderBold("EXIT CODES") + "\n")
	b.WriteString("  " + renderAccent("0") + "   Success\n")
	b.WriteString("  " + renderAccent("1") + "   Error (config invalid, source unreachable, signing failed, etc.)\n")
	b.WriteString("  " + renderAccent("130") + " Cancelled (Ctrl+C)\n")

	return b.String()
}

// CheckHelp returns help for the check subcommand.
func CheckHelp() string {
	var b strings.Builder

	b.WriteString(renderBold("zsp check") + " " + renderWhite("— Verify an APK and show what Zapstore knows about it") + "\n\n")

	b.WriteString(renderBold("USAGE") + "\n")
	b.WriteString("  " + renderAccent("zsp check") + " [options] [zapstore.yaml | app.apk]\n\n")

	b.WriteString(renderGreyDark("  Finds and verifies the APK, then reports whether the app is listed, which") + "\n")
	b.WriteString(renderGreyDark("  proof is active, the published version code, whether SIGN_WITH may publish,") + "\n")
	b.WriteString(renderGreyDark("  and the next step. Signs, uploads, and publishes nothing.") + "\n\n")

	b.WriteString(renderBold("SOURCE FLAGS") + "\n")
	writeFlag(&b, "-r <url>", "Source code repository URL")
	writeFlag(&b, "-s <url>", "Release/download source URL")
	writeFlag(&b, "--match <pattern>", "Regex pattern to filter APK assets")
	writeFlag(&b, "--release-filter <pattern>", "Regex pattern to filter source releases")
	writeFlag(&b, "--prerelease-channel <channel>", "Fetch releases from a prerelease channel")
	writeFlag(&b, "--channel <channel>", "Release channel to compare against (default: main)")
	writeFlag(&b, "--apk-hash <hash>", "Select one verified APK when several candidates match")
	b.WriteString("\n")

	b.WriteString(renderBold("OTHER FLAGS") + "\n")
	writeFlag(&b, "--json", "JSON output (default when stdout is not a terminal)")
	writeFlag(&b, "--verbose", "Debug output")
	writeFlag(&b, "--no-color", "Disable colored output")
	writeFlag(&b, "-h, --help", "Show this help")
	b.WriteString("\n")

	b.WriteString(renderBold("EXAMPLES") + "\n\n")
	b.WriteString(renderGreyDark("  # Check the app in zapstore.yaml") + "\n")
	b.WriteString("  " + renderAccent("zsp check") + "\n\n")
	b.WriteString(renderGreyDark("  # Check a local build") + "\n")
	b.WriteString("  " + renderAccent("zsp check app-release.apk") + "\n\n")

	b.WriteString(renderBold("EXIT CODES") + "\n")
	b.WriteString("  " + renderAccent("0") + "   Checked (see next_step)\n")
	b.WriteString("  " + renderAccent("1") + "   Error (invalid APK, source unreachable, relays unreachable)\n")
	b.WriteString("  " + renderAccent("130") + " Cancelled (Ctrl+C)\n")

	return b.String()
}

// HandleHelp processes help for a command.
func HandleHelp(cmd cli.Command, args []string) {
	if cmd == cli.CommandNone && len(args) > 0 {
		cmd = cli.Command(args[0])
	}
	switch cmd {
	case cli.CommandPublish:
		fmt.Fprint(os.Stdout, PublishHelp())
	case cli.CommandCheck:
		fmt.Fprint(os.Stdout, CheckHelp())
	default:
		fmt.Fprint(os.Stdout, RootHelp())
	}
}

// Helper to write a flag line
func writeFlag(b *strings.Builder, flag, desc string) {
	b.WriteString("  " + renderAccent(flag))
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
	b.WriteString("  " + renderAccent(cmd))
	// Pad to align descriptions
	padding := 38 - len(cmd)
	if padding > 0 {
		b.WriteString(strings.Repeat(" ", padding))
		b.WriteString(renderGrey(desc) + "\n")
		return
	}
	b.WriteString("\n      " + renderGrey(desc) + "\n")
}
