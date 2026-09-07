package tui

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

type browserFilterResultMsg struct {
	generation uint64
	matches    list.FilterMatchesMsg
}

func (model *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if command, handled := model.handleSystemMessage(message); handled {
		return model, command
	}

	keyMessage, isKey := message.(tea.KeyPressMsg)
	if model.loading {
		if isKey && shortcutString(keyMessage) == keyEscape && model.cancel != nil {
			model.cancelOperation()
		}

		return model, nil
	}

	if model.terminalTooSmall() {
		if !isKey {
			return model, nil
		}

		key := shortcutString(keyMessage)
		if model.screen == screenConfirm {
			switch key {
			case keyEnter:
				return model, model.confirm()
			case keyEscape:
				model.cancelConfirmation()
			}

			return model, nil
		}
		if key == "q" {
			model.beginConfirmation(confirmationQuit)
		}

		return model, nil
	}

	return model.updateScreen(message)
}

func (model *Model) handleSystemMessage(message tea.Msg) (tea.Cmd, bool) {
	switch typedMessage := message.(type) {
	case tea.WindowSizeMsg:
		model.resize(typedMessage.Width, typedMessage.Height)

		return nil, true
	case tea.BackgroundColorMsg:
		model.dark = typedMessage.IsDark()
		model.applyTheme()

		return nil, true
	case spinnerDelayMsg:
		if typedMessage.id == model.operationID && model.loading {
			model.showSpinner = true

			return model.spinner.Tick, true
		}

		return nil, true
	case spinner.TickMsg:
		if model.showSpinner {
			updatedSpinner, command := model.spinner.Update(message)
			model.spinner = updatedSpinner

			return command, true
		}

		return nil, true
	case browserFilterResultMsg:
		if typedMessage.generation != model.browserFilterGeneration {
			return nil, true
		}
		model.browserFilterPending = false

		return model.applyBrowserFilter(typedMessage.matches), true
	case list.FilterMatchesMsg:
		// Filter results are valid only when tagged with the input that produced them.
		return nil, true
	case operationResultMsg:
		if typedMessage.id != model.operationID {
			return nil, true
		}
		model.finishOperation()

		return model.handleOperation(typedMessage.value), true
	case openLoginResultMsg:
		if typedMessage.id != model.loginOpenID || model.loginOpenCancel == nil {
			return nil, true
		}
		model.loginOpenCancel = nil
		if model.screen != screenLogin || model.loading ||
			typedMessage.url != vaultLoginURL(model.config.Address, model.config.Namespace) {
			return nil, true
		}
		if typedMessage.err != nil {
			model.showOperationError("Could not open Vault login", typedMessage.err)
		}

		return nil, true
	}

	return nil, false
}

func (model *Model) updateScreen(message tea.Msg) (tea.Model, tea.Cmd) {
	switch model.screen {
	case screenSetup:
		return model.updateSetup(message)
	case screenLogin:
		return model.updateLogin(message)
	case screenBrowser:
		return model.updateBrowser(message)
	case screenMount:
		return model.updateMount(message)
	case screenPath:
		return model.updatePath(message)
	case screenEditor:
		return model.updateEditor(message)
	case screenConfirm:
		return model.updateConfirmation(message)
	case screenHelp:
		return model.updateHelp(message)
	case screenError:
		return model.updateError(message)
	default:
		model.setError("Internal error: unknown screen")

		return model, nil
	}
}

func (model *Model) updateSetup(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		switch shortcutString(keyMessage) {
		case keyQuitInput:
			model.beginConfirmation(confirmationQuit)

			return model, nil
		case keyEscape:
			if model.setupEditing {
				model.screen = model.returnScreen
				model.setupEditing = false
			}

			return model, nil
		case "tab", "down":
			model.focusSetup((model.setupIndex + 1) % len(model.setupInputs))

			return model, nil
		case "shift+tab", "up":
			model.focusSetup((model.setupIndex - 1 + len(model.setupInputs)) % len(model.setupInputs))

			return model, nil
		case keyEnter:
			if model.setupIndex == 0 {
				model.focusSetup(1)

				return model, nil
			}

			candidate := model.config
			candidate.Address = model.setupInputs[0].Value()
			candidate.Namespace = model.setupInputs[1].Value()
			normalized, err := model.normalizeSetupConfig(candidate)
			if err != nil {
				model.setError(err.Error())

				return model, nil
			}

			if normalized.Address != model.config.Address || normalized.Namespace != model.config.Namespace {
				model.pendingConfig = &normalized
				model.beginConfirmation(confirmationSwitchVault)

				return model, nil
			}

			return model, model.startBootstrap(normalized, true)
		}
	}

	updatedInput, command := model.setupInputs[model.setupIndex].Update(message)
	model.setupInputs[model.setupIndex] = updatedInput

	return model, command
}

