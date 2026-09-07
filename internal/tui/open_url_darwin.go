package tui

import (
	"context"
	"os/exec"
)

func openURL(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "open", target)
	command.Env = browserEnvironment()

	return command.Run()
}
