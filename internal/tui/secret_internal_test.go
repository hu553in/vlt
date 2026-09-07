package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

const testMountName = "secret"

type dangerousActionCase struct {
	screen screen
	key    tea.KeyPressMsg
	action confirmationAction
	setup  func(*Model) error
}

func newTestModel(t *testing.T, loaded config.Config) *Model {
	t.Helper()

	model := New(config.NewStore(filepath.Join(t.TempDir(), "config.toml")), loaded, nil)
	t.Cleanup(model.Close)

	return model
}

func TestDecodeEditorAcceptsOneJSONObject(t *testing.T) {
	t.Parallel()

	data, err := decodeEditor(`{"enabled":true,"replicas":2,"exact":9007199254740992}`)
	if err != nil {
		t.Fatalf("decodeEditor() error = %v", err)
	}
	replicas, ok := data["replicas"].(json.Number)
	if !ok || replicas.String() != "2" || data["enabled"] != true {
		t.Fatalf("decodeEditor() = %#v", data)
	}
	exact, ok := data["exact"].(json.Number)
	if !ok || exact.String() != "9007199254740992" {
		t.Fatalf("exact number = %#v", data["exact"])
	}
}

func TestDecodeEditorRejectsInvalidSecretDocuments(t *testing.T) {
	t.Parallel()

	for name, document := range map[string]string{
		"array":                `[]`,
		"null":                 `null`,
		"trailing":             `{} {}`,
		"duplicate key":        `{"value":1,"value":2}`,
		"nested duplicate key": `{"nested":{"value":1,"value":2}}`,
		"array duplicate key":  `{"nested":[{"value":1,"value":2}]}`,
		"oversized editor":     strings.Repeat(" ", maximumEditorBytes+1),
		"oversized payload":    `{"value":"` + strings.Repeat("x", vaultcli.MaximumSecretBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := decodeEditor(document); err == nil {
				t.Fatalf("decodeEditor(%q) unexpectedly succeeded", name)
			}
		})
	}
}

func TestDecodeEditorKeepsDuplicateNameErrorsBounded(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("k", vaultcli.MaximumFieldBytes+1)
	_, err := decodeEditor(`{"` + name + `":1,"` + name + `":2}`)
	if err == nil || !strings.Contains(err.Error(), "duplicate object name at byte") || len(err.Error()) > 128 {
		t.Fatalf("duplicate-name error = %q", err)
	}
}

func TestBeginEditorAcceptsASecretAtTheStorageLimit(t *testing.T) {
	for _, character := range []string{"\u061c", "\u007f"} {
		t.Run(fmt.Sprintf("U+%04X", []rune(character)[0]), func(t *testing.T) {
			model := newTestModel(t, config.Config{})
			base, err := json.Marshal(map[string]any{"value": ""})
			if err != nil {
				t.Fatal(err)
			}
			valueBytes := vaultcli.MaximumSecretBytes - len(base)
			secret := map[string]any{
				"value": strings.Repeat(
					character,
					valueBytes/len(character),
				) + strings.Repeat(
					"x",
					valueBytes%len(character),
				),
			}
			payload, err := vaultcli.EncodeSecret(secret)
			if err != nil || len(payload) != vaultcli.MaximumSecretBytes {
				t.Fatalf("storage-limit fixture: %d bytes, %v", len(payload), err)
			}
			if err = model.beginEditor("at-limit", secret, 1); err != nil {
				t.Fatal(err)
			}
			model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			if model.pendingWrite == nil || !reflect.DeepEqual(model.pendingWrite.data, secret) {
				t.Fatalf("storage-limit secret did not reach save confirmation intact: %s", model.status)
			}
		})
	}
}

func TestBeginEditorFallsBackToBoundedCompactJSON(t *testing.T) {
	t.Parallel()

	var nested any = true
	for range 3200 {
		nested = []any{nested}
	}
	model := Model{editor: textarea.New()}
	model.editor.CharLimit = maximumEditorBytes
	if err := model.beginEditor("deep", map[string]any{"value": nested}, 1); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}
	if strings.Contains(model.editor.Value(), "\n") {
		t.Fatal("deep secret used an unbounded indented representation")
	}
	if _, err := decodeEditor(model.editor.Value()); err != nil {
		t.Fatalf("decodeEditor(compact fallback) error = %v", err)
	}
}

func TestCreatePathPreservesLongParent(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.currentPath = strings.Repeat("segment/", 600)
	model.currentPath = strings.TrimSuffix(model.currentPath, "/")
	model.beginCreatePath()

	if model.screen != screenPath {
		t.Fatal("beginCreatePath() did not open the path input")
	}
	if actual, want := model.pathInput.Value(), model.currentPath+"/"; actual != want {
		t.Fatalf("path input length = %d, want %d", len(actual), len(want))
	}
}

