package vaultcli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hu553in/vlt/internal/testutil"
	"github.com/hu553in/vlt/internal/vaultcli"
)

const kvV2Preflight = `case "$1:$5" in
  read:sys/internal/ui/mounts/secret)
    printf '%s\n' '{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}'
    exit 0 ;;
esac
`

const asynchronousTestTimeout = 10 * time.Second

func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}

func TestClientReadFlow(t *testing.T) {
	binary, files := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_ADDR", "https://stale.example.com")
	t.Setenv("VAULT_AGENT_ADDR", "https://stale-agent.example.com")
	t.Setenv("VAULT_NAMESPACE", "stale")
	t.Setenv("VAULT_FORMAT", "table")

	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "team/platform")
	ctx := t.Context()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Version != "2.0.3" || status.Sealed {
		t.Errorf("Status() = %#v", status)
	}

	auth, err := client.AuthStatus(ctx)
	if err != nil {
		t.Fatalf("AuthStatus() error = %v", err)
	}
	if auth.DisplayName != "developer" || auth.FromEnv {
		t.Errorf("AuthStatus() = %#v", auth)
	}

	mounts, err := client.ListMounts(ctx)
	if err != nil {
		t.Fatalf("ListMounts() error = %v", err)
	}
	if len(mounts) != 1 || mounts[0].Path != "secret" || mounts[0].Description != "application secrets" {
		t.Errorf("ListMounts() = %#v", mounts)
	}

	keys, err := client.List(ctx, "secret", "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !slices.Equal(keys, []string{"a", "folder/", "z"}) {
		t.Errorf("List() = %v", keys)
	}

	secret, err := client.Get(ctx, "secret", "apps/api")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if secret.Version != 7 || secret.Data["password"] != "hunter2" {
		t.Errorf("Get() = %#v", secret)
	}
	number, ok := secret.Data["replicas"].(json.Number)
	if !ok || number.String() != "2" {
		t.Errorf("replicas = %#v", secret.Data["replicas"])
	}

	if actual := readFile(t, files.address); actual != "https://vault.example.com" {
		t.Errorf("VAULT_ADDR = %q", actual)
	}
	if actual := readFile(t, files.agentAddress); actual != "" {
		t.Errorf("VAULT_AGENT_ADDR = %q, want empty", actual)
	}
	if actual := readFile(t, files.namespace); actual != "team/platform" {
		t.Errorf("VAULT_NAMESPACE = %q", actual)
	}
	if actual := readFile(t, files.format); actual != "json" {
		t.Errorf("VAULT_FORMAT = %q", actual)
	}
}

func TestStatusNeverSendsConfiguredCredentials(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "must-not-reach-candidate")
	t.Setenv("VAULT_MFA", "must-not-reach-candidate")
	capturedToken := filepath.Join(t.TempDir(), "token")
	capturedMFA := filepath.Join(t.TempDir(), "mfa")
	t.Setenv("VLT_TEST_TOKEN", capturedToken)
	t.Setenv("VLT_TEST_MFA", capturedMFA)
	binary := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s' \"$VAULT_TOKEN\" > \"$VLT_TEST_TOKEN\"\n"+
			"printf '%s' \"$VAULT_MFA\" > \"$VLT_TEST_MFA\"\n"+
			"printf '%s\\n' '{\"version\":\"2.0.3\",\"sealed\":false}'\n",
	)

	if _, err := vaultcli.NewWithBinary(binary, "https://candidate.example.com", "").
		Status(t.Context()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	actual := readFile(t, capturedToken)
	if actual == "" || actual == "must-not-reach-candidate" {
		t.Fatalf("candidate received token %q", actual)
	}
	if mfa := readFile(t, capturedMFA); mfa != "" {
		t.Fatalf("candidate received MFA credential %q", mfa)
	}
}

