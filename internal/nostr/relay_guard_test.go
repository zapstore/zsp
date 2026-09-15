package nostr

import (
	"strconv"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
)

func TestHighestLinkedReleaseVersionUsesMainLinkedAssetsOnly(t *testing.T) {
	secret := gonostr.GeneratePrivateKey()
	pubkey, err := gonostr.GetPublicKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	makeAsset := func(versionCode int64) *gonostr.Event {
		event := &gonostr.Event{
			Kind:      KindSoftwareAsset,
			PubKey:    pubkey,
			CreatedAt: 10,
			Tags: gonostr.Tags{
				{"i", "com.example.app"},
				{"version_code", strconv.FormatInt(versionCode, 10)},
				{"apk_certificate_hash", "certificate-hash"},
			},
		}
		if err := event.Sign(secret); err != nil {
			t.Fatal(err)
		}
		return event
	}
	makeRelease := func(channel string, createdAt gonostr.Timestamp, asset *gonostr.Event) *gonostr.Event {
		event := &gonostr.Event{
			Kind:      KindRelease,
			PubKey:    pubkey,
			CreatedAt: createdAt,
			Tags: gonostr.Tags{
				{"i", "com.example.app"},
				{"c", channel},
				{"e", asset.ID},
			},
		}
		if err := event.Sign(secret); err != nil {
			t.Fatal(err)
		}
		return event
	}

	linked := makeAsset(42)
	unlinked := makeAsset(900)
	betaAsset := makeAsset(1000)
	malformedAsset := makeAsset(2000)
	malformedAsset.Tags = append(malformedAsset.Tags, gonostr.Tag{"version_code", "2000"})
	if err := malformedAsset.Sign(secret); err != nil {
		t.Fatal(err)
	}
	mainRelease := makeRelease("main", 20, linked)
	betaRelease := makeRelease("beta", 30, betaAsset)
	malformedRelease := makeRelease("main", 40, malformedAsset)
	events := map[string]*gonostr.Event{
		linked.ID:           linked,
		unlinked.ID:         unlinked,
		betaAsset.ID:        betaAsset,
		malformedAsset.ID:   malformedAsset,
		mainRelease.ID:      mainRelease,
		betaRelease.ID:      betaRelease,
		malformedRelease.ID: malformedRelease,
	}

	versionCode, publishedAt := highestLinkedReleaseVersion(events, "certificate-hash", "main")
	if versionCode != 42 {
		t.Fatalf("versionCode = %d, want 42", versionCode)
	}
	if want := time.Unix(20, 0); !publishedAt.Equal(want) {
		t.Fatalf("publishedAt = %v, want %v", publishedAt, want)
	}

	versionCode, publishedAt = highestLinkedReleaseVersion(events, "certificate-hash", "beta")
	if versionCode != 1000 {
		t.Fatalf("beta versionCode = %d, want 1000", versionCode)
	}
	if want := time.Unix(30, 0); !publishedAt.Equal(want) {
		t.Fatalf("beta publishedAt = %v, want %v", publishedAt, want)
	}
}
