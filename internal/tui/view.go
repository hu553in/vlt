package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (model *Model) View() tea.View {
	content := model.render()
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "vlt"
	if model.screen == screenBrowser && !model.terminalTooSmall() {
		view.MouseMode = tea.MouseModeCellMotion
	}
	view.KeyboardEnhancements.ReportAlternateKeys = true
	view.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	view.KeyboardEnhancements.ReportAssociatedText = true

	return view
}

func (model *Model) render() string {
	if model.width == 0 || model.height == 0 {
		return "Starting vlt..."
	}
	if model.terminalTooSmall() {
		hint := "[q] quit"
		message := model.theme.emphasis.Render("Terminal is too small. Resize to at least 90×24.")
		if model.screen == screenConfirm {
			title := ansi.Truncate(displayText(model.confirmationTitle()), max(1, model.width), "...")
			message = model.theme.emphasis.Render(title)
			hint = "[Enter] " + model.confirmationVerb() + "  [Esc] cancel"
		} else if model.loading {
			hint = model.renderOperationStatus()
		}
		message += "\n" + model.theme.muted.Render(hint)
		message = lipgloss.NewStyle().MaxWidth(model.width).MaxHeight(model.height).Render(message)

		return lipgloss.Place(model.width, model.height, lipgloss.Center, lipgloss.Center, message)
	}

	switch model.screen {
	case screenSetup:
		return model.renderSetup()
	case screenLogin:
		return model.renderLogin()
	case screenBrowser:
		return model.renderBrowser()
	case screenMount:
		return model.renderMount()
	case screenPath:
		return model.renderPath()
	case screenEditor:
		return model.renderEditor()
	case screenConfirm:
		return model.renderConfirmation()
	case screenHelp:
		return model.renderHelp()
	case screenError:
		return model.renderError()
	default:
		return lipgloss.Place(model.width, model.height, lipgloss.Center, lipgloss.Center, "Unknown screen")
	}
}

func (model *Model) renderSetup() string {
	title := "Connect to Vault"
	if model.setupEditing {
		title = "Vault settings"
	}

	body := model.theme.emphasis.Render(title) + "\n\n" +
		model.setupInputs[0].View() + "\n" +
		model.setupInputs[1].View() + "\n\n" +
		model.theme.muted.Render(model.modalPathLine("Config: ", model.store.Path())) + "\n" +
		model.theme.muted.Render("[Tab] field  [Enter] continue")
	if model.setupEditing {
		body += model.theme.muted.Render("  [Esc] cancel")
	} else {
		body += model.theme.muted.Render("  [Ctrl+D] quit")
	}

	return model.centeredModal(body)
}

func (model *Model) renderLogin() string {
	title := "Authenticate"
	escapeAction := "settings"
	if model.reauthenticating {
		title = "Authenticate again"
		escapeAction = cancelAction
	}
	shortcuts := "[Ctrl+O] open Vault UI  [Enter] login"
	if !model.reauthRequired {
		shortcuts += "  [Esc] " + escapeAction
	}
	shortcuts += "  [Ctrl+D] quit"
	body := model.theme.emphasis.Render(title) + "\n\n" +
		model.theme.defaultText.Render(model.modalLine(model.config.Address)) + "\n\n" +
		model.loginInput.View() + "\n\n" +
		model.theme.muted.Render("Paste a Vault token. It is stored by the Vault CLI token helper.") + "\n" +
		model.theme.muted.Render(shortcuts)

	return model.centeredModal(body)
}

func (model *Model) renderMount() string {
	title := "Add KV v2 mount"
	detail := "Enter a KV v2 mount you can access."
	escapeAction := cancelAction
	if model.editingMount != "" {
		title = "Correct KV v2 mount"
		detail = "The configured mount could not be opened. Correct it or cancel."
		escapeAction = cancelAction
	}
	body := model.theme.emphasis.Render(title) + "\n\n" +
		model.theme.defaultText.Render(detail) + "\n\n" +
		model.mountInput.View() + "\n\n" +
		model.theme.muted.Render(
			"[Enter] save  [Ctrl+O] log out  [Esc] "+escapeAction+"  [Ctrl+D] quit",
		)

	return model.centeredModal(body)
}