func TestClearTokenUsesOfficialHelper(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.WriteFile(tokenPath, []byte("hvs.old-target"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := vaultcli.NewWithBinary("/bin/false", "https://candidate.example.com", "").ClearToken(
		t.Context(),
	); err != nil {
		t.Fatalf("ClearToken() error = %v", err)
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cached token still exists: %v", err)
	}
}

func TestClientMutationsUseStdinAndCAS(t *testing.T) {
	binary, files := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "")

	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")
	secretData := map[string]any{"password": "not-in-argv", "replicas": 3}
	writtenVersion, err := client.Put(t.Context(), "secret", "apps/api", secretData, 7)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if writtenVersion != 8 {
		t.Fatalf("Put() version = %d, want 8", writtenVersion)
	}

	arguments := readFile(t, files.arguments)
	if !strings.Contains(arguments, "<-cas=7>") || !strings.Contains(arguments, "<apps/api>") {
		t.Errorf("Put() arguments = %s", arguments)
	}
	if strings.Contains(arguments, "not-in-argv") {
		t.Errorf("secret leaked into argv: %s", arguments)
	}
	payload := readFile(t, files.stdin)
	if payload != "{\"password\":\"not-in-argv\",\"replicas\":3}" {
		t.Errorf("Put() stdin = %q", payload)
	}

	if deleteError := client.Delete(t.Context(), "secret", "apps/api"); deleteError != nil {
		t.Fatalf("Delete() error = %v", deleteError)
	}
	arguments = readFile(t, files.arguments)
	wantDeleteArguments := "<kv>\n<delete>\n<-mount=secret>\n<-non-interactive>\n<-->\n<apps/api>\n"
	if arguments != wantDeleteArguments {
		t.Errorf("Delete() arguments = %q, want %q", arguments, wantDeleteArguments)
	}

	if deleteError := client.Delete(t.Context(), "secret", "-root-secret"); deleteError != nil {
		t.Fatalf("Delete(option-like path) error = %v", deleteError)
	}
	arguments = readFile(t, files.arguments)
	if !strings.Contains(arguments, "<-->\n<-root-secret>") {
		t.Errorf("option-like path is not separated from flags: %s", arguments)
	}
}

func TestClientPreservesAddressableWhitespaceInSecretPaths(t *testing.T) {
	binary, files := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "test-token")
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

	assertPath := func(operation, path string) {
		t.Helper()

		arguments := readFile(t, files.arguments)
		if !strings.Contains(arguments, "<"+path+">") {
			t.Fatalf("%s arguments changed exact path %q: %q", operation, path, arguments)
		}
	}

	if _, err := client.List(t.Context(), "secret", "parent/ folder"); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	assertPath("List", "parent/ folder")
	if _, err := client.Get(t.Context(), "secret", "parent/ name"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertPath("Get", "parent/ name")
	if _, err := client.Put(
		t.Context(),
		"secret",
		"parent/ name",
		map[string]any{"value": true},
		0,
	); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	assertPath("Put", "parent/ name")
	if err := client.Delete(t.Context(), "secret", "parent/ name"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertPath("Delete", "parent/ name")
}

func TestClientReadsASecretAtTheWriteLimit(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	base, err := json.Marshal(map[string]any{"value": ""})
	if err != nil {
		t.Fatalf("Marshal(base) error = %v", err)
	}
	data := map[string]any{"value": strings.Repeat("x", vaultcli.MaximumSecretBytes-len(base))}
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal(data) error = %v", err)
	}
	if len(payload) != vaultcli.MaximumSecretBytes {
		t.Fatalf("payload length = %d, want %d", len(payload), vaultcli.MaximumSecretBytes)
	}
	response, err := json.MarshalIndent(map[string]any{
		"data": map[string]any{
			"data": data,
			"metadata": map[string]any{
				"version": 1,
			},
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("Marshal(response) error = %v", err)
	}
	if len(response) <= vaultcli.MaximumSecretBytes {
		t.Fatalf("response length = %d, want larger than payload limit", len(response))
	}
	responsePath := filepath.Join(t.TempDir(), "response.json")
	if err = os.WriteFile(responsePath, response, 0o600); err != nil {
		t.Fatalf("WriteFile(response) error = %v", err)
	}
	t.Setenv("VLT_TEST_RESPONSE", responsePath)
	binary := writeExecutable(t, `#!/bin/sh
case "$1:$2" in
  kv:put) cat >/dev/null ;;
  kv:get) cat "$VLT_TEST_RESPONSE" ;;
  *) exit 64 ;;
esac
`)
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")
	if _, err = client.Put(t.Context(), "secret", "large", data, 0); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	secret, err := client.Get(t.Context(), "secret", "large")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if secret.Version != 1 || secret.Data["value"] != data["value"] {
		t.Fatal("Get() did not preserve the boundary secret")
	}
}

func TestClientReturnsDeletedLatestVersion(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, `#!/bin/sh
printf '%s\n' '{"data":{"data":null,"metadata":{"version":7,"deletion_time":"2026-08-12T00:00:00Z","destroyed":false}}}'
`)

	secret, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Get(
		t.Context(),
		"secret",
		"deleted",
	)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !secret.Deleted || secret.Version != 7 || len(secret.Data) != 0 {
		t.Fatalf("Get() = %#v, want deleted version 7", secret)
	}
}

func TestCanceledMutationAfterDispatchHasAnUnknownOutcome(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	committed := filepath.Join(t.TempDir(), "committed")
	t.Setenv("VLT_TEST_COMMITTED", committed)
	binary := writeExecutable(t, "#!/bin/sh\ntouch \"$VLT_TEST_COMMITTED\"\nexec sleep 60\n")
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
			ctx,
			"secret",
			"apps/api",
			map[string]any{"value": true},
			1,
		)
		result <- err
	}()
	waitForFile(t, committed)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, vaultcli.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Put() error = %v, want unknown canceled outcome", err)
		}
	case <-time.After(asynchronousTestTimeout):
		t.Fatal("Put() did not stop after cancellation")
	}
}

