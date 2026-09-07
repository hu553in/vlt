package vaultcli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hu553in/vlt/internal/vaultcli"
)

func TestClientRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")
	if _, err := client.Get(t.Context(), "secret", "../root"); err == nil {
		t.Fatal("Get() accepted a parent path segment")
	}
	if _, err := client.Get(t.Context(), "secret", " name "); err == nil ||
		!strings.Contains(err.Error(), "Vault CLI would reinterpret it") {
		t.Fatalf("Get(ambiguous path) error = %v", err)
	}
	if _, err := client.List(t.Context(), "", ""); err == nil {
		t.Fatal("List() accepted an empty mount")
	}
	if _, err := client.List(t.Context(), "/secret/", ""); err == nil {
		t.Fatal("List() accepted a non-canonical mount")
	}
	if _, err := client.List(t.Context(), " secret ", ""); err == nil {
		t.Fatal("List() accepted a mount the Vault CLI would reinterpret")
	}
	if _, err := client.Get(t.Context(), "secret", "apps/unsafe\x1b[2J"); err == nil {
		t.Fatal("Get() accepted control characters in a path")
	}
	normalized, err := vaultcli.NormalizeSecretPath(" / apps/api / ")
	if err != nil || normalized != "apps/api" {
		t.Fatalf("NormalizeSecretPath() = %q, %v", normalized, err)
	}
}

func TestClientRejectsSecretsTheTUICannotBound(t *testing.T) {
	t.Parallel()

	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")
	tooMany := make(map[string]any, vaultcli.MaximumCollectionEntries+1)
	for index := 0; index <= vaultcli.MaximumCollectionEntries; index++ {
		tooMany[strconv.Itoa(index)] = true
	}
	if _, err := client.Put(t.Context(), "secret", "apps/api", tooMany, 1); err == nil ||
		!strings.Contains(err.Error(), "top-level entries") {
		t.Fatalf("Put(too many entries) error = %v", err)
	}
	tooLong := map[string]any{strings.Repeat("k", vaultcli.MaximumFieldBytes+1): true}
	if _, err := client.Put(t.Context(), "secret", "apps/api", tooLong, 1); err == nil ||
		!strings.Contains(err.Error(), "key exceeds") {
		t.Fatalf("Put(oversized key) error = %v", err)
	}
}

func TestClientPreservesContextDeadline(t *testing.T) {
	binary := writeExecutable(t, "#!/bin/sh\nexec sleep 2\n")
	t.Setenv("VAULT_TOKEN", "")

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Status(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Status() error = %v, want context deadline exceeded", err)
	}
}