func (model *Model) renderPath() string {
	body := model.theme.emphasis.Render("Create secret") + "\n\n" +
		model.theme.muted.Render(model.modalPathLine("", model.currentMount+":/"+model.currentPath)) + "\n\n" +
		model.pathInput.View() + "\n\n" +
		model.theme.muted.Render("[Enter] edit JSON  [Esc] cancel  [Ctrl+D] quit")

	return model.centeredModal(body)
}

func (model *Model) renderBrowser() string {
	header := model.renderHeader()
	bodyHeight := model.height - tableChromeHeight
	leftWidth, rightWidth := paneWidths(model.width)

	leftStyle := model.theme.panel
	if model.focus == paneBrowser {
		leftStyle = model.theme.focusPanel
	}
	leftStyle = leftStyle.
		Width(max(1, leftWidth)).
		Height(max(1, bodyHeight))

	rightStyle := model.theme.panel
	if model.focus == paneSecret {
		rightStyle = model.theme.focusPanel
	}
	rightStyle = rightStyle.
		Width(max(1, rightWidth)).
		Height(max(1, bodyHeight))

	leftPanel := leftStyle.Render(model.browser.View())
	rightPanel := rightStyle.Render(model.renderSecretPanel())
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)

	return header + "\n" + body + "\n" + model.renderFooter()
}

func paneWidths(width int) (int, int) {
	left := max(minimumPaneWidth, width*browserWidthRatio/percentageBase)

	return left, max(minimumPaneWidth, width-left)
}

func (model *Model) renderHeader() string {
	namespace := "root namespace"
	if model.config.Namespace != "" {
		namespace = "namespace " + model.config.Namespace
	}
	auth := model.auth.DisplayName
	if auth == "" {
		auth = "authenticated"
	}
	if model.auth.FromEnv {
		auth += " (VAULT_TOKEN)"
	}

	header := displayText(fmt.Sprintf(" vlt | %s | %s | %s", model.config.Address, namespace, auth))

	return model.theme.emphasis.Render(ansi.Truncate(header, model.width, "..."))
}

func (model *Model) renderSecretPanel() string {
	contentHeight := max(1, model.height-screenChromeHeight)
	if model.secret == nil {
		return lipgloss.Place(
			max(1, model.secretTable.Width()),
			contentHeight,
			lipgloss.Center,
			lipgloss.Center,
			model.theme.muted.Render("Select a secret and press Enter"),
		)
	}
	if model.secret.Deleted {
		version := fmt.Sprintf("version %d", model.secret.Version)
		return lipgloss.Place(
			max(1, model.secretTable.Width()),
			contentHeight,
			lipgloss.Center,
			lipgloss.Center,
			model.theme.warning.Render("Latest "+version+" is deleted")+"\n"+
				model.theme.muted.Render("Press e to write the next version"),
		)
	}

	version := fmt.Sprintf("  version %d", model.secret.Version)
	pathWidth := max(1, model.secretTable.Width()-ansi.StringWidth(version))
	title := model.theme.emphasis.Render(truncateLeft(displayText(model.selectedPath), pathWidth)) +
		model.theme.muted.Render(version)

	return title + "\n\n" + model.secretTable.View()
}

func (model *Model) renderFooter() string {
	shortcuts := model.browserShortcuts()
	if model.focus == paneSecret {
		shortcuts = model.secretShortcuts()
	}

	status := displayText(model.status)
	statusStyle := model.theme.success
	if model.statusError {
		statusStyle = model.theme.errorText
	}
	if model.loading {
		status = model.renderOperationStatus()
		statusStyle = model.theme.warning
	}
	status = strings.Join(strings.Fields(status), " ")

	firstLine := model.theme.muted.Render(ansi.Truncate(shortcuts, model.width, "..."))
	secondLine := statusStyle.Render(ansi.Truncate(status, model.width, "..."))

	return firstLine + "\n" + secondLine
}

