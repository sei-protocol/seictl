// Package githubpr wraps the git + gh CLI invocations seictl onboard
// uses to land an engineer cell as a PR against the platform repo.
//
// Production callers stub this whole package via the onboard verb's
// dep seam; tests don't usually need to exercise the shell-out layer.
package githubpr

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options drives a CreatePR run.
type Options struct {
	RepoPath      string // platform repo on disk
	Branch        string // e.g. "seictl/onboard-bdc"
	BaseBranch    string // usually "main"
	CommitMessage string
	PRTitle       string
	PRBody        string
	Files         map[string][]byte // repo-relative path → content
}

// Result is what CreatePR returns on success.
type Result struct {
	Branch string
	URL    string
}

// CheckAuth runs `gh auth status`. seictl onboard refuses to run
// without it because we'd fail at PR creation anyway and leave a
// half-prepared branch behind.
func CheckAuth() error {
	cmd := exec.Command("gh", "auth", "status")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh auth status: %v\n%s", err, out)
	}
	return nil
}

// EnsureBaseUpToDate fetches origin/<baseBranch> and errors if local
// HEAD is missing any of its commits. Onboard reads files from the
// working tree (e.g. the engineers/kustomization.yaml aggregator) and
// includes them in the PR; without this guard, a stale local main
// would silently overwrite a peer's onboard entry with the older
// content.
func EnsureBaseUpToDate(repoPath, baseBranch string) error {
	if _, err := runIn(repoPath, "git", "fetch", "origin", baseBranch); err != nil {
		return fmt.Errorf("git fetch origin %s: %w", baseBranch, err)
	}
	out, err := runIn(repoPath, "git", "rev-list", "--count", "HEAD..origin/"+baseBranch)
	if err != nil {
		return fmt.Errorf("git rev-list HEAD..origin/%s: %w", baseBranch, err)
	}
	behind := strings.TrimSpace(string(out))
	if behind != "0" {
		return fmt.Errorf("local HEAD is %s commit(s) behind origin/%s; run `git pull origin %s` first", behind, baseBranch, baseBranch)
	}
	return nil
}

// CheckCleanTree returns nil if the platform repo has no staged or
// unstaged changes to tracked files. Untracked files are tolerated
// because CreatePR adds explicit paths via `git add <file>`, never `-A`.
func CheckCleanTree(repoPath string) error {
	out, err := runIn(repoPath, "git", "status", "--porcelain=v1")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if isUntrackedOrEmpty(line) {
			continue
		}
		return fmt.Errorf("working tree dirty: %s", line)
	}
	return nil
}

// isUntrackedOrEmpty reports whether a `git status --porcelain` line
// can be skipped. The two-character status prefix is "??" for
// untracked files; everything else with non-space codes is a tracked
// file with staged or unstaged modifications.
func isUntrackedOrEmpty(line string) bool {
	if len(line) < 2 {
		return true
	}
	return line[0] == '?' && line[1] == '?'
}

// CreatePR branches, writes files, commits, pushes, and opens a PR.
// Idempotent against a prior partial run: a remote branch with an open
// PR returns that PR's URL with no further mutation.
func CreatePR(opts Options) (*Result, error) {
	if opts.BaseBranch == "" {
		opts.BaseBranch = "main"
	}

	if existing, err := findOpenPR(opts.RepoPath, opts.Branch); err == nil && existing != "" {
		return &Result{Branch: opts.Branch, URL: existing}, nil
	}

	if _, err := runIn(opts.RepoPath, "git", "fetch", "origin", opts.BaseBranch); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	if _, err := runIn(opts.RepoPath, "git", "checkout", "-B", opts.Branch, "origin/"+opts.BaseBranch); err != nil {
		return nil, fmt.Errorf("git checkout branch: %w", err)
	}

	addArgs := []string{"add"}
	for path, body := range opts.Files {
		full := filepath.Join(opts.RepoPath, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", full, err)
		}
		addArgs = append(addArgs, path)
	}
	if _, err := runIn(opts.RepoPath, "git", addArgs...); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}
	if _, err := runIn(opts.RepoPath, "git", "commit", "-m", opts.CommitMessage); err != nil {
		return nil, fmt.Errorf("git commit: %w", err)
	}
	if _, err := runIn(opts.RepoPath, "git", "push", "-u", "origin", opts.Branch); err != nil {
		return nil, fmt.Errorf("git push: %w", err)
	}

	urlOut, err := runIn(opts.RepoPath, "gh", "pr", "create",
		"--title", opts.PRTitle,
		"--body", opts.PRBody,
		"--base", opts.BaseBranch,
		"--head", opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("gh pr create: %w", err)
	}
	return &Result{Branch: opts.Branch, URL: strings.TrimSpace(string(urlOut))}, nil
}

// findOpenPR returns the PR URL if a prior onboard run already opened
// one for this branch; empty string otherwise.
func findOpenPR(repoPath, branch string) (string, error) {
	out, err := runIn(repoPath, "gh", "pr", "list",
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--jq", ".[0].url // empty")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DiscoverRepo walks up from start looking for a directory that
// contains both `.git/` and `clusters/harbor/`. Returns the absolute
// path or an error if no marker is found by the filesystem root.
func DiscoverRepo(start string) (string, error) {
	cur, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		git := filepath.Join(cur, ".git")
		harbor := filepath.Join(cur, "clusters", "harbor")
		if isDir(git) && isDir(harbor) {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("platform repo not found (no .git + clusters/harbor/ ancestor of " + start + ")")
		}
		cur = parent
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func runIn(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}
