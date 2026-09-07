package vaultcli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hu553in/vlt/internal/vaultcli"
)

func TestVaultDevServerLifecycle(t *testing.T) {
	binary := os.Getenv("VLT_INTEGRATION_VAULT_BINARY")
	if binary == "" {
		t.Skip("set VLT_INTEGRATION_VAULT_BINARY to a Vault CLI binary")
	}

	home := t.TempDir()
	tokenPath := configureIntegrationTokenHelper(t, home)
	t.Setenv("VAULT_TOKEN", "vlt-integration-root")
	address := startVaultDevServer(t, binary, home)
	t.Setenv("VAULT_ADDR", address)
	client := vaultcli.NewWithBinary(binary, address, "")
	waitForVault(t, client)
	assertVaultAccess(t, client)
	assertSecretLifecycle(t, client)
	assertScheduledDeletion(t, binary, client)
	assertNumberPrecision(t, client)
	assertKVv1Rejected(t, binary, client)
	assertVaultCLIPathBoundary(t, client, address)
	assertRestrictedToken(t, binary, address)
	assertTokenHelperLifecycle(t, binary, address, tokenPath)
}

func assertVaultAccess(t *testing.T, client *vaultcli.Client) {
	t.Helper()

	ctx := t.Context()
	status, err := client.Status(ctx)
	if err != nil || status.Sealed {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	t.Logf("Vault integration version: %s", status.Version)
	auth, err := client.AuthStatus(ctx)
	if err != nil || auth.DisplayName == "" || !auth.FromEnv {
		t.Fatalf("AuthStatus() = %#v, %v", auth, err)
	}
	mounts, err := client.ListMounts(ctx)
	if err != nil || !hasMount(mounts, "secret") {
		t.Fatalf("ListMounts() = %#v, %v", mounts, err)
	}
	keys, err := client.List(ctx, "secret", "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("initial List() = %v, %v", keys, err)
	}
}

func assertSecretLifecycle(t *testing.T, client *vaultcli.Client) {
	t.Helper()

	ctx := t.Context()
	writtenVersion, err := client.Put(ctx, "secret", "apps/api", map[string]any{"value": "first"}, 0)
	if err != nil || writtenVersion != 1 {
		t.Fatalf("initial Put() = version %d, %v", writtenVersion, err)
	}
	secret, err := client.Get(ctx, "secret", "apps/api")
	if err != nil || secret.Deleted || secret.Version != 1 || secret.Data["value"] != "first" {
		t.Fatalf("initial Get() = %#v, %v", secret, err)
	}
	writtenVersion, err = client.Put(ctx, "secret", "apps/api", map[string]any{"value": "second"}, secret.Version)
	if err != nil || writtenVersion != 2 {
		t.Fatalf("update Put() = version %d, %v", writtenVersion, err)
	}
	if _, err = client.Put(ctx, "secret", "apps/api", map[string]any{"value": "stale"}, secret.Version); err == nil ||
		errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("stale-CAS Put() error = %v, want a definite Vault rejection", err)
	}
	if err = client.Delete(ctx, "secret", "apps/api"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	deleted, err := client.Get(ctx, "secret", "apps/api")
	if err != nil || !deleted.Deleted || deleted.Version != 2 || len(deleted.Data) != 0 {
		t.Fatalf("deleted Get() = %#v, %v", deleted, err)
	}
	writtenVersion, err = client.Put(
		ctx,
		"secret",
		"apps/api",
		map[string]any{"value": "after-delete"},
		deleted.Version,
	)
	if err != nil || writtenVersion != 3 {
		t.Fatalf("post-delete Put() = version %d, %v", writtenVersion, err)
	}
	secret, err = client.Get(ctx, "secret", "apps/api")
	if err != nil || secret.Deleted || secret.Version != 3 || secret.Data["value"] != "after-delete" {
		t.Fatalf("post-delete Get() = %#v, %v", secret, err)
	}
}

func assertVaultCLIPathBoundary(t *testing.T, client *vaultcli.Client, address string) {
	t.Helper()

	ctx := t.Context()
	version, err := client.Put(ctx, "secret", "name", map[string]any{"value": "plain"}, 0)
	if err != nil || version != 1 {
		t.Fatalf("Put(name) = version %d, %v", version, err)
	}
	writeRawSecret(t, address, " name ")
	if _, err = client.List(ctx, "secret", ""); err == nil ||
		!strings.Contains(err.Error(), "cannot be addressed exactly by Vault CLI") {
		t.Fatalf("List() error = %v, want exact-addressing rejection", err)
	}
	if _, err = client.Get(ctx, "secret", " name "); err == nil ||
		!strings.Contains(err.Error(), "Vault CLI would reinterpret it") {
		t.Fatalf("Get(spaced path) error = %v, want exact-addressing rejection", err)
	}
	plain, err := client.Get(ctx, "secret", "name")
	if err != nil || plain.Deleted || plain.Data["value"] != "plain" {
		t.Fatalf("plain secret changed after rejecting spaced identity: %#v, %v", plain, err)
	}
}

func writeRawSecret(t *testing.T, address, secretPath string) {
	t.Helper()

	target := address + "/v1/secret/data/" + url.PathEscape(secretPath)
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		target,
		strings.NewReader(`{"data":{"value":"spaced"}}`),
	)
	if err != nil {
		t.Fatalf("create raw Vault request: %v", err)
	}
	request.Header.Set("X-Vault-Token", "vlt-integration-root")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("write exact raw Vault path: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("write exact raw Vault path: status %s", response.Status)
	}
}