func (model *Model) browserShortcuts() string {
	switch {
	case model.currentMount == "":
		return "[q] quit  [/] search  [r] refresh  [o] log out  [?] help  [Enter] open"
	case model.secret == nil:
		return "[Enter] open  [Esc] back  [/] search  [n] new  [r] refresh  [o] log out  [?] help"
	default:
		shortcuts := "[Enter] open  [Esc] back  [/] search  [n] new  [e] edit"
		if model.copied != nil {
			shortcuts += "  [p] paste"
		}

		return shortcuts + "  [o] log out  [?] help"
	}
}

func (model *Model) secretShortcuts() string {
	allAction := "reveal all"
	if model.allSecretValuesRevealed() {
		allAction = "mask all"
	}
	shortcuts := "[Tab] focus  [Enter] reveal  [a] " + allAction + "  [c] copy"
	if model.copied != nil {
		shortcuts += "  [p] paste"
	}

	return shortcuts + "  [e] edit  [d] delete"
}

func (model *Model) renderEditor() string {
	cas := fmt.Sprintf("CAS %d", model.editorCAS)
	if model.editorCAS == unknownCAS {
		cas = "CAS unknown — reload required"
	}
	title := fmt.Sprintf("Edit %s (%s)", displayText(model.editorPath), cas)
	header := model.theme.emphasis.Render(ansi.Truncate(title, model.width, "..."))
	footer := model.theme.muted.Render("[Ctrl+S] validate and save  [Esc] discard")
	if model.loading {
		footer = model.theme.warning.Render(model.renderOperationStatus())
	} else if model.status != "" {
		statusStyle := model.theme.success
		if model.statusError {
			statusStyle = model.theme.errorText
		}
		footer += "\n" + statusStyle.Render(ansi.Truncate(displayText(model.status), model.width, "..."))
	}

	return header + "\n\n" + model.editor.View() + "\n" + footer
}

func (model *Model) renderConfirmation() string {
	var body string
	switch model.confirmation {
	case confirmationQuit:
		body = model.theme.emphasis.Render(model.confirmationTitle()) + "\n\n" +
			model.theme.defaultText.Render("The current vlt session will close.") + "\n\n" +
			model.theme.emphasis.Render("[Enter] quit  [Esc] cancel")
	case confirmationLogout:
		detail := "This revokes the current token.\nThe Vault CLI token helper entry will also be removed."
		if model.auth.FromEnv {
			detail = "This revokes the current token.\nVAULT_TOKEN cannot be cleared by vlt."
		}
		body = model.theme.errorText.Render(model.confirmationTitle()) + "\n\n" +
			model.theme.defaultText.Render(detail) + "\n\n" +
			model.theme.emphasis.Render("[Enter] log out  [Esc] cancel")
	case confirmationSave:
		version := model.editorCAS
		if model.pendingWrite != nil {
			version = model.pendingWrite.version
		}
		body = model.theme.emphasis.Render(model.modalLine(model.confirmationTitle())) + "\n\n" +
			model.theme.muted.Render(fmt.Sprintf("A new version will be written with CAS %d.", version)) +
			"\n\n" + model.theme.emphasis.Render("[Enter] save  [Esc] cancel")
	case confirmationDiscard:
		body = model.theme.errorText.Render(model.confirmationTitle()) + "\n\n" +
			model.theme.defaultText.Render(model.modalPathLine("", model.currentMount+":/"+model.editorPath)) +
			"\n\n" + model.theme.emphasis.Render("[Enter] discard  [Esc] keep editing")
	case confirmationDelete:
		target := secretTarget{mount: model.currentMount, path: model.selectedPath}
		if model.pendingDelete != nil {
			target = *model.pendingDelete
		}
		body = model.theme.errorText.Render(model.confirmationTitle()) + "\n\n" +
			model.theme.defaultText.Render(model.modalPathLine("", target.mount+":/"+target.path)) +
			"\n\n" + model.theme.muted.Render(
			"This soft-deletes the latest version at execution time and is recoverable.",
		) + "\n\n" + model.theme.emphasis.Render("[Enter] delete  [Esc] cancel")
	case confirmationPaste:
		body = model.renderPasteConfirmation()
	case confirmationSwitchVault:
		target := ""
		detail := "Existing Vault CLI credentials may be sent to this target."
		clearLoaded := ""
		if model.pendingConfig != nil {
			target = model.pendingConfig.Address
			if model.pendingConfig.Namespace != "" {
				target += " | namespace " + model.pendingConfig.Namespace
			}
		}
		if model.config.Address != "" {
			detail = "The current local Vault CLI token will be cleared but not revoked."
			clearLoaded = "\n" + model.theme.muted.Render("All loaded Vault data will be cleared.")
		}
		body = model.theme.errorText.Render(model.confirmationTitle()) + "\n\n" +
			model.theme.defaultText.Render(model.modalLine(target)) + "\n\n" +
			model.theme.muted.Render(detail) + clearLoaded + "\n\n" +
			model.theme.emphasis.Render("[Enter] continue  [Esc] cancel")
	case confirmationNone:
		body = model.theme.errorText.Render("Missing confirmation")
	default:
		body = model.theme.errorText.Render("Unknown confirmation")
	}

	return model.placeModal(body)
}

