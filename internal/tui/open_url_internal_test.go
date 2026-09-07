//go:build darwin || linux

package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenURLDoesNotExposeVaultEnvironment(t *testing.T) {
	directory := t.TempDir()
	provider := "xdg-open"
	if runtime.GOOS == "darwin" {
		provider = "open"
	}
	environmentPath := filepath.Join(directory, "environment")
	displayPath := filepath.Join(directory, "display")
	targetPath := filepath.Join(directory, "target")
	script := "#!/bin/sh\nset > \"$VLT_TEST_ENVIRONMENT\"\n" +
		"printf '%s' \"$DISPLAY\" > \"$VLT_TEST_DISPLAY\"\n" +
		"printf '%s' \"$1\" > \"$VLT_TEST_TARGET\"\n"
	if err := os.WriteFile(filepath.Join(directory, provider), []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("PATH", directory)
	t.Setenv("DISPLAY", ":test")
	t.Setenv("VLT_TEST_DISPLAY", displayPath)
	t.Setenv("VLT_TEST_ENVIRONMENT", environmentPath)
	t.Setenv("VLT_TEST_TARGET", targetPath)
	for _, variable := range []string{
		"VAULT_TOKEN",
		"VAULT_MFA",
		"VAULT_HEADERS",
		"VAULT_CLIENT_CERT",
		"VAULT_CLIENT_KEY",
	} {
		t.Setenv(variable, "must-not-reach-browser")
	}

	const target = "https://vault.example.com/ui/vault/auth"
	if err := openURL(t.Context(), target); err != nil {
		t.Fatalf("openURL() error = %v", err)
	}
	environment, err := os.ReadFile(environmentPath)
	if err != nil {
		t.Fatalf("ReadFile(environment) error = %v", err)
	}
	for line := range strings.SplitSeq(string(environment), "\n") {
		name, _, found := strings.Cut(line, "=")
		if found && strings.HasPrefix(name, "VAULT_") {
			t.Fatalf("browser environment contains Vault variable %q", name)
		}
	}
	display, err := os.ReadFile(displayPath)
	if err != nil {
		t.Fatalf("ReadFile(display) error = %v", err)
	}
	if string(display) != ":test" {
		t.Fatalf("browser DISPLAY = %q, want :test", display)
	}
	openedTarget, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(openedTarget) != target {
		t.Fatalf("opened target = %q, want %q", openedTarget, target)
	}
}
