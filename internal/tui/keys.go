package tui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

func shortcutKey(message tea.KeyPressMsg) tea.KeyPressMsg {
	key := tea.Key(message)
	if key.BaseCode == 0 {
		return message
	}

	key.Text = ""
	if key.Mod.Contains(tea.ModShift) &&
		key.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModHyper|tea.ModSuper) == 0 &&
		unicode.IsLetter(key.BaseCode) {
		key.Mod &^= tea.ModShift
		key.Text = string(unicode.ToUpper(key.BaseCode))
	}

	return tea.KeyPressMsg(key)
}

func shortcutString(message tea.KeyPressMsg) string {
	return shortcutKey(message).String()
}
