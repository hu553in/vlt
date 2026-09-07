package testutil

import (
	"os"
	"strings"
	"testing"
)

// Run isolates Vault credentials and settings for the lifetime of a test process.
// Tests opt into their own environment with t.Setenv; callers must exit with the returned code.
func Run(m *testing.M) int {
	home, err := os.MkdirTemp("", "vlt-test-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "VAULT_") {
			if err = os.Unsetenv(name); err != nil {
				panic(err)
			}
		}
	}
	if err = os.Setenv("HOME", home); err != nil {
		panic(err)
	}

	return m.Run()
}
