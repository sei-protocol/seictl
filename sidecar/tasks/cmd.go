package tasks

import (
	"context"
	"fmt"
	"os/exec"
)

// CommandRunner executes a command and returns its stdout. Abstracting this
// allows tests to verify seid invocations without a real binary.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandRunner shells out via os/exec. Stderr is captured and
// included in the error message on non-zero exit.
func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s %v failed: %w\nstderr: %s", name, args, err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}
