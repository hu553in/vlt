package tui_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/testutil"
	"github.com/hu553in/vlt/internal/tui"
)

const testTimeout = 10 * time.Second

func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}

func newTestModel(t *testing.T, loaded config.Config) *tui.Model {
	t.Helper()

	model := tui.New(config.NewStore(filepath.Join(t.TempDir(), "config.toml")), loaded, nil)
	t.Cleanup(model.Close)

	return model
}

func TestMinimumTerminalGate(t *testing.T) {
	for name, size := range map[string]tea.WindowSizeMsg{
		"narrow": {Width: 89, Height: 24},
		"short":  {Width: 90, Height: 23},
	} {
		t.Run(name, func(t *testing.T) {
			model := newTestModel(t, config.Config{})
			_, _ = model.Update(size)
			output := model.View().Content
			if !strings.Contains(output, "Terminal is too small. Resize to at least 90×24.") {
				t.Fatalf("minimum-size view = %q", output)
			}
		})
	}

	model := newTestModel(t, config.Config{})
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if strings.Contains(model.View().Content, "Terminal is too small") {
		t.Fatal("minimum supported terminal was rejected")
	}

	_, _ = model.Update(tea.WindowSizeMsg{Width: 89, Height: 23})
	_, command := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if command != nil || !strings.Contains(model.View().Content, "Quit vlt?") {
		t.Fatal("small-terminal q did not open quit confirmation")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(model.View().Content, "Terminal is too small") {
		t.Fatal("canceling quit did not restore the resize gate")
	}
}

func TestViewRequestsPhysicalKeyboardCodes(t *testing.T) {
	model := newTestModel(t, config.Config{})
	view := model.View()
	if !view.KeyboardEnhancements.ReportAlternateKeys ||
		!view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes ||
		!view.KeyboardEnhancements.ReportAssociatedText {
		t.Fatalf("keyboard enhancements = %+v", view.KeyboardEnhancements)
	}
}

func TestStartupBlocksInputUntilBootstrapBegins(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://vault.example.com"})
	if command := model.Init(); command == nil {
		t.Fatal("Init() did not schedule bootstrap")
	}
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if output := model.View().Content; !strings.Contains(output, "Loading... [Esc] cancel") {
		t.Fatalf("initial bootstrap view = %q", output)
	}
	for _, key := range []rune{'r', 'o', 's', 'q'} {
		if _, command := model.Update(tea.KeyPressMsg{Code: key, Text: string(key)}); command != nil {
			t.Fatalf("startup key %q returned a command before bootstrap", key)
		}
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
}

func TestTextInputKeepsCurrentLayoutCharacters(t *testing.T) {
	model := newTestModel(t, config.Config{})
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	_, _ = model.Update(tea.KeyPressMsg{Code: 'й', Text: "й"})
	if output := model.View().Content; !strings.Contains(output, "й") {
		t.Fatalf("setup input lost current-layout text: %q", output)
	}
}

func TestBrowserHotkeysUsePhysicalKeys(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://vault.example.com"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	_, command := model.Update(tea.KeyPressMsg{Code: 'ض', BaseCode: 'q', Text: "ض"})
	if command != nil || !strings.Contains(model.View().Content, "Quit vlt?") {
		t.Fatal("physical q did not open quit confirmation")
	}
	_, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("confirmed physical q did not return a command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("confirmed physical q did not quit")
	}
}

func TestShiftedPhysicalHelpKey(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://vault.example.com"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	_, _ = model.Update(tea.KeyPressMsg{Code: ',', BaseCode: '/', Text: ",", Mod: tea.ModShift})
	if output := model.View().Content; !strings.Contains(output, "Keyboard help") {
		t.Fatalf("physical ? did not open help: %q", output)
	}
}

func TestQuitFromHelpUsesPhysicalKey(t *testing.T) {
	model := newTestModel(t, config.Config{Address: "https://vault.example.com"})
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	_, _ = model.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	_, command := model.Update(tea.KeyPressMsg{Code: 'ض', BaseCode: 'q', Text: "ض"})
	if command != nil || !strings.Contains(model.View().Content, "Quit vlt?") {
		t.Fatal("physical q on help did not open quit confirmation")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(model.View().Content, "Keyboard help") {
		t.Fatal("canceling quit did not return to help")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: 'ض', BaseCode: 'q', Text: "ض"})
	_, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("confirmed physical q on help did not return a command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("confirmed physical q on help did not quit")
	}
}

func TestSetupScreenShowsLiteralConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "vlt", "config.toml")
	model := tui.New(config.NewStore(path), config.Config{}, nil)
	t.Cleanup(model.Close)
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	output := model.View().Content
	if !strings.Contains(output, "Connect to Vault") ||
		!strings.Contains(output, "Config:") ||
		!strings.Contains(output, "config.toml") {
		t.Fatalf("setup view = %q", output)
	}
}

