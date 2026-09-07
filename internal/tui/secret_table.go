package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const halfPageDivisor = 2

// secretTableModel owns its absolute viewport offset because the Bubbles table
// can reverse the viewport after moving to the bottom, partway up, then down.
type secretTableModel struct {
	columns []table.Column
	rows    []table.Row
	styles  table.Styles
	keyMap  table.KeyMap
	cursor  int
	offset  int
	width   int
	height  int
	focused bool
}

func newSecretTable(columns []table.Column, height int) secretTableModel {
	model := secretTableModel{
		columns: columns,
		styles:  table.DefaultStyles(),
		keyMap:  table.DefaultKeyMap(),
	}
	model.SetHeight(height)

	return model
}

func (model *secretTableModel) Update(message tea.Msg) (secretTableModel, tea.Cmd) {
	if !model.focused {
		return *model, nil
	}

	keyMessage, ok := message.(tea.KeyPressMsg)
	if !ok {
		return *model, nil
	}

	switch {
	case key.Matches(keyMessage, model.keyMap.LineUp):
		model.move(-1)
	case key.Matches(keyMessage, model.keyMap.LineDown):
		model.move(1)
	case key.Matches(keyMessage, model.keyMap.PageUp):
		model.move(-model.height)
	case key.Matches(keyMessage, model.keyMap.PageDown):
		model.move(model.height)
	case key.Matches(keyMessage, model.keyMap.HalfPageUp):
		model.move(-model.height / halfPageDivisor)
	case key.Matches(keyMessage, model.keyMap.HalfPageDown):
		model.move(model.height / halfPageDivisor)
	case key.Matches(keyMessage, model.keyMap.GotoTop):
		model.SetCursor(0)
	case key.Matches(keyMessage, model.keyMap.GotoBottom):
		model.SetCursor(len(model.rows) - 1)
	}

	return *model, nil
}

func (model *secretTableModel) move(distance int) {
	model.SetCursor(model.cursor + distance)
}

func (model *secretTableModel) SetCursor(cursor int) {
	if len(model.rows) == 0 {
		model.cursor = 0
		model.offset = 0

		return
	}

	model.cursor = min(max(cursor, 0), len(model.rows)-1)
	model.ensureCursorVisible()
}

func (model *secretTableModel) Cursor() int {
	return model.cursor
}

func (model *secretTableModel) SetRows(rows []table.Row) {
	model.rows = rows
	model.SetCursor(model.cursor)
}

func (model *secretTableModel) SetRow(index int, row table.Row) {
	if index >= 0 && index < len(model.rows) {
		model.rows[index] = row
	}
}

func (model *secretTableModel) SetColumns(columns []table.Column) {
	model.columns = columns
}

func (model *secretTableModel) SetStyles(styles table.Styles) {
	model.styles = styles
}

func (model *secretTableModel) SetWidth(width int) {
	model.width = max(0, width)
}

func (model *secretTableModel) Width() int {
	return model.width
}

func (model *secretTableModel) SetHeight(height int) {
	model.height = max(0, height-1)
	model.ensureCursorVisible()
}

func (model *secretTableModel) Focus() {
	model.focused = true
}

func (model *secretTableModel) Blur() {
	model.focused = false
}

func (model *secretTableModel) ensureCursorVisible() {
	if len(model.rows) == 0 || model.height == 0 {
		model.offset = 0

		return
	}

	if model.cursor < model.offset {
		model.offset = model.cursor
	}
	if model.cursor >= model.offset+model.height {
		model.offset = model.cursor - model.height + 1
	}
	model.offset = min(model.offset, max(0, len(model.rows)-model.height))
}

func (model *secretTableModel) View() string {
	header := model.renderCells(model.columnTitles(), model.styles.Header)
	end := min(len(model.rows), model.offset+model.height)
	rows := make([]string, 0, end-model.offset)
	for index := model.offset; index < end; index++ {
		row := model.renderCells(model.rows[index], model.styles.Cell)
		if index == model.cursor {
			row = model.styles.Selected.Render(row)
		}
		rows = append(rows, row)
	}

	return header + "\n" + lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (model *secretTableModel) columnTitles() table.Row {
	titles := make(table.Row, len(model.columns))
	for index, column := range model.columns {
		titles[index] = column.Title
	}

	return titles
}

func (model *secretTableModel) renderCells(values table.Row, cellStyle lipgloss.Style) string {
	cells := make([]string, 0, min(len(values), len(model.columns)))
	for index, value := range values {
		if index >= len(model.columns) {
			break
		}
		width := model.columns[index].Width
		if width <= 0 {
			continue
		}
		cell := lipgloss.NewStyle().Width(width).MaxWidth(width).Inline(true).
			Render(ansi.Truncate(value, width, "…"))
		cells = append(cells, cellStyle.Render(cell))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}
