package tui

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

type spinnerDelayMsg struct {
	id uint64
}

type operationResultMsg struct {
	id    uint64
	value any
}

type bootstrapResult struct {
	config              config.Config
	client              *vaultcli.Client
	status              vaultcli.Status
	auth                vaultcli.AuthInfo
	persist             bool
	targetChanged       bool
	needsLogin          bool
	metadataUnavailable bool
	err                 error
}

type bootstrapCommitResult struct {
	bootstrap    bootstrapResult
	tokenCleared bool
	err          error
}

type mountsResult struct {
	mounts []vaultcli.Mount
	err    error
}

type entriesResult struct {
	mount          string
	folder         string
	keys           []string
	preserveSecret bool
	err            error
}

type secretResult struct {
	secretPath string
	secret     vaultcli.Secret
	err        error
}

type loginResult struct {
	auth                vaultcli.AuthInfo
	metadataUnavailable bool
	err                 error
}

type logoutResult struct {
	fromEnvironment bool
	outcome         vaultcli.LogoutOutcome
	err             error
}

type mountSavedResult struct {
	config config.Config
	err    error
}

type saveResult struct {
	secretPath string
	secret     vaultcli.Secret
	err        error
}

type deleteResult struct {
	secretPath string
	err        error
}

func (model *Model) startOperation(operation func(context.Context) any) tea.Cmd {
	return model.startAsyncOperation(operation, true)
}

func (model *Model) startMutation(operation func(context.Context) any) tea.Cmd {
	return model.startAsyncOperation(operation, false)
}

func (model *Model) startAsyncOperation(operation func(context.Context) any, cancellable bool) tea.Cmd {
	if model.cancel != nil {
		model.cancel()
	}

	ctx, cancel := context.WithTimeout(model.lifetime, operationTimeout)
	model.cancel = nil
	if cancellable {
		model.cancel = cancel
	}
	model.operationID++
	operationID := model.operationID
	model.loading = true
	model.showSpinner = false

	operationCommand := func() tea.Msg {
		return model.operations.run(func() tea.Msg {
			defer cancel()

			return operationResultMsg{id: operationID, value: operation(ctx)}
		})
	}
	spinnerCommand := tea.Tick(spinnerDelay, func(time.Time) tea.Msg {
		return spinnerDelayMsg{id: operationID}
	})

	return tea.Batch(operationCommand, spinnerCommand)
}

func (model *Model) cancelOperation() {
	if model.cancel != nil {
		model.cancel()
	}
	model.cancel = nil
	model.operationID++
	model.loading = false
	model.showSpinner = false
	if model.client == nil {
		model.screen = screenSetup
		model.focusSetup(0)
	}
	model.setStatus("Canceled")
}

func (model *Model) finishOperation() {
	model.cancel = nil
	model.loading = false
	model.showSpinner = false
}

func (model *Model) startBootstrap(candidate config.Config, persist bool) tea.Cmd {
	targetChanged := model.changingVaultTarget(candidate)

	operation := func(ctx context.Context) any {
		if targetChanged {
			if err := vaultSwitchCredentialError(); err != nil {
				return bootstrapResult{err: err}
			}
		}
		client, err := vaultcli.New(candidate.Address, candidate.Namespace)
		if err != nil {
			return bootstrapResult{err: err}
		}

		status, err := client.Status(ctx)
		if err != nil {
			return bootstrapResult{err: err}
		}
		if status.Sealed {
			return bootstrapResult{err: errors.New("vault is sealed")}
		}

		auth := vaultcli.AuthInfo{}
		metadataUnavailable := false
		hasToken := false
		if !targetChanged {
			hasToken, err = client.HasToken(ctx)
			if err != nil {
				return bootstrapResult{err: err}
			}
			if hasToken {
				auth, err = client.AuthStatus(ctx)
				metadataUnavailable = err != nil
			}
		}

		return bootstrapResult{
			config:              candidate,
			client:              client,
			status:              status,
			auth:                auth,
			persist:             persist,
			targetChanged:       targetChanged,
			needsLogin:          targetChanged || !hasToken,
			metadataUnavailable: metadataUnavailable,
		}
	}

	return model.startOperation(operation)
}

func (model *Model) changingVaultTarget(candidate config.Config) bool {
	return model.config.Address != "" &&
		(candidate.Address != model.config.Address || candidate.Namespace != model.config.Namespace)
}