func (model *Model) renderPasteConfirmation() string {
	write := model.pendingWrite
	if write == nil || write.copied == nil {
		return model.theme.errorText.Render("Paste is no longer available")
	}

	detail := "The key does not exist in the destination."
	if write.replaces {
		detail = "The existing destination value will be replaced."
	}
	source := write.copied.sourceMount + ":/" + write.copied.sourcePath
	destination := write.target.mount + ":/" + write.target.path

	return model.theme.emphasis.Render(model.modalLine(model.confirmationTitle())) + "\n\n" +
		model.theme.muted.Render(model.modalPathLine("From: ", source)) + "\n" +
		model.theme.muted.Render(model.modalPathLine("To:   ", destination)) + "\n\n" +
		model.theme.defaultText.Render(detail) + "\n" +
		model.theme.muted.Render(fmt.Sprintf("A new version will be written with CAS %d.", write.version)) +
		"\n\n" + model.theme.emphasis.Render("[Enter] paste  [Esc] cancel")
}

func (model *Model) confirmationTitle() string {
	switch model.confirmation {
	case confirmationQuit:
		return "Quit vlt?"
	case confirmationLogout:
		return "Log out?"
	case confirmationSave:
		if model.pendingWrite != nil {
			return "Save " + model.pendingWrite.target.mount + ":/" + model.pendingWrite.target.path + "?"
		}

		return "Save " + model.currentMount + ":/" + model.editorPath + "?"
	case confirmationDiscard:
		if model.editorCAS == 0 {
			return "Discard unsaved secret?"
		}

		return "Discard unsaved changes?"
	case confirmationDelete:
		return "Delete latest secret version?"
	case confirmationPaste:
		write := model.pendingWrite
		if write == nil || write.copied == nil {
			return "Paste is no longer available"
		}
		action := "Add"
		if write.replaces {
			action = "Replace"
		}

		return fmt.Sprintf("%s %q?", action, write.copied.key)
	case confirmationSwitchVault:
		if model.config.Address == "" {
			return "Connect to this Vault?"
		}

		return "Switch Vault?"
	case confirmationNone:
		return "Missing confirmation"
	default:
		return "Unknown confirmation"
	}
}

func (model *Model) confirmationVerb() string {
	switch model.confirmation {
	case confirmationQuit:
		return "quit"
	case confirmationLogout:
		return "log out"
	case confirmationSave:
		return "save"
	case confirmationDiscard:
		return "discard"
	case confirmationDelete:
		return "delete"
	case confirmationPaste:
		return "paste"
	case confirmationSwitchVault:
		return "continue"
	case confirmationNone:
		return "confirm"
	default:
		return "confirm"
	}
}

func (model *Model) renderHelp() string {
	body := model.theme.emphasis.Render("Keyboard help") + "\n\n" +
		"Up/Down, j/k   Move selection\n" +
		"Enter, l       Open browser item\n" +
		"Enter, Space   Reveal selected value\n" +
		"Esc / h        Return focus or go back; Esc clears filter\n" +
		"/              Filter current list\n" +
		"Tab            Switch panel focus\n" +
		"Wheel          Scroll pane under pointer\n" +
		"a              Reveal or mask all values\n" +
		"n              Create secret\n" +
		"e              Edit loaded secret\n" +
		"d              Soft-delete loaded secret\n" +
		"c / p          Copy selected entry / paste into loaded secret\n" +
		"r              Refresh\n" +
		"s              Vault settings\n" +
		"o              Log out and return to authentication\n" +
		"q              Quit\n\n" +
		model.theme.muted.Render("[Esc/?] close")

	return model.placeModal(body)
}