func TestDisplayTextEscapesTerminalAndBidiControls(t *testing.T) {
	t.Parallel()

	const unsafe = "left\x1b[2J\u202eright"
	const want = `left\x1b[2J\u202eright`
	if actual := displayText(unsafe); actual != want {
		t.Fatalf("displayText() = %q, want %q", actual, want)
	}
}

func TestDisplayMultilineTextPreservesLinesAndEscapesTerminalControls(t *testing.T) {
	t.Parallel()

	const unsafe = "first line\r\n\tsecond\x1b[2J\nthird"
	const want = "first line\n    second\\x1b[2J\nthird"
	if actual := displayMultilineText(unsafe); actual != want {
		t.Fatalf("displayMultilineText() = %q, want %q", actual, want)
	}
}

func TestErrorModalExpandsToFitLongLines(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	const urlLine = "URL: GET https://vault.qic-int.online/v1/sys/internal/ui/mounts/kv-core"
	operationError := errors.New(
		"vault kv list: Error making API request.\r\n\r\n" + urlLine +
			"\r\nCode: 403. Errors:\r\n\r\n* 2 errors occurred:\r\n" +
			"\t* permission denied\r\n\t* invalid token",
	)
	model.showOperationError("Could not load entries", operationError)

	if actual, want := model.errorView.Width(), lipgloss.Width(urlLine); actual != want {
		t.Fatalf("error viewport width = %d, want %d", actual, want)
	}
	output := model.View().Content
	if !strings.Contains(output, urlLine) || !strings.Contains(output, "    * permission denied") {
		t.Fatalf("error modal = %q", output)
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("error modal height = %d", height)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("error modal line width = %d", width)
		}
	}
}

func TestSecretTableScrollsInCursorDirectionAfterReversal(t *testing.T) {
	t.Parallel()

	const rowCount = 20
	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	data := make(map[string]any, rowCount)
	for index := range rowCount {
		data[fmt.Sprintf("key-%02d", index)] = index
	}
	model.setSecret(vaultcli.Secret{Data: data, Version: 1})
	model.focus = paneSecret
	model.secretTable.Focus()

	for range rowCount - 1 {
		model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	for range 8 {
		model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if model.secretTable.Cursor() != 11 {
		t.Fatalf("cursor after reversal = %d, want 11", model.secretTable.Cursor())
	}

	previousTop := firstVisibleSecretIndex(model.secretTable.View(), rowCount)
	if previousTop < 0 {
		t.Fatal("secret table has no visible rows after reversal")
	}
	for expectedCursor := 12; expectedCursor < rowCount; expectedCursor++ {
		previousCursor := model.secretTable.Cursor()
		model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if model.secretTable.Cursor() != expectedCursor {
			t.Fatalf("cursor after moving down = %d, want %d", model.secretTable.Cursor(), expectedCursor)
		}
		top := firstVisibleSecretIndex(model.secretTable.View(), rowCount)
		if top < 0 || top < previousTop {
			t.Fatalf(
				"cursor %d -> %d: first visible row moved %d -> %d",
				previousCursor,
				model.secretTable.Cursor(),
				previousTop,
				top,
			)
		}
		previousTop = top
	}
}

func firstVisibleSecretIndex(view string, rowCount int) int {
	plain := ansi.Strip(view)
	for index := range rowCount {
		if strings.Contains(plain, fmt.Sprintf("key-%02d", index)) {
			return index
		}
	}

	return -1
}

func TestEditorEscapesBidiControlsWithoutChangingSecret(t *testing.T) {
	t.Parallel()

	model := Model{editor: textarea.New()}
	model.editor.CharLimit = maximumEditorBytes
	const value = "left\u202eright"
	if err := model.beginEditor("secret", map[string]any{"value": value}, 1); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}
	if strings.ContainsRune(model.editor.Value(), '\u202e') ||
		!strings.Contains(model.editor.Value(), `\u202e`) {
		t.Fatalf("editor contains an unsafe bidi control: %q", model.editor.Value())
	}

	decoded, err := decodeEditor(model.editor.Value())
	if err != nil {
		t.Fatalf("decodeEditor() error = %v", err)
	}
	if decoded["value"] != value {
		t.Fatalf("decoded value = %q, want %q", decoded["value"], value)
	}
}

func TestDangerousActionsRequireConfirmation(t *testing.T) {
	t.Parallel()

	tests := map[string]dangerousActionCase{
		"quit browser": {
			screen: screenBrowser,
			key:    tea.KeyPressMsg{Code: 'q', Text: "q"},
			action: confirmationQuit,
		},
		"quit setup": {
			screen: screenSetup,
			key:    tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl},
			action: confirmationQuit,
		},
		"quit login": {
			screen: screenLogin,
			key:    tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl},
			action: confirmationQuit,
		},
		"quit mount": {
			screen: screenMount,
			key:    tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl},
			action: confirmationQuit,
		},
		"quit path": {
			screen: screenPath,
			key:    tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl},
			action: confirmationQuit,
		},
		"logout browser": {
			screen: screenBrowser,
			key:    tea.KeyPressMsg{Code: 'o', Text: "o"},
			action: confirmationLogout,
		},
		"logout mount": {
			screen: screenMount,
			key:    tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl},
			action: confirmationLogout,
		},
		"save secret": {
			screen: screenEditor,
			key:    tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl},
			action: confirmationSave,
			setup: func(model *Model) error {
				return model.beginEditor("api", map[string]any{"key": "value"}, 4)
			},
		},
		"discard changes": {
			screen: screenEditor,
			key:    tea.KeyPressMsg{Code: tea.KeyEscape},
			action: confirmationDiscard,
			setup: func(model *Model) error {
				if err := model.beginEditor("api", map[string]any{"key": "value"}, 4); err != nil {
					return err
				}
				model.editor.SetValue(`{"key":"changed"}`)

				return nil
			},
		},
		"discard new secret": {
			screen: screenEditor,
			key:    tea.KeyPressMsg{Code: tea.KeyEscape},
			action: confirmationDiscard,
			setup: func(model *Model) error {
				return model.beginEditor("new-secret", map[string]any{}, 0)
			},
		},
		"delete secret": {
			screen: screenBrowser,
			key:    tea.KeyPressMsg{Code: 'd', Text: "d"},
			action: confirmationDelete,
			setup: func(model *Model) error {
				model.selectedPath = "api"
				model.setSecret(vaultcli.Secret{Data: map[string]any{"key": "value"}, Version: 4})

				return nil
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertDangerousActionConfirmation(t, test)
		})
	}
}