func vaultSwitchCredentialError() error {
	credential := vaultcli.AmbientCredentialVariable()
	if credential == "" {
		return nil
	}

	return fmt.Errorf(
		"%s is set; unset it and restart vlt before changing the Vault address or namespace",
		credential,
	)
}

func (model *Model) startBootstrapCommit(result bootstrapResult) tea.Cmd {
	store := model.store
	currentClient := model.client
	currentConfig := model.config

	return model.startMutation(func(ctx context.Context) any {
		tokenCleared := false
		if result.targetChanged {
			clearClient := currentClient
			if clearClient == nil {
				var err error
				clearClient, err = vaultcli.New(currentConfig.Address, currentConfig.Namespace)
				if err != nil {
					return bootstrapCommitResult{bootstrap: result, err: err}
				}
			}
			if err := clearClient.ClearToken(ctx); err != nil {
				return bootstrapCommitResult{bootstrap: result, err: err}
			}
			tokenCleared = true
		}
		if err := store.Save(result.config); err != nil {
			if tokenCleared {
				err = fmt.Errorf("local Vault token was cleared, but the new configuration was not saved: %w", err)
			}

			return bootstrapCommitResult{bootstrap: result, tokenCleared: tokenCleared, err: err}
		}

		return bootstrapCommitResult{bootstrap: result, tokenCleared: tokenCleared}
	})
}

func (model *Model) startLoadMounts() tea.Cmd {
	client := model.client

	return model.startOperation(func(ctx context.Context) any {
		mounts, err := client.ListMounts(ctx)

		return mountsResult{mounts: mounts, err: err}
	})
}

func (model *Model) startLoadEntries(mount, folder string) tea.Cmd {
	return model.startEntriesOperation(mount, folder, false)
}

func (model *Model) refreshEntriesPreservingSecret() tea.Cmd {
	return model.startEntriesOperation(model.currentMount, model.currentPath, true)
}

func (model *Model) startEntriesOperation(mount, folder string, preserveSecret bool) tea.Cmd {
	client := model.client

	return model.startOperation(func(ctx context.Context) any {
		keys, err := client.List(ctx, mount, folder)

		return entriesResult{
			mount: mount, folder: folder, keys: keys, preserveSecret: preserveSecret, err: err,
		}
	})
}

func (model *Model) startGetSecret(secretPath string) tea.Cmd {
	client := model.client
	mount := model.currentMount

	return model.startOperation(func(ctx context.Context) any {
		secret, err := client.Get(ctx, mount, secretPath)

		return secretResult{secretPath: secretPath, secret: secret, err: err}
	})
}

func (model *Model) startLogin(token string) tea.Cmd {
	client := model.client

	return model.startMutation(func(ctx context.Context) any {
		if err := client.Login(ctx, token); err != nil {
			return loginResult{err: err}
		}
		auth, err := client.AuthStatus(ctx)

		return loginResult{auth: auth, metadataUnavailable: err != nil}
	})
}

func (model *Model) startLogout() tea.Cmd {
	client := model.client
	fromEnvironment := model.auth.FromEnv

	return model.startMutation(func(ctx context.Context) any {
		outcome, err := client.Logout(ctx)

		return logoutResult{fromEnvironment: fromEnvironment, outcome: outcome, err: err}
	})
}

func (model *Model) startSaveMount(candidate config.Config) tea.Cmd {
	store := model.store

	return model.startMutation(func(context.Context) any {
		return mountSavedResult{config: candidate, err: store.Save(candidate)}
	})
}

func (model *Model) startSaveSecret(target secretTarget, data map[string]any, version int) tea.Cmd {
	return model.startMutation(func(ctx context.Context) any {
		writtenVersion, err := target.client.Put(ctx, target.mount, target.path, data, version)
		if err != nil {
			return saveResult{err: err}
		}

		return saveResult{
			secretPath: target.path,
			secret:     vaultcli.Secret{Data: data, Version: writtenVersion},
		}
	})
}

func (model *Model) startDeleteSecret(target secretTarget) tea.Cmd {
	return model.startMutation(func(ctx context.Context) any {
		return deleteResult{
			secretPath: target.path,
			err:        target.client.Delete(ctx, target.mount, target.path),
		}
	})
}

