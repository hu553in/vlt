//go:build darwin || linux

package vaultcli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Give ordinary output copies time to drain, but never let orphaned descendants
// keep inherited command pipes open indefinitely.
const orphanedPipeWaitDelay = time.Second

func configureCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = orphanedPipeWaitDelay
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}
}
