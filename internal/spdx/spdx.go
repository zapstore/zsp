// Package spdx matches license text to the SPDX license list.
package spdx

import "strings"

// ID returns the canonical SPDX identifier for raw. raw may be an identifier
// or a license name, in any case. Several values separated by commas or by
// OR/AND are each matched, and every part must be known. An unknown value
// returns "".
func ID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if id, ok := one(raw); ok {
		return id
	}
	parts := splitList(raw)
	if len(parts) < 2 {
		return ""
	}
	ids := make([]string, len(parts))
	for i, part := range parts {
		id, ok := one(part)
		if !ok {
			return ""
		}
		ids[i] = id
	}
	return strings.Join(ids, ", ")
}

func one(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if id, ok := licenseIDs[key]; ok {
		return id, true
	}
	if id, ok := licenseNames[key]; ok {
		return id, true
	}
	return "", false
}

// splitList separates a license expression into parts. A name that itself
// contains "or" is matched whole by ID before this runs.
func splitList(raw string) []string {
	normalized := raw
	for _, op := range []string{" OR ", " AND ", " or ", " and "} {
		normalized = strings.ReplaceAll(normalized, op, ",")
	}
	var parts []string
	for _, part := range strings.Split(normalized, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}
