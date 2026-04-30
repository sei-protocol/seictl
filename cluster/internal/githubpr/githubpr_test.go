package githubpr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	mustGit(t, root, "init", "--bare", "--initial-branch=main", origin)
	mustGit(t, root, "clone", origin, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mustGit(t, repo, "checkout", "-B", "main")
	mustGit(t, repo, "add", "README.md")
	mustGit(t, repo, "commit", "-m", "initial")
	mustGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestRefuseLossyCheckout_NoLocalBranch(t *testing.T) {
	repo := gitFixture(t)
	if err := refuseLossyCheckout(repo, "seictl/never-existed"); err != nil {
		t.Errorf("expected no error when branch is absent; got %v", err)
	}
}

func TestRefuseLossyCheckout_BranchUpToDateWithRemote(t *testing.T) {
	repo := gitFixture(t)
	mustGit(t, repo, "checkout", "-b", "seictl/clean", "main")
	mustGit(t, repo, "push", "-u", "origin", "seictl/clean")
	if err := refuseLossyCheckout(repo, "seictl/clean"); err != nil {
		t.Errorf("expected no error for tracked branch with no extra commits; got %v", err)
	}
}

func TestRefuseLossyCheckout_LocalCommitsAheadOfRemote(t *testing.T) {
	repo := gitFixture(t)
	mustGit(t, repo, "checkout", "-b", "seictl/onboard-bdc", "main")
	mustGit(t, repo, "push", "-u", "origin", "seictl/onboard-bdc")
	if err := os.WriteFile(filepath.Join(repo, "extra.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mustGit(t, repo, "add", "extra.txt")
	mustGit(t, repo, "commit", "-m", "local-only")

	err := refuseLossyCheckout(repo, "seictl/onboard-bdc")
	if err == nil {
		t.Fatalf("expected refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "git reflog") || !strings.Contains(msg, "git switch") {
		t.Errorf("error should hint git reflog and git switch; got %q", msg)
	}
}

func TestRefuseLossyCheckout_LocalBranchNeverPushed(t *testing.T) {
	repo := gitFixture(t)
	mustGit(t, repo, "checkout", "-b", "seictl/onboard-bdc", "main")
	if err := os.WriteFile(filepath.Join(repo, "extra.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mustGit(t, repo, "add", "extra.txt")
	mustGit(t, repo, "commit", "-m", "local-only")

	err := refuseLossyCheckout(repo, "seictl/onboard-bdc")
	if err == nil {
		t.Fatalf("expected refusal for never-pushed branch")
	}
	if !strings.Contains(err.Error(), "no remote tracking ref") {
		t.Errorf("error should name the no-remote-tracking-ref case; got %q", err)
	}
}