func TestDeleteFailureAfterDispatchHasAnUnknownOutcome(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	committed := filepath.Join(t.TempDir(), "committed")
	t.Setenv("VLT_TEST_COMMITTED", committed)
	binary := writeExecutable(t, `#!/bin/sh
touch "$VLT_TEST_COMMITTED"
printf '%s\n' 'response was lost' >&2
exit 2
`)
	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")

	err := client.Delete(t.Context(), "secret", "apps/api")
	if !errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Delete() error = %v, want unknown outcome", err)
	}
	if _, statError := os.Stat(committed); statError != nil {
		t.Fatalf("fake delete did not reach its commit point: %v", statError)
	}

	err = vaultcli.NewWithBinary(filepath.Join(t.TempDir(), "missing"), "https://vault.example.com", "").Delete(
		t.Context(),
		"secret",
		"apps/api",
	)
	if err == nil || errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Delete() start error = %v, want a definite failure", err)
	}
}

func TestMutationOutcomeUsesTheProcessStartBoundary(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	client := vaultcli.NewWithBinary(
		writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' 'response was lost' >&2\nexit 1\n"),
		"https://vault.example.com",
		"",
	)

	if _, err := client.Put(
		t.Context(),
		"secret",
		"apps/api",
		map[string]any{"value": true},
		1,
	); !errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Put() error = %v, want unknown dispatched outcome", err)
	}
	if err := client.Login(t.Context(), "hvs.valid-shape"); !errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Login() error = %v, want unknown dispatched outcome", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Put(
		canceled,
		"secret",
		"apps/api",
		map[string]any{"value": true},
		1,
	); err == nil || errors.Is(err, vaultcli.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() pre-start error = %v, want definite cancellation", err)
	}
	if err := client.Delete(
		canceled,
		"secret",
		"apps/api",
	); err == nil || errors.Is(err, vaultcli.ErrOutcomeUnknown) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() pre-start error = %v, want definite cancellation", err)
	}
}

func TestStructuredMutationRejectionHasADefiniteOutcome(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, `#!/bin/sh
printf '%s\n' 'Error making API request.' '' 'Code: 400. Errors:' '' '* CAS mismatch' >&2
exit 2
`)
	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
		t.Context(),
		"secret",
		"apps/api",
		map[string]any{"value": true},
		1,
	)
	if err == nil || errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Put() error = %v, want definite Vault rejection", err)
	}
}

func TestStructuredServerErrorHasUnknownMutationOutcome(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, `#!/bin/sh
printf '%s\n' 'Error making API request.' '' 'Code: 503. Errors:' '' '* temporarily unavailable' >&2
exit 2
`)
	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
		t.Context(),
		"secret",
		"apps/api",
		map[string]any{"value": true},
		1,
	)
	if !errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Put() error = %v, want unknown outcome after Vault server error", err)
	}
}

