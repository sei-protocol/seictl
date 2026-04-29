package aws

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CredsHint returns a one-line remediation for the "no AWS credentials
// resolvable" case. It reads AWS_PROFILE and ~/.aws/config so the message
// names a profile the engineer can actually use, instead of a generic
// "Unable to locate credentials".
func CredsHint() string {
	if p := os.Getenv("AWS_PROFILE"); p != "" {
		return fmt.Sprintf("AWS_PROFILE=%s is set but credentials don't resolve; try `aws sso login --profile %s`", p, p)
	}
	profiles := readAWSConfigProfiles()
	switch len(profiles) {
	case 0:
		return "no AWS credentials and no profiles in ~/.aws/config; configure SSO via `aws configure sso`"
	case 1:
		return fmt.Sprintf("no default profile; ~/.aws/config has profile %q — try `export AWS_PROFILE=%s && aws sso login --profile %s`",
			profiles[0], profiles[0], profiles[0])
	default:
		return fmt.Sprintf("no default profile; ~/.aws/config has profiles %v — set AWS_PROFILE to one and `aws sso login --profile <name>`",
			profiles)
	}
}

// readAWSConfigProfiles returns the profile names from ~/.aws/config in
// stable (alphabetical) order. Section types other than `default` /
// `profile X` (e.g. `sso-session X`) are skipped — they're not selectable
// via AWS_PROFILE.
func readAWSConfigProfiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(home, ".aws", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		header := strings.TrimSpace(line[1 : len(line)-1])
		switch {
		case header == "default":
			seen["default"] = struct{}{}
		case strings.HasPrefix(header, "profile "):
			seen[strings.TrimSpace(strings.TrimPrefix(header, "profile "))] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
