package tui

import (
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

type theme struct {
	defaultText lipgloss.Style
	muted       lipgloss.Style
	emphasis    lipgloss.Style
	accent      lipgloss.Style
	errorText   lipgloss.Style
	warning     lipgloss.Style
	success     lipgloss.Style
	panel       lipgloss.Style
	focusPanel  lipgloss.Style
	modal       lipgloss.Style
}

func newTheme(dark bool) theme {
	foreground := lipgloss.Color("0")
	emphasisColor := lipgloss.Color("0")
	accentColor := lipgloss.Color("4")
	if dark {
		foreground = lipgloss.Color("7")
		emphasisColor = lipgloss.Color("15")
		accentColor = lipgloss.Color("6")
	}
	defaultText := lipgloss.NewStyle().Foreground(foreground)
	muted := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("8"))
	emphasis := lipgloss.NewStyle().Bold(true).Foreground(emphasisColor)
	accent := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	errorText := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	warning := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	success := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	border := lipgloss.NormalBorder()
	panel := lipgloss.NewStyle().Border(border).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	focusPanel := panel.BorderForeground(lipgloss.Color("6"))

	return theme{
		defaultText: defaultText,
		muted:       muted,
		emphasis:    emphasis,
		accent:      accent,
		errorText:   errorText,
		warning:     warning,
		success:     success,
		panel:       panel,
		focusPanel:  focusPanel,
		modal:       lipgloss.NewStyle().Border(border).Padding(1, modalPadding),
	}
}

func (model *Model) applyTheme() {
	model.theme = newTheme(model.dark)

	listStyles := list.DefaultStyles(model.dark)
	listStyles.Title = model.theme.emphasis.Padding(0, 1)
	listStyles.StatusBar = model.theme.muted.PaddingLeft(1)
	listStyles.HelpStyle = model.theme.muted.PaddingLeft(1)
	listStyles.NoItems = model.theme.muted
	model.browser.Styles = listStyles

	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	delegate.Styles.NormalTitle = model.theme.defaultText.PaddingLeft(1)
	delegate.Styles.NormalDesc = model.theme.muted.PaddingLeft(1)
	delegate.Styles.SelectedTitle = model.theme.accent.Reverse(true).PaddingLeft(1)
	delegate.Styles.SelectedDesc = model.theme.muted.Reverse(true).PaddingLeft(1)
	delegate.Styles.DimmedTitle = model.theme.muted.PaddingLeft(1)
	delegate.Styles.DimmedDesc = model.theme.muted.PaddingLeft(1)
	delegate.Styles.FilterMatch = lipgloss.NewStyle().Underline(true)
	model.browser.SetDelegate(delegate)

	tableStyles := table.DefaultStyles()
	tableStyles.Header = model.theme.emphasis.Padding(0, 1)
	tableStyles.Cell = model.theme.defaultText.Padding(0, 1)
	tableStyles.Selected = model.theme.accent.Reverse(true)
	model.secretTable.SetStyles(tableStyles)

	inputStyles := textinput.DefaultStyles(model.dark)
	editorStyles := textarea.DefaultStyles(model.dark)
	for index := range model.setupInputs {
		model.setupInputs[index].SetStyles(inputStyles)
	}
	model.loginInput.SetStyles(inputStyles)
	model.mountInput.SetStyles(inputStyles)
	model.pathInput.SetStyles(inputStyles)
	model.editor.SetStyles(editorStyles)
	model.errorView.Style = model.theme.defaultText
	model.spinner.Style = model.theme.warning
}
