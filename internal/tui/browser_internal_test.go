package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

func TestOperationErrorsUseTheLiteralModal(t *testing.T) {
	t.Parallel()

	operationError := errors.New("first line\r\n\tsecond line")
	model := New(
		config.NewStore(filepath.Join(t.TempDir(), "config.toml")),
		config.Config{},
		operationError,
	)
	t.Cleanup(model.Close)
	model.resize(minimumWidth, minimumHeight)
	if model.screen != screenError || model.errorTitle != "Could not load configuration" ||
		model.errorView.GetContent() != "first line\n    second line" {
		t.Fatalf("config error modal = title %q, content %q", model.errorTitle, model.errorView.GetContent())
	}

	model.screen = screenBrowser
	model.handleLogin(loginResult{err: operationError})
	if model.screen != screenError || model.errorTitle != authenticationError ||
		model.errorView.GetContent() != "first line\n    second line" {
		t.Fatalf("login error modal = title %q, content %q", model.errorTitle, model.errorView.GetContent())
	}
}

func TestWheelRoutesDuringFilteringButIgnoresHeaderAndFooter(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.browser.SetItems([]list.Item{
		browserItem{title: "first"},
		browserItem{title: "second"},
		browserItem{title: "third"},
	})
	_, _ = model.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !model.browser.SettingFilter() {
		t.Fatal("browser did not enter filter mode")
	}
	_, _ = model.Update(tea.MouseWheelMsg{X: 1, Y: 2, Button: tea.MouseWheelDown})
	if model.browser.Index() != 1 {
		t.Fatalf("filtering wheel selection = %d, want 1", model.browser.Index())
	}
	_, _ = model.Update(tea.MouseWheelMsg{X: 1, Y: 0, Button: tea.MouseWheelDown})
	_, _ = model.Update(tea.MouseWheelMsg{X: 1, Y: minimumHeight - 1, Button: tea.MouseWheelDown})
	if model.browser.Index() != 1 {
		t.Fatalf("header/footer wheel moved selection to %d", model.browser.Index())
	}
}

func TestFilterResultAppliesAfterFocusSwitchAndSelectsFirstMatch(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	items := []list.Item{
		browserItem{title: "item one"},
		browserItem{title: "item two"},
		browserItem{title: "item three"},
		browserItem{title: "target one"},
		browserItem{title: "target two"},
	}
	model.browser.SetItems(items)
	model.browser.SetFilterText("item")
	model.browser.Select(2)
	model.browser.FilterInput.SetValue("target")
	command := model.browser.SetItems(items)
	if command == nil {
		t.Fatal("changing filtered items did not schedule filtering")
	}

	model.focus = paneSecret
	model.browserFilterGeneration++
	model.browserFilterPending = true
	message := tagBrowserFilterCommand(command, model.browserFilterGeneration)()
	_, _ = model.Update(message)

	visible := model.browser.VisibleItems()
	selected, ok := model.browser.SelectedItem().(browserItem)
	if len(visible) != 2 || !ok || selected.title != "target one" || model.browser.Index() != 0 {
		t.Fatalf(
			"filtered browser = %d items, selected %#v at %d",
			len(visible),
			model.browser.SelectedItem(),
			model.browser.Index(),
		)
	}
}