func TestCtrlCDoesNotBypassQuitConfirmation(t *testing.T) {
	t.Parallel()

	for name, filtering := range map[string]bool{
		"browser":   false,
		"filtering": true,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			model.screen = screenBrowser
			if filtering {
				model.browser.SetFilterState(list.Filtering)
			}

			_, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if command != nil {
				if _, quits := command().(tea.QuitMsg); quits {
					t.Fatal("Ctrl+C bypassed vlt quit confirmation")
				}
			}
		})
	}
}

func TestCloseCancelsAndJoinsActiveOperation(t *testing.T) {
	t.Parallel()

	model := New(config.NewStore(filepath.Join(t.TempDir(), "config.toml")), config.Config{}, nil)
	started := make(chan struct{})
	finished := make(chan struct{})
	command := model.startOperation(func(ctx context.Context) any {
		close(started)
		<-ctx.Done()
		close(finished)

		return ctx.Err()
	})
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("startOperation() command = %T, want a non-empty batch", message)
	}
	commandFinished := make(chan struct{})
	go func() {
		_ = batch[0]()
		close(commandFinished)
	}()
	<-started

	model.Close()
	select {
	case <-finished:
	default:
		t.Fatal("Close() returned before the active operation stopped")
	}
	<-commandFinished
}

func assertDangerousActionConfirmation(t *testing.T, test dangerousActionCase) {
	t.Helper()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	model.screen = test.screen
	if test.setup != nil {
		if err := test.setup(model); err != nil {
			t.Fatalf("prepare model: %v", err)
		}
	}

	_, command := model.Update(test.key)
	if command != nil || model.screen != screenConfirm || model.confirmation != test.action {
		t.Fatalf(
			"dangerous key state = screen %d, action %d, command %v",
			model.screen,
			model.confirmation,
			command,
		)
	}
	assertConfirmationFits(t, model)

	_, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || model.screen != test.screen || model.confirmation != confirmationNone {
		t.Fatalf(
			"canceled confirmation = screen %d, action %d, command %v",
			model.screen,
			model.confirmation,
			command,
		)
	}
}

func assertConfirmationFits(t *testing.T, model *Model) {
	t.Helper()

	output := model.View().Content
	if !strings.Contains(output, model.confirmationTitle()) {
		t.Fatalf("confirmation view does not contain %q", model.confirmationTitle())
	}
	if height := strings.Count(output, "\n") + 1; height > minimumHeight {
		t.Fatalf("confirmation height = %d, want at most %d", height, minimumHeight)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("confirmation line width = %d, want at most %d", width, minimumWidth)
		}
	}
}

func TestUnchangedEditorClosesWithoutDiscardConfirmation(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	if err := model.beginEditor("api", map[string]any{"key": "value"}, 4); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}

	_, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || model.screen != screenBrowser || model.confirmation != confirmationNone {
		t.Fatalf("unchanged editor close = screen %d, action %d", model.screen, model.confirmation)
	}
}

