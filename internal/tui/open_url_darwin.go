package tui

import (
	"context"
	"os/exec"
)

func openURL(ctx context.Context, target string) error {
	// #nosec G204 -- Fixed launcher receives a validated HTTP(S) URL as a separate argument.
	command := exec.CommandContext(ctx, "open", target)
	command.Env = browserEnvironment()

	return command.Run()
}