func (model *Model) normalizeSetupConfig(candidate config.Config) (config.Config, error) {
	normalized, err := config.Normalize(candidate)
	if err != nil {
		return config.Config{}, err
	}
	if normalized.Address == "" {
		return config.Config{}, errors.New("address must not be empty")
	}
	if normalized.Address != model.config.Address || normalized.Namespace != model.config.Namespace {
		if model.changingVaultTarget(normalized) {
			if err = vaultSwitchCredentialError(); err != nil {
				return config.Config{}, err
			}
		}
		normalized.Mounts = nil
	}

	return normalized, nil
}

func (model *Model) updateLogin(message tea.Msg) (tea.Model, tea.Cmd) {
	keyMessage, ok := message.(tea.KeyPressMsg)
	if !ok {
		updatedInput, command := model.loginInput.Update(message)
		model.loginInput = updatedInput

		return model, command
	}

	switch shortcutString(keyMessage) {
	case keyQuitInput:
		model.beginConfirmation(confirmationQuit)

		return model, nil
	case keyEscape:
		model.invalidateLoginOpen()
		if model.reauthenticating {
			if model.reauthRequired {
				return model, nil
			}
			model.reauthenticating = false
			returnScreen := model.reauthReturnScreen
			model.screen = returnScreen
			model.reauthReturnScreen = screenSetup
			if returnScreen == screenLogin {
				model.loginInput.Focus()
			} else {
				model.loginInput.Blur()
			}
			model.setStatus("")

			return model, nil
		}
		model.beginSetup(screenLogin)

		return model, nil
	case keyControlO:
		if model.loginOpenCancel != nil {
			return model, nil
		}

		return model, model.startOpenLogin()
	case keyEnter:
		token := model.loginInput.Value()
		if strings.TrimSpace(token) == "" {
			model.setError("Token must not be empty")

			return model, nil
		}
		model.invalidateLoginOpen()
		command := model.startLogin(token)
		model.loginInput.Reset()

		return model, command
	}

	updatedInput, command := model.loginInput.Update(message)
	model.loginInput = updatedInput

	return model, command
}

func (model *Model) updateMount(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		switch shortcutString(keyMessage) {
		case keyQuitInput:
			model.beginConfirmation(confirmationQuit)

			return model, nil
		case keyEscape:
			model.editingMount = ""
			model.mountInput.Blur()
			model.screen = screenBrowser

			return model, nil
		case keyControlO:
			model.beginConfirmation(confirmationLogout)

			return model, nil
		case keyEnter:
			candidate := model.config
			candidate.Mounts = slices.Clone(candidate.Mounts)
			if model.editingMount != "" {
				candidate.Mounts = slices.DeleteFunc(candidate.Mounts, func(mount string) bool {
					return mount == model.editingMount
				})
			}
			candidate.Mounts = append(candidate.Mounts, model.mountInput.Value())
			normalized, err := config.Normalize(candidate)
			if err != nil {
				model.setError(err.Error())

				return model, nil
			}

			return model, model.startSaveMount(normalized)
		}
	}

	updatedInput, command := model.mountInput.Update(message)
	model.mountInput = updatedInput

	return model, command
}

func (model *Model) updatePath(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		switch shortcutString(keyMessage) {
		case keyQuitInput:
			model.beginConfirmation(confirmationQuit)

			return model, nil
		case keyEscape:
			model.pathInput.Blur()
			model.screen = screenBrowser

			return model, nil
		case keyEnter:
			secretPath, err := vaultcli.NormalizeSecretPath(model.pathInput.Value())
			if err != nil {
				model.setError(err.Error())

				return model, nil
			}
			if err = model.beginEditor(secretPath, map[string]any{}, 0); err != nil {
				model.setError(err.Error())
			}

			return model, nil
		}
	}

	updatedInput, command := model.pathInput.Update(message)
	model.pathInput = updatedInput

	return model, command
}