//nolint:gocognit // Keep the policy-dependent read/write/delete lifecycle in one ordered scenario.
func assertRestrictedToken(t *testing.T, binary, address string) {
	t.Helper()

	policy := exec.CommandContext(t.Context(), binary, "policy", "write", "vlt-scoped", "-")
	policy.Stdin = strings.NewReader(`
path "secret/data/apps/*" { capabilities = ["create", "read", "update", "delete"] }
path "secret/metadata/apps/*" { capabilities = ["list"] }
`)
	if output, err := policy.CombinedOutput(); err != nil {
		t.Fatalf("write scoped policy: %v: %s", err, output)
	}
	for _, policyName := range []string{"default", "vlt-scoped"} {
		t.Run(policyName, func(t *testing.T) {
			command := exec.CommandContext(
				t.Context(),
				binary,
				"token",
				"create",
				"-format=json",
				"-policy="+policyName,
			)
			output, err := command.Output()
			if err != nil {
				t.Fatalf("create restricted Vault token: %v", err)
			}
			var response struct {
				Auth struct {
					ClientToken string `json:"client_token"`
				} `json:"auth"`
			}
			if err = json.Unmarshal(output, &response); err != nil || response.Auth.ClientToken == "" {
				t.Fatalf("decode restricted token: %v", err)
			}
			t.Setenv("VAULT_TOKEN", response.Auth.ClientToken)
			client := vaultcli.NewWithBinary(binary, address, "")
			secret, err := client.Get(t.Context(), "secret", "apps/api")
			if policyName == "default" {
				if !errors.Is(err, vaultcli.ErrPermissionDenied) {
					t.Fatalf("restricted Get() = %v, want permission denied", err)
				}
				return
			}
			if err != nil || secret.Data["value"] != "after-delete" {
				t.Fatalf("scoped Get() = %#v, %v", secret, err)
			}
			if keys, listError := client.List(
				t.Context(),
				"secret",
				"apps",
			); listError != nil || len(keys) != 1 ||
				keys[0] != "api" {
				t.Fatalf("scoped List() = %v, %v", keys, listError)
			}
			if _, err = client.Put(
				t.Context(),
				"secret",
				"apps/api",
				map[string]any{"value": "scoped"},
				secret.Version,
			); err != nil {
				t.Fatalf("scoped Put(): %v", err)
			}
			if err = client.Delete(t.Context(), "secret", "apps/api"); err != nil {
				t.Fatalf("scoped Delete(): %v", err)
			}
			if secret, err = client.Get(t.Context(), "secret", "apps/api"); err != nil || !secret.Deleted {
				t.Fatalf("scoped deleted Get() = %#v, %v", secret, err)
			}
		})
	}
}