func TestSetupModalFitsMinimumTerminalWithLongConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("segment", 100), "config.toml")
	model := tui.New(config.NewStore(path), config.Config{}, nil)
	t.Cleanup(model.Close)
	_, _ = model.Update(tea.WindowSizeMsg{Width: 90, Height: 24})

	output := model.View().Content
	if !strings.Contains(output, "...") {
		t.Fatal("long config path was not truncated")
	}
	if height := strings.Count(output, "\n") + 1; height > 24 {
		t.Fatalf("setup height = %d, want at most 24", height)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if width := lipgloss.Width(line); width > 90 {
			t.Fatalf("setup line width = %d, want at most 90", width)
		}
	}
}

func TestBrowserPanelsStayAlignedWithoutSecret(t *testing.T) {
	loaded := config.Config{Address: "https://vault.example.com"}
	model := newTestModel(t, loaded)
	assertPanelGeometryAtSizes(t, model)
}

func TestReadAndRevealFlow(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")

	loaded := config.Config{Address: "https://vault.example.com"}
	testModel := teatest.NewTestModel(
		t,
		newTestModel(t, loaded),
		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret:/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "version 4")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "hunter2")
	testModel.Send(tea.KeyPressMsg{Code: 'c', Text: "c"})
	waitForText(t, testModel, "Copied \"password\"")
	testModel.Send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForText(t, testModel, "\\x1b[2J")
	testModel.Send(tea.KeyPressMsg{Code: 'a', Text: "a"})
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
	finalModel, ok := testModel.FinalModel(t).(*tui.Model)
	if !ok {
		t.Fatal("FinalModel() did not return *tui.Model")
	}
	output := finalModel.View().Content
	if strings.Contains(output, "hunter2") || strings.Contains(output, "\\x1b[2J") ||
		strings.Contains(output, "\x1b[2J") || !strings.Contains(output, "********") ||
		!strings.Contains(output, "Masked all values") {
		t.Fatalf("masked secret view = %q", output)
	}
	assertPanelGeometryAtSizes(t, finalModel)
}

func TestCopyAndPasteReplacesExactKeyWithLoadedCAS(t *testing.T) {
	binaryDirectory := t.TempDir()
	payloadPath := filepath.Join(binaryDirectory, "payload.json")
	argumentsPath := filepath.Join(binaryDirectory, "arguments")
	writeFakePasteVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")
	t.Setenv("VLT_TEST_PASTE_PAYLOAD", payloadPath)
	t.Setenv("VLT_TEST_PASTE_ARGUMENTS", argumentsPath)

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "2 entries")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "version 4")
	testModel.Send(tea.KeyPressMsg{Code: 'с', BaseCode: 'c', Text: "с"})
	waitForText(t, testModel, "Copied \"password\"")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "version 7")
	testModel.Send(tea.KeyPressMsg{Code: 'з', BaseCode: 'p', Text: "з"})
	waitForText(t, testModel, "Replace \"password\"?")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Saved target (version 8)")

	var payload map[string]any
	if err := json.Unmarshal([]byte(readFile(t, payloadPath)), &payload); err != nil {
		t.Fatalf("decode pasted payload: %v", err)
	}
	if payload["password"] != "hunter2" || payload["keep"] != true || len(payload) != 2 {
		t.Fatalf("pasted payload = %#v", payload)
	}
	arguments := readFile(t, argumentsPath)
	if !strings.Contains(arguments, "-cas=7") || !strings.Contains(arguments, "target") {
		t.Fatalf("paste arguments = %q", arguments)
	}

	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestPermissionErrorOffersReauthentication(t *testing.T) {
	binaryDirectory := t.TempDir()
	authenticatedMarker := filepath.Join(binaryDirectory, "authenticated")
	tokenInput := filepath.Join(binaryDirectory, "token")
	writeFakeReauthVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VLT_TEST_AUTH_MARKER", authenticatedMarker)
	t.Setenv("VLT_TEST_TOKEN", tokenInput)
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.WriteFile(tokenPath, []byte("expired-token"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com", Mounts: []string{"secret"}}),

		teatest.WithInitialTermSize(90, 24),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "[Enter] use another token")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Authenticate again")
	const replacementToken = "hvs.replacement"
	testModel.Send(tea.PasteMsg{Content: replacementToken})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret/")
	if actual := strings.TrimSpace(readFile(t, tokenInput)); actual != replacementToken {
		t.Fatalf("replacement token stdin = %q", actual)
	}

	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestFolderNavigationReturnsToMounts(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret:/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "nested")
	testModel.Send(tea.KeyPressMsg{Code: 'р', BaseCode: 'h', Text: "р"})
	waitForText(t, testModel, "api")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	waitForText(t, testModel, "KV mounts")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestRefreshReloadsMounts(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeRefreshVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")
	t.Setenv("VLT_TEST_REFRESH_MARKER", filepath.Join(binaryDirectory, "listed-once"))

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "initial")
	testModel.Send(tea.KeyPressMsg{Code: 'к', BaseCode: 'r', Text: "к"})
	waitForText(t, testModel, "refreshed")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestColorlessRendererRemovesColorsFromInputsAndListFilter(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")
	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.ASCII)),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: '/', Text: "/"})
	testModel.Type("sec")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	confirmQuit(t, testModel)
	output, err := io.ReadAll(testModel.FinalOutput(t, teatest.WithFinalTimeout(testTimeout)))
	if err != nil {
		t.Fatalf("read final terminal output: %v", err)
	}
	assertNoTerminalColors(t, string(output))
}