func TestSaveConfirmationCapturesValidatedWrite(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	if err := model.beginEditor("api", map[string]any{"value": "old"}, 4); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}
	model.editor.SetValue(`{"value":"new"}`)

	_, command := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command != nil || model.confirmation != confirmationSave || model.pendingWrite == nil {
		t.Fatalf(
			"save confirmation = action %d, pending %#v, command %t",
			model.confirmation,
			model.pendingWrite,
			command != nil,
		)
	}
	if model.pendingWrite.target.client != model.client || model.pendingWrite.target.mount != testMountName ||
		model.pendingWrite.target.path != "api" ||
		model.pendingWrite.version != 4 ||
		!reflect.DeepEqual(model.pendingWrite.data, map[string]any{"value": "new"}) {
		t.Fatalf("pending write = %#v", model.pendingWrite)
	}

	model.editor.SetValue(`{"value":"changed after confirmation"}`)
	model.currentMount = "other"
	model.editorPath = "other"
	if !reflect.DeepEqual(model.pendingWrite.data, map[string]any{"value": "new"}) {
		t.Fatalf("pending write changed with editor = %#v", model.pendingWrite.data)
	}
	output := model.View().Content
	if !strings.Contains(output, "secret:/api") || strings.Contains(output, "other:/other") {
		t.Fatalf("save confirmation changed target: %q", output)
	}
	_, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || model.pendingWrite != nil || model.screen != screenEditor {
		t.Fatal("canceling save did not clear only the pending write")
	}
}

func TestDeleteConfirmationCapturesTarget(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.currentMount = testMountName
	model.selectedPath = "api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"value": true}, Version: 4})

	_, command := model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if command != nil || model.pendingDelete == nil || model.confirmation != confirmationDelete {
		t.Fatalf("delete confirmation = pending %#v, action %d", model.pendingDelete, model.confirmation)
	}
	model.currentMount = "other"
	model.selectedPath = "other"
	output := model.View().Content
	if !strings.Contains(output, "secret:/api") || strings.Contains(output, "other:/other") {
		t.Fatalf("delete confirmation changed target: %q", output)
	}

	_, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || model.pendingDelete != nil || model.screen != screenBrowser {
		t.Fatal("canceling delete did not clear only the pending target")
	}
}

func TestConfirmationSurvivesTerminalResize(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	model.selectedPath = "api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"key": "value"}, Version: 4})

	_, _ = model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	model.resize(minimumWidth-1, minimumHeight-1)
	_, command := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if command != nil || model.confirmation != confirmationDelete ||
		!strings.Contains(model.View().Content, "Delete latest secret version?") {
		t.Fatal("terminal resize replaced the pending delete confirmation")
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.screen != screenBrowser || model.confirmation != confirmationNone {
		t.Fatal("canceling a resized confirmation did not return to the browser")
	}
}

func TestSmallTerminalConfirmationSanitizesAndTruncatesItsTitle(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	path := "\u202e" + strings.Repeat("very-long-path", 20)
	if err := model.beginEditor(path, map[string]any{"key": "value"}, 1); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}
	model.beginConfirmation(confirmationSave)
	model.resize(minimumWidth-1, minimumHeight-1)

	output := model.View().Content
	if strings.ContainsRune(output, '\u202e') || !strings.Contains(output, `\u202e`) ||
		!strings.Contains(output, "...") {
		t.Fatalf("small-terminal confirmation did not sanitize and truncate its title: %q", output)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > minimumWidth-1 {
			t.Fatalf("small-terminal line width = %d, want at most %d", width, minimumWidth-1)
		}
	}
}

func TestSmallTerminalBoundsEveryFallback(t *testing.T) {
	t.Parallel()

	for _, size := range [][2]int{{20, 24}, {1, 1}, {89, 1}} {
		for _, state := range []string{"idle", "loading", "confirmation"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], state), func(t *testing.T) {
				t.Parallel()

				model := newTestModel(t, config.Config{Address: "https://example.test"})
				model.resize(size[0], size[1])
				switch state {
				case "loading":
					model.loading = true
				case "confirmation":
					model.beginConfirmation(confirmationQuit)
				}
				output := model.View().Content
				if output == "" {
					t.Fatal("small-terminal fallback is empty")
				}
				if width, height := lipgloss.Size(output); width > size[0] || height > size[1] {
					t.Fatalf("fallback size = %dx%d, terminal = %dx%d", width, height, size[0], size[1])
				}
			})
		}
	}
}