func TestClientRejectsOversizedOutput(t *testing.T) {
	binary := writeExecutable(t, "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=33 2>/dev/null\n")
	t.Setenv("VAULT_TOKEN", "")

	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Status(t.Context())
	if err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("Status() error = %v, want bounded-output error", err)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	missingVersionBinary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' '{}'\n")
	client := vaultcli.NewWithBinary(missingVersionBinary, "https://vault.example.com", "")
	if _, err := client.Status(t.Context()); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Fatalf("Status() error = %v, want missing-version error", err)
	}
	missingSealedStateBinary := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s\\n' '{\"version\":\"2.0.3\"}'\n",
	)
	client = vaultcli.NewWithBinary(missingSealedStateBinary, "https://vault.example.com", "")
	if _, err := client.Status(t.Context()); err == nil || !strings.Contains(err.Error(), "sealed state") {
		t.Fatalf("Status() error = %v, want missing-sealed-state error", err)
	}

	nullBinary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' 'null'\n")
	client = vaultcli.NewWithBinary(nullBinary, "https://vault.example.com", "")
	if _, err := client.AuthStatus(t.Context()); err == nil || !strings.Contains(err.Error(), "token data") {
		t.Fatalf("AuthStatus() error = %v, want token-data error", err)
	}
	client = vaultcli.NewWithBinary(nullBinary, "https://vault.example.com", "")
	if _, err := client.ListMounts(t.Context()); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("ListMounts() error = %v, want object-shape error", err)
	}
	if _, err := client.List(t.Context(), "secret", ""); err == nil ||
		!strings.Contains(err.Error(), "JSON array") {
		t.Fatalf("List() error = %v, want array-shape error", err)
	}
	zeroVersionBinary := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s\\n' '{\"data\":{\"data\":{\"password\":\"hunter2\"},\"metadata\":{\"version\":0}}}'\n",
	)
	client = vaultcli.NewWithBinary(zeroVersionBinary, "https://vault.example.com", "")
	if _, err := client.Get(t.Context(), "secret", "apps/api"); err == nil ||
		!strings.Contains(err.Error(), "valid KV v2 version") {
		t.Fatalf("Get() error = %v, want invalid-version error", err)
	}
	contradictoryDestructionBinary := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s\\n' '{\"data\":{\"data\":{\"password\":\"hunter2\"},\"metadata\":{\"version\":1,\"destroyed\":true}}}'\n",
	)
	client = vaultcli.NewWithBinary(contradictoryDestructionBinary, "https://vault.example.com", "")
	if _, err := client.Get(t.Context(), "secret", "apps/api"); err == nil ||
		!strings.Contains(err.Error(), "data with destruction metadata") {
		t.Fatalf("Get() error = %v, want contradictory-destruction error", err)
	}

	invalidListBinary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' '[\"../\"]'\n")
	client = vaultcli.NewWithBinary(invalidListBinary, "https://vault.example.com", "")
	if _, err := client.List(t.Context(), "secret", ""); err == nil ||
		!strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("List() error = %v, want invalid-key error", err)
	}
}