func (model *Model) handleOperation(value any) tea.Cmd {
	switch result := value.(type) {
	case bootstrapResult:
		return model.handleBootstrap(result)
	case bootstrapCommitResult:
		return model.handleBootstrapCommit(result)
	case mountsResult:
		return model.handleMounts(result)
	case entriesResult:
		return model.handleEntries(result)
	case secretResult:
		return model.handleSecret(result)
	case loginResult:
		return model.handleLogin(result)
	case logoutResult:
		return model.handleLogout(result)
	case mountSavedResult:
		return model.handleMountSaved(result)
	case saveResult:
		return model.handleSave(result)
	case deleteResult:
		return model.handleDelete(result)
	default:
		model.showOperationError("Operation failed", errors.New("internal error: unknown operation result"))

		return nil
	}
}

func (model *Model) handleBootstrap(result bootstrapResult) tea.Cmd {
	if result.err != nil {
		model.screen = screenSetup
		model.focusSetup(0)
		model.showOperationError("Could not connect to Vault", result.err)

		return nil
	}
	if result.persist {
		return model.startBootstrapCommit(result)
	}

	return model.applyBootstrap(result)
}

func (model *Model) handleBootstrapCommit(result bootstrapCommitResult) tea.Cmd {
	if result.err != nil {
		if result.tokenCleared || errors.Is(result.err, vaultcli.ErrOutcomeUnknown) {
			model.resetVaultContext()
			model.auth = vaultcli.AuthInfo{}
		}
		model.screen = screenSetup
		model.focusSetup(0)
		model.showOperationError("Could not change Vault settings", result.err)

		return nil
	}

	return model.applyBootstrap(result.bootstrap)
}

func (model *Model) applyBootstrap(result bootstrapResult) tea.Cmd {
	if result.targetChanged {
		model.resetVaultContext()
	}

	model.config = result.config
	model.client = result.client
	model.auth = result.auth
	model.setupEditing = false
	if result.needsLogin {
		model.screen = screenLogin
		model.loginInput.Reset()
		model.loginInput.Focus()
		model.setStatus("Connected to Vault " + result.status.Version)

		return nil
	}

	model.screen = screenBrowser
	if result.metadataUnavailable {
		model.setStatus("Connected to Vault " + result.status.Version + "; token metadata unavailable")
	} else {
		model.setStatus("Connected to Vault " + result.status.Version)
	}

	return model.startLoadMounts()
}

func (model *Model) resetVaultContext() {
	model.resetVaultBrowserContext()
	model.pendingWrite = nil
	model.pendingDelete = nil
	model.loginInput.Reset()
	model.loginInput.Blur()
	model.editor.Reset()
	model.editor.Blur()
	model.editorPath = ""
	model.editorCAS = 0
	model.editorOriginal = ""
}

func (model *Model) resetVaultBrowserContext() {
	model.invalidateLoginOpen()
	model.mounts = nil
	model.discoveredMounts = nil
	model.currentMount = ""
	model.currentPath = ""
	model.clearSecret()
	model.clearCopiedEntry()
	model.replaceBrowserItems(nil)
	model.browser.Title = browserTitle
	model.pathInput.Reset()
	model.pathInput.Blur()
	model.mountInput.Reset()
	model.mountInput.Blur()
	model.editingMount = ""
}

func (model *Model) handleMounts(result mountsResult) tea.Cmd {
	if result.err != nil {
		if errors.Is(result.err, vaultcli.ErrPermissionDenied) {
			model.mounts = mountsFromConfig(model.config.Mounts)
			model.showMounts()
			model.showMountOperationError("Could not load mounts", result.err, "")

			return nil
		}
		model.showVaultOperationError("Could not load mounts", result.err)

		return nil
	}

	model.discoveredMounts = slices.Clone(result.mounts)
	model.mounts = mergeMounts(result.mounts, model.config.Mounts)
	model.showMounts()
	if len(model.mounts) == 0 {
		model.setStatus("No accessible KV v2 mounts")
	}

	return nil
}

func (model *Model) handleEntries(result entriesResult) tea.Cmd {
	if result.err != nil {
		if result.folder == "" && slices.Contains(model.config.Mounts, result.mount) {
			model.showMountOperationError("Could not load entries", result.err, result.mount)

			return nil
		}
		model.showVaultOperationError("Could not load entries", result.err)

		return nil
	}

	model.currentMount = result.mount
	model.currentPath = result.folder
	if !result.preserveSecret {
		model.clearSecret()
	}
	items := make([]list.Item, 0, len(result.keys))
	for _, key := range result.keys {
		folder := strings.HasSuffix(key, "/")
		name := strings.TrimSuffix(key, "/")
		itemPath := path.Join(result.folder, name)
		if folder {
			items = append(items, browserItem{
				title: name + "/", description: "folder", path: itemPath, kind: itemFolder,
			})
		} else {
			items = append(items, browserItem{
				title: name, description: secretDescription, path: itemPath, kind: itemSecret,
			})
		}
	}
	model.browser.Title = displayText(result.mount + ":/" + result.folder)
	model.replaceBrowserItems(items)
	if result.preserveSecret && model.selectedPath != "" {
		for index, item := range items {
			browserEntry, ok := item.(browserItem)
			if ok && browserEntry.path == model.selectedPath {
				model.browser.Select(index)

				break
			}
		}
	}
	if !result.preserveSecret {
		model.focus = paneBrowser
		model.secretTable.Blur()
	}

	return nil
}

