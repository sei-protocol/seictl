package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

func TestWrite_RoundTrip(t *testing.T) {
	// t.TempDir() returns a directory with mode 0755; Write refuses loose
	// parents, so write into a fresh subdir it can create with 0700.
	path := filepath.Join(t.TempDir(), "seictl", "engineer.json")

	if err := Write(path, Engineer{Alias: "bdc", Name: "Brandon"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Alias != "bdc" || got.Name != "Brandon" {
		t.Errorf("round-trip mismatch: got %+v", got)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != FileMode {
		t.Errorf("file mode: got %#o, want %#o", mode, FileMode)
	}
}

func TestWrite_CreatesParentDirWithStrictMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "engineer.json")

	if err := Write(path, Engineer{Alias: "bdc", Name: "Brandon"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dirInfo, statErr := os.Stat(filepath.Dir(path))
	if statErr != nil {
		t.Fatalf("stat dir: %v", statErr)
	}
	if mode := dirInfo.Mode().Perm(); mode != DirMode {
		t.Errorf("dir mode: got %#o, want %#o", mode, DirMode)
	}
}

func TestWrite_RefusesLooseParentDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "loose")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "engineer.json")

	cliErr := Write(path, Engineer{Alias: "bdc", Name: "Brandon"})
	if cliErr == nil {
		t.Fatalf("expected refusal, got nil")
	}
	if cliErr.Category != clioutput.CatPermsLoose {
		t.Errorf("category: got %q, want %q", cliErr.Category, clioutput.CatPermsLoose)
	}
}

func TestWrite_RejectsEmptyAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engineer.json")

	cliErr := Write(path, Engineer{Alias: "", Name: "Anon"})
	if cliErr == nil || cliErr.Category != clioutput.CatMalformed {
		t.Errorf("expected malformed error, got %+v", cliErr)
	}
}

func TestRead_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, cliErr := Read(path)
	if cliErr == nil {
		t.Fatalf("expected error for missing file")
	}
	if cliErr.Code != clioutput.ExitIdentity {
		t.Errorf("code: got %d, want %d", cliErr.Code, clioutput.ExitIdentity)
	}
	if cliErr.Category != clioutput.CatMissing {
		t.Errorf("category: got %q, want %q", cliErr.Category, clioutput.CatMissing)
	}
}

func TestRead_Malformed(t *testing.T) {
	dir := tightTempDir(t)
	path := filepath.Join(dir, "engineer.json")
	if err := os.WriteFile(path, []byte("not-json"), FileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, cliErr := Read(path)
	if cliErr == nil || cliErr.Category != clioutput.CatMalformed {
		t.Errorf("expected malformed, got %+v", cliErr)
	}
}

func TestRead_MissingAliasField(t *testing.T) {
	dir := tightTempDir(t)
	path := filepath.Join(dir, "engineer.json")
	body, _ := json.Marshal(Engineer{Name: "Anon"})
	if err := os.WriteFile(path, body, FileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, cliErr := Read(path)
	if cliErr == nil || cliErr.Category != clioutput.CatMalformed {
		t.Errorf("expected malformed, got %+v", cliErr)
	}
}

func TestRead_RefusesLoosePerms(t *testing.T) {
	dir := tightTempDir(t)
	path := filepath.Join(dir, "engineer.json")
	body, _ := json.Marshal(Engineer{Alias: "bdc", Name: "Brandon"})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, cliErr := Read(path)
	if cliErr == nil {
		t.Fatalf("expected perms-loose error")
	}
	if cliErr.Category != clioutput.CatPermsLoose {
		t.Errorf("category: got %q, want %q", cliErr.Category, clioutput.CatPermsLoose)
	}
}

// tightTempDir returns a fresh subdirectory with mode 0700 — needed because
// t.TempDir() returns the system default 0755, which Read rejects as loose.
func tightTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "seictl")
	if err := os.Mkdir(dir, DirMode); err != nil {
		t.Fatalf("mkdir tight: %v", err)
	}
	return dir
}

func TestDefaultPath_PointsUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(home, ".seictl", "engineer.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