func TestPasteDraftAddsOrReplacesExactJSONValue(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		target  map[string]any
		value   any
		replace bool
	}{
		"add null": {
			target: map[string]any{"keep": true},
			value:  nil,
		},
		"replace nested value": {
			target: map[string]any{"copied": map[string]any{"old": true}, "keep": true},
			value: map[string]any{
				"array": []any{json.Number("9007199254740992"), nil, false},
			},
			replace: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			model.currentMount = testMountName
			model.selectedPath = "target"
			model.setSecret(vaultcli.Secret{Data: test.target, Version: 7})
			model.copied = &copiedEntry{
				key: "copied", value: test.value, sourceMount: testMountName, sourcePath: "source",
			}

			if err := model.beginPaste(); err != nil {
				t.Fatalf("beginPaste() error = %v", err)
			}
			if model.screen != screenConfirm ||
				model.confirmation != confirmationPaste || model.pendingWrite == nil {
				t.Fatalf("paste state = screen %d, pending %#v", model.screen, model.pendingWrite)
			}
			if model.pendingWrite.replaces != test.replace ||
				!reflect.DeepEqual(model.pendingWrite.data["copied"], test.value) ||
				model.pendingWrite.target.path != "target" || model.pendingWrite.version != 7 {
				t.Fatalf("pending paste = %#v", model.pendingWrite)
			}
			if !reflect.DeepEqual(model.secret.Data, test.target) {
				t.Fatalf("loaded secret was changed before confirmation: %#v", model.secret.Data)
			}
			_, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if command != nil || model.pendingWrite != nil || model.screen != screenBrowser {
				t.Fatal("canceling paste left pending work")
			}
		})
	}
}

func TestLoadedSecretFocusesSecretPane(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	model.selectedPath = "old"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"old": true}, Version: 1})
	model.focus = paneBrowser
	model.secretTable.Blur()

	command := model.handleSecret(secretResult{
		secretPath: "new",
		secret:     vaultcli.Secret{Data: map[string]any{"new": true}, Version: 2},
	})

	if command != nil || model.focus != paneSecret || !model.secretTable.focused {
		t.Fatalf("loaded secret focus = pane %d, table focused %t", model.focus, model.secretTable.focused)
	}
	if model.selectedPath != "new" || !reflect.DeepEqual(model.secret.Data, map[string]any{"new": true}) {
		t.Fatalf("loaded secret = path %q, data %#v", model.selectedPath, model.secret.Data)
	}
}

func TestEmptyAndDeletedSecretsStayInTheBrowserPane(t *testing.T) {
	t.Parallel()

	for name, secret := range map[string]vaultcli.Secret{
		"empty":   {Data: map[string]any{}, Version: 3},
		"deleted": {Data: map[string]any{}, Version: 4, Deleted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			model.screen = screenBrowser
			model.currentMount = testMountName
			model.focus = paneSecret
			model.secretTable.Focus()

			model.handleSecret(secretResult{secretPath: name, secret: secret})
			if model.focus != paneBrowser || model.secretTable.focused {
				t.Fatalf("loaded %s secret kept empty table focus", name)
			}
			if shortcuts := model.browserShortcuts(); strings.Contains(shortcuts, "delete") {
				t.Fatalf("loaded %s secret exposed delete shortcut: %q", name, shortcuts)
			}
		})
	}
}

func TestBrowserFooterShowsOpenActionWithinMount(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.currentMount = testMountName
	if shortcuts := model.browserShortcuts(); !strings.Contains(shortcuts, "[Enter] open") {
		t.Fatalf("browser shortcuts without a loaded secret = %q", shortcuts)
	}

	model.setSecret(vaultcli.Secret{Data: map[string]any{"token": "secret"}, Version: 1})
	if shortcuts := model.browserShortcuts(); !strings.Contains(shortcuts, "[Enter] open") {
		t.Fatalf("browser shortcuts with a loaded secret = %q", shortcuts)
	}
}

func TestDeletedLatestVersionCanBeRecreatedWithItsExactCAS(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.currentMount = testMountName
	model.handleSecret(secretResult{
		secretPath: "api",
		secret:     vaultcli.Secret{Data: map[string]any{}, Version: 7, Deleted: true},
	})

	output := model.View().Content
	if !strings.Contains(output, "Latest version 7 is deleted") ||
		!strings.Contains(output, "Press e to write the next version") {
		t.Fatalf("deleted secret view = %q", output)
	}
	_, command := model.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if command != nil || model.screen != screenEditor || model.editorCAS != 7 || model.editor.Value() != "{}" {
		t.Fatalf(
			"deleted secret edit = screen %d, CAS %d, value %q",
			model.screen,
			model.editorCAS,
			model.editor.Value(),
		)
	}
}

