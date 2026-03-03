package tasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// SignalSeidFn is the function used to signal the seid process.
// Replaceable for testing.
var SignalSeidFn = SignalSeid

// SignalSeid finds the seid process in the shared PID namespace and sends
// the specified signal. With shareProcessNamespace: true, /proc is shared
// across all containers in the pod.
func SignalSeid(sig syscall.Signal) error {
	pid, err := findSeidPID()
	if err != nil {
		return fmt.Errorf("finding seid process: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("sending signal %d to seid (pid %d): %w", sig, pid, err)
	}

	return nil
}

// findSeidPID scans /proc for a process whose cmdline contains "seid".
func findSeidPID() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("reading /proc: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}

		if isSeidProcess(string(cmdline)) {
			return pid, nil
		}
	}

	return 0, fmt.Errorf("seid process not found in /proc")
}

// isSeidProcess checks if a /proc/[pid]/cmdline belongs to seid.
// cmdline uses null bytes as separators between arguments.
func isSeidProcess(cmdline string) bool {
	parts := strings.Split(cmdline, "\x00")
	if len(parts) == 0 {
		return false
	}
	base := filepath.Base(parts[0])
	return base == "seid"
}