func (model *Model) handleSecret(result secretResult) tea.Cmd {
	if result.err != nil {
		model.showVaultOperationError("Could not load secret", result.err)

		return nil
	}

	model.selectedPath = result.secretPath
	model.setSecret(result.secret)
	if len(model.secretKeys) > 0 {
		model.focus = paneSecret
		model.secretTable.Focus()
	}
	if result.secret.Deleted {
		model.setStatus(
			fmt.Sprintf("Loaded %s (latest version %d is deleted)", result.secretPath, result.secret.Version),
		)
	} else {
		model.setStatus(fmt.Sprintf("Loaded %s (version %d)", result.secretPath, result.secret.Version))
	}

	return nil
}

func (model *Model) handleLogin(result loginResult) tea.Cmd {
	if result.err != nil {
		title := authenticationError
		if errors.Is(result.err, vaultcli.ErrOutcomeUnknown) {
			title = "Authentication outcome unknown"
			model.auth = vaultcli.AuthInfo{}
			if model.reauthenticating {
				model.reauthRequired = true
			} else {
				model.resetVaultContext()
				model.screen = screenLogin
				model.loginInput.Focus()
			}
		}
		model.showVaultOperationError(title, result.err)

		return nil
	}

	returnScreen := model.reauthReturnScreen
	wasReauthentication := model.reauthenticating
	model.reauthenticating = false
	model.reauthRequired = false
	model.reauthReturnScreen = screenSetup
	model.auth = result.auth
	model.loginInput.Blur()
	if result.metadataUnavailable {
		model.setStatus("Authenticated; token metadata unavailable")
	} else {
		model.setStatus("Authenticated as " + result.auth.DisplayName)
	}

	if wasReauthentication && returnScreen == screenEditor {
		model.clearCopiedEntry()
		model.screen = screenEditor

		return nil
	}

	model.resetVaultContext()
	model.screen = screenBrowser

	return model.startLoadMounts()
}

func (model *Model) handleLogout(result logoutResult) tea.Cmd {
	loggedOut := result.outcome.TokenRevoked || result.outcome.LocalTokenCleared
	unknown := errors.Is(result.err, vaultcli.ErrOutcomeUnknown)
	if result.err != nil && !loggedOut {
		if unknown {
			model.auth = vaultcli.AuthInfo{FromEnv: result.fromEnvironment}
			model.resetVaultContext()
			model.screen = screenLogin
			model.loginInput.Reset()
			model.loginInput.Focus()
			model.showVaultOperationError("Logout outcome unknown", result.err)
		} else {
			model.showVaultOperationError("Could not log out", result.err)
		}

		return nil
	}

	model.auth = vaultcli.AuthInfo{FromEnv: result.fromEnvironment}
	model.resetVaultContext()
	model.screen = screenLogin
	model.loginInput.Reset()
	model.loginInput.Focus()
	switch {
	case result.err != nil:
		detail := "logout failed after changing authentication state"
		switch {
		case result.outcome.LocalTokenCleared && unknown:
			detail = "local token cleared, but Vault revoke outcome is unknown"
		case result.outcome.LocalTokenCleared:
			detail = "local token cleared, but Vault revoke failed"
		case result.outcome.TokenRevoked && unknown:
			detail = "token revoked, but local token cleanup outcome is unknown"
		case result.outcome.TokenRevoked:
			detail = "token revoked, but local token cleanup failed"
		}
		model.showVaultOperationError(
			"Logout incomplete",
			fmt.Errorf("%s: %w", detail, result.err),
		)
	case result.fromEnvironment:
		model.setStatus("Token revoked; unset VAULT_TOKEN and restart vlt")
	default:
		model.setStatus("Logged out")
	}

	return nil
}