func TestUnselectedAndDeletedSecretPanelsFillAvailableHeight(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	wantHeight := minimumHeight - screenChromeHeight

	for name, secret := range map[string]*vaultcli.Secret{
		"unselected": nil,
		"deleted":    {Data: map[string]any{}, Version: 1, Deleted: true},
	} {
		t.Run(name, func(t *testing.T) {
			model.secret = secret
			if height := strings.Count(model.renderSecretPanel(), "\n") + 1; height != wantHeight {
				t.Fatalf("secret panel height = %d, want %d", height, wantHeight)
			}
		})
	}
}

func TestReplacingBrowserItemsResetsAnOutOfRangeCursor(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	keys := make([]string, 20)
	for index := range keys {
		keys[index] = fmt.Sprintf("secret-%02d", index)
	}
	model.handleEntries(entriesResult{mount: testMountName, keys: keys})
	model.browser.Select(len(keys) - 1)

	model.handleEntries(entriesResult{mount: testMountName, folder: "folder", keys: []string{"only"}})
	selected, ok := model.browser.SelectedItem().(browserItem)
	if !ok || model.browser.Index() != 0 || selected.title != "only" {
		t.Fatalf("replacement selection = index %d, item %#v", model.browser.Index(), model.browser.SelectedItem())
	}
}

func TestUnknownSaveOutcomeInvalidatesCASAndKeepsTheDraft(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	model.selectedPath = "api"
	model.setSecret(vaultcli.Secret{Data: map[string]any{"value": "old"}, Version: 4})
	if err := model.beginEditor("api", model.secret.Data, model.secret.Version); err != nil {
		t.Fatalf("beginEditor() error = %v", err)
	}
	const draft = `{"value":"new"}`
	model.editor.SetValue(draft)

	model.handleSave(saveResult{err: fmt.Errorf("%w: %w", vaultcli.ErrOutcomeUnknown, context.DeadlineExceeded)})
	if model.screen != screenError || model.errorReturnScreen != screenEditor || model.secret != nil ||
		model.editorCAS != unknownCAS || model.editor.Value() != draft {
		t.Fatalf(
			"unknown save state = screen %d, return %d, secret %#v, CAS %d, draft %q",
			model.screen,
			model.errorReturnScreen,
			model.secret,
			model.editorCAS,
			model.editor.Value(),
		)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, command := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command != nil || model.screen != screenEditor ||
		!strings.Contains(model.status, "Reload the secret before saving") {
		t.Fatalf("unknown CAS was allowed to save: screen %d, status %q", model.screen, model.status)
	}
}

func TestUnknownDeleteAndLogoutOutcomesInvalidateLoadedState(t *testing.T) {
	t.Parallel()

	for name, handle := range map[string]func(*Model){
		"delete": func(model *Model) {
			model.handleDelete(deleteResult{err: fmt.Errorf("%w: deadline", vaultcli.ErrOutcomeUnknown)})
		},
		"logout": func(model *Model) {
			model.handleLogout(logoutResult{err: fmt.Errorf("%w: deadline", vaultcli.ErrOutcomeUnknown)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			model.screen = screenBrowser
			model.auth = vaultcli.AuthInfo{DisplayName: "developer"}
			model.selectedPath = "api"
			model.setSecret(vaultcli.Secret{Data: map[string]any{"value": true}, Version: 4})
			model.copied = &copiedEntry{key: "value", value: true}

			handle(model)
			if model.screen != screenError || model.secret != nil {
				t.Fatalf("unknown %s state = screen %d, secret %#v", name, model.screen, model.secret)
			}
			if name == "logout" && (model.copied != nil || model.auth.DisplayName != "" ||
				model.errorReturnScreen != screenLogin) {
				t.Fatalf(
					"unknown logout state = copied %t, auth %q, return screen %d",
					model.copied != nil,
					model.auth.DisplayName,
					model.errorReturnScreen,
				)
			}
		})
	}
}

func TestPartialUnknownLogoutReportsKnownState(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		outcome vaultcli.LogoutOutcome
		want    string
	}{
		"local token cleared": {
			outcome: vaultcli.LogoutOutcome{LocalTokenCleared: true},
			want:    "local token cleared, but Vault revoke outcome is unknown",
		},
		"token revoked": {
			outcome: vaultcli.LogoutOutcome{TokenRevoked: true},
			want:    "token revoked, but local token cleanup outcome is unknown",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			model.screen = screenBrowser
			model.auth = vaultcli.AuthInfo{DisplayName: "developer"}
			model.setSecret(vaultcli.Secret{Data: map[string]any{"value": true}, Version: 4})

			model.handleLogout(logoutResult{
				outcome: test.outcome,
				err:     fmt.Errorf("%w: deadline", vaultcli.ErrOutcomeUnknown),
			})
			if model.screen != screenError || model.errorReturnScreen != screenLogin ||
				model.errorTitle != "Logout incomplete" || model.auth.DisplayName != "" ||
				model.secret != nil || !strings.Contains(model.errorView.GetContent(), test.want) {
				t.Fatalf(
					"partial logout state = screen %d, return %d, title %q, auth %q, secret %t, error %q",
					model.screen,
					model.errorReturnScreen,
					model.errorTitle,
					model.auth.DisplayName,
					model.secret != nil,
					model.errorView.GetContent(),
				)
			}
		})
	}
}