func (model *Model) renderError() string {
	footer := "[Esc] close"
	canScrollVertically := !model.errorView.AtTop() || !model.errorView.AtBottom()
	canScrollHorizontally := model.errorView.XOffset() > 0 ||
		model.errorView.HorizontalScrollPercent() < 1
	if canScrollVertically || canScrollHorizontally {
		footer = "[↑/↓/←/→] scroll  " + footer
	}
	if model.errorCanReauth {
		footer = "[Enter] use another token  " + footer
	}
	if model.errorCanEditMount {
		action := "enter mount"
		if model.errorMount != "" {
			action = "correct mount"
		}
		footer = "[m] " + action + "  " + footer
	}

	title := ansi.Truncate(displayText(model.errorTitle), model.errorModalContentWidth(), "...")
	body := model.theme.errorText.Render(title) + "\n\n" +
		model.errorView.View() + "\n\n" + model.theme.muted.Render(footer)
	return model.placeModalWithWidth(body, model.errorModalOuterWidth())
}

func (model *Model) centeredModal(body string) string {
	if model.loading {
		body += "\n\n" + model.theme.warning.Render(model.renderOperationStatus())
	} else if model.status != "" {
		statusStyle := model.theme.success
		if model.statusError {
			statusStyle = model.theme.errorText
		}
		body += "\n\n" + statusStyle.Render(
			model.modalLine(strings.Join(strings.Fields(displayText(model.status)), " ")),
		)
	}

	return model.placeModal(body)
}

func (model *Model) placeModal(body string) string {
	return model.placeModalWithWidth(body, model.modalOuterWidth())
}

func (model *Model) placeModalWithWidth(body string, width int) string {
	modal := model.theme.modal.Width(width).Render(body)

	return lipgloss.Place(model.width, model.height, lipgloss.Center, lipgloss.Center, modal)
}

func (model *Model) modalLine(value string) string {
	return ansi.Truncate(displayText(value), model.modalContentWidth(), "...")
}

func (model *Model) modalPathLine(prefix, value string) string {
	prefix = displayText(prefix)
	valueWidth := max(1, model.modalContentWidth()-ansi.StringWidth(prefix))

	return prefix + truncateLeft(displayText(value), valueWidth)
}

func (model *Model) modalContentWidth() int {
	return max(1, model.modalOuterWidth()-model.theme.modal.GetHorizontalFrameSize())
}

func (model *Model) modalOuterWidth() int {
	return min(modalWidth, max(1, model.width-modalFrameWidth))
}

func (model *Model) errorModalContentWidth() int {
	return max(1, model.errorModalOuterWidth()-model.theme.modal.GetHorizontalFrameSize())
}

func (model *Model) errorModalOuterWidth() int {
	frameWidth := model.theme.modal.GetHorizontalFrameSize()
	contentWidth := modalWidth - frameWidth
	contentWidth = max(contentWidth, ansi.StringWidth(displayText(model.errorTitle)))
	for line := range strings.SplitSeq(model.errorView.GetContent(), "\n") {
		contentWidth = max(contentWidth, ansi.StringWidth(line))
	}

	return min(contentWidth+frameWidth, max(1, model.width-modalFrameWidth))
}

func truncateLeft(value string, width int) string {
	valueWidth := ansi.StringWidth(value)
	if valueWidth <= width {
		return value
	}
	prefix := ansi.Truncate("...", width, "")

	return ansi.TruncateLeft(value, valueWidth-width+ansi.StringWidth(prefix), prefix)
}

func (model *Model) renderOperationStatus() string {
	status := "Working..."
	if model.cancel != nil {
		status = "Loading... [Esc] cancel"
	}
	if model.showSpinner {
		return model.spinner.View() + " " + status
	}

	return status
}