func TestBrowserKeepsLatestFilterResult(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.browser.SetItems([]list.Item{
		browserItem{title: "alpha"},
		browserItem{title: "beta"},
	})

	_, _ = model.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	_, oldCommand := model.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	oldResult := browserFilterResult(t, oldCommand)
	_, latestCommand := model.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	latestResult := browserFilterResult(t, latestCommand)
	if !model.browserFilterPending || latestResult.generation <= oldResult.generation {
		t.Fatalf(
			"filter state = pending %t, old generation %d, latest generation %d",
			model.browserFilterPending,
			oldResult.generation,
			latestResult.generation,
		)
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !model.browser.SettingFilter() {
		t.Fatal("accepted a filter before its latest result arrived")
	}

	_, _ = model.Update(latestResult)
	_, _ = model.Update(oldResult)
	visible := model.browser.VisibleItems()
	if model.browserFilterPending || model.browser.FilterValue() != "al" || len(visible) != 1 {
		t.Fatalf(
			"latest filter = pending %t, value %q, items %#v",
			model.browserFilterPending,
			model.browser.FilterValue(),
			visible,
		)
	}
	item, ok := visible[0].(browserItem)
	if !ok || item.title != "alpha" {
		t.Fatalf("latest filtered item = %#v", visible[0])
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.browser.FilterState() != list.FilterApplied {
		t.Fatalf("accepted filter state = %s", model.browser.FilterState())
	}
}

func browserFilterResult(t *testing.T, command tea.Cmd) browserFilterResultMsg {
	t.Helper()
	if command == nil {
		t.Fatal("filter input did not schedule filtering")
	}
	result, ok := findBrowserFilterResult(command())
	if !ok {
		t.Fatal("filter command did not return a tagged result")
	}

	return result
}

func findBrowserFilterResult(message tea.Msg) (browserFilterResultMsg, bool) {
	switch typedMessage := message.(type) {
	case browserFilterResultMsg:
		return typedMessage, true
	case tea.BatchMsg:
		for _, t := range slices.Backward(typedMessage) {
			if result, ok := findBrowserFilterResult(t()); ok {
				return result, true
			}
		}
	}

	return browserFilterResultMsg{}, false
}

func TestHelpFitsMinimumTerminalAndDoesNotAppendStatus(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenHelp
	model.setStatus("A status line that belongs to the underlying screen")

	output := model.View().Content
	if strings.Contains(output, model.status) {
		t.Fatal("help modal contains the underlying screen status")
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("help height = %d, want at most %d", height, minimumHeight)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("help line width = %d, want at most %d", width, minimumWidth)
		}
	}
}

func TestEscapeClearsAppliedFilterBeforeGoingBack(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.currentMount = testMountName
	model.browser.SetItems([]list.Item{browserItem{title: "secret"}})
	model.browser.SetFilterText("secret")

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.browser.IsFiltered() || model.currentMount != testMountName {
		t.Fatalf("first escape = filtered %t, mount %q", model.browser.IsFiltered(), model.currentMount)
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.currentMount != "" {
		t.Fatalf("second escape left mount %q", model.currentMount)
	}
}

func TestTrackpadMomentumStaysWithHoveredPaneAfterFocusSwitch(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.browser.SetItems([]list.Item{
		browserItem{title: "first"},
		browserItem{title: "second"},
		browserItem{title: "third"},
	})
	model.setSecret(vaultcli.Secret{
		Data:    map[string]any{"first": 1, "second": 2, "third": 3},
		Version: 1,
	})

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	leftWidth, _ := paneWidths(model.width)
	_, _ = model.Update(tea.MouseWheelMsg{X: leftWidth - 1, Y: 2, Button: tea.MouseWheelDown})
	if model.focus != paneSecret || model.browser.Index() != 1 || model.secretTable.Cursor() != 0 {
		t.Fatalf(
			"left wheel after focus switch = focus %d, browser %d, secret %d",
			model.focus,
			model.browser.Index(),
			model.secretTable.Cursor(),
		)
	}

	_, _ = model.Update(tea.MouseWheelMsg{X: leftWidth, Y: 2, Button: tea.MouseWheelDown})
	if model.browser.Index() != 1 || model.secretTable.Cursor() != 1 {
		t.Fatalf(
			"right wheel = browser %d, secret %d",
			model.browser.Index(),
			model.secretTable.Cursor(),
		)
	}
	if model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("browser mouse mode = %d", model.View().MouseMode)
	}
}

func TestPasteSkipsIdenticalValue(t *testing.T) {
	t.Parallel()

	value := map[string]any{"enabled": true, "replicas": json.Number("2")}
	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.setSecret(vaultcli.Secret{Data: map[string]any{"config": value}, Version: 3})
	model.copied = &copiedEntry{key: "config", value: map[string]any{
		"enabled": true, "replicas": json.Number("2"),
	}}

	if err := model.beginPaste(); err != nil {
		t.Fatalf("beginPaste() error = %v", err)
	}
	if model.pendingWrite != nil || model.confirmation == confirmationPaste {
		t.Fatal("identical value created a paste operation")
	}
	if !strings.Contains(model.status, "already has the copied value") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestOperationErrorModalFitsAndOffersAccurateAuthenticationAction(t *testing.T) {
	t.Parallel()

	for name, fromEnvironment := range map[string]bool{
		"token helper": false,
		"environment":  true,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertPermissionErrorModal(t, fromEnvironment)
		})
	}
}