type editorPasteResultMsg struct {
	generation uint64
	content    string
	err        error
}

//nolint:gocognit // Clipboard, paste, and key events share the insertion guards before textarea dispatch.
func (model *Model) updateEditor(message tea.Msg) (tea.Model, tea.Cmd) {
	if result, ok := message.(editorPasteResultMsg); ok {
		if result.generation != model.editorGeneration {
			return model, nil
		}
		if result.err != nil {
			model.setError("Could not read clipboard")
			return model, nil
		}
		message = tea.PasteMsg{Content: result.content}
	}
	if pasted, ok := message.(tea.PasteMsg); ok {
		pasted.Content = escapeEditorText(pasted.Content)
		if !model.editorInputFits(pasted.Content) {
			return model, nil
		}
		message = pasted
	}

	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		switch shortcutString(keyMessage) {
		case "ctrl+v":
			generation := model.editorGeneration
			return model, func() tea.Msg {
				content, err := clipboard.ReadAll()
				return editorPasteResultMsg{generation: generation, content: content, err: err}
			}
		case keyEscape:
			if model.editorCAS > 0 && model.editor.Value() == model.editorOriginal {
				model.discardEditor()

				return model, nil
			}
			model.beginConfirmation(confirmationDiscard)

			return model, nil
		case "ctrl+s":
			if model.editorCAS == unknownCAS {
				model.setError("Reload the secret before saving; the previous write outcome is unknown")

				return model, nil
			}
			data, err := decodeEditor(model.editor.Value())
			if err != nil {
				model.setError(err.Error())

				return model, nil
			}
			model.pendingWrite = &pendingSecretWrite{
				target: secretTarget{
					client: model.client,
					mount:  model.currentMount,
					path:   model.editorPath,
				},
				data:    data,
				version: model.editorCAS,
			}
			model.beginConfirmation(confirmationSave)

			return model, nil
		}
	}

	if key, ok := message.(tea.KeyPressMsg); ok && key.Text != "" {
		key.Text = escapeEditorText(key.Text)
		if !model.editorInputFits(key.Text) {
			return model, nil
		}
		message = key
	}
	updatedEditor, command := model.editor.Update(message)
	model.editor = updatedEditor

	return model, command
}

// Check before Update: textarea truncates oversized insertions and deletes the
// selection first. Its sanitizer expands tabs and treats both CR and LF as lines.
func (model *Model) editorInputFits(text string) bool {
	const tabExpansionBytes = 3 // Bubbles replaces each one-byte tab with four spaces.
	selected := model.editor.SelectedText()
	size := len(model.editor.Value()) - len(selected) + len(text) + tabExpansionBytes*strings.Count(text, "\t")
	lines := model.editor.LineCount() - strings.Count(
		selected,
		"\n",
	) + strings.Count(
		text,
		"\n",
	) + strings.Count(
		text,
		"\r",
	)
	if size > maximumEditorBytes || lines > maximumEditorLines {
		model.setError(
			fmt.Sprintf(
				"Insertion exceeds editor limit (%d bytes or %d lines); nothing inserted",
				maximumEditorBytes,
				maximumEditorLines,
			),
		)
		return false
	}
	model.setStatus("")
	return true
}

func (model *Model) updateConfirmation(message tea.Msg) (tea.Model, tea.Cmd) {
	keyMessage, ok := message.(tea.KeyPressMsg)
	if !ok {
		return model, nil
	}

	switch shortcutString(keyMessage) {
	case keyEnter:
		return model, model.confirm()
	case keyEscape:
		model.cancelConfirmation()

		return model, nil
	default:
		return model, nil
	}
}

func (model *Model) beginConfirmation(action confirmationAction) {
	model.confirmation = action
	model.confirmationReturnScreen = model.screen
	model.screen = screenConfirm
}

func (model *Model) closeConfirmation() confirmationAction {
	action := model.confirmation
	model.confirmation = confirmationNone
	model.screen = model.confirmationReturnScreen

	return action
}