func TestClientRejectsDuplicateSecretNamesBeforeRoundTrip(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")

	for name, secretData := range map[string]string{
		"top level": `{"value":1,"value":2}`,
		"nested":    `{"nested":{"value":1,"value":2}}`,
		"in array":  `{"nested":[{"value":1,"value":2}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := `{"data":{"data":` + secretData + `,"metadata":{"version":1}}}`
			binary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(response)+"\n")
			client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

			_, err := client.Get(t.Context(), "secret", "apps/api")
			if err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("Get() error = %v, want duplicate-name rejection", err)
			}
		})
	}
}

func TestClientRejectsDiscoveredMountTheVaultCLICannotAddressExactly(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, "#!/bin/sh\ncat \"$VLT_TEST_RESPONSE\"\n")
	setJSONResponse(t, map[string]any{
		" spaced /": map[string]any{
			"type":    "kv",
			"options": map[string]string{"version": "2"},
		},
	})

	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").ListMounts(t.Context())
	if err == nil || !strings.Contains(err.Error(), "must not begin or end with whitespace") {
		t.Fatalf("ListMounts() error = %v, want unsupported exact mount error", err)
	}
}

func TestClientValidatesListedKeysAgainstTheFullVaultCLIPath(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' '[\" name\"]'\n")
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

	keys, err := client.List(t.Context(), "secret", "parent")
	if err != nil || !slices.Equal(keys, []string{" name"}) {
		t.Fatalf("List(addressable nested key) = %q, %v", keys, err)
	}

	binary = writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' '[\" name \"]'\n")
	client = vaultcli.NewWithBinary(binary, "https://vault.example.com", "")
	_, err = client.List(t.Context(), "secret", "")
	if err == nil || !strings.Contains(err.Error(), "cannot be addressed exactly by Vault CLI") {
		t.Fatalf("List() error = %v, want unsupported exact key error", err)
	}
}

func TestClientTreatsEmptyMountAsAnEmptyList(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	binary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' '{}'\nexit 2\n")

	keys, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").List(
		t.Context(),
		"secret",
		"",
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List() = %v, want no keys", keys)
	}
}

func TestClientBoundsVaultKeyLists(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, "#!/bin/sh\ncat \"$VLT_TEST_RESPONSE\"\n")
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

	keys := make([]string, vaultcli.MaximumCollectionEntries)
	for index := range keys {
		keys[index] = "key-" + strconv.Itoa(index)
	}
	setJSONResponse(t, keys)
	actual, err := client.List(t.Context(), "secret", "")
	if err != nil || len(actual) != vaultcli.MaximumCollectionEntries {
		t.Fatalf("List(at limit) = %d keys, %v", len(actual), err)
	}

	tooManyKeys := make([]string, len(keys)+1)
	copy(tooManyKeys, keys)
	tooManyKeys[len(keys)] = "one-too-many"
	setJSONResponse(t, tooManyKeys)
	if _, err = client.List(t.Context(), "secret", ""); err == nil ||
		!strings.Contains(err.Error(), "more than") {
		t.Fatalf("List(over limit) error = %v", err)
	}

	setJSONResponse(t, []string{strings.Repeat("k", vaultcli.MaximumFieldBytes)})
	if _, err = client.List(t.Context(), "secret", ""); err != nil {
		t.Fatalf("List(field at limit) error = %v", err)
	}
	setJSONResponse(t, []string{strings.Repeat("k", vaultcli.MaximumFieldBytes+1)})
	if _, err = client.List(t.Context(), "secret", ""); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("List(oversized field) error = %v", err)
	}
}

func TestClientBoundsVaultMountAndSecretCollections(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, "#!/bin/sh\ncat \"$VLT_TEST_RESPONSE\"\n")
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

	setJSONResponse(t, map[string]any{
		"secret/": map[string]any{
			"type":        "kv",
			"description": strings.Repeat("d", vaultcli.MaximumFieldBytes+1),
			"options":     map[string]string{"version": "2"},
		},
	})
	if _, err := client.ListMounts(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "description exceeds") {
		t.Fatalf("ListMounts(oversized description) error = %v", err)
	}
	mounts := make(map[string]any, vaultcli.MaximumCollectionEntries+1)
	for index := 0; index <= vaultcli.MaximumCollectionEntries; index++ {
		mounts["mount-"+strconv.Itoa(index)+"/"] = map[string]any{}
	}
	setJSONResponse(t, mounts)
	if _, err := client.ListMounts(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "more than") {
		t.Fatalf("ListMounts(over limit) error = %v", err)
	}

	secretData := make(map[string]any, vaultcli.MaximumCollectionEntries+1)
	for index := 0; index <= vaultcli.MaximumCollectionEntries; index++ {
		secretData["key-"+strconv.Itoa(index)] = true
	}
	setJSONResponse(t, map[string]any{
		"data": map[string]any{
			"data":     secretData,
			"metadata": map[string]any{"version": 1},
		},
	})
	if _, err := client.Get(t.Context(), "secret", "large"); err == nil ||
		!strings.Contains(err.Error(), "top-level entries") {
		t.Fatalf("Get(too many entries) error = %v", err)
	}
}

func TestListClassifiesStructuredPermissionError(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	binary := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s\\n' 'Error making API request.' '' 'Code: 403. Errors:' '' '* permission denied' >&2\nexit 2\n",
	)

	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").List(
		t.Context(),
		"secret",
		"",
	)
	if !errors.Is(err, vaultcli.ErrPermissionDenied) {
		t.Fatalf("List() error = %v, want permission denied", err)
	}
}

func TestSensitiveCommandErrorReportsStructuredHTTPStatus(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	binary := writeExecutable(
		t,
		"#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' 'Error making API request.' '' 'Code: 400. Errors:' '' '* server detail must stay hidden' >&2\nexit 2\n",
	)
	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
		t.Context(),
		"secret",
		"apps/api",
		map[string]any{"password": "must-not-leak"},
		7,
	)
	if err == nil || !strings.Contains(err.Error(), "request failed with Vault HTTP 400") {
		t.Fatalf("Put() error = %v, want safe structured HTTP status", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "server detail") {
		t.Fatalf("Put() error leaked sensitive command data: %v", err)
	}
}

func TestMountValidationStopsCommandsBeforeSecretDispatch(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "vault")
	marker := filepath.Join(directory, "secret-command")
	t.Setenv("VLT_TEST_SECRET_COMMAND", marker)
	script := `#!/bin/sh
if [ "$1:$5" = read:sys/internal/ui/mounts/secret ]; then
 cat "$VLT_TEST_RESPONSE"
 exit 0
fi
touch "$VLT_TEST_SECRET_COMMAND"
exit 0
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := vaultcli.NewWithBinary(binary, "https://example.test", "")
	for name, details := range map[string]any{
		"v1":                 map[string]any{"path": "secret/", "type": "kv", "options": map[string]string{"version": "1"}},
		"other engine":       map[string]any{"path": "secret/", "type": "transit", "options": map[string]string{"version": "2"}},
		"wrong root":         map[string]any{"path": "other/", "type": "kv", "options": map[string]string{"version": "2"}},
		"missing metadata":   nil,
		"malformed metadata": "not a mount",
	} {
		t.Run(name, func(t *testing.T) {
			setJSONResponse(t, map[string]any{"data": details})
			for operation, run := range map[string]func() error{
				"list": func() error {
					_, err := client.List(t.Context(), "secret", "")
					return err
				},
				"get": func() error {
					_, err := client.Get(t.Context(), "secret", "api")
					return err
				},
				"put": func() error {
					_, err := client.Put(t.Context(), "secret", "api", map[string]any{"value": true}, 0)
					return err
				},
				"delete": func() error { return client.Delete(t.Context(), "secret", "api") },
			} {
				if err := run(); err == nil || errors.Is(err, vaultcli.ErrOutcomeUnknown) {
					t.Fatalf("%s = %v, want definite mount rejection", operation, err)
				}
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("secret command dispatched: %v", err)
			}
		})
	}
}

func TestSecretNumberPrecisionBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		number string
		valid  bool
	}{
		{"0", true}, {"-0", true}, {"0e-999999999", true}, {"0.1", true}, {"1.00e2", true},
		{"9007199254740992", true}, {"9007199254740993", false}, {"-9007199254740993", false},
		{"1.2345678901234567", true}, {"1.234567890123456789", false},
		{"1e308", true}, {"1e309", false}, {"5e-324", true}, {"1e-324", false}, {"1e-999999999", false},
	} {
		t.Run(test.number, func(t *testing.T) {
			data := map[string]any{"nested": []any{map[string]any{"value": json.Number(test.number)}}}
			_, err := vaultcli.EncodeSecret(data)
			if (err == nil) != test.valid {
				t.Fatalf("EncodeSecret(%s): %v", test.number, err)
			}
			if !test.valid {
				client := vaultcli.NewWithBinary("/does-not-exist", "https://example.test", "")
				_, err = client.Put(t.Context(), "secret", "api", data, 1)
				if err == nil || !strings.Contains(err.Error(), "quote it") ||
					errors.Is(err, vaultcli.ErrOutcomeUnknown) {
					t.Fatalf("Put: %v", err)
				}
			}
		})
	}
	for _, value := range []any{uint64(9007199254740993), int64(-9007199254740993)} {
		if _, err := vaultcli.EncodeSecret(map[string]any{"value": value}); err == nil {
			t.Fatal("native integer precision loss accepted")
		}
	}
}

func TestClientReadsScheduledDeletion(t *testing.T) {
	binary := writeExecutable(t, `#!/bin/sh
printf '%s\n' '{"data":{"data":{"value":"live"},"metadata":{"version":1,"deletion_time":"2099-01-01T00:00:00Z","destroyed":false}}}'
`)
	secret, err := vaultcli.NewWithBinary(binary, "https://example.test", "").Get(t.Context(), "secret", "api")
	if err != nil || secret.Deleted || secret.Data["value"] != "live" {
		t.Fatalf("scheduled secret: %#v, %v", secret, err)
	}
}
