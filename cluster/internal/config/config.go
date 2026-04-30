// Package config manages ~/.seictl/config.json — the per-engineer
// alias and operating namespace seictl reads on every cluster-facing
// invocation.
//
//   - File mode 0600, parent dir 0700.
//   - Read refuses to proceed if perms are loose.
//   - Two fields: alias, namespace.
//
// Convention enforcement (e.g. namespace = "eng-<alias>" for engineer
// cells) lives in the writer side — `seictl onboard`. Verbs read
// namespace verbatim so non-engineer flows (nightly, CI) can drop a
// shim with whatever namespace they operate against.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/validate"
)

const (
	FileMode os.FileMode = 0o600
	DirMode  os.FileMode = 0o700
)

type Config struct {
	Alias     string `json:"alias"`
	Namespace string `json:"namespace"`
}

// DefaultPath returns ~/.seictl/config.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".seictl", "config.json"), nil
}

// Read returns the config at path. Refuses if the file has any group or
// world permission bits set.
func Read(path string) (*Config, *clioutput.Error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMissing,
				"seictl config not found at %s; run `seictl onboard --alias <alias>`", path)
		}
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatPermsLoose,
			"seictl config file %s has loose permissions %#o; expected 0600", path, mode)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"read %s: %v", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"parse %s: %v", path, err)
	}
	if c.Alias == "" {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"seictl config is missing required field: alias").WithDetail(path)
	}
	if vErr := validate.Alias(c.Alias); vErr != nil {
		return nil, vErr.ExitWith(clioutput.ExitIdentity).WithDetail(path)
	}
	if c.Namespace == "" {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"seictl config is missing required field: namespace").WithDetail(path)
	}
	if vErr := validate.Namespace(c.Namespace); vErr != nil {
		return nil, vErr.ExitWith(clioutput.ExitIdentity).WithDetail(path)
	}
	return &c, nil
}

// Write atomically writes the config to path with mode 0600 and ensures
// the parent directory is 0700. Refuses if an existing parent directory
// has loose perms — silently tightening would mask a security concern.
func Write(path string, c Config) *clioutput.Error {
	if c.Alias == "" {
		return clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"alias is required")
	}
	if c.Namespace == "" {
		return clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"namespace is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"create %s: %v", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"stat %s: %v", dir, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatPermsLoose,
			"directory %s has loose permissions %#o; tighten to 0700 and re-run", dir, mode)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"marshal config: %v", err)
	}
	raw = append(raw, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, FileMode); err != nil {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"write %s: %v", tmp, err)
	}
	if err := os.Chmod(tmp, FileMode); err != nil {
		_ = os.Remove(tmp)
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatPermsLoose,
			"set %s mode %#o: %v", tmp, FileMode, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"rename %s -> %s: %v", tmp, path, err)
	}
	return nil
}