func (model *Model) cancelConfirmation() {
	action := model.closeConfirmation()
	if action == confirmationSave || action == confirmationPaste {
		model.pendingWrite = nil
	}
	if action == confirmationDelete {
		model.pendingDelete = nil
	}
	if action == confirmationPaste {
		model.setStatus("Paste canceled")
	}
	if action == confirmationSwitchVault {
		model.pendingConfig = nil
	}
}

func (model *Model) confirm() tea.Cmd {
	action := model.closeConfirmation()

	switch action {
	case confirmationQuit:
		return tea.Quit
	case confirmationLogout:
		return model.startLogout()
	case confirmationSave, confirmationPaste:
		if model.pendingWrite == nil {
			model.setError("Pending secret write is no longer available")

			return nil
		}
		write := model.pendingWrite
		model.pendingWrite = nil

		return model.startSaveSecret(write.target, write.data, write.version)
	case confirmationDiscard:
		model.discardEditor()

		return nil
	case confirmationDelete:
		if model.pendingDelete == nil {
			model.setError("Pending secret deletion is no longer available")

			return nil
		}
		target := model.pendingDelete
		model.pendingDelete = nil

		return model.startDeleteSecret(*target)
	case confirmationSwitchVault:
		if model.pendingConfig == nil {
			model.setError("Internal error: Vault settings are no longer available")

			return nil
		}
		candidate := *model.pendingConfig
		model.pendingConfig = nil

		return model.startBootstrap(candidate, true)
	case confirmationNone:
		model.setError("Internal error: missing confirmation")

		return nil
	default:
		model.setError("Internal error: unknown confirmation")

		return nil
	}
}

func (model *Model) discardEditor() {
	model.editor.Blur()
	model.screen = screenBrowser
	model.setStatus("Edit discarded")
}

func (model *Model) updateHelp(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		switch shortcutString(keyMessage) {
		case "q":
			model.beginConfirmation(confirmationQuit)

			return model, nil
		case keyEscape, "?", "shift+/":
			model.screen = model.returnScreen
		}
	}

	return model, nil
}

func (model *Model) updateError(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		keyMessage = shortcutKey(keyMessage)
		switch keyMessage.String() {
		case keyEscape:
			model.closeError()

			return model, nil
		case keyEnter:
			if model.errorCanReauth {
				model.beginReauthentication()
			}

			return model, nil
		case "m":
			if model.errorCanEditMount {
				mount := model.errorMount
				model.clearError()
				model.beginMountEdit(mount)
			}

			return model, nil
		}
		message = keyMessage
	}

	updatedView, command := model.errorView.Update(message)
	model.errorView = updatedView

	return model, command
}

func (model *Model) updateBrowser(message tea.Msg) (tea.Model, tea.Cmd) {
	if wheelMessage, ok := message.(tea.MouseWheelMsg); ok {
		model.scrollPaneUnderPointer(wheelMessage)

		return model, nil
	}
	if model.focus == paneBrowser && model.browser.SettingFilter() {
		if keyMessage, ok := message.(tea.KeyPressMsg); ok &&
			shortcutString(keyMessage) == keyEnter && model.browserFilterPending {
			return model, nil
		}

		return model, model.updateBrowserList(message)
	}
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		keyMessage = shortcutKey(keyMessage)
		message = keyMessage
		if model.focus == paneBrowser && keyMessage.String() == keyEscape && model.browser.IsFiltered() {
			return model, model.updateBrowserList(message)
		}
		if command, handled := model.handleBrowserKey(keyMessage.String()); handled {
			return model, command
		}
	}

	if model.focus == paneBrowser {
		return model, model.updateBrowserList(message)
	}

	updatedTable, command := model.secretTable.Update(message)
	model.secretTable = updatedTable

	return model, command
}

func (model *Model) updateBrowserList(message tea.Msg) tea.Cmd {
	previousFilter := model.browser.FilterValue()
	previousState := model.browser.FilterState()
	updatedList, command := model.browser.Update(message)
	model.browser = updatedList

	switch {
	case previousState != list.Unfiltered && model.browser.FilterState() == list.Unfiltered:
		model.browserFilterGeneration++
		model.browserFilterPending = false
	case previousFilter != model.browser.FilterValue():
		model.browserFilterGeneration++
		model.browserFilterPending = true
	}

	return tagBrowserFilterCommand(command, model.browserFilterGeneration)
}

