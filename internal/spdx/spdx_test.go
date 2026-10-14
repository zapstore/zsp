package spdx

import "testing"

func TestID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"MIT", "MIT"},
		{"mit", "MIT"},
		{"MIT License", "MIT"},
		{"apache-2.0", "Apache-2.0"},
		{"Apache License 2.0", "Apache-2.0"},
		{"gpl-3.0", "GPL-3.0"},
		{"GPL-3.0-only", "GPL-3.0-only"},
		{"GNU General Public License v3.0 only", "GPL-3.0-only"},
		{"GNU General Public License v3.0 or later", "GPL-3.0-or-later"},
		{"MIT, Apache-2.0", "MIT, Apache-2.0"},
		{"MIT OR Apache-2.0", "MIT, Apache-2.0"},
		{"MIT, not-a-license", ""},
		{"NOASSERTION", ""},
		{"other", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ID(tt.in); got != tt.want {
				t.Errorf("ID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