func TestBootstrapPermissionErrorDoesNotOfferLoginWithoutAnActiveClient(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{})
	model.resize(minimumWidth, minimumHeight)
	model.handleBootstrap(bootstrapResult{err: fmt.Errorf("probe rejected: %w", vaultcli.ErrPermissionDenied)})
	if model.screen != screenError || model.client != nil || model.errorCanReauth {
		t.Fatalf(
			"failed bootstrap state = screen %d, client %t, reauth %t",
			model.screen,
			model.client != nil,
			model.errorCanReauth,
		)
	}
	_, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || model.screen != screenError {
		t.Fatal("failed bootstrap allowed authentication without an active client")
	}
}

func TestSuccessfulReauthenticationReturnsToUnsavedEditor(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.editor.SetValue(`{"unsaved":"draft"}`)
	model.screen = screenLogin
	model.reauthReturnScreen = screenEditor
	model.reauthenticating = true
	model.copied = &copiedEntry{key: "old", value: "context"}

	command := model.handleLogin(loginResult{auth: vaultcli.AuthInfo{DisplayName: "developer"}})
	if command != nil || model.screen != screenEditor || model.reauthenticating {
		t.Fatalf("reauthentication state = screen %d, active %t", model.screen, model.reauthenticating)
	}
	if model.editor.Value() != `{"unsaved":"draft"}` {
		t.Fatalf("editor draft = %q", model.editor.Value())
	}
	if model.copied != nil {
		t.Fatal("reauthentication kept a copied entry from the previous token")
	}
}

func TestFailedReauthenticationKeepsTheEditorWorkflowTarget(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	const draft = `{"unsaved":"draft"}`
	model.editor.SetValue(draft)
	model.screen = screenLogin
	model.reauthReturnScreen = screenEditor
	model.reauthenticating = true
	model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, "")

	model.handleLogin(loginResult{err: fmt.Errorf("invalid token: %w", vaultcli.ErrPermissionDenied)})
	if model.screen != screenError || model.errorReturnScreen != screenLogin ||
		model.reauthReturnScreen != screenEditor {
		t.Fatalf(
			"failed reauthentication targets = screen %d, error return %d, reauth return %d",
			model.screen,
			model.errorReturnScreen,
			model.reauthReturnScreen,
		)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.screen != screenLogin || model.reauthReturnScreen != screenEditor {
		t.Fatalf("retry targets = screen %d, reauth return %d", model.screen, model.reauthReturnScreen)
	}
	command := model.handleLogin(loginResult{auth: vaultcli.AuthInfo{DisplayName: "developer"}})
	if command != nil || model.screen != screenEditor || model.editor.Value() != draft {
		t.Fatalf("retry discarded editor draft: screen %d, draft %q", model.screen, model.editor.Value())
	}
}

func TestUnknownReauthenticationOutcomeCannotReturnToStaleState(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	const draft = `{"unsaved":"draft"}`
	model.editor.SetValue(draft)
	model.currentMount = testMountName
	model.screen = screenLogin
	model.reauthReturnScreen = screenEditor
	model.reauthenticating = true
	model.auth = vaultcli.AuthInfo{DisplayName: "old identity"}

	model.handleLogin(loginResult{err: fmt.Errorf("login timed out: %w", vaultcli.ErrOutcomeUnknown)})
	if model.screen != screenError || !model.reauthRequired || model.auth != (vaultcli.AuthInfo{}) ||
		model.editor.Value() != draft {
		t.Fatalf(
			"unknown login state = screen %d, required %t, auth %#v, draft %q",
			model.screen,
			model.reauthRequired,
			model.auth,
			model.editor.Value(),
		)
	}
	model.closeError()
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin || model.editor.Value() != draft {
		t.Fatalf("required reauthentication was canceled: screen %d, draft %q", model.screen, model.editor.Value())
	}

	command := model.handleLogin(loginResult{auth: vaultcli.AuthInfo{DisplayName: "new identity"}})
	if command != nil || model.screen != screenEditor || model.reauthRequired ||
		model.editor.Value() != draft || model.currentMount != testMountName {
		t.Fatalf(
			"confirmed retry = command %t, screen %d, required %t, draft %q, mount %q",
			command != nil,
			model.screen,
			model.reauthRequired,
			model.editor.Value(),
			model.currentMount,
		)
	}
}

