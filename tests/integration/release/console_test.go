//go:build release && !race

package release

import "testing"

func TestStripANSIKeepsCarriageReturnAsNewline(t *testing.T) {
	got := stripANSI("redraw\rSIGN_WITH='bunker://abc'\n")
	want := "redraw\nSIGN_WITH='bunker://abc'\n"
	if got != want {
		t.Fatalf("stripANSI = %q, want %q", got, want)
	}
}