func TestConfirmedMutationSuccessDoesNotDependOnCommandOutput(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "test-token")
	binary := writeExecutable(t, "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=33 2>/dev/null\n")
	version, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Put(
		t.Context(),
		"secret",
		"apps/api",
		map[string]any{"value": true},
		1,
	)
	if err != nil || version != 2 {
		t.Fatalf("Put() = version %d, %v, want confirmed version 2", version, err)
	}
}

func TestFailedTokenHelperOperationsNeverReturnCredentialOutput(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	const credential = "hvs.must-never-reach-an-error"
	helper := writeExecutable(
		t,
		"#!/bin/sh\nprintf '%s\\n' '"+credential+"'\nprintf '%s\\n' 'also-sensitive' >&2\nexit 1\n",
	)
	configureExternalTokenHelper(t, helper)

	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")
	_, readError := client.HasToken(t.Context())
	for operation, err := range map[string]error{
		"read":  readError,
		"erase": client.ClearToken(t.Context()),
	} {
		if err == nil {
			t.Fatalf("token helper %s unexpectedly succeeded", operation)
		}
		if strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), "also-sensitive") {
			t.Fatalf("token helper %s leaked helper output: %v", operation, err)
		}
	}
}

func TestExternalTokenHelperUsesTheClientTarget(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		namespace string
	}{
		{name: "namespace", namespace: "team/platform"},
		{name: "empty namespace"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("VAULT_TOKEN", "")
			t.Setenv("VAULT_ADDR", "https://ambient.example.com")
			t.Setenv("VAULT_AGENT_ADDR", "https://ambient-agent.example.com")
			t.Setenv("VAULT_NAMESPACE", "ambient")
			t.Setenv("VLT_TEST_HELPER_VALUE", "preserved")
			getEnvironment := filepath.Join(t.TempDir(), "get-environment")
			eraseEnvironment := filepath.Join(t.TempDir(), "erase-environment")
			t.Setenv("VLT_TEST_GET_ENVIRONMENT", getEnvironment)
			t.Setenv("VLT_TEST_ERASE_ENVIRONMENT", eraseEnvironment)
			helper := writeExecutable(t, `#!/bin/sh
destination="$VLT_TEST_GET_ENVIRONMENT"
if [ "$1" = erase ]; then destination="$VLT_TEST_ERASE_ENVIRONMENT"; fi
printf '%s\n%s\n%s' "$VAULT_ADDR" "${VAULT_NAMESPACE-}" "$VLT_TEST_HELPER_VALUE" > "$destination"
if [ "$1" = get ]; then printf '%s' 'hvs.target-token'; fi
`)
			configureExternalTokenHelper(t, helper)
			client := vaultcli.NewWithBinary(
				writeExecutable(t, "#!/bin/sh\nexit 0\n"),
				"https://vault.example.com",
				testCase.namespace,
			)

			hasToken, err := client.HasToken(t.Context())
			if err != nil || !hasToken {
				t.Fatalf("HasToken() = %t, %v", hasToken, err)
			}
			outcome, err := client.Logout(t.Context())
			if err != nil || !outcome.TokenRevoked || !outcome.LocalTokenCleared {
				t.Fatalf("Logout() = %#v, %v", outcome, err)
			}
			want := "https://vault.example.com\n" + testCase.namespace + "\npreserved"
			if actual := readFile(t, getEnvironment); actual != want {
				t.Fatalf("get environment = %q, want %q", actual, want)
			}
			if actual := readFile(t, eraseEnvironment); actual != want {
				t.Fatalf("erase environment = %q, want %q", actual, want)
			}
		})
	}
}