func TestCreateInEmptyMountRefreshesBrowserAndKeepsSecret(t *testing.T) {
	binaryDirectory := t.TempDir()
	marker := filepath.Join(binaryDirectory, "created")
	writeFakeCreateVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "test-environment-token")
	t.Setenv("VLT_TEST_CREATED", marker)

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret:/")
	testModel.Send(tea.KeyPressMsg{Code: 'т', BaseCode: 'n', Text: "т"})
	waitForText(t, testModel, "Create secret")
	testModel.Type(" /created/ ")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Edit created (CAS 0)")
	testModel.Send(tea.KeyPressMsg{Code: 'ы', BaseCode: 's', Mod: tea.ModCtrl})
	waitForText(t, testModel, "[Enter] save  [Esc] cancel")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "1 entry")
	testModel.Send(tea.KeyPressMsg{Code: 'e', Text: "e"})
	waitForText(t, testModel, "Edit created (CAS 1)")
	testModel.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Saved created (version 2)")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
	finalModel, ok := testModel.FinalModel(t).(*tui.Model)
	if !ok {
		t.Fatal("FinalModel() did not return *tui.Model")
	}
	output := finalModel.View().Content
	if strings.Count(output, "created") < 2 || !strings.Contains(output, "version 2") {
		t.Fatalf("created secret view = %q", output)
	}
}

func TestLoginPassesTokenExactly(t *testing.T) {
	binaryDirectory := t.TempDir()
	marker := filepath.Join(binaryDirectory, "authenticated")
	tokenFile := filepath.Join(binaryDirectory, "token")
	writeFakeLoginVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "")
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	if err := os.Remove(tokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove() error = %v", err)
	}
	t.Setenv("VLT_TEST_AUTH_MARKER", marker)
	t.Setenv("VLT_TEST_TOKEN", tokenFile)

	loaded := config.Config{Address: "https://vault.example.com"}
	testModel := teatest.NewTestModel(
		t,
		newTestModel(t, loaded),
		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })

	waitForText(t, testModel, "Authenticate")
	token := "hvs." + strings.Repeat("qsn", 3000)
	testModel.Send(tea.PasteMsg{Content: token})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret/")
	if actual := strings.TrimSpace(readFile(t, tokenFile)); actual != token {
		t.Fatalf("login stdin = %q, want %q", actual, token)
	}
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestPartialLogoutMovesToAccurateLocalState(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "")
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.WriteFile(tokenPath, []byte("hvs.local"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: 'o', Text: "o"})
	waitForText(t, testModel, "Log out?")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("canceling logout changed the cached token: %v", err)
	}
	testModel.Send(tea.KeyPressMsg{Code: 'o', Text: "o"})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "local token cleared, but Vault revoke failed")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	waitForText(t, testModel, "Authenticate")
	confirmQuitWithKey(t, testModel, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cached token still exists: %v", err)
	}
}

func TestRestrictedTokenWithoutLookupSelfStillBrowses(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeRestrictedVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "restricted-token")

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "token metadata unavailable")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
	finalModel, ok := testModel.FinalModel(t).(*tui.Model)
	if !ok || !strings.Contains(finalModel.View().Content, "secret/") {
		t.Fatalf("restricted token did not reach browser")
	}
}

func TestRestrictedMountDiscoveryPersistsManualMount(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeManualMountVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "restricted-token")

	store := config.NewStore(filepath.Join(t.TempDir(), "config.toml"))
	model := tui.New(store, config.Config{Address: "https://vault.example.com"}, nil)
	t.Cleanup(model.Close)
	testModel := teatest.NewTestModel(
		t,
		model,
		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "permission denied")
	testModel.Send(tea.KeyPressMsg{Code: 'm', Text: "m"})
	waitForText(t, testModel, "Add KV v2 mount")
	testModel.Type("prod-secret")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "prod-secret/")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Mounts) != 1 || loaded.Mounts[0] != "prod-secret" {
		t.Fatalf("saved mounts = %v, want prod-secret", loaded.Mounts)
	}
}

