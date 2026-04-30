// Package identity manages ~/.seictl/engineer.json — the engineer's
// alias and display name used to scope cluster-facing seictl commands.
//
// Per docs/design/cluster-cli.md §Identity file:
//   - File mode 0600, parent dir 0700.
//   - Read refuses to proceed if perms are loose.
//   - Two fields: alias, name.
package identity

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

type Engineer struct {
	Alias string `json:"alias"`
	Name  string `json:"name"`
}

// DefaultPath returns ~/.seictl/engineer.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".seictl", "engineer.json"), nil
}

// Read returns the engineer record at path. Refuses if the file has any
// group or world permission bits set.
func Read(path string) (*Engineer, *clioutput.Error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMissing,
				"engineer identity not found at %s; run `seictl onboard --alias <alias>`", path)
		}
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatPermsLoose,
			"engineer identity file %s has loose permissions %#o; expected 0600", path, mode)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"read %s: %v", path, err)
	}
	var e Engineer
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"parse %s: %v", path, err)
	}
	if e.Alias == "" {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"engineer identity is missing required field: alias").WithDetail(path)
	}
	if vErr := validate.Alias(e.Alias); vErr != nil {
		return nil, vErr.ExitWith(clioutput.ExitIdentity).WithDetail(path)
	}
	return &e, nil
}

// Write atomically writes the engineer record to path with mode 0600 and
// ensures the parent directory is 0700. Refuses if an existing parent
// directory has loose perms — silently tightening would mask a security
// concern.
func Write(path string, e Engineer) *clioutput.Error {
	if e.Alias == "" {
		return clioutput.New(clioutput.ExitIdentity, clioutput.CatMalformed,
			"alias is required")
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

	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
			"marshal engineer: %v", err)
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
