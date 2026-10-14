package help

import (
	"strings"
	"testing"
)

func TestRootHelp_ReferencesWizard(t *testing.T) {
	out := RootHelp()
	if !strings.Contains(out, "Start the interactive app setup wizard") {
		t.Error("RootHelp() should reference the wizard as the identity onboarding path")
	}
	if strings.Contains(out, "identity create") {
		t.Error("RootHelp() should not expose the removed identity command")
	}
}

func TestRootHelp_DocumentsEnvironment(t *testing.T) {
	out := RootHelp()
	for _, want := range []string{"nsec1... or bunker://", "GITHUB_TOKEN", "GITEA_TOKEN", "GITEA_HOST", "GITLAB_TOKEN", "GITLAB_HOST"} {
		if !strings.Contains(out, want) {
			t.Errorf("RootHelp() does not mention %q", want)
		}
	}
	if strings.Contains(out, "hex,") {
		t.Error("RootHelp() lists hex private keys, which SIGN_WITH no longer accepts")
	}
}
