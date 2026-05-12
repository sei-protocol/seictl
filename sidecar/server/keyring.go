package server

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/keyring"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
)

// Backend constants are the values accepted by SEI_KEYRING_BACKEND.
// We define our own aliases (rather than re-using keyring.BackendFile
// etc. directly in switch statements at call sites) so the env-contract
// surface lives in one place and is searchable.
const (
	BackendTest = keyring.BackendTest
	BackendFile = keyring.BackendFile
	BackendOS   = keyring.BackendOS
)

// AllowedBackends lists the backends seictl supports today. The list is
// intentionally narrow: KMS / Vault / remote-signer backends are deferred
// per the in-pod governance signing design doc.
var AllowedBackends = []string{BackendTest, BackendFile, BackendOS}

// smokeTestAttempts and smokeTestBackoff bound the retry window for the
// startup-time keyring liveness check. The retry exists to absorb the
// rare kubelet Secret-mount race where the projected file is briefly
// absent; beyond this window the keyring is genuinely broken and we
// fail fast so the pod CrashLoopBackOffs.
const (
	smokeTestAttempts = 3
	smokeTestBackoff  = 2 * time.Second
)

// smokeTestBackoffTestHook overrides smokeTestBackoff in tests. It is
// not part of the package's public contract; the indirection exists so
// failure-path tests don't wait the full 6 seconds.
var smokeTestBackoffTestHook = smokeTestBackoff

// OpenKeyring constructs a Cosmos SDK keyring for the given backend.
//
// For backend == file, the SDK's file backend prompts for the passphrase
// via the supplied io.Reader. Some code paths inside 99designs/keyring
// (which the SDK wraps) call the prompt twice (once to read, once to
// confirm on key creation paths). We feed the passphrase twice to cover
// both cases — the reader is consumed lazily so an unused second line is
// harmless.
//
// The caller is responsible for unsetting SEI_KEYRING_PASSPHRASE from
// the process environment after this function returns; doing it here
// would couple the factory to the env-loading layer and make the
// function harder to test.
func OpenKeyring(backend, dir, passphrase string) (keyring.Keyring, error) {
	var input io.Reader
	rootDir := dir
	switch backend {
	case BackendTest, BackendOS:
		// rootDir is honored as-is; no passphrase prompt.
	case BackendFile:
		if passphrase == "" {
			return nil, fmt.Errorf("keyring backend %q requires a passphrase", backend)
		}
		input = strings.NewReader(passphrase + "\n" + passphrase + "\n")
		// The SDK appends "keyring-file" to the supplied rootDir, so a
		// caller passing /sei/keyring-file would land at
		// /sei/keyring-file/keyring-file. Strip the suffix when present.
		if filepath.Base(dir) == "keyring-file" {
			rootDir = filepath.Dir(dir)
		}
	default:
		return nil, fmt.Errorf("unsupported keyring backend %q (allowed: %s)",
			backend, strings.Join(AllowedBackends, "|"))
	}

	kr, err := keyring.New(sdk.KeyringServiceName(), backend, rootDir, input)
	if err != nil {
		return nil, fmt.Errorf("open keyring: %s", redactPassphrase(err.Error(), passphrase))
	}
	return kr, nil
}

// redactPassphrase removes any verbatim occurrence of the passphrase from
// a string. The SDK keyring is not known to embed passphrases in error
// chains, but defensive redaction is cheap and protects against future
// regressions in upstream libraries.
func redactPassphrase(s, passphrase string) string {
	if passphrase == "" {
		return s
	}
	return strings.ReplaceAll(s, passphrase, "[redacted]")
}

// SmokeTestKeyring verifies the keyring is structurally usable by
// listing its entries. An empty keyring is a permitted outcome; the
// first sign-tx will surface missing-key errors clearly to the caller.
// The retry window absorbs the kubelet Secret-mount race.
//
// The underlying 99designs/keyring library can panic on a malformed
// configuration (e.g. a non-directory FileDir). We treat any such
// panic as a smoke-test failure rather than letting it propagate; the
// alternative is a process crash that bypasses the fail-fast logging.
func SmokeTestKeyring(kr keyring.Keyring) error {
	var lastErr error
	for attempt := 1; attempt <= smokeTestAttempts; attempt++ {
		err := smokeTestAttempt(kr)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < smokeTestAttempts {
			time.Sleep(smokeTestBackoffTestHook)
		}
	}
	return fmt.Errorf("keyring smoke test failed after %d attempts: %w",
		smokeTestAttempts, lastErr)
}

func smokeTestAttempt(kr keyring.Keyring) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("keyring backend panicked during smoke test: %v", r)
		}
	}()
	_, err = kr.List()
	return err
}