func assertTokenHelperLifecycle(t *testing.T, binary, address, tokenPath string) {
	t.Helper()

	ctx := t.Context()
	t.Setenv("VLT_INTEGRATION_EXPECTED_ADDR", address)
	t.Setenv("VAULT_ADDR", "https://ambient.invalid")
	t.Setenv("VAULT_TOKEN", "")
	if err := os.Remove(tokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove helper token: %v", err)
	}
	helperClient := vaultcli.NewWithBinary(binary, address, "")
	hasToken, err := helperClient.HasToken(ctx)
	if err != nil || hasToken {
		t.Fatalf("HasToken() before login = %t, %v", hasToken, err)
	}
	if err = helperClient.Login(ctx, "vlt-integration-root"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	hasToken, err = helperClient.HasToken(ctx)
	if err != nil || !hasToken {
		t.Fatalf("HasToken() after login = %t, %v", hasToken, err)
	}
	outcome, err := helperClient.Logout(ctx)
	if err != nil || !outcome.TokenRevoked || !outcome.LocalTokenCleared {
		t.Fatalf("Logout() = %#v, %v", outcome, err)
	}
	hasToken, err = helperClient.HasToken(ctx)
	if err != nil || hasToken {
		t.Fatalf("HasToken() after logout = %t, %v", hasToken, err)
	}
}

func configureIntegrationTokenHelper(t *testing.T, directory string) string {
	t.Helper()

	helperPath := filepath.Join(directory, "token-helper")
	tokenPath := filepath.Join(directory, "helper-token")
	configPath := filepath.Join(directory, "vault.hcl")
	helper := `#!/bin/sh
set -eu
if [ "$VAULT_ADDR" != "$VLT_INTEGRATION_EXPECTED_ADDR" ]; then
  exit 3
fi
if [ -n "${VAULT_NAMESPACE-}" ]; then
  exit 4
fi
case "$1" in
  get)
    if [ -f "$VLT_INTEGRATION_TOKEN_FILE" ]; then
      cat "$VLT_INTEGRATION_TOKEN_FILE"
    fi
    ;;
  store)
    umask 077
    cat > "$VLT_INTEGRATION_TOKEN_FILE"
    ;;
  erase)
    rm -f "$VLT_INTEGRATION_TOKEN_FILE"
    ;;
  *)
    exit 2
    ;;
esac
`
	if err := os.WriteFile(helperPath, []byte(helper), 0o700); err != nil {
		t.Fatalf("write integration token helper: %v", err)
	}
	config := fmt.Sprintf("token_helper = %q\n", helperPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write integration Vault config: %v", err)
	}
	t.Setenv("VAULT_CONFIG_PATH", configPath)
	t.Setenv("VLT_INTEGRATION_TOKEN_FILE", tokenPath)
	t.Setenv("HOME", directory)

	return tokenPath
}

func startVaultDevServer(t *testing.T, binary, home string) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Vault port: %v", err)
	}
	address := "http://" + listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("release Vault port: %v", err)
	}

	command := exec.Command(
		binary,
		"server",
		"-dev",
		"-dev-no-store-token",
		"-dev-root-token-id=vlt-integration-root",
		"-dev-listen-address="+strings.TrimPrefix(address, "http://"),
		"-log-level=error",
	)
	command.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
	}
	if err = command.Start(); err != nil {
		t.Fatalf("start Vault dev server: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	})

	return address
}

func waitForVault(t *testing.T, client *vaultcli.Client) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		_, lastError = client.Status(ctx)
		cancel()
		if lastError == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("Vault dev server did not become ready: %v", lastError)
}

