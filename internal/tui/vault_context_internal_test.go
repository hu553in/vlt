package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

func TestFailedVaultSwitchCommitClearsStateAfterTokenRemoval(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://old.example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.auth = vaultcli.AuthInfo{DisplayName: "developer"}
	model.currentMount = testMountName
	model.currentPath = "apps"
	model.selectedPath = "apps/api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"value": true}, Version: 4})
	model.copied = &copiedEntry{key: "value", value: true}
	model.browser.SetItems([]list.Item{browserItem{title: "api", path: "apps/api", kind: itemSecret}})

	model.handleBootstrapCommit(bootstrapCommitResult{
		tokenCleared: true,
		err:          errors.New("config storage is unavailable"),
	})
	if model.screen != screenError || model.errorReturnScreen != screenSetup || model.secret != nil ||
		model.copied != nil || model.auth.DisplayName != "" || model.currentMount != "" ||
		len(model.browser.Items()) != 0 {
		t.Fatalf(
			"failed switch state = screen %d, return %d, secret %t, copied %t, auth %q, mount %q, items %d",
			model.screen,
			model.errorReturnScreen,
			model.secret != nil,
			model.copied != nil,
			model.auth.DisplayName,
			model.currentMount,
			len(model.browser.Items()),
		)
	}
	if model.errorTitle != "Could not change Vault settings" ||
		model.errorView.GetContent() != "config storage is unavailable" {
		t.Fatalf("failed switch error = %q: %q", model.errorTitle, model.errorView.GetContent())
	}
}