func tagBrowserFilterCommand(command tea.Cmd, generation uint64) tea.Cmd {
	if command == nil {
		return nil
	}

	return func() tea.Msg {
		switch message := command().(type) {
		case list.FilterMatchesMsg:
			return browserFilterResultMsg{generation: generation, matches: message}
		case tea.BatchMsg:
			commands := make(tea.BatchMsg, len(message))
			for index, child := range message {
				commands[index] = tagBrowserFilterCommand(child, generation)
			}

			return commands
		default:
			return message
		}
	}
}

func (model *Model) replaceBrowserItems(items []list.Item) {
	model.browserFilterGeneration++
	model.browserFilterPending = false
	model.browser.ResetFilter()
	model.browser.SetItems(items)
	model.browser.ResetSelected()
}

func (model *Model) applyBrowserFilter(message list.FilterMatchesMsg) tea.Cmd {
	updatedList, command := model.browser.Update(message)
	model.browser = updatedList
	model.browser.SetSize(model.browser.Width(), model.browser.Height())
	model.browser.ResetSelected()

	return command
}

func (model *Model) scrollPaneUnderPointer(message tea.MouseWheelMsg) {
	bodyTop := 1
	bodyBottom := bodyTop + max(0, model.height-tableChromeHeight)
	if message.X < 0 || message.X >= model.width || message.Y < bodyTop || message.Y >= bodyBottom {
		return
	}

	var direction int
	switch message.Button {
	case tea.MouseWheelUp:
		direction = -1
	case tea.MouseWheelDown:
		direction = 1
	default:
		return
	}

	leftWidth, _ := paneWidths(model.width)
	if message.X < leftWidth {
		if direction < 0 {
			model.browser.CursorUp()
		} else {
			model.browser.CursorDown()
		}

		return
	}

	model.secretTable.move(direction)
}

func (model *Model) handleBrowserKey(key string) (tea.Cmd, bool) {
	switch key {
	case "q":
		model.beginConfirmation(confirmationQuit)
	case "?", "shift+/":
		model.returnScreen = screenBrowser
		model.screen = screenHelp
	case "tab", "shift+tab":
		model.togglePaneFocus()
	case "s":
		model.beginSetup(screenBrowser)
	case "o":
		model.beginConfirmation(confirmationLogout)
	case "r":
		return model.reloadBrowser(), true
	case "e":
		model.editSelectedSecret()
	case "d":
		if model.secret != nil && !model.secret.Deleted {
			model.pendingDelete = &secretTarget{
				client: model.client,
				mount:  model.currentMount,
				path:   model.selectedPath,
			}
			model.beginConfirmation(confirmationDelete)
		}
	case "p":
		if err := model.beginPaste(); err != nil {
			model.setError(err.Error())
		}
	case "n":
		if model.currentMount != "" {
			model.beginCreatePath()
		}
	default:
		return model.handleFocusedPaneKey(key)
	}

	return nil, true
}

func (model *Model) handleFocusedPaneKey(key string) (tea.Cmd, bool) {
	if model.focus == paneBrowser {
		switch key {
		case keyEnter, "l":
			return model.openSelectedItem(), true
		case keyEscape, "backspace", "h":
			return model.goBack(), true
		default:
			return nil, false
		}
	}

	switch key {
	case keyEnter, "space":
		model.toggleSelectedSecretValue()

		return nil, true
	case "a":
		model.toggleAllSecretValues()

		return nil, true
	case "c":
		value, exists := model.copySelectedEntry()
		if !exists {
			return nil, true
		}

		model.setStatus(fmt.Sprintf("Copied %q; value sent to clipboard", model.copied.key))

		return tea.SetClipboard(value), true
	case keyEscape, "h":
		model.focus = paneBrowser
		model.secretTable.Blur()

		return nil, true
	default:
		return nil, false
	}
}

func (model *Model) editSelectedSecret() {
	if model.secret == nil {
		return
	}

	if err := model.beginEditor(model.selectedPath, model.secret.Data, model.secret.Version); err != nil {
		model.setError(err.Error())
	}
}

