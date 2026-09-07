# vlt

[![CI](https://github.com/hu553in/vlt/actions/workflows/ci.yml/badge.svg)](https://github.com/hu553in/vlt/actions/workflows/ci.yml)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/hu553in/vlt)](https://github.com/hu553in/vlt/blob/main/go.mod)

Terminal interface for browsing and editing HashiCorp Vault KV v2 secrets through the official Vault
CLI. Navigate mounts and folders, inspect masked values, and edit JSON with version checks that
prevent overwriting someone else's changes.

## What it does

- Connects to a Vault address and optional namespace using your existing CLI token or a pasted token
- Discovers accessible KV v2 mounts, with a manual-mount fallback for restricted policies
- Browses and filters folders and secret paths in a two-panel view
- Masks values until explicitly revealed, with selected-value and reveal-all controls
- Copies a typed key/value entry and pastes it into another loaded secret
- Creates and edits JSON secrets with KV v2 check-and-set protection
- Soft-deletes the latest secret version at execution time
- Confirms quit, logout, writes, deletes, and discarding unsaved work

## Requirements

- macOS or Linux
- [Go](https://go.dev/doc/install), using the version declared in [`go.mod`](go.mod) or newer
- [Vault CLI](https://developer.hashicorp.com/vault/install) available as `vault` on `PATH`
- Access to a Vault server with a KV v2 mount and a token permitted to perform the intended
  operations
- A terminal at least 90 columns wide and 24 rows tall

Opening the Vault login page requires a browser opener: `open` on macOS, or `xdg-open`,
`x-www-browser`, `www-browser`, or `wslview` on Linux. Copying a value requires terminal clipboard
support; reading the system clipboard from the JSON editor on Linux requires `xclip` or `xsel`.

## Installation

```bash
go install github.com/hu553in/vlt/cmd/vlt@latest
vlt --help
```

Go installs the executable into `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset. Add that
directory to your `PATH` if the shell cannot find `vlt`. Run the same `go install` command to
update.

## User guide

### Connect to Vault

1. Run `vlt`.
2. Enter the Vault address and optional namespace, then confirm the target before existing CLI
   credentials can be used. Remote addresses must use HTTPS; HTTP is allowed only on loopback.
3. If prompted for a token, press `Ctrl+O` to open the Vault UI login page. Sign in there, copy the
   token, return to the terminal, and paste it. `vlt` does not read the browser session.
4. Open a KV v2 mount and navigate to a secret. If mount discovery is forbidden, press `m` in the
   error dialog and enter a known mount name.

### Edit a secret

Press `e` to edit the loaded secret as JSON, or `n` to create one in the current mount. Press
`Ctrl+S` to validate and confirm the write. Updates use the loaded version for check-and-set (CAS);
new secrets use CAS `0`, so they cannot overwrite an existing path. Reload a secret after a conflict
before deciding what to save.

Press `c` in the value panel to copy a typed entry, then open another secret and press `p` to
confirm adding or replacing that key. This writes the complete destination secret with the same CAS
checks. The selected value is also sent to the terminal clipboard: treat clipboard history as
sensitive.

Press `d` to confirm a recoverable soft delete. It deletes the latest version at execution time, not
necessarily the version you loaded. Permanent destruction is unavailable.

### Keyboard controls

The browser has mounts and paths on the left and the loaded secret on the right. Terminals smaller
than 90×24 show a resize message. On terminals with extended keyboard reporting, shortcuts follow
physical US-key positions; text fields keep the active keyboard layout.

| Key              | Action                                            |
| ---------------- | ------------------------------------------------- |
| `↑`/`↓`, `j`/`k` | Move selection                                    |
| `Enter`, `l`     | Open mount, folder, or secret                     |
| `Esc`            | Return focus, clear an applied filter, or go back |
| `h`              | Return focus or go back                           |
| `/`              | Filter the current list                           |
| `Tab`            | Switch browser and secret panels                  |
| Trackpad/wheel   | Scroll the pane under the pointer                 |
| `Enter`, `Space` | Reveal or mask the selected value                 |
| `a`              | Reveal or mask all values                         |
| `c`              | Copy the selected key and value                   |
| `p`              | Paste the copied key into the secret              |
| `n`              | Create a secret                                   |
| `e`              | Edit the loaded secret as JSON                    |
| `Ctrl+S`         | Validate JSON and confirm a CAS write             |
| `Ctrl+V`         | Paste system clipboard text into the JSON editor  |
| `d`              | Confirm a recoverable soft delete                 |
| `r`              | Refresh                                           |
| `s`              | Change Vault address or namespace                 |
| `o`              | Confirm logout in the main view                   |
| `Ctrl+O`         | Open Vault UI on login; log out in mount input    |
| `?`              | Show contextual keyboard help                     |
| `q`              | Confirm quit from the browser or help             |
| `Ctrl+D`         | Confirm quit from a single-line input             |

Confirmation dialogs use `Enter` to confirm and `Esc` to cancel. This includes quit, logout,
changing the Vault target, writes, soft delete, and discarding drafts. An unchanged existing secret
closes directly.

### Command-line options

| Option            | Description                |
| ----------------- | -------------------------- |
| `-h`, `--help`    | Print help and exit        |
| `-v`, `--version` | Print the version and exit |

## Configuration

The app stores only non-secret settings under the platform config directory:

```text
$XDG_CONFIG_HOME/vlt/config.toml
```

Without `XDG_CONFIG_HOME`, the default is `~/.config/vlt/config.toml` on Linux and
`~/Library/Application Support/vlt/config.toml` on macOS. `XDG_CONFIG_HOME` must be absolute.

The setup screen saves these settings; a config file is not required before the first run:

```toml
address = "https://vault.example.com"
namespace = "team/platform"
mounts = ["secret"]
```

The config directory and file are written with `0700` and `0600` permissions. Config writes replace
the file atomically.

| Field       | Description                                              |
| ----------- | -------------------------------------------------------- |
| `address`   | Vault origin URL without a path, query, or credentials   |
| `namespace` | Optional Vault namespace; empty means the root namespace |
| `mounts`    | Optional known KV v2 mounts for restricted policies      |

## Vault behavior and security

- Every secret operation verifies the mount is KV v2, including manually configured mounts. An
  inaccessible configured mount can be corrected with `m` in its error dialog.
- Login accepts tokens up to 64 KiB and sends them to `vault login` over stdin. A token is never
  stored in `config.toml` or placed in command arguments.
- Without a custom Vault CLI `token_helper`, Vault stores the token unencrypted in `~/.vault-token`
  with `0600` permissions. If `token_helper` is configured in the Vault CLI config, that helper
  controls storage instead; `vlt` uses the same official helper configuration and bounds external
  helper reads and erases by the active operation timeout.
- Existing `VAULT_TOKEN` is respected. `vlt` will not replace it; after logout, unset the variable
  and restart the app.
- Logout attempts to revoke the current token. For helper-backed tokens, it also clears the local
  helper entry. This affects other Vault CLI sessions using that token; partial failures are shown
  explicitly.
- Changing the address or namespace asks for confirmation, probes the new target without the current
  token, clears the local Vault CLI helper token without revoking it, saves the new settings, clears
  all loaded Vault data, and asks for a token for the new target.
- Operation error text is shown without wrapping or added explanations. Use the arrow keys to
  scroll. For a helper-token HTTP 403, press `Enter` to authenticate again. If the token comes from
  `VAULT_TOKEN`, unset the variable and restart `vlt` instead. After an unknown reauthentication
  attempt, `Esc` cannot return to the previous Vault state; authenticate successfully or quit.
  Errors from commands that receive secret data over stdin remain redacted. Failed token-helper
  operations never expose helper output because the helper is a credential boundary.
- The configured address and namespace override inherited `VAULT_ADDR`, `VAULT_AGENT_ADDR`, and
  `VAULT_NAMESPACE` values.
- Address or namespace changes are blocked while `VAULT_TOKEN`, `VAULT_MFA`, `VAULT_HEADERS`,
  `VAULT_CLIENT_CERT`, or `VAULT_CLIENT_KEY` is set, so a candidate target cannot receive ambient
  authentication material.
- Secret writes send the JSON payload over stdin. Compact secret data is limited to 2 MiB, 16,384
  top-level entries, and 64 KiB per key. A large or deeply nested valid secret opens as bounded
  compact JSON when an indented representation would exceed the editor byte limit or 10,000 lines.
  Oversized insertions are rejected in full, preserving the current text and selection. Unicode
  characters that the editor would strip are shown as JSON escapes and preserved when saved. JSON
  numbers that Vault would round are rejected before writing; use a JSON string when the exact
  digits must be preserved.
- Mount discovery, folder listings, and other persistent server-controlled fields are limited to
  16,384 entries per response and 64 KiB per field before they enter TUI state.
- A soft-deleted latest version remains visible in Vault's key list. `vlt` shows that state and `e`
  opens an empty JSON object using the deleted version as CAS, so the next ordinary write recreates
  the path safely. A future automatic-deletion date does not prevent reading live data.
- The in-memory copied entry is cleared on logout, successful reauthentication, or an
  address/namespace change. This does not clear the system clipboard or its history.
- If a mutation starts but returns neither a confirmed success nor a structured Vault HTTP 4xx
  rejection, its outcome is unknown: `vlt` clears the affected state and requires an authoritative
  reload before another write. HTTP 5xx responses remain unknown even when Vault formats them as
  structured errors.
- Vault reads can be canceled with `Esc`, and every Vault operation has a 20-second timeout.
  Confirmed mutations are not cancellable from the UI because Vault may commit them before the
  client receives the result. Timed-out mutations also require an authoritative reload before
  another write.

## Development

Requirements in addition to Go and Vault CLI:

- Bun
- Make
- golangci-lint
- Prek

```bash
git clone https://github.com/hu553in/vlt.git
cd vlt
make install-deps
prek install
make check
make check-fix
```

`make check` runs formatting, linting, hook/workflow/Renovate validation, a build, module
consistency, vulnerability checks, and race-enabled tests with a total coverage threshold.
`make check-fix` applies automatic fixes before running the same gate. Go tool dependencies are
pinned in `go.mod`.

```bash
make lint                  # Formatting and Go lint checks
make lint-fix              # Automatic formatting and lint fixes
make build                 # Build dist/vlt
make test                  # Tests with race detection and dist/coverage.out
make verify-test-coverage  # Tests and coverage threshold
make test-integration      # All tests, including the real-Vault lifecycle
make check-deps            # Verify go.mod and go.sum
make check-vulns           # Reachable Go vulnerabilities
make clean                 # Remove build and coverage output
```

The real-Vault lifecycle test starts an isolated in-memory dev server with temporary credentials. It
verifies create, update, soft delete, write-after-delete CAS, restricted policies, exact-path
handling, numeric precision, and token-helper login/logout. Ordinary `make test` skips this test
unless `VLT_INTEGRATION_VAULT_BINARY` points to a Vault executable; `make test-integration` requires
`vault` on `PATH` and enables it explicitly.

CI uses the shared Go check workflow and runs real-Vault tests and builds on Linux and macOS with
Vault from HashiCorp's package repositories. It does not publish binaries or create releases.

## Project structure

```text
cmd/vlt/             CLI entry point and flags
internal/config/     Local settings, validation, and atomic writes
internal/strictjson/ JSON decoding shared by the editor and Vault client
internal/testutil/   Isolated Vault test environment
internal/tui/        Terminal views, input handling, and state
internal/vaultcli/   Vault CLI calls, token helpers, and response validation
```

## Tech stack

- Go
- Bubble Tea, Bubbles, Lip Gloss
- HashiCorp Vault CLI and Vault API token helpers
- TOML configuration with renameio atomic writes
- golangci-lint, Prek, commitlint, Renovate

## License

[MIT](LICENSE)
