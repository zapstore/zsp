package release

import (
	"regexp"
	"strings"
)

// changelogVersionPattern matches a Keep a Changelog version heading label:
// Unreleased, or a version such as 1.2.3, 1.0.0-rc2, or v1.2.3.
var changelogVersionPattern = regexp.MustCompile(`(?i)^(?:unreleased|v?\d[\w.+-]*)$`)

// changelogLinkRef matches a CommonMark link reference definition. Keep a
// Changelog puts those at the bottom of the file; they are not release notes.
var changelogLinkRef = regexp.MustCompile(`^[ ]{0,3}\[[^\]\n]+\]:[ \t]+\S`)

type fenceState struct {
	char   byte
	length int
}

// releaseNotesFromChangelog returns text unchanged when it is not a Keep a
// Changelog file. Otherwise it returns the matching version section, or a
// warning when that section is absent.
func releaseNotesFromChangelog(text, versionName, releaseVersion string) (string, string) {
	section, isChangelog, found := changelogSection(text, releaseVersion, versionName)
	if !isChangelog {
		return text, ""
	}
	if !found {
		return "", changelogMissWarning(versionName, releaseVersion)
	}
	return section, ""
}

// changelogSection returns the body of the first matching version heading.
// Versions are tried in order. A file with no version headings is returned
// unchanged and isChangelog is false.
func changelogSection(text string, versions ...string) (section string, isChangelog, found bool) {
	normalized := strings.TrimPrefix(text, "\uFEFF")
	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	type heading struct {
		version string
		line    int
	}
	var headings []heading
	var boundaries []int
	var fence fenceState
	for i, line := range lines {
		if fence.length > 0 {
			if closesFence(line, fence) {
				fence = fenceState{}
			}
			continue
		}
		if char, length, _ := fenceMark(line); length > 0 {
			fence = fenceState{char: char, length: length}
			continue
		}
		level, label, ok := atxHeading(line)
		if !ok || level > 2 {
			continue
		}
		boundaries = append(boundaries, i)
		version, ok := changelogVersion(label)
		if !ok {
			continue
		}
		headings = append(headings, heading{version: version, line: i})
	}
	if len(headings) == 0 {
		return text, false, false
	}
	for _, want := range versions {
		if normalizeChangelogVersion(want) == "" {
			continue
		}
		for _, heading := range headings {
			if !changelogVersionsEqual(heading.version, want) {
				continue
			}
			end := len(lines)
			for _, boundary := range boundaries {
				if boundary > heading.line {
					end = boundary
					break
				}
			}
			return trimChangelogBody(lines[heading.line+1 : end]), true, true
		}
	}
	return "", true, false
}

func changelogMissWarning(versionName, releaseVersion string) string {
	var parts []string
	seen := map[string]struct{}{}
	for _, version := range []string{releaseVersion, versionName} {
		key := normalizeChangelogVersion(version)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, strings.TrimSpace(version))
	}
	if len(parts) == 0 {
		return "changelog has no version to match"
	}
	return "no changelog section for " + strings.Join(parts, " or ")
}

func changelogVersionsEqual(heading, want string) bool {
	left := normalizeChangelogVersion(heading)
	right := normalizeChangelogVersion(want)
	return left != "" && left == right
}

func normalizeChangelogVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) >= 2 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		version = version[1:]
	}
	return strings.ToLower(version)
}

func changelogVersion(label string) (string, bool) {
	if !strings.HasPrefix(label, "[") {
		return "", false
	}
	end := strings.IndexByte(label, ']')
	if end < 0 {
		return "", false
	}
	version := strings.TrimSpace(label[1:end])
	if version == "" || strings.ContainsAny(version, " \t") || !changelogVersionPattern.MatchString(version) {
		return "", false
	}
	return version, true
}

func atxHeading(line string) (level int, text string, ok bool) {
	line = strings.TrimRight(line, "\r")
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) {
		return 0, "", false
	}
	rest := line[indent:]
	for level < len(rest) && rest[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(rest) {
		return 0, "", false
	}
	if rest[level] != ' ' && rest[level] != '\t' {
		return 0, "", false
	}
	return level, strings.TrimSpace(rest[level:]), true
}

func trimChangelogBody(lines []string) string {
	end := len(lines)
	for end > 0 {
		line := strings.TrimRight(lines[end-1], " \t\r")
		if line == "" || changelogLinkRef.MatchString(line) {
			end--
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}

func fenceMark(line string) (char byte, length int, hasInfo bool) {
	line = strings.TrimRight(line, "\r")
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) {
		return 0, 0, false
	}
	char = line[indent]
	if char != '`' && char != '~' {
		return 0, 0, false
	}
	end := indent
	for end < len(line) && line[end] == char {
		end++
	}
	length = end - indent
	if length < 3 {
		return 0, 0, false
	}
	info := strings.TrimSpace(line[end:])
	if char == '`' && strings.Contains(info, "`") {
		return 0, 0, false
	}
	return char, length, info != ""
}

func closesFence(line string, open fenceState) bool {
	char, length, hasInfo := fenceMark(line)
	return char == open.char && length >= open.length && !hasInfo
}