func (model *Model) beginSetup(returnScreen screen) {
	model.returnScreen = returnScreen
	model.setupEditing = true
	model.screen = screenSetup
	model.setupInputs[0].SetValue(model.config.Address)
	model.setupInputs[1].SetValue(model.config.Namespace)
	model.focusSetup(0)
}

func (model *Model) beginReauthentication() {
	if !model.reauthenticating {
		model.reauthReturnScreen = model.errorReturnScreen
	}
	model.clearError()
	model.reauthenticating = true
	model.screen = screenLogin
	model.loginInput.Reset()
	model.loginInput.Focus()
	model.setStatus("")
}

func (model *Model) closeError() {
	model.screen = model.errorReturnScreen
	model.clearError()
}

func (model *Model) clearError() {
	model.errorTitle = ""
	model.errorCanReauth = false
	model.errorCanEditMount = false
	model.errorMount = ""
	model.errorView.SetContent("")
}

func (model *Model) beginCreatePath() {
	model.screen = screenPath
	value := model.currentPath
	if model.currentPath != "" {
		value += "/"
	}
	model.pathInput.SetValue(value)
	model.pathInput.CursorEnd()
	model.pathInput.Focus()
	model.setStatus("")
}

func (model *Model) togglePaneFocus() {
	if model.focus == paneBrowser && model.secret != nil && len(model.secretKeys) > 0 {
		model.focus = paneSecret
		model.secretTable.Focus()

		return
	}

	model.focus = paneBrowser
	model.secretTable.Blur()
}

func (model *Model) openSelectedItem() tea.Cmd {
	selected, ok := model.browser.SelectedItem().(browserItem)
	if !ok {
		return nil
	}

	switch selected.kind {
	case itemMount:
		return model.startLoadEntries(selected.path, "")
	case itemFolder:
		return model.startLoadEntries(model.currentMount, selected.path)
	case itemSecret:
		return model.startGetSecret(selected.path)
	default:
		model.setError("Internal error: unknown item type")

		return nil
	}
}

func (model *Model) goBack() tea.Cmd {
	if model.currentMount == "" {
		return nil
	}
	if model.currentPath == "" {
		model.showMounts()

		return nil
	}

	parent := path.Dir(model.currentPath)
	if parent == "." {
		parent = ""
	}

	return model.startLoadEntries(model.currentMount, parent)
}

func (model *Model) reloadBrowser() tea.Cmd {
	if model.currentMount == "" {
		return model.startLoadMounts()
	}

	return model.startLoadEntries(model.currentMount, model.currentPath)
}

func (model *Model) resize(width, height int) {
	model.width = width
	model.height = height
	contentHeight := max(1, height-screenChromeHeight)
	leftWidth, rightWidth := paneWidths(width)
	model.browser.SetSize(max(1, leftWidth-panelFrameWidth), max(1, contentHeight-panelFrameHeight))

	tableWidth := max(1, rightWidth-panelFrameWidth)
	keyWidth := max(minimumKeyWidth, tableWidth/keyColumnRatio)
	model.secretTable.SetWidth(tableWidth)
	model.secretTable.SetHeight(max(minimumTableHeight, contentHeight-tableChromeHeight))
	model.secretTable.SetColumns([]table.Column{
		{Title: "Key", Width: keyWidth},
		{Title: "Value", Width: max(minimumValueWidth, tableWidth-keyWidth-panelFrameWidth)},
	})

	inputWidth := max(minimumInputWidth, min(maximumInputWidth, width-inputChromeWidth))
	for index := range model.setupInputs {
		model.setupInputs[index].SetWidth(inputWidth)
	}
	model.loginInput.SetWidth(inputWidth)
	model.mountInput.SetWidth(inputWidth)
	model.pathInput.SetWidth(inputWidth)
	model.editor.SetWidth(max(minimumEditorWidth, width-editorChromeWidth))
	model.editor.SetHeight(max(minimumEditorHeight, height-editorChromeHeight))
	model.resizeErrorView()
	model.errorView.SetXOffset(model.errorView.XOffset())
	if model.errorView.PastBottom() {
		model.errorView.GotoBottom()
	}
}

func (model *Model) resizeErrorView() {
	model.errorView.SetWidth(model.errorModalContentWidth())
	model.errorView.SetHeight(errorViewportHeight)
}
