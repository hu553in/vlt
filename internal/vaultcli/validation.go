package vaultcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

func validateServerField(name, value string) error {
	if len(value) > MaximumFieldBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, MaximumFieldBytes)
	}

	return nil
}

func validateSecretData(data map[string]any) error {
	if len(data) > MaximumCollectionEntries {
		return fmt.Errorf("secret contains more than %d top-level entries", MaximumCollectionEntries)
	}
	for key := range data {
		if err := validateServerField("secret key", key); err != nil {
			return err
		}
	}

	return nil
}

// EncodeSecret validates the payload before any write is dispatched.
func EncodeSecret(data map[string]any) ([]byte, error) {
	if data == nil {
		return nil, errors.New("secret data must be a JSON object")
	}
	if err := validateSecretData(data); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode secret: %w", err)
	}
	if len(payload) > MaximumSecretBytes {
		return nil, fmt.Errorf("secret exceeds %d bytes", MaximumSecretBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	for {
		token, tokenError := decoder.Token()
		if errors.Is(tokenError, io.EOF) {
			break
		}
		if tokenError != nil {
			return nil, fmt.Errorf("validate secret: %w", tokenError)
		}
		if number, ok := token.(json.Number); ok && !numberSurvivesVault(number) {
			return nil, fmt.Errorf(
				"number at JSON byte %d would lose precision in Vault; quote it to store an exact string",
				decoder.InputOffset(),
			)
		}
	}
	return payload, nil
}

// Vault decodes JSON numbers as float64. Compare decimal values after its JSON
// round trip, rather than rejecting ordinary decimals such as 0.1.
func numberSurvivesVault(number json.Number) bool {
	value, err := number.Float64()
	if err != nil {
		return false
	}
	if value == 0 {
		mantissa, _, _ := strings.Cut(strings.ToLower(string(number)), "e")
		return strings.Trim(mantissa, "-0.") == ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	original, ok := new(big.Rat).SetString(string(number))
	if !ok {
		return false
	}
	rounded, ok := new(big.Rat).SetString(string(encoded))
	return ok && original.Cmp(rounded) == 0
}

func mutationError(err error) error {
	if err == nil || !errors.Is(err, errCommandDispatched) {
		return err
	}
	if commandError, ok := errors.AsType[*CommandError](err); ok && commandError.definiteFailure {
		return err
	}

	return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON data")
		}

		return fmt.Errorf("decode trailing JSON data: %w", err)
	}

	return nil
}

func validateMount(mount string) error {
	if err := validateServerField("mount", mount); err != nil {
		return err
	}
	if containsControl(mount) {
		return errors.New("mount must not contain control characters")
	}
	if mount == "" {
		return errors.New("mount must not be empty")
	}
	if strings.TrimSpace(mount) != mount {
		return errors.New("mount must not begin or end with whitespace")
	}
	if err := validateSegments(mount); err != nil {
		return fmt.Errorf("invalid mount: %w", err)
	}

	return nil
}

func validatePath(secretPath string, allowEmpty bool) error {
	if err := validateServerField("secret path", secretPath); err != nil {
		return err
	}
	if containsControl(secretPath) {
		return errors.New("secret path must not contain control characters")
	}
	if secretPath == "" && allowEmpty {
		return nil
	}
	if secretPath == "" {
		return errors.New("secret path must not be empty")
	}
	if strings.TrimSpace(secretPath) != secretPath {
		return errors.New("secret path must not begin or end with whitespace because Vault CLI would reinterpret it")
	}
	if err := validateSegments(secretPath); err != nil {
		return fmt.Errorf("invalid secret path: %w", err)
	}

	return nil
}

func NormalizeSecretPath(secretPath string) (string, error) {
	if err := validateServerField("secret path", secretPath); err != nil {
		return "", err
	}
	if containsControl(secretPath) {
		return "", errors.New("secret path must not contain control characters")
	}
	secretPath = strings.TrimFunc(secretPath, func(character rune) bool {
		return character == '/' || unicode.IsSpace(character)
	})
	if err := validatePath(secretPath, false); err != nil {
		return "", err
	}

	return secretPath, nil
}

func validateSegments(value string) error {
	for segment := range strings.SplitSeq(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path must not contain empty, '.' or '..' segments")
		}
	}

	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func sanitizeError(message, input string, cause error, statusCode int, parsed bool) string {
	message = strings.TrimSpace(message)
	if strings.TrimSpace(input) != "" {
		if cause != nil {
			return cause.Error()
		}
		if parsed {
			return fmt.Sprintf(
				"request failed with Vault HTTP %d; details hidden because stdin contained sensitive data",
				statusCode,
			)
		}

		return "request failed; Vault CLI details hidden because stdin contained sensitive data"
	}
	if len(message) > maximumErrorLength {
		message = message[:maximumErrorLength] + "..."
	}

	return message
}

func parseVaultHTTPStatus(message string) (int, bool) {
	for line := range strings.SplitSeq(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		remainder, found := strings.CutPrefix(line, "Code: ")
		if !found {
			continue
		}
		code, suffix, found := strings.Cut(remainder, ".")
		if !found || strings.TrimSpace(suffix) != "Errors:" {
			return 0, false
		}
		parsedCode, err := strconv.Atoi(code)
		if err != nil || parsedCode < 100 || parsedCode > 599 {
			return 0, false
		}

		return parsedCode, true
	}

	return 0, false
}

func classifyVaultHTTPStatus(statusCode int, parsed bool) error {
	if !parsed {
		return nil
	}
	if statusCode == httpStatusForbidden {
		return ErrPermissionDenied
	}

	return nil
}

func commandName(arguments []string) string {
	if len(arguments) == 0 {
		return "command"
	}
	if len(arguments) > 1 && (arguments[0] == "kv" || arguments[0] == "token" || arguments[0] == "secrets") {
		return arguments[0] + " " + arguments[1]
	}

	return arguments[0]
}
