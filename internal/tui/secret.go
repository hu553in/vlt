package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/hu553in/vlt/internal/strictjson"
	"github.com/hu553in/vlt/internal/vaultcli"
)

const (
	maskedValue  = "********"
	editorIndent = "  "
)

func (model *Model) setSecret(secret vaultcli.Secret) {
	model.secret = &secret
	model.secretKeys = slices.Sorted(maps.Keys(secret.Data))
	model.revealed = make([]bool, len(model.secretKeys))
	model.revealedCount = 0
	model.updateSecretRows()
	if len(model.secretKeys) > 0 {
		model.secretTable.SetCursor(0)

		return
	}
	model.focus = paneBrowser
	model.secretTable.Blur()
}

func (model *Model) clearSecret() {
	model.secret = nil
	model.selectedPath = ""
	model.secretKeys = nil
	model.revealed = nil
	model.revealedCount = 0
	model.secretTable.SetRows(nil)
	model.secretTable.Blur()
	model.focus = paneBrowser
}

func (model *Model) updateSecretRows() {
	rows := make([]table.Row, len(model.secretKeys))
	for index, key := range model.secretKeys {
		rows[index] = model.secretRow(index, key)
	}
	model.secretTable.SetRows(rows)
}

func (model *Model) secretRow(index int, key string) table.Row {
	value := maskedValue
	if model.revealed[index] {
		value = displayText(valueForClipboard(model.secret.Data[key]))
	}

	return table.Row{displayText(key), value}
}

func (model *Model) toggleSelectedSecretValue() {
	index := model.secretTable.Cursor()
	if index < 0 || index >= len(model.secretKeys) {
		return
	}
	key := model.secretKeys[index]
	model.revealed[index] = !model.revealed[index]
	if model.revealed[index] {
		model.revealedCount++
	} else {
		model.revealedCount--
	}
	model.secretTable.SetRow(index, model.secretRow(index, key))
}

func (model *Model) toggleAllSecretValues() {
	if len(model.secretKeys) == 0 {
		return
	}

	reveal := !model.allSecretValuesRevealed()
	for index := range model.revealed {
		model.revealed[index] = reveal
	}
	model.revealedCount = 0
	if reveal {
		model.revealedCount = len(model.secretKeys)
	}
	cursor := model.secretTable.Cursor()
	model.updateSecretRows()
	model.secretTable.SetCursor(cursor)
	if reveal {
		model.setStatus("Revealed all values")

		return
	}
	model.setStatus("Masked all values")
}

func (model *Model) allSecretValuesRevealed() bool {
	return len(model.secretKeys) > 0 && model.revealedCount == len(model.secretKeys)
}

func (model *Model) selectedSecretEntry() (string, any, bool) {
	index := model.secretTable.Cursor()
	if model.secret == nil || index < 0 || index >= len(model.secretKeys) {
		return "", nil, false
	}

	key := model.secretKeys[index]
	value, exists := model.secret.Data[key]

	return key, value, exists
}

func (model *Model) copySelectedEntry() (string, bool) {
	key, value, exists := model.selectedSecretEntry()
	if !exists {
		return "", false
	}
	model.copied = &copiedEntry{
		key:         key,
		value:       value,
		sourceMount: model.currentMount,
		sourcePath:  model.selectedPath,
	}

	return valueForClipboard(value), true
}

func (model *Model) beginPaste() error {
	if model.copied == nil {
		return errors.New("copy a value first")
	}
	if model.secret == nil {
		return errors.New("load a destination secret first")
	}

	existing, replace := model.secret.Data[model.copied.key]
	if replace && reflect.DeepEqual(existing, model.copied.value) {
		model.setStatus(fmt.Sprintf("%q already has the copied value", model.copied.key))

		return nil
	}

	data := maps.Clone(model.secret.Data)
	data[model.copied.key] = model.copied.value
	if _, err := vaultcli.EncodeSecret(data); err != nil {
		return err
	}

	copied := *model.copied
	model.pendingWrite = &pendingSecretWrite{
		target: secretTarget{
			client: model.client,
			mount:  model.currentMount,
			path:   model.selectedPath,
		},
		data:     data,
		version:  model.secret.Version,
		copied:   &copied,
		replaces: replace,
	}
	model.beginConfirmation(confirmationPaste)
	model.setStatus("")

	return nil
}

func (model *Model) clearCopiedEntry() {
	model.copied = nil
	if model.pendingWrite != nil && model.pendingWrite.copied != nil {
		model.pendingWrite = nil
	}
}

