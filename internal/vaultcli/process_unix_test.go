//go:build darwin || linux

package vaultcli_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hu553in/vlt/internal/vaultcli"
)

func TestVaultCancellationTerminatesDescendants(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	directory := t.TempDir()
	started := filepath.Join(directory, "started")
	childPIDPath := filepath.Join(directory, "child-pid")
	t.Setenv("VLT_TEST_STARTED", started)
	t.Setenv("VLT_TEST_CHILD_PID", childPIDPath)
	binary := writeExecutable(t, `#!/bin/sh
sleep 60 &
child=$!
printf '%s' "$child" > "$VLT_TEST_CHILD_PID"
touch "$VLT_TEST_STARTED"
wait "$child"
`)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
			ctx,
			"secret",
			"apps/api",
			map[string]any{"value": true},
			1,
		)
		result <- err
	}()
	waitForFile(t, started)
	childPID, err := strconv.Atoi(strings.TrimSpace(waitForFileContent(t, childPIDPath)))
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	cancel()
	if resultError := <-result; !errors.Is(resultError, context.Canceled) ||
		!errors.Is(resultError, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Put() error = %v, want unknown canceled outcome", resultError)
	}

	waitForProcessExit(t, childPID)
}

func TestVaultCommandBoundsOrphanedPipeWait(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	childPIDPath := filepath.Join(t.TempDir(), "child-pid")
	t.Setenv("VLT_TEST_CHILD_PID", childPIDPath)
	binary := writeExecutable(t, `#!/bin/sh
sleep 60 &
printf '%s' "$!" > "$VLT_TEST_CHILD_PID"
printf '%s\n' '{"version":"2.0.3","sealed":false}'
`)
	result := make(chan error, 1)
	go func() {
		_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Status(t.Context())
		result <- err
	}()
	childPID, err := strconv.Atoi(strings.TrimSpace(waitForFileContent(t, childPIDPath)))
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("Status() error = %v, want exec.ErrWaitDelay", err)
		}
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		<-result
		t.Fatal("Vault command remained blocked on an orphaned pipe")
	}

	waitForProcessExit(t, childPID)
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(asynchronousTestTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child process %d survived command cleanup", pid)
}