func (model *Model) handleMountSaved(result mountSavedResult) tea.Cmd {
	if result.err != nil {
		model.showOperationError("Could not save mount configuration", result.err)

		return nil
	}

	model.config = result.config
	model.editingMount = ""
	model.mountInput.Reset()
	model.mountInput.Blur()
	model.screen = screenBrowser
	model.mounts = mergeMounts(model.discoveredMounts, result.config.Mounts)
	model.showMounts()
	model.setStatus("Saved mount configuration")

	return nil
}

func (model *Model) handleSave(result saveResult) tea.Cmd {
	if result.err != nil {
		if errors.Is(result.err, vaultcli.ErrOutcomeUnknown) {
			returnScreen := model.screen
			model.clearSecret()
			if returnScreen == screenEditor {
				model.editorCAS = unknownCAS
				model.screen = screenEditor
			}
			model.showVaultOperationError("Save outcome unknown", result.err)

			return nil
		}
		model.showVaultOperationError("Could not save secret", result.err)

		return nil
	}

	model.selectedPath = result.secretPath
	model.setSecret(result.secret)
	model.editor.Blur()
	model.screen = screenBrowser
	model.setStatus(fmt.Sprintf("Saved %s (version %d)", result.secretPath, result.secret.Version))

	return model.refreshEntriesPreservingSecret()
}

func (model *Model) handleDelete(result deleteResult) tea.Cmd {
	model.screen = screenBrowser
	if result.err != nil {
		if errors.Is(result.err, vaultcli.ErrOutcomeUnknown) {
			model.clearSecret()
			model.showVaultOperationError("Delete outcome unknown", result.err)

			return nil
		}
		model.showVaultOperationError("Could not delete secret", result.err)

		return nil
	}

	model.clearSecret()
	model.setStatus("Deleted latest version of " + result.secretPath)

	return nil
}

func (model *Model) showOperationError(title string, err error) {
	if model.screen != screenError {
		model.errorReturnScreen = model.screen
	}
	model.screen = screenError
	model.errorTitle = title
	model.errorCanReauth = false
	model.errorCanEditMount = false
	model.errorMount = ""
	model.errorView.SetContent(displayMultilineText(err.Error()))
	model.errorView.GotoTop()
	model.errorView.SetXOffset(0)
	model.resizeErrorView()
	model.setStatus("")
}

func (model *Model) showVaultOperationError(title string, err error) {
	model.showOperationError(title, err)
	model.errorCanReauth = model.client != nil &&
		errors.Is(err, vaultcli.ErrPermissionDenied) && !model.auth.FromEnv
}

func (model *Model) showMountOperationError(title string, err error, mount string) {
	model.showVaultOperationError(title, err)
	model.errorCanEditMount = true
	model.errorMount = mount
}

func (model *Model) showMounts() {
	items := make([]list.Item, 0, len(model.mounts))
	for _, mount := range model.mounts {
		description := mount.Description
		if description == "" {
			description = kvV2Description
		}
		items = append(items, browserItem{
			title: mount.Path + "/", description: description, path: mount.Path, kind: itemMount,
		})
	}
	model.currentMount = ""
	model.currentPath = ""
	model.clearSecret()
	model.browser.Title = browserTitle
	model.replaceBrowserItems(items)
	model.focus = paneBrowser
	model.secretTable.Blur()
}

func (model *Model) beginMountEdit(mount string) {
	model.screen = screenMount
	model.editingMount = mount
	model.mountInput.Reset()
	if mount != "" {
		model.mountInput.SetValue(mount)
	}
	model.mountInput.CursorEnd()
	model.mountInput.Focus()
}

func mountsFromConfig(paths []string) []vaultcli.Mount {
	mounts := make([]vaultcli.Mount, 0, len(paths))
	for _, mountPath := range paths {
		mounts = append(mounts, vaultcli.Mount{Path: mountPath, Description: configuredMountDescription})
	}

	return mounts
}

func mergeMounts(discovered []vaultcli.Mount, configured []string) []vaultcli.Mount {
	merged := append([]vaultcli.Mount(nil), discovered...)
	known := make(map[string]struct{}, len(discovered)+len(configured))
	for _, mount := range discovered {
		known[mount.Path] = struct{}{}
	}
	for _, configuredPath := range configured {
		if _, exists := known[configuredPath]; exists {
			continue
		}
		merged = append(merged, vaultcli.Mount{Path: configuredPath, Description: configuredMountDescription})
		known[configuredPath] = struct{}{}
	}
	slices.SortFunc(merged, func(left, right vaultcli.Mount) int {
		return strings.Compare(left.Path, right.Path)
	})

	return merged
}