func (model *Model) beginEditor(secretPath string, secret map[string]any, version int) error {
	compact, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("format secret: %w", err)
	}
	if len(compact) > vaultcli.MaximumSecretBytes {
		return fmt.Errorf("secret exceeds %d bytes", vaultcli.MaximumSecretBytes)
	}
	escaped := []byte(escapeEditorText(string(compact)))
	if len(escaped) > maximumEditorBytes {
		return fmt.Errorf("escaped secret exceeds %d bytes", maximumEditorBytes)
	}
	editorJSON := escaped
	if indentedJSONFits(escaped, maximumEditorBytes) {
		var formatted bytes.Buffer
		if err = json.Indent(&formatted, escaped, "", editorIndent); err != nil {
			return fmt.Errorf("format secret: %w", err)
		}
		if formatted.Len() <= maximumEditorBytes && bytes.Count(formatted.Bytes(), []byte("\n")) < maximumEditorLines {
			editorJSON = formatted.Bytes()
		}
	}
	editorValue := string(editorJSON)

	model.editor.SetValue(editorValue)
	if model.editor.Value() != editorValue {
		model.editor.Reset()
		return errors.New("secret could not be loaded completely into the editor")
	}

	model.editorGeneration++
	model.editorPath = secretPath
	model.editorCAS = version
	model.editorOriginal = editorValue
	model.editor.Focus()
	model.screen = screenEditor
	model.setStatus("")

	return nil
}

// indentedJSONFits avoids materializing indentation that could expand a deeply nested secret
// far beyond the editor limit before [json.Indent] returns.
func indentedJSONFits(data []byte, limit int) bool {
	if len(data) > limit {
		return false
	}
	size := len(data)
	depth := 0
	inString := false
	escaped := false
	for _, character := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case character == '\\':
				escaped = true
			case character == '"':
				inString = false
			}
			continue
		}

		added := 0
		switch character {
		case '"':
			inString = true
		case '{', '[':
			depth++
			added = 1 + len(editorIndent)*depth
		case ',':
			added = 1 + len(editorIndent)*depth
		case ':':
			added = 1
		case '}', ']':
			depth--
			added = 1 + len(editorIndent)*max(0, depth)
		}
		if added > limit-size {
			return false
		}
		size += added
	}

	return true
}

func decodeEditor(value string) (map[string]any, error) {
	if len(value) > maximumEditorBytes {
		return nil, fmt.Errorf("secret exceeds %d bytes", maximumEditorBytes)
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	decoded, err := strictjson.DecodeValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("invalid JSON: multiple JSON values")
		}

		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	data, ok := decoded.(map[string]any)
	if !ok || data == nil {
		return nil, errors.New("secret must be a JSON object")
	}
	if _, err = vaultcli.EncodeSecret(data); err != nil {
		return nil, err
	}

	return data, nil
}

func valueForClipboard(value any) string {
	if stringValue, ok := value.(string); ok {
		return stringValue
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "<unsupported value>"
	}

	return string(encoded)
}

func displayText(value string) string {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, character := range value {
		if !unicode.IsControl(character) && !unicode.Is(unicode.Bidi_Control, character) {
			escaped.WriteRune(character)

			continue
		}
		quoted := strconv.QuoteRune(character)
		escaped.WriteString(quoted[1 : len(quoted)-1])
	}

	return escaped.String()
}

func displayMultilineText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		parts := strings.Split(lines[index], "\t")
		for partIndex := range parts {
			parts[partIndex] = displayText(parts[partIndex])
		}
		lines[index] = strings.Join(parts, "\t")
	}

	return lipgloss.NewStyle().Render(strings.Join(lines, "\n"))
}

// textarea removes RuneError and controls even when they are valid JSON string data.
// Escape them and bidi controls before insertion; leave JSON whitespace intact.
func escapeEditorText(value string) string {
	needsEscape := func(character rune) bool {
		return character == utf8.RuneError || unicode.Is(unicode.Bidi_Control, character) ||
			(character >= 0x20 && unicode.IsControl(character))
	}
	if strings.IndexFunc(value, needsEscape) < 0 {
		return value
	}
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, character := range value {
		if needsEscape(character) {
			fmt.Fprintf(&escaped, "\\u%04x", character)
		} else {
			escaped.WriteRune(character)
		}
	}
	return escaped.String()
}

func (model *Model) setStatus(message string) {
	model.status = message
	model.statusError = false
}

func (model *Model) setError(message string) {
	model.status = message
	model.statusError = true
}