func TestExternalTokenHelperHonorsContext(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("VLT_TEST_HELPER_STARTED", started)
	helper := writeExecutable(t, `#!/bin/sh
sleep 60 &
touch "$VLT_TEST_HELPER_STARTED"
wait
`)
	configureExternalTokenHelper(t, helper)
	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")

	t.Run("read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := client.HasToken(ctx)
			result <- err
		}()
		waitForFile(t, started)
		cancel()
		assertCanceledHelperResult(t, result, false)
	})

	if err := os.Remove(started); err != nil {
		t.Fatalf("Remove(start marker) error = %v", err)
	}
	t.Run("erase", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { result <- client.ClearToken(ctx) }()
		waitForFile(t, started)
		cancel()
		assertCanceledHelperResult(t, result, true)
	})
}

func TestExternalTokenHelperBoundsOrphanedPipeWait(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	childPIDPath := filepath.Join(t.TempDir(), "child-pid")
	t.Setenv("VLT_TEST_CHILD_PID", childPIDPath)
	helper := writeExecutable(t, `#!/bin/sh
sleep 60 &
printf '%s' "$!" > "$VLT_TEST_CHILD_PID"
printf '%s' 'hvs.unusable-until-pipes-close'
`)
	configureExternalTokenHelper(t, helper)
	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")
	result := make(chan error, 1)
	go func() {
		_, err := client.HasToken(t.Context())
		result <- err
	}()
	childPID, err := strconv.Atoi(waitForFileContent(t, childPIDPath))
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	child, err := os.FindProcess(childPID)
	if err != nil {
		t.Fatalf("find child process: %v", err)
	}
	t.Cleanup(func() { _ = child.Kill() })

	select {
	case err = <-result:
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("HasToken() error = %v, want exec.ErrWaitDelay", err)
		}
	case <-time.After(5 * time.Second):
		_ = child.Kill()
		<-result
		t.Fatal("token helper remained blocked on an orphaned pipe")
	}
	waitForProcessExit(t, childPID)
}

func TestLoginUsesStdinAndRedactsErrors(t *testing.T) {
	binary, files := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VLT_TEST_MODE", "login-failure")

	const token = "hvs.test-token-must-stay-private"
	err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Login(t.Context(), token)
	if err == nil {
		t.Fatal("Login() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("Login() error leaked token: %v", err)
	}
	if strings.Contains(err.Error(), "test-token-must-stay-private") {
		t.Errorf("Login() error leaked a token fragment: %v", err)
	}
	if arguments := readFile(t, files.arguments); strings.Contains(arguments, token) {
		t.Errorf("token leaked into argv: %s", arguments)
	}
	if actual := strings.TrimSpace(readFile(t, files.stdin)); actual != token {
		t.Errorf("Login() stdin = %q", actual)
	}
}

func TestLoginRejectsOversizedTokenBeforeDispatch(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")

	err := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "").Login(
		t.Context(),
		strings.Repeat("x", vaultcli.MaximumFieldBytes+1),
	)
	if err == nil || !strings.Contains(err.Error(), "token exceeds") ||
		errors.Is(err, vaultcli.ErrOutcomeUnknown) {
		t.Fatalf("Login(oversized token) error = %v", err)
	}
}

func TestAuthStatusPreservesPermissionDenied(t *testing.T) {
	binary, _ := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VLT_TEST_MODE", "permission")

	_, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").AuthStatus(t.Context())
	if !errors.Is(err, vaultcli.ErrPermissionDenied) {
		t.Fatalf("AuthStatus() error = %v, want ErrPermissionDenied", err)
	}
}

func TestHasTokenUsesEnvironmentAndOfficialHelper(t *testing.T) {
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.Remove(tokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove() error = %v", err)
	}

	t.Setenv("VAULT_TOKEN", "environment-token")
	hasToken, err := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "").HasToken(
		t.Context(),
	)
	if err != nil || !hasToken {
		t.Fatalf("HasToken() = %t, %v, want true", hasToken, err)
	}

	t.Setenv("VAULT_TOKEN", "")
	client := vaultcli.NewWithBinary("/bin/false", "https://vault.example.com", "")
	hasToken, err = client.HasToken(t.Context())
	if err != nil || hasToken {
		t.Fatalf("HasToken() without token = %t, %v, want false", hasToken, err)
	}
	if writeError := os.WriteFile(tokenPath, []byte("hvs.cached\n"), 0o600); writeError != nil {
		t.Fatalf("WriteFile() error = %v", writeError)
	}
	hasToken, err = client.HasToken(t.Context())
	if err != nil || !hasToken {
		t.Fatalf("HasToken() with helper token = %t, %v, want true", hasToken, err)
	}
}