func hasMount(mounts []vaultcli.Mount, path string) bool {
	for _, mount := range mounts {
		if mount.Path == path {
			return true
		}
	}

	return false
}

func assertKVv1Rejected(t *testing.T, binary string, client *vaultcli.Client) {
	t.Helper()

	for _, arguments := range [][]string{
		{"secrets", "enable", "-path=legacy", "-version=1", "kv"},
		{"kv", "put", "-mount=legacy", "api", "value=original"},
	} {
		if output, err := exec.CommandContext(t.Context(), binary, arguments...).CombinedOutput(); err != nil {
			t.Fatalf("prepare KV v1: %v: %s", err, output)
		}
	}
	for name, operation := range map[string]func() error{
		"list": func() error {
			_, err := client.List(t.Context(), "legacy", "")
			return err
		},
		"get": func() error {
			_, err := client.Get(t.Context(), "legacy", "api")
			return err
		},
		"put": func() error {
			_, err := client.Put(t.Context(), "legacy", "api", map[string]any{"value": "replacement"}, 0)
			return err
		},
		"delete": func() error { return client.Delete(t.Context(), "legacy", "api") },
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "not KV v2") ||
			errors.Is(err, vaultcli.ErrOutcomeUnknown) {
			t.Fatalf("%s on KV v1 = %v, want definite rejection", name, err)
		}
	}
	output, err := exec.CommandContext(t.Context(), binary, "kv", "get", "-field=value", "-mount=legacy", "api").
		Output()
	if err != nil || strings.TrimSpace(string(output)) != "original" {
		t.Fatalf("KV v1 value changed: %q, %v", output, err)
	}
}

func assertScheduledDeletion(t *testing.T, binary string, client *vaultcli.Client) {
	t.Helper()
	if out, err := exec.CommandContext(t.Context(), binary, "write", "secret/metadata/scheduled", "delete_version_after=1h").
		CombinedOutput(); err != nil {
		t.Fatalf("schedule deletion: %v: %s", err, out)
	}
	if _, err := client.Put(t.Context(), "secret", "scheduled", map[string]any{"value": "live"}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(t.Context(), "secret", "scheduled")
	if err != nil || got.Deleted || got.Data["value"] != "live" {
		t.Fatalf("scheduled Get: %#v, %v", got, err)
	}
	if err = client.Delete(t.Context(), "secret", "scheduled"); err != nil {
		t.Fatal(err)
	}
	got, err = client.Get(t.Context(), "secret", "scheduled")
	if err != nil || !got.Deleted {
		t.Fatalf("deleted scheduled Get: %#v, %v", got, err)
	}
}

func assertNumberPrecision(t *testing.T, client *vaultcli.Client) {
	t.Helper()
	data := map[string]any{
		"numbers": []any{
			json.Number("9007199254740992"),
			json.Number("0.1"),
			json.Number("1.2345678901234567"),
			json.Number("5e-324"),
		},
		"exact": "9007199254740993",
	}
	if _, err := client.Put(t.Context(), "secret", "numbers", data, 0); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(t.Context(), "secret", "numbers")
	if err != nil || !reflect.DeepEqual(got.Data, data) {
		t.Fatalf("numeric round trip: %#v, %v", got, err)
	}
	if _, err = client.Put(
		t.Context(),
		"secret",
		"numbers",
		map[string]any{"value": json.Number("9007199254740993")},
		1,
	); err == nil ||
		errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("lossy write: %v", err)
	}
	got, err = client.Get(t.Context(), "secret", "numbers")
	if err != nil || got.Version != 1 || !reflect.DeepEqual(got.Data, data) {
		t.Fatalf("rejected write changed secret: %#v, %v", got, err)
	}
}
