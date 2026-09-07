package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuiltCommandVersionAndHelp(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	binary := filepath.Join(t.TempDir(), "vlt")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "./cmd/vlt")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vlt: %v\n%s", err, output)
	}

	version := exec.CommandContext(ctx, binary, "--version")
	versionOutput, err := version.CombinedOutput()
	if err != nil {
		t.Fatalf("vlt --version: %v\n%s", err, versionOutput)
	}
	if actual := strings.TrimSpace(string(versionOutput)); actual != "0.1.0" {
		t.Fatalf("vlt --version = %q", actual)
	}

	help := exec.CommandContext(ctx, binary, "--help")
	helpOutput, err := help.CombinedOutput()
	if err != nil {
		t.Fatalf("vlt --help: %v\n%s", err, helpOutput)
	}
	helpText := string(helpOutput)
	for _, expected := range []string{
		"A focused TUI for HashiCorp Vault KV v2.",
		"Usage: vlt",
		"--version",
		"--help",
	} {
		if !strings.Contains(helpText, expected) {
			t.Fatalf("vlt --help does not contain %q:\n%s", expected, helpText)
		}
	}

	invalid := exec.CommandContext(ctx, binary, "--not-a-vlt-flag")
	invalidOutput, err := invalid.CombinedOutput()
	if err == nil {
		t.Fatal("vlt accepted an unknown flag")
	}
	if text := string(invalidOutput); strings.Count(text, "flag provided but not defined") != 1 ||
		!strings.HasPrefix(text, "vlt: ") {
		t.Fatalf("vlt unknown-flag output = %q", text)
	}
}
