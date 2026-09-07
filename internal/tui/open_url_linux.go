package tui

import (
	"context"
	"os/exec"
	"strings"
)

func openURL(ctx context.Context, target string) error {
	providers := []string{"xdg-open", "x-www-browser", "www-browser", "wslview"}
	for _, provider := range providers {
		if _, err := exec.LookPath(provider); err == nil {
			command := exec.CommandContext(ctx, provider, target)
			command.Env = browserEnvironment()

			return command.Run()
		}
	}

	return &exec.Error{Name: strings.Join(providers, ","), Err: exec.ErrNotFound}
}