func TestEnvironmentTokenCannotBeReplaced(t *testing.T) {
	binary, files := fakeVault(t)
	t.Setenv("VAULT_TOKEN", "environment-token")

	client := vaultcli.NewWithBinary(binary, "https://vault.example.com", "")
	if err := client.Login(t.Context(), "replacement"); !errors.Is(err, vaultcli.ErrEnvironmentToken) {
		t.Fatalf("Login() error = %v", err)
	}
	logout, err := client.Logout(t.Context())
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !logout.TokenRevoked || logout.LocalTokenCleared {
		t.Errorf("Logout() = %#v", logout)
	}
	arguments := readFile(t, files.arguments)
	if !strings.Contains(arguments, "<token>") || !strings.Contains(arguments, "<revoke>") ||
		!strings.Contains(arguments, "<-self>") {
		t.Errorf("Logout() arguments = %s", arguments)
	}
}

func TestLogoutReportsPartialLocalSuccess(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.WriteFile(tokenPath, []byte("hvs.local"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	binary := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' 'revoke unavailable' >&2\nexit 2\n")

	outcome, err := vaultcli.NewWithBinary(binary, "https://vault.example.com", "").Logout(t.Context())
	if err == nil {
		t.Fatal("Logout() unexpectedly succeeded")
	}
	if outcome.TokenRevoked || !outcome.LocalTokenCleared {
		t.Fatalf("Logout() = %#v, want local-only success", outcome)
	}
	if _, statError := os.Stat(tokenPath); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf("cached token still exists: %v", statError)
	}
}

func TestLogoutPreservesSuccessfulRevokeWhenHelperEraseIsCanceled(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	started := filepath.Join(t.TempDir(), "erase-started")
	t.Setenv("VLT_TEST_HELPER_STARTED", started)
	helper := writeExecutable(t, `#!/bin/sh
touch "$VLT_TEST_HELPER_STARTED"
sleep 60 &
wait
`)
	configureExternalTokenHelper(t, helper)
	client := vaultcli.NewWithBinary(
		writeExecutable(t, "#!/bin/sh\nexit 0\n"),
		"https://vault.example.com",
		"",
	)
	ctx, cancel := context.WithCancel(t.Context())
	type result struct {
		outcome vaultcli.LogoutOutcome
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		outcome, err := client.Logout(ctx)
		completed <- result{outcome: outcome, err: err}
	}()
	waitForFile(t, started)
	cancel()

	select {
	case actual := <-completed:
		if !actual.outcome.TokenRevoked || actual.outcome.LocalTokenCleared {
			t.Fatalf("Logout() = %#v, want successful revoke only", actual.outcome)
		}
		if !errors.Is(actual.err, vaultcli.ErrOutcomeUnknown) || !errors.Is(actual.err, context.Canceled) {
			t.Fatalf("Logout() error = %v, want unknown canceled helper erase", actual.err)
		}
	case <-time.After(asynchronousTestTimeout):
		t.Fatal("Logout() did not stop after helper cancellation")
	}
}

type fakeFiles struct {
	arguments    string
	stdin        string
	address      string
	agentAddress string
	namespace    string
	format       string
}

func fakeVault(t *testing.T) (string, fakeFiles) {
	t.Helper()

	directory := t.TempDir()
	files := fakeFiles{
		arguments:    filepath.Join(directory, "arguments"),
		stdin:        filepath.Join(directory, "stdin"),
		address:      filepath.Join(directory, "address"),
		agentAddress: filepath.Join(directory, "agent-address"),
		namespace:    filepath.Join(directory, "namespace"),
		format:       filepath.Join(directory, "format"),
	}
	t.Setenv("VLT_TEST_ARGS", files.arguments)
	t.Setenv("VLT_TEST_STDIN", files.stdin)
	t.Setenv("VLT_TEST_ADDR", files.address)
	t.Setenv("VLT_TEST_AGENT_ADDR", files.agentAddress)
	t.Setenv("VLT_TEST_NAMESPACE", files.namespace)
	t.Setenv("VLT_TEST_FORMAT", files.format)
	t.Setenv("VLT_TEST_MODE", "")

	script := "#!/bin/sh\n" + kvV2Preflight +
		"for argument in \"$@\"; do printf '<%s>\\n' \"$argument\"; done > \"$VLT_TEST_ARGS\"\n" +
		"printf '%s' \"$VAULT_ADDR\" > \"$VLT_TEST_ADDR\"\n" +
		"printf '%s' \"$VAULT_AGENT_ADDR\" > \"$VLT_TEST_AGENT_ADDR\"\n" +
		"printf '%s' \"$VAULT_NAMESPACE\" > \"$VLT_TEST_NAMESPACE\"\n" +
		"printf '%s' \"$VAULT_FORMAT\" > \"$VLT_TEST_FORMAT\"\n" +
		"case \"$1:$2\" in\n" +
		"  status:-format=json) printf '%s\\n' '{\"version\":\"2.0.3\",\"sealed\":false}' ;;\n" +
		"  token:lookup)\n" +
		"    if [ \"$VLT_TEST_MODE\" = permission ]; then printf '%s\\n' 'Error making API request.' '' 'Code: 403. Errors:' '' '* permission denied' >&2; exit 2; fi\n" +
		"    printf '%s\\n' '{\"data\":{\"display_name\":\"developer\"}}' ;;\n" +
		"  secrets:list) printf '%s\\n' '{\"secret/\":{\"type\":\"kv\",\"description\":\"application secrets\",\"options\":{\"version\":\"2\"}},\"kv1/\":{\"type\":\"kv\",\"options\":{\"version\":\"1\"}},\"sys/\":{\"type\":\"system\",\"options\":{}}}' ;;\n" +
		"  kv:list) printf '%s\\n' '[\"z\",\"folder/\",\"a\"]' ;;\n" +
		"  kv:get) printf '%s\\n' '{\"data\":{\"data\":{\"password\":\"hunter2\",\"replicas\":2},\"metadata\":{\"version\":7}}}' ;;\n" +
		"  kv:put) cat > \"$VLT_TEST_STDIN\"; printf '%s\\n' '{\"data\":{\"version\":8}}' ;;\n" +
		"  kv:delete) : ;;\n" +
		"  login:-no-print)\n" +
		"    cat > \"$VLT_TEST_STDIN\"\n" +
		"    if [ \"$VLT_TEST_MODE\" = login-failure ]; then printf '%s\\n' 'bad token fragment test-token-must-stay-private' >&2; exit 2; fi ;;\n" +
		"  token:revoke) : ;;\n" +
		"  *) printf 'unexpected command: %s %s\\n' \"$1\" \"$2\" >&2; exit 64 ;;\n" +
		"esac\n"

	binary := filepath.Join(directory, "vault")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return binary, files
}

func writeExecutable(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vault")
	contents = strings.Replace(contents, "#!/bin/sh\n", "#!/bin/sh\n"+kvV2Preflight, 1)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return path
}

func setJSONResponse(t *testing.T, value any) {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(response) error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "response.json")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(response) error = %v", err)
	}
	t.Setenv("VLT_TEST_RESPONSE", path)
}

func configureExternalTokenHelper(t *testing.T, helper string) {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "vault.hcl")
	contents := "token_helper = " + strconv.Quote(helper) + "\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(helper config) error = %v", err)
	}
	t.Setenv("VAULT_CONFIG_PATH", configPath)
}

func assertCanceledHelperResult(t *testing.T, result <-chan error, unknown bool) {
	t.Helper()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("helper error = %v, want context cancellation", err)
		}
		if errors.Is(err, vaultcli.ErrOutcomeUnknown) != unknown {
			t.Fatalf(
				"helper error = %v, unknown outcome = %t, want %t",
				err,
				errors.Is(err, vaultcli.ErrOutcomeUnknown),
				unknown,
			)
		}
	case <-time.After(asynchronousTestTimeout):
		t.Fatal("token helper did not stop after cancellation")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(asynchronousTestTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("file %q was not created", path)
}

func waitForFileContent(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(asynchronousTestTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadFile(%q) error = %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("file %q remained empty", path)

	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	return string(data)
}