func TestUnknownInitialLoginOutcomeReturnsToFocusedTokenInput(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.screen = screenLogin
	model.loginInput.Focus()

	model.handleLogin(loginResult{err: fmt.Errorf("login timed out: %w", vaultcli.ErrOutcomeUnknown)})
	if model.screen != screenError || model.errorReturnScreen != screenLogin {
		t.Fatalf("unknown initial login state = screen %d, return %d", model.screen, model.errorReturnScreen)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin || !model.loginInput.Focused() {
		t.Fatalf(
			"closing unknown login outcome = screen %d, input focused %t",
			model.screen,
			model.loginInput.Focused(),
		)
	}
}

func TestCancelingReauthenticationBackToLoginKeepsTokenInputFocused(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, "")
	model.screen = screenLogin
	model.loginInput.Focus()

	model.handleLogin(loginResult{err: fmt.Errorf("invalid token: %w", vaultcli.ErrPermissionDenied)})
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.screen != screenLogin || !model.reauthenticating || !model.loginInput.Focused() {
		t.Fatalf(
			"reauthentication start = screen %d, active %t, input focused %t",
			model.screen,
			model.reauthenticating,
			model.loginInput.Focused(),
		)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin || model.reauthenticating || !model.loginInput.Focused() {
		t.Fatalf(
			"reauthentication cancel = screen %d, active %t, input focused %t",
			model.screen,
			model.reauthenticating,
			model.loginInput.Focused(),
		)
	}
}

func TestFailedVaultProbeCanReturnThroughSetupToTheBrowser(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://old.example.test"})
	model.beginSetup(screenBrowser)
	model.handleBootstrap(bootstrapResult{err: errors.New("candidate unavailable")})
	if model.screen != screenError || model.errorReturnScreen != screenSetup ||
		model.returnScreen != screenBrowser {
		t.Fatalf(
			"failed probe targets = screen %d, error return %d, setup return %d",
			model.screen,
			model.errorReturnScreen,
			model.returnScreen,
		)
	}
	model.closeError()
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenBrowser {
		t.Fatalf("canceling failed settings returned to screen %d", model.screen)
	}
}

func TestSuccessfulAuthenticationClearsLoadedStateBeforeMountDiscovery(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenLogin
	model.reauthReturnScreen = screenBrowser
	model.reauthenticating = true
	model.currentMount = testMountName
	model.currentPath = "apps"
	model.selectedPath = "apps/api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"token": "old"}, Version: 4})
	model.copied = &copiedEntry{key: "token", value: "old"}
	model.browser.SetItems([]list.Item{browserItem{title: "api", path: "apps/api", kind: itemSecret}})

	command := model.handleLogin(loginResult{auth: vaultcli.AuthInfo{DisplayName: "new identity"}})
	if command == nil {
		t.Fatal("successful authentication did not start mount discovery")
	}
	if model.secret != nil || model.copied != nil || model.currentMount != "" || model.currentPath != "" ||
		len(model.browser.Items()) != 0 {
		t.Fatalf(
			"successful authentication retained state: secret=%t copied=%t mount=%q path=%q items=%d",
			model.secret != nil,
			model.copied != nil,
			model.currentMount,
			model.currentPath,
			len(model.browser.Items()),
		)
	}

	model.finishOperation()
	model.handleMounts(mountsResult{err: errors.New("discovery unavailable")})
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenBrowser || model.secret != nil || model.currentMount != "" ||
		len(model.browser.Items()) != 0 {
		t.Fatalf(
			"failed discovery restored state: screen=%d secret=%t mount=%q items=%d",
			model.screen,
			model.secret != nil,
			model.currentMount,
			len(model.browser.Items()),
		)
	}
}

func TestLoginShortcutOpensConfiguredVaultUI(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://vault.example.test", Namespace: "team/platform"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenLogin
	model.loginInput.Focus()
	var openedURL string
	model.openURL = func(_ context.Context, target string) error {
		openedURL = target

		return nil
	}
	if output := model.View().Content; !strings.Contains(output, "[Ctrl+O] open Vault UI") {
		t.Fatalf("login view does not expose the Vault UI shortcut: %q", output)
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if model.loginInput.Value() != "o" {
		t.Fatalf("ordinary o was not entered into the token field: value %q", model.loginInput.Value())
	}

	_, command := model.Update(tea.KeyPressMsg{Code: 'щ', BaseCode: 'o', Text: "щ", Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("physical Ctrl+O did not schedule the Vault login page")
	}
	message := command()
	_, followUp := model.Update(message)
	if followUp != nil {
		t.Fatal("opening the Vault login page returned an unexpected command")
	}
	const wantURL = "https://vault.example.test/ui/vault/auth?namespace=team%2Fplatform"
	if openedURL != wantURL {
		t.Fatalf("opened URL = %q, want %q", openedURL, wantURL)
	}
	if model.screen != screenLogin || !model.loginInput.Focused() {
		t.Fatal("opening Vault UI moved focus away from the token input")
	}
}

func TestLoginInputBoundsPastedTokens(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://vault.example.test"})
	model.screen = screenLogin
	model.loginInput.Focus()
	_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("x", vaultcli.MaximumFieldBytes+1)})
	if len(model.loginInput.Value()) != vaultcli.MaximumFieldBytes {
		t.Fatalf("pasted token length = %d, want %d", len(model.loginInput.Value()), vaultcli.MaximumFieldBytes)
	}
}

func TestSetupInputsBoundPastedValues(t *testing.T) {
	t.Parallel()

	for index, name := range []string{"address", "namespace"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{})
			model.focusSetup(index)
			_, _ = model.Update(tea.PasteMsg{Content: strings.Repeat("x", vaultcli.MaximumFieldBytes+1)})
			if length := len(model.setupInputs[index].Value()); length != vaultcli.MaximumFieldBytes {
				t.Fatalf("pasted %s length = %d, want %d", name, length, vaultcli.MaximumFieldBytes)
			}
		})
	}
}