func TestChangingVaultRequiresConfirmationAndClearsLoadedContext(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")

	model := newTestModel(
		t,
		config.Config{Address: "https://old.example.test", Namespace: "old", Mounts: []string{"secret"}},
	)
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	model.currentPath = "apps"
	model.selectedPath = "apps/api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"token": "secret"}, Version: 3})
	model.copied = &copiedEntry{key: "token", value: "secret"}
	model.loginInput.SetValue("hvs.secret")
	model.editor.SetValue(`{"token":"secret"}`)
	model.pathInput.SetValue("apps/new")
	model.browser.SetItems([]list.Item{browserItem{title: "api", path: "apps/api", kind: itemSecret}})
	model.beginSetup(screenBrowser)
	model.setupInputs[0].SetValue("https://new.example.test")
	model.setupInputs[1].SetValue("new")
	model.focusSetup(1)

	_, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || model.screen != screenConfirm || model.confirmation != confirmationSwitchVault ||
		model.pendingConfig == nil {
		t.Fatalf("Vault switch did not require confirmation")
	}
	if model.secret == nil || model.copied == nil || model.currentMount == "" {
		t.Fatal("Vault context was cleared before confirmation")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.pendingConfig != nil || model.screen != screenSetup || model.secret == nil {
		t.Fatal("canceling Vault switch changed loaded context")
	}

	model.applyBootstrap(bootstrapResult{
		config:        config.Config{Address: "https://new.example.test", Namespace: "new"},
		client:        vaultcli.NewWithBinary("vault", "https://new.example.test", "new"),
		status:        vaultcli.Status{Version: "2.0.3"},
		targetChanged: true,
		needsLogin:    true,
	})
	if model.secret != nil || model.copied != nil || model.currentMount != "" || model.currentPath != "" ||
		len(model.browser.Items()) != 0 || model.config.Address != "https://new.example.test" ||
		model.screen != screenLogin || model.loginInput.Value() != "" || model.editor.Value() != "" ||
		model.pathInput.Value() != "" {
		t.Fatalf(
			"new Vault state = secret %t, copied %t, mount %q, path %q, items %d, address %q, screen %d, login %d bytes, editor %d bytes, path input %q",
			model.secret != nil,
			model.copied != nil,
			model.currentMount,
			model.currentPath,
			len(model.browser.Items()),
			model.config.Address,
			model.screen,
			len(model.loginInput.Value()),
			len(model.editor.Value()),
			model.pathInput.Value(),
		)
	}
}

func TestVaultSwitchClearsOnlyTheOldTargetToken(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_ADDR", "https://ambient.example.test")
	t.Setenv("VAULT_NAMESPACE", "ambient")

	directory := t.TempDir()
	helperPath := filepath.Join(directory, "token-helper")
	helper := `#!/bin/sh
case "$1:$VAULT_ADDR:${VAULT_NAMESPACE-}" in
  erase:https://old.example.test:old) touch "$VLT_TEST_OLD_MARKER" ;;
  erase:https://new.example.test:new) touch "$VLT_TEST_CANDIDATE_MARKER" ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(helperPath, []byte(helper), 0o700); err != nil {
		t.Fatalf("WriteFile(helper) error = %v", err)
	}
	configPath := filepath.Join(directory, "vault.hcl")
	if err := os.WriteFile(
		configPath,
		[]byte(fmt.Sprintf("token_helper = %q\n", helperPath)),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(helper config) error = %v", err)
	}
	t.Setenv("VAULT_CONFIG_PATH", configPath)
	vaultPath := filepath.Join(directory, "vault")
	if err := os.WriteFile(vaultPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(vault) error = %v", err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Run("active client", func(t *testing.T) { assertVaultSwitchClearsOldTarget(t, true) })
	t.Run("configured target after failed bootstrap", func(t *testing.T) {
		assertVaultSwitchClearsOldTarget(t, false)
	})
}

func assertVaultSwitchClearsOldTarget(t *testing.T, activeClient bool) {
	t.Helper()

	directory := t.TempDir()
	oldMarker := filepath.Join(directory, "old-cleared")
	candidateMarker := filepath.Join(directory, "candidate-cleared")
	t.Setenv("VLT_TEST_OLD_MARKER", oldMarker)
	t.Setenv("VLT_TEST_CANDIDATE_MARKER", candidateMarker)
	model := newTestModel(t, config.Config{
		Address:   "https://old.example.test",
		Namespace: "old",
	})
	if activeClient {
		model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, model.config.Namespace)
	}
	candidate := bootstrapResult{
		config: config.Config{
			Address:   "https://new.example.test",
			Namespace: "new",
		},
		client:        vaultcli.NewWithBinary("/bin/false", "https://new.example.test", "new"),
		targetChanged: true,
	}

	commandMessage := model.startBootstrapCommit(candidate)()
	batch, ok := commandMessage.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("Vault switch command = %T, want a non-empty batch", commandMessage)
	}
	operationMessage := batch[0]()
	message, ok := operationMessage.(operationResultMsg)
	if !ok {
		t.Fatalf("Vault switch result = %T, want operationResultMsg", operationMessage)
	}
	result, ok := message.value.(bootstrapCommitResult)
	if !ok || result.err != nil || !result.tokenCleared {
		t.Fatalf("Vault switch result = %#v", message.value)
	}
	model.finishOperation()

	if _, err := os.Stat(oldMarker); err != nil {
		t.Fatalf("old target token was not cleared: %v", err)
	}
	if _, err := os.Stat(candidateMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate target token was cleared: %v", err)
	}
}

func TestAmbientCredentialsBlockChangingVault(t *testing.T) {
	for _, variable := range []string{
		"VAULT_TOKEN",
		"VAULT_MFA",
		"VAULT_HEADERS",
		"VAULT_CLIENT_CERT",
		"VAULT_CLIENT_KEY",
	} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv(variable, "ambient-credential")

			model := newTestModel(t, config.Config{Address: "https://old.example.test"})
			_, err := model.normalizeSetupConfig(config.Config{Address: "https://new.example.test"})
			if err == nil || !strings.Contains(err.Error(), variable+" is set") {
				t.Fatalf("normalizeSetupConfig() error = %v", err)
			}
		})
	}
}

func TestBootstrapRechecksAmbientCredentialsBeforeCandidateProbe(t *testing.T) {
	t.Setenv("VAULT_HEADERS", `{"Authorization":"Bearer secret"}`)

	model := newTestModel(t, config.Config{Address: "https://old.example.test"})
	command := model.startBootstrap(config.Config{Address: "https://new.example.test"}, false)
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("bootstrap command = %T", command())
	}
	message, ok := batch[0]().(operationResultMsg)
	if !ok {
		t.Fatalf("bootstrap result = %T", batch[0]())
	}
	result, ok := message.value.(bootstrapResult)
	if !ok || result.err == nil || !strings.Contains(result.err.Error(), "VAULT_HEADERS is set") {
		t.Fatalf("bootstrap result = %#v", message.value)
	}
}

func TestFirstConnectionDisclosesExistingVaultCLICredentials(t *testing.T) {
	t.Setenv("VAULT_HEADERS", `{"Authorization":"Bearer secret"}`)

	model := newTestModel(t, config.Config{})
	candidate, err := model.normalizeSetupConfig(
		config.Config{Address: "https://first.example.test"},
	)
	if err != nil {
		t.Fatalf("normalizeSetupConfig() error = %v", err)
	}
	if model.changingVaultTarget(candidate) {
		t.Fatal("first connection was classified as a cross-target switch")
	}
	model.resize(minimumWidth, minimumHeight)
	model.pendingConfig = &candidate
	model.beginConfirmation(confirmationSwitchVault)
	output := model.View().Content
	if !strings.Contains(output, "Connect to this Vault?") ||
		!strings.Contains(output, "Existing Vault CLI credentials may be sent to this target") ||
		strings.Contains(output, "cleared but not revoked") {
		t.Fatalf("first connection confirmation = %q", output)
	}
}

func TestConfiguredMountFailureCanBeCorrectedInPlace(t *testing.T) {
	t.Parallel()

	store := config.NewStore(filepath.Join(t.TempDir(), "config.toml"))
	loaded := config.Config{Address: "https://example.test", Mounts: []string{"keep", "wrong"}}

	model := New(store, loaded, nil)
	t.Cleanup(model.Close)
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser

	model.handleEntries(entriesResult{mount: "wrong", err: errors.New("literal\n\tfailure")})
	if model.screen != screenError || model.errorReturnScreen != screenBrowser || model.editingMount != "" ||
		model.errorView.GetContent() != "literal\n    failure" {
		t.Fatalf("mount correction error state = screen %d, return %d, mount %q, error %q",
			model.screen, model.errorReturnScreen, model.editingMount, model.errorView.GetContent())
	}
	if !strings.Contains(model.View().Content, "[m] correct mount") {
		t.Fatal("mount error did not offer an explicit correction")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if model.screen != screenMount || !strings.Contains(model.View().Content, "Correct KV v2 mount") {
		t.Fatal("explicit mount correction did not open the input")
	}
	model.mountInput.SetValue("fixed")
	_, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("corrected mount did not start a save")
	}
	if !slices.Equal(model.config.Mounts, []string{"keep", "wrong"}) {
		t.Fatalf("pending correction mutated live config: %v", model.config.Mounts)
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("mount save command = %T", command())
	}
	_, _ = model.Update(batch[0]())
	if !slices.Equal(model.config.Mounts, []string{"fixed", "keep"}) || model.editingMount != "" ||
		model.screen != screenBrowser {
		t.Fatalf("corrected mounts = %v, editing %q, screen %d", model.config.Mounts, model.editingMount, model.screen)
	}
}

func TestSavingConfiguredMountPreservesDiscoveredMounts(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.handleMounts(mountsResult{
		mounts: []vaultcli.Mount{{Path: "discovered", Description: kvV2Description}},
	})
	model.handleMountSaved(mountSavedResult{config: config.Config{
		Address: "https://example.test",
		Mounts:  []string{"configured"},
	}})

	if !reflect.DeepEqual(model.mounts, []vaultcli.Mount{
		{Path: "configured", Description: configuredMountDescription},
		{Path: "discovered", Description: kvV2Description},
	}) {
		t.Fatalf("mounts after config save = %#v", model.mounts)
	}
}
