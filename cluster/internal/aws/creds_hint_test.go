package aws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredsHint(t *testing.T) {
	// Isolate from the developer's real ~/.aws/config and AWS_PROFILE.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AWS_PROFILE", "")
	awsDir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(awsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Run("AWS_PROFILE set takes precedence", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "sei")
		got := CredsHint()
		if !strings.Contains(got, "AWS_PROFILE=sei") || !strings.Contains(got, "aws sso login --profile sei") {
			t.Errorf("hint should reference the set profile and login command; got %q", got)
		}
	})

	t.Run("no profile, single named profile in config", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "")
		writeConfig(t, awsDir, `[profile sei]
sso_region = us-west-1
`)
		got := CredsHint()
		if !strings.Contains(got, `"sei"`) || !strings.Contains(got, "AWS_PROFILE=sei") {
			t.Errorf("hint should suggest the only profile; got %q", got)
		}
	})

	t.Run("no profile, multiple profiles in config", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "")
		writeConfig(t, awsDir, `[profile alpha]
[profile beta]
[sso-session ignored]
[default]
`)
		got := CredsHint()
		// All three (alpha, beta, default) should appear, sso-session should not.
		if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") || !strings.Contains(got, "default") {
			t.Errorf("hint should list named profiles + default; got %q", got)
		}
		if strings.Contains(got, "ignored") {
			t.Errorf("hint should skip sso-session entries; got %q", got)
		}
	})

	t.Run("no config file at all", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "")
		_ = os.Remove(filepath.Join(awsDir, "config"))
		got := CredsHint()
		if !strings.Contains(got, "no AWS credentials") {
			t.Errorf("hint should explain the empty state; got %q", got)
		}
	})
}

func writeConfig(t *testing.T, awsDir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(awsDir, "config"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
