package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hu553in/vlt/internal/config"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	actual, err := config.Normalize(config.Config{
		Address:   " https://vault.example.com/ ",
		Namespace: " /team/platform/ ",
		Mounts:    []string{"apps/", "shared", "apps"},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if actual.Address != "https://vault.example.com" {
		t.Errorf("Address = %q", actual.Address)
	}
	if actual.Namespace != "team/platform" {
		t.Errorf("Namespace = %q", actual.Namespace)
	}
	if !slices.Equal(actual.Mounts, []string{"apps", "shared"}) {
		t.Errorf("Mounts = %v", actual.Mounts)
	}
}

func TestNormalizeIsIdempotentAcrossMixedWhitespaceAndSlashBoundaries(t *testing.T) {
	t.Parallel()

	once, err := config.Normalize(config.Config{
		Address:   "https://vault.example.com",
		Namespace: "/ team/platform /",
		Mounts:    []string{"/ apps /", "// shared //"},
	})
	if err != nil {
		t.Fatalf("first Normalize() error = %v", err)
	}
	twice, err := config.Normalize(once)
	if err != nil {
		t.Fatalf("second Normalize() error = %v", err)
	}
	if once.Address != twice.Address || once.Namespace != "team/platform" || twice.Namespace != once.Namespace ||
		!slices.Equal(once.Mounts, []string{"apps", "shared"}) || !slices.Equal(twice.Mounts, once.Mounts) {
		t.Fatalf("Normalize() changed on repetition: once %#v, twice %#v", once, twice)
	}
}

func TestDefaultPathUsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	actual, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	want := filepath.Join(root, "vlt", "config.toml")
	if actual != want {
		t.Errorf("DefaultPath() = %q, want %q", actual, want)
	}
}

func TestDefaultPathRejectsRelativeXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")

	if _, err := config.DefaultPath(); err == nil {
		t.Fatal("DefaultPath() accepted a relative XDG_CONFIG_HOME")
	}
}

func TestNormalizeRejectsUnsafeAddress(t *testing.T) {
	t.Parallel()

	for name, address := range map[string]string{
		"credentials":    "https://user:pass@vault.example.com",
		"remote http":    "http://vault.example.com",
		"path":           "https://vault.example.com/proxy",
		"query":          "https://vault.example.com?token=secret",
		"empty query":    "https://vault.example.com?",
		"fragment":       "https://vault.example.com#section",
		"empty fragment": "https://vault.example.com#",
		"missing host":   "https://:8200",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := config.Normalize(config.Config{Address: address}); err == nil {
				t.Fatalf("Normalize() accepted unsafe address %q", address)
			}
		})
	}
}

func TestNormalizeAllowsHTTPOnlyForLoopback(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"http://localhost:8200",
		"http://127.0.0.1:8200",
		"http://127.255.255.254:8200",
		"http://[::1]:8200",
	} {
		actual, err := config.Normalize(config.Config{Address: address})
		if err != nil {
			t.Errorf("Normalize(%q) error = %v", address, err)
		} else if actual.Address != address {
			t.Errorf("Normalize(%q) address = %q", address, actual.Address)
		}
	}
}

func TestNormalizeRejectsInvalidStructuredFields(t *testing.T) {
	t.Parallel()

	for name, candidate := range map[string]config.Config{
		"address control": {
			Address: "https://vault.example.com\n",
		},
		"namespace control": {
			Address:   "https://vault.example.com",
			Namespace: "team\x1b[2J",
		},
		"parent mount segment": {
			Address: "https://vault.example.com",
			Mounts:  []string{"team/../secret"},
		},
		"empty mount segment": {
			Address: "https://vault.example.com",
			Mounts:  []string{"team//secret"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := config.Normalize(candidate); err == nil {
				t.Fatalf("Normalize(%#v) unexpectedly succeeded", candidate)
			}
		})
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), ".config", "vlt")
	path := filepath.Join(directory, "config.toml")
	store := config.NewStore(path)
	want := config.Config{
		Address:   "https://vault.example.com/",
		Namespace: "engineering/",
		Mounts:    []string{"secret/"},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	actual, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if actual.Address != "https://vault.example.com" || actual.Namespace != "engineering" ||
		!slices.Equal(actual.Mounts, []string{"secret"}) {
		t.Errorf("Load() = %#v", actual)
	}

	assertMode(t, directory, 0o700)
	assertMode(t, path, 0o600)
}

func TestStoreReplacesExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vlt", "config.toml")
	store := config.NewStore(path)
	if err := store.Save(config.Config{Address: "https://first.example.com"}); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("Chmod(config directory) error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod(config file) error = %v", err)
	}
	if err := store.Save(config.Config{Address: "https://new.example.com", Mounts: []string{"secret"}}); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Address != "https://new.example.com" || !slices.Equal(loaded.Mounts, []string{"secret"}) {
		t.Errorf("Load() = %#v", loaded)
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestStoreRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "address = 'https://vault.example.com'\ntoken = 'secret'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.NewStore(path).Load()
	if err == nil {
		t.Fatal("Load() accepted an unknown token field")
	}
}

func TestStoreBoundsConfigInput(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "address = 'https://vault.example.com'\n# " + strings.Repeat("x", 2<<20)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.NewStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load() error = %v, want config size error", err)
	}
}

func TestStoreDoesNotWriteAConfigItCannotLoad(t *testing.T) {
	t.Parallel()

	mounts := make([]string, 33)
	for index := range mounts {
		mounts[index] = strings.Repeat("m", (64<<10)-8) + fmt.Sprintf("-%07d", index)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	err := config.NewStore(path).Save(config.Config{
		Address: "https://vault.example.com",
		Mounts:  mounts,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Save() error = %v, want config size error", err)
	}
	if _, statError := os.Stat(path); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf("oversized config was created: %v", statError)
	}
}

func TestNormalizeBoundsConfigFields(t *testing.T) {
	t.Parallel()

	for name, candidate := range map[string]config.Config{
		"address":   {Address: "https://" + strings.Repeat("a", 64<<10)},
		"namespace": {Namespace: strings.Repeat("n", (64<<10)+1)},
		"mount":     {Mounts: []string{strings.Repeat("m", (64<<10)+1)}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := config.Normalize(candidate); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("Normalize() error = %v, want field size error", err)
			}
		})
	}
}

func TestMissingStoreReturnsEmptyConfig(t *testing.T) {
	t.Parallel()

	actual, err := config.NewStore(filepath.Join(t.TempDir(), "missing.toml")).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if actual.Address != "" || actual.Namespace != "" || len(actual.Mounts) != 0 {
		t.Errorf("Load() = %#v", actual)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if actual := info.Mode().Perm(); actual != want {
		t.Errorf("mode of %q = %04o, want %04o", path, actual, want)
	}
}