func TestChangingNamespaceClearsConfiguredMounts(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "")
	tokenPath := filepath.Join(os.Getenv("HOME"), ".vault-token")
	t.Cleanup(func() { _ = os.Remove(tokenPath) })
	if err := os.WriteFile(tokenPath, []byte("hvs.old-target"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := config.NewStore(filepath.Join(t.TempDir(), "config.toml"))
	loaded := config.Config{Address: "https://vault.example.com", Namespace: "old", Mounts: []string{"team-secret"}}
	model := tui.New(store, loaded, nil)
	t.Cleanup(model.Close)

	testModel := teatest.NewTestModel(
		t,
		model,
		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	waitForText(t, testModel, "Vault settings")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	for range len("old") {
		testModel.Send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	testModel.Type("new")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Switch Vault?")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "Authenticate")
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old target token still exists after confirmed switch: %v", err)
	}
	testModel.Type("hvs.new-target")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "1 entry")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))

	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved.Namespace != "new" || len(saved.Mounts) != 0 {
		t.Fatalf("saved config = %#v, want new namespace without stale mounts", saved)
	}
}

func TestDeleteSoftDeletesLatestVersionAtExecution(t *testing.T) {
	binaryDirectory := t.TempDir()
	deleteMarker := filepath.Join(binaryDirectory, "deleted")
	deleteStarted := filepath.Join(binaryDirectory, "delete-started")
	deleteRelease := filepath.Join(binaryDirectory, "delete-release")
	writeFakeDeleteVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "environment-token")
	t.Setenv("VLT_TEST_DELETE_MARKER", deleteMarker)
	t.Setenv("VLT_TEST_DELETE_STARTED", deleteStarted)
	t.Setenv("VLT_TEST_DELETE_RELEASE", deleteRelease)

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret:/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "version 4")
	testModel.Send(tea.KeyPressMsg{Code: 'd', Text: "d"})
	waitForText(t, testModel, "Delete latest secret version?")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, err := os.Stat(deleteStarted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled delete ran Vault CLI: %v", err)
	}
	testModel.Send(tea.KeyPressMsg{Code: 'd', Text: "d"})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForFile(t, deleteStarted)
	waitForText(t, testModel, "Working...")
	testModel.Send(tea.WindowSizeMsg{Width: 89, Height: 23})
	waitForText(t, testModel, "Terminal is too small")
	testModel.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, err := os.Stat(deleteMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked delete finished before release: %v", err)
	}
	testModel.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	if err := os.WriteFile(deleteRelease, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(delete release) error = %v", err)
	}
	waitForText(t, testModel, "Deleted latest version of api")
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))

	if _, err := os.Stat(deleteMarker); err != nil {
		t.Fatalf("confirmed delete did not finish: %v", err)
	}
}

func TestSaveRejectionStaysInEditorWithStructuredHTTPStatus(t *testing.T) {
	binaryDirectory := t.TempDir()
	writeFakeCASVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "environment-token")

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForText(t, testModel, "secret/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "secret:/")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "version 4")
	testModel.Send(tea.KeyPressMsg{Code: 'e', Text: "e"})
	waitForText(t, testModel, "Edit api (CAS 4)")
	testModel.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	waitForText(t, testModel, "[Enter] save  [Esc] cancel")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForText(t, testModel, "request failed with Vault HTTP 400")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	waitForText(t, testModel, "Edit api (CAS 4)")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	confirmQuit(t, testModel)
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
}

func TestRunningOperationCanBeCanceled(t *testing.T) {
	binaryDirectory := t.TempDir()
	operationStarted := filepath.Join(binaryDirectory, "operation-started")
	writeFakeSlowVault(t, filepath.Join(binaryDirectory, "vault"))
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VAULT_TOKEN", "environment-token")
	t.Setenv("VLT_TEST_OPERATION_STARTED", operationStarted)

	testModel := teatest.NewTestModel(
		t,
		newTestModel(
			t,
			config.Config{Address: "https://vault.example.com"}),

		teatest.WithInitialTermSize(100, 30),
	)
	t.Cleanup(func() { _ = testModel.Quit() })
	waitForFile(t, operationStarted)
	waitForText(t, testModel, "Loading... [Esc] cancel")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	waitForText(t, testModel, "Canceled")
	confirmQuitWithKey(t, testModel, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	testModel.WaitFinished(t, teatest.WithFinalTimeout(testTimeout))
	finalModel, ok := testModel.FinalModel(t).(*tui.Model)
	if !ok || !strings.Contains(finalModel.View().Content, "Connect to Vault") {
		t.Fatal("canceling initial bootstrap did not return to setup")
	}
}