func TestLoginOpenErrorUsesLiteralErrorModal(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://vault.example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenLogin
	model.loginInput.Focus()
	var openedURL string
	model.openURL = func(_ context.Context, target string) error {
		openedURL = target

		return errors.New("browser executable not found")
	}

	_, command := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Vault UI shortcut did not schedule browser opening")
	}
	_, _ = model.Update(command())
	output := model.View().Content
	if model.screen != screenError || !strings.Contains(output, "Could not open Vault login") ||
		!strings.Contains(output, "browser executable not found") {
		t.Fatalf("browser error modal = %q", output)
	}
	const wantURL = "https://vault.example.test/ui/vault/auth"
	if openedURL != wantURL {
		t.Fatalf("opened URL = %q, want %q", openedURL, wantURL)
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin || !model.loginInput.Focused() {
		t.Fatal("closing the browser error did not return to token input")
	}
}

func TestLoginOpenRequestsCannotTrapTheErrorModal(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://vault.example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenLogin
	model.loginInput.Focus()
	model.openURL = func(context.Context, string) error { return errors.New("browser unavailable") }

	_, first := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if first == nil {
		t.Fatal("first Vault UI request was not scheduled")
	}
	_, second := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if second != nil {
		t.Fatal("second Vault UI request was scheduled while the first was pending")
	}

	model.showOperationError(authenticationError, errors.New("invalid token"))
	_, _ = model.Update(first())
	if model.errorTitle != authenticationError || model.errorReturnScreen != screenLogin {
		t.Fatalf(
			"stale opener error state = title %q, return screen %d",
			model.errorTitle,
			model.errorReturnScreen,
		)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin {
		t.Fatalf("closing the error returned to screen %d, want login", model.screen)
	}

	model.showOperationError("First", errors.New("first"))
	model.showOperationError("Second", errors.New("second"))
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenLogin {
		t.Fatalf("nested error returned to screen %d, want login", model.screen)
	}
}

func TestCloseCancelsAndWaitsForBrowserOpen(t *testing.T) {
	t.Parallel()

	model := New(
		config.NewStore(filepath.Join(t.TempDir(), "config.toml")),
		config.Config{Address: "https://vault.example.test"},
		nil,
	)
	started := make(chan struct{})
	model.openURL = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()

		return ctx.Err()
	}
	command := model.startOpenLogin()
	result := make(chan tea.Msg, 1)
	go func() {
		result <- command()
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("browser opener did not start")
	}
	closed := make(chan struct{})
	go func() {
		model.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel and join the browser opener")
	}
	message := <-result
	opened, ok := message.(openLoginResultMsg)
	if !ok || !errors.Is(opened.err, context.Canceled) {
		t.Fatalf("browser result = %#v, want context cancellation", message)
	}
}