func TestUnknownEnvironmentLogoutDoesNotOfferAnImpossibleTokenRetry(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.screen = screenBrowser
	model.client = vaultcli.NewWithBinary("/bin/false", model.config.Address, "")
	model.auth = vaultcli.AuthInfo{DisplayName: "developer", FromEnv: true}
	model.handleLogout(logoutResult{
		fromEnvironment: true,
		err:             fmt.Errorf("%w: deadline", vaultcli.ErrOutcomeUnknown),
	})

	if model.screen != screenError || model.errorReturnScreen != screenLogin ||
		model.auth.DisplayName != "" || !model.auth.FromEnv || model.errorCanReauth {
		t.Fatalf(
			"unknown env logout state = screen %d, return %d, auth %q, from env %t, reauth %t",
			model.screen,
			model.errorReturnScreen,
			model.auth.DisplayName,
			model.auth.FromEnv,
			model.errorCanReauth,
		)
	}
}

//nolint:gocognit // Each boundary case checks both lossless loading and the resulting editing behavior.
func TestEditorPreservesDocumentsAcrossLineLimits(t *testing.T) {
	t.Parallel()

	for _, count := range []int{100, maximumEditorLines - 2, maximumEditorLines - 1, vaultcli.MaximumCollectionEntries} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()

			model := newTestModel(t, config.Config{Address: "https://example.test"})
			model.resize(minimumWidth, minimumHeight)
			data := make(map[string]any, count)
			for index := range count {
				data[fmt.Sprintf("key-%05d", index)] = true
			}
			if err := model.beginEditor("large", data, 1); err != nil {
				t.Fatal(err)
			}
			actual, err := decodeEditor(model.editor.Value())
			if err != nil || !reflect.DeepEqual(actual, data) {
				t.Fatalf("editor changed %d entries: %v", count, err)
			}
			if count >= maximumEditorLines-1 && strings.Contains(model.editor.Value(), "\n") {
				t.Fatal("oversized line count did not use compact JSON")
			}
			if count == 100 {
				before := model.editor.Value()
				model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if model.editor.Value() != before+"\n" {
					t.Fatal("Enter did not add a newline after 99 lines")
				}
			}
		})
	}
}

func TestEditorShowsProgressDuringConfirmedSave(t *testing.T) {
	t.Parallel()

	model := newTestModel(t, config.Config{Address: "https://example.test"})
	model.resize(minimumWidth, minimumHeight)
	model.currentMount = testMountName
	if err := model.beginEditor("api", map[string]any{"key": true}, 1); err != nil {
		t.Fatal(err)
	}
	model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	output := model.View().Content
	if !model.loading || !strings.Contains(output, "Working...") || strings.Contains(output, "[Esc] discard") {
		t.Fatalf("saving editor view = %q", output)
	}
}

func TestEditorRejectsPartialInsertions(t *testing.T) {
	for _, text := range []string{
		"1" + strings.Repeat("\n,1", maximumEditorLines),
		strings.Repeat("x", maximumEditorBytes),
		strings.Repeat("\t", maximumEditorBytes/4),
		strings.Repeat("\r", maximumEditorLines),
	} {
		model := newTestModel(t, config.Config{})
		model.resize(minimumWidth, minimumHeight)
		if err := model.beginEditor("api", map[string]any{}, 1); err != nil {
			t.Fatal(err)
		}
		model.editor.SetValue(`{"values":[]}`)
		model.editor.SetCursorColumn(11)
		before := model.editor.Value()
		model.Update(tea.PasteMsg{Content: text})
		if model.editor.Value() != before || !model.statusError {
			t.Fatal("oversized paste changed the document or failed silently")
		}
		model.editor.SelectAll()
		selected := model.editor.SelectedText()
		model.Update(tea.PasteMsg{Content: strings.Repeat("x", maximumEditorBytes+1)})
		if model.editor.Value() != before || model.editor.SelectedText() != selected {
			t.Fatal("rejected paste changed the selection")
		}
		model.Update(tea.PasteMsg{Content: `{"replacement":true}`})
		if model.editor.Value() != `{"replacement":true}` {
			t.Fatal("valid replacement failed")
		}
	}
}

