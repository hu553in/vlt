package strictjson

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DecodeValue uses the v1 token stream because its ordinary object decode silently overwrites
// duplicate names.
func DecodeValue(decoder *json.Decoder) (any, error) {
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}

	switch delimiter {
	case '{':
		return decodeObject(decoder)
	case '[':
		return decodeArray(decoder)
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func decodeObject(decoder *json.Decoder) (map[string]any, error) {
	object := make(map[string]any)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("object name is not a string")
		}
		if _, exists := object[name]; exists {
			return nil, fmt.Errorf("duplicate object name at byte %d", decoder.InputOffset())
		}
		value, err := DecodeValue(decoder)
		if err != nil {
			return nil, err
		}
		object[name] = value
	}
	if err := consumeDelimiter(decoder, '}'); err != nil {
		return nil, err
	}

	return object, nil
}

func decodeArray(decoder *json.Decoder) ([]any, error) {
	array := make([]any, 0)
	for decoder.More() {
		value, err := DecodeValue(decoder)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	if err := consumeDelimiter(decoder, ']'); err != nil {
		return nil, err
	}

	return array, nil
}

func consumeDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != expected {
		return fmt.Errorf("expected delimiter %q", expected)
	}

	return nil
}
