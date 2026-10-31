package release

import (
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	internalnostr "github.com/zapstore/zsp/v2/internal/nostr"
)

func TestOtherApplicationAuthor(t *testing.T) {
	const app = "com.example.app"
	app32267 := func(pubkey, d string, at int64) *gonostr.Event {
		return &gonostr.Event{PubKey: pubkey, Kind: internalnostr.KindAppMetadata, CreatedAt: gonostr.Timestamp(at), Tags: gonostr.Tags{{"d", d}}}
	}
	tests := []struct {
		name   string
		events []*gonostr.Event
		want   string
	}{
		{"none", nil, ""},
		{"own only", []*gonostr.Event{app32267("me", app, 1)}, ""},
		{"another app", []*gonostr.Event{app32267("dev", "com.other", 1)}, ""},
		{"developer", []*gonostr.Event{app32267("me", app, 5), app32267("dev", app, 1)}, "dev"},
		{"newest of several", []*gonostr.Event{app32267("a", app, 1), app32267("b", app, 2)}, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := otherApplicationAuthor(tt.events, app, "me"); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
