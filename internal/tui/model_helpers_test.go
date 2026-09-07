package tui_test

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/hu553in/vlt/internal/tui"
)

func waitForText(t *testing.T, testModel *teatest.TestModel, expected string) {
	t.Helper()
	t.Logf("waiting for %q", expected)

	teatest.WaitFor(
		t,
		testModel.Output(),
		func(output []byte) bool { return bytes.Contains(output, []byte(expected)) },
		teatest.WithDuration(testTimeout),
		teatest.WithCheckInterval(10*time.Millisecond),
	)
}

func confirmQuit(t *testing.T, testModel *teatest.TestModel) {
	t.Helper()
	confirmQuitWithKey(t, testModel, tea.KeyPressMsg{Code: 'q', Text: "q"})
}

func confirmQuitWithKey(t *testing.T, testModel *teatest.TestModel, key tea.KeyPressMsg) {
	t.Helper()
	testModel.Send(key)
	waitForText(t, testModel, "Quit vlt?")
	testModel.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("file %q was not created within %s", path, testTimeout)
}

func assertPanelBottomsAligned(t *testing.T, output string) {
	t.Helper()

	for line := range strings.SplitSeq(output, "\n") {
		if strings.Count(line, "└") == 2 {
			return
		}
	}

	var bottomLines []string
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "└") {
			bottomLines = append(bottomLines, line)
		}
	}
	t.Fatalf("panel bottoms are not aligned:\n%s", strings.Join(bottomLines, "\n"))
}

func assertPanelGeometryAtSizes(t *testing.T, model *tui.Model) {
	t.Helper()

	for _, size := range []tea.WindowSizeMsg{
		{Width: 90, Height: 24},
		{Width: 100, Height: 30},
		{Width: 120, Height: 40},
		{Width: 200, Height: 60},
	} {
		_, _ = model.Update(size)
		output := model.View().Content
		assertPanelBottomsAligned(t, output)
		assertPanelWidth(t, output, size.Width)
		if actual := strings.Count(output, "\n") + 1; actual > size.Height {
			t.Fatalf("browser height = %d, want at most %d", actual, size.Height)
		}
	}
}

func assertPanelWidth(t *testing.T, output string, want int) {
	t.Helper()

	for line := range strings.SplitSeq(output, "\n") {
		if strings.Count(line, "┌") != 2 {
			continue
		}
		if actual := lipgloss.Width(line); actual != want {
			t.Fatalf("joined panel width = %d, want %d", actual, want)
		}

		return
	}

	t.Fatal("joined panel top border not found")
}

func assertNoTerminalColors(t *testing.T, output string) {
	t.Helper()

	colorParameter := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	for _, match := range colorParameter.FindAllStringSubmatch(output, -1) {
		for parameter := range strings.SplitSeq(match[1], ";") {
			code, err := strconv.Atoi(parameter)
			if err != nil {
				continue
			}
			if code == 38 || code == 48 || code >= 30 && code <= 37 || code >= 40 && code <= 47 ||
				code >= 90 && code <= 97 || code >= 100 && code <= 107 {
				t.Fatalf("NO_COLOR view contains color SGR %d: %q", code, output)
			}
		}
	}
}

const (
	vaultStatusCase = `  status:-format=json) printf '%s\n' '{"version":"2.0.3","sealed":false}' ;;`
	vaultAuthCase   = `  token:lookup) printf '%s\n' '{"data":{"display_name":"developer"}}' ;;`
	vaultMountsCase = `  secrets:list) printf '%s\n' '{"secret/":{"type":"kv","description":"application secrets","options":{"version":"2"}}}' ;;`
	vaultPermission = `printf '%s\n' 'Error making API request.' '' 'Code: 403. Errors:' '' '* permission denied' >&2; exit 2`
)

func writeFakeVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		vaultAuthCase,
		vaultMountsCase,
		`  login:-no-print) cat >/dev/null ;;`,
		`  token:revoke)
    printf '%s\n' 'Error making API request.' '' 'Code: 400. Errors:' '' '* revoke unavailable' >&2
    exit 2 ;;`,
		`  kv:list)
    if [ "$7" = folder ]; then printf '%s\n' '["nested"]'; else printf '%s\n' '["api","folder/"]'; fi ;;`,
		`  kv:get) printf '%s\n' '{"data":{"data":{"password":"hunter2","username":"\u001b[2J"},"metadata":{"version":4}}}' ;;`,
	)
}

func writeFakePasteVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		vaultAuthCase,
		vaultMountsCase,
		`  kv:list) printf '%s\n' '["source","target"]' ;;`,
		`  kv:get)
    if [ "$7" = source ]; then
      printf '%s\n' '{"data":{"data":{"password":"hunter2"},"metadata":{"version":4}}}'
    else
      printf '%s\n' '{"data":{"data":{"keep":true,"password":"old"},"metadata":{"version":7}}}'
    fi ;;`,
		`  kv:put) printf '%s\n' "$@" > "$VLT_TEST_PASTE_ARGUMENTS"; cat > "$VLT_TEST_PASTE_PAYLOAD" ;;`,
	)
}

func writeFakeReauthVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		`  token:lookup)
    if [ ! -f "$VLT_TEST_AUTH_MARKER" ]; then `+vaultPermission+`; fi
    printf '%s\n' '{"data":{"display_name":"developer"}}' ;;`,
		`  login:-no-print) cat > "$VLT_TEST_TOKEN"; touch "$VLT_TEST_AUTH_MARKER" ;;`,
		`  secrets:list)
    if [ ! -f "$VLT_TEST_AUTH_MARKER" ]; then `+vaultPermission+`; fi
    printf '%s\n' '{"secret/":{"type":"kv","description":"application secrets","options":{"version":"2"}}}' ;;`,
		`  kv:list)
    if [ ! -f "$VLT_TEST_AUTH_MARKER" ]; then `+vaultPermission+`; fi
    printf '%s\n' '["api"]' ;;`,
	)
}

func writeFakeLoginVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(t, path,
		vaultStatusCase,
		`  token:lookup)
    if [ ! -f "$VLT_TEST_AUTH_MARKER" ]; then printf '%s\n' 'missing client token' >&2; exit 2; fi
    printf '%s\n' '{"data":{"display_name":"developer"}}' ;;`,
		`  login:-no-print) cat > "$VLT_TEST_TOKEN"; touch "$VLT_TEST_AUTH_MARKER" ;;`,
		vaultMountsCase,
	)
}

func writeFakeRefreshVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		vaultAuthCase,
		`  secrets:list)
    if [ -f "$VLT_TEST_REFRESH_MARKER" ]; then
      printf '%s\n' '{"secret/":{"type":"kv","description":"refreshed","options":{"version":"2"}}}'
    else
      touch "$VLT_TEST_REFRESH_MARKER"
      printf '%s\n' '{"secret/":{"type":"kv","description":"initial","options":{"version":"2"}}}'
    fi ;;`,
	)
}

func writeFakeCreateVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		vaultAuthCase,
		vaultMountsCase,
		`  kv:list) if [ -f "$VLT_TEST_CREATED" ]; then printf '%s\n' '["created"]'; else printf '%s\n' '{}'; exit 2; fi ;;`,
		`  kv:put) cat >/dev/null; touch "$VLT_TEST_CREATED" ;;`,
	)
}

func writeFakeRestrictedVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(t, path,
		vaultStatusCase,
		permissionCase("token:lookup"),
		vaultMountsCase,
	)
}

func writeFakeManualMountVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(t, path,
		vaultStatusCase,
		vaultAuthCase,
		permissionCase("secrets:list"),
	)
}

func writeFakeDeleteVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(t, path,
		vaultStatusCase,
		vaultAuthCase,
		vaultMountsCase,
		`  kv:list) printf '%s\n' '["api"]' ;;`,
		`  kv:get) printf '%s\n' '{"data":{"data":{"password":"hunter2"},"metadata":{"version":4}}}' ;;`,
		`  kv:delete)
    touch "$VLT_TEST_DELETE_STARTED"
    while [ ! -f "$VLT_TEST_DELETE_RELEASE" ]; do sleep 0.01; done
    touch "$VLT_TEST_DELETE_MARKER" ;;`,
	)
}

func writeFakeCASVault(t *testing.T, path string) {
	t.Helper()

	writeVaultScript(
		t,
		path,
		vaultStatusCase,
		vaultAuthCase,
		vaultMountsCase,
		`  kv:list) printf '%s\n' '["api"]' ;;`,
		`  kv:get) printf '%s\n' '{"data":{"data":{"password":"hunter2"},"metadata":{"version":4}}}' ;;`,
		`  kv:put) cat >/dev/null; printf '%s\n' 'Error making API request.' '' 'Code: 400. Errors:' '' '* check-and-set parameter did not match the current version' >&2; exit 2 ;;`,
	)
}

func writeFakeSlowVault(t *testing.T, path string) {
	t.Helper()

	writeExecutable(
		t,
		path,
		"#!/bin/sh\ntouch \"$VLT_TEST_OPERATION_STARTED\"\nwhile :; do sleep 0.01; done\n",
	)
}

func permissionCase(command string) string {
	return "  " + command + ") " + vaultPermission + " ;;"
}

func writeVaultScript(t *testing.T, path string, cases ...string) {
	t.Helper()

	cases = append(
		cases,
		`  read:-format=json) printf '%s\n' '{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}' ;;`,
	)
	script := "#!/bin/sh\ncase \"$1:$2\" in\n" + strings.Join(cases, "\n") +
		"\n  *) printf 'unexpected command: %s %s\\n' \"$1\" \"$2\" >&2; exit 64 ;;\nesac\n"
	writeExecutable(t, path, script)
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	return string(data)
}