func TestMergeMountsKeepsDiscoveryAndAddsOnlyMissingConfiguredMounts(t *testing.T) {
	t.Parallel()

	discovered := []vaultcli.Mount{
		{Path: "shared", Description: "discovered"},
		{Path: "apps", Description: "applications"},
	}
	actual := mergeMounts(discovered, []string{"shared", "private", "private"})
	want := []vaultcli.Mount{
		{Path: "apps", Description: "applications"},
		{Path: "private", Description: configuredMountDescription},
		{Path: "shared", Description: "discovered"},
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("mergeMounts() = %#v, want %#v", actual, want)
	}
}

func assertPermissionErrorModal(t *testing.T, fromEnvironment bool) {
	t.Helper()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, "")
	model.auth.FromEnv = fromEnvironment
	operationError := fmt.Errorf(
		"Error making API request.\n\n%s\n\n* %w",
		strings.Repeat("long detail ", 20),
		vaultcli.ErrPermissionDenied,
	)
	model.showVaultOperationError("Could not load entries", operationError)

	output := model.View().Content
	if actual := model.errorView.GetContent(); actual != operationError.Error() {
		t.Fatalf("error content = %q, want %q", actual, operationError.Error())
	}
	if model.errorView.SoftWrap || !strings.Contains(output, "[↑/↓/←/→] scroll") {
		t.Fatalf("permission modal = %q", output)
	}
	if model.errorCanReauth == fromEnvironment {
		t.Fatalf("errorCanReauth = %t, environment token = %t", model.errorCanReauth, fromEnvironment)
	}
	if !fromEnvironment && !strings.Contains(output, "[Enter] use another token") {
		t.Fatalf("helper-token modal = %q", output)
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("error modal height = %d", height)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("error modal line width = %d", width)
		}
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if model.errorView.XOffset() == 0 {
		t.Fatal("right arrow did not scroll the unwrapped error")
	}

	if !fromEnvironment {
		_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if model.screen != screenLogin || !model.reauthenticating {
			t.Fatal("permission modal did not open reauthentication")
		}
		if model.errorTitle != "" || model.errorView.GetContent() != "" {
			t.Fatal("reauthentication retained the closed error")
		}
	}
}

func TestMountPermissionErrorActionsFitMinimumTerminal(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, "")
	model.showMountOperationError(
		"Could not load entries",
		fmt.Errorf("%s: %w", strings.Repeat("permission detail ", 20), vaultcli.ErrPermissionDenied),
		"secret",
	)

	output := model.View().Content
	for _, action := range []string{"use another token", "correct mount", "scroll", "close"} {
		if !strings.Contains(output, action) {
			t.Fatalf("error modal does not show %q: %q", action, output)
		}
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("error modal height = %d, want at most %d", height, minimumHeight)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("error modal line width = %d, want at most %d", width, minimumWidth)
		}
	}
}

func TestLongSecretPathStaysInsideBrowserGeometry(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.selectedPath = strings.Repeat("segment", 600)
	model.setSecret(vaultcli.Secret{Data: map[string]any{"key": "value"}, Version: 1})

	output := model.View().Content
	if !strings.Contains(output, "...") {
		t.Fatal("long secret path was not truncated")
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("browser height = %d, want at most %d", height, minimumHeight)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("browser line width = %d, want at most %d", width, minimumWidth)
		}
	}
}