func TestEditorAcceptsPasteAtLineLimit(t *testing.T) {
	model := newTestModel(t, config.Config{})
	if err := model.beginEditor("api", map[string]any{}, 1); err != nil {
		t.Fatal(err)
	}
	model.editor.SetValue(`{"values":[]}`)
	model.editor.SetCursorColumn(11)
	model.Update(tea.PasteMsg{Content: "1" + strings.Repeat("\n,1", maximumEditorLines-1)})
	data, err := decodeEditor(model.editor.Value())
	if err != nil || len(data["values"].([]any)) != maximumEditorLines {
		t.Fatalf("paste at limit: %v", err)
	}
	model.editor.SelectAll()
	model.Update(tea.PasteMsg{Content: "{}"})
	if model.editor.Value() != "{}" {
		t.Fatal("replacement did not reclaim selected lines")
	}
}

func TestEditorClipboardResultsUseTheSameInsertionGuard(t *testing.T) {
	model := newTestModel(t, config.Config{})
	if err := model.beginEditor("api", map[string]any{}, 1); err != nil {
		t.Fatal(err)
	}
	_, command := model.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Ctrl+V did not request clipboard contents")
	}
	generation := model.editorGeneration
	model.Update(editorPasteResultMsg{generation: generation, content: strings.Repeat("\n", maximumEditorLines)})
	if model.editor.Value() != "{}" || !model.statusError {
		t.Fatal("clipboard result bypassed insertion guard")
	}
	if err := model.beginEditor("other", map[string]any{}, 1); err != nil {
		t.Fatal(err)
	}
	model.Update(editorPasteResultMsg{generation: generation, content: "stale"})
	if model.editor.Value() != "{}" {
		t.Fatal("old clipboard result changed a new editor")
	}
	model.Update(editorPasteResultMsg{generation: model.editorGeneration, err: errors.New("private clipboard detail")})
	if model.status != "Could not read clipboard" {
		t.Fatalf("clipboard error: %q", model.status)
	}
}

func TestEditorAndCopiedEntryRejectLossyNumbers(t *testing.T) {
	const document = `{"nested":[{"value":9007199254740993}]}`
	if _, err := decodeEditor(document); err == nil || !strings.Contains(err.Error(), "quote it") {
		t.Fatalf("decodeEditor: %v", err)
	}
	model := newTestModel(t, config.Config{})
	model.setSecret(vaultcli.Secret{Data: map[string]any{}, Version: 1})
	model.copied = &copiedEntry{key: "value", value: json.Number("9007199254740993")}
	if err := model.beginPaste(); err == nil || model.pendingWrite != nil {
		t.Fatalf("beginPaste: %v", err)
	}
}

//nolint:gocognit // Exercise every input mode against the same Unicode round-trip assertions.
func TestEditorPreservesUnicodeThroughSave(t *testing.T) {
	for _, character := range []rune{'\ufffd', '\u007f', '\u0085', '\u009f', '\u202e'} {
		t.Run(fmt.Sprintf("U+%04X", character), func(t *testing.T) {
			value := "left" + string(character) + "right"
			want := map[string]any{value: []any{value, `literal\ufffd`, "Привет 🌍"}}
			document, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"open", "paste", "clipboard", "typing"} {
				t.Run(mode, func(t *testing.T) {
					model := newTestModel(t, config.Config{})
					initial := map[string]any{}
					if mode == "open" {
						initial = want
					}
					if editorError := model.beginEditor("api", initial, 1); editorError != nil {
						t.Fatal(editorError)
					}
					if mode != "open" {
						model.editor.SelectAll()
						switch mode {
						case "paste":
							model.Update(tea.PasteMsg{Content: string(document)})
						case "clipboard":
							model.Update(
								editorPasteResultMsg{generation: model.editorGeneration, content: string(document)},
							)
						case "typing":
							for _, typed := range string(document) {
								model.Update(tea.KeyPressMsg{Code: typed, Text: string(typed)})
							}
						}
					}
					model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
					if model.pendingWrite == nil {
						t.Fatalf("save rejected: %s", model.status)
					}
					if !reflect.DeepEqual(model.pendingWrite.data, want) {
						t.Fatalf("save data = %#v, want %#v", model.pendingWrite.data, want)
					}
				})
			}
		})
	}
}

func TestEditorRejectsOversizedEscapedPaste(t *testing.T) {
	model := newTestModel(t, config.Config{})
	if err := model.beginEditor("api", map[string]any{"value": "original"}, 1); err != nil {
		t.Fatal(err)
	}
	model.editor.SelectAll()
	before := model.editor.Value()
	model.Update(tea.PasteMsg{Content: strings.Repeat("\u0085", maximumEditorBytes/6+1)})
	if model.editor.Value() != before || model.editor.SelectedText() != before || !model.statusError {
		t.Fatal("escaped paste exceeded the limit without preserving the document and selection")
	}
}
