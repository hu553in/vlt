package vaultcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api/cliconfig"
	"github.com/hu553in/vlt/internal/strictjson"
)

const (
	MaximumSecretBytes       = 2 << 20
	MaximumCollectionEntries = 16 << 10
	MaximumFieldBytes        = 64 << 10
	// Vault responses wrap the payload in metadata. Keep that untrusted process
	// output bounded independently, then enforce MaximumSecretBytes on decoded data.
	maximumCommandOutputSize = 32 << 20
	maximumErrorLength       = 4096
	vaultEnvironmentCap      = 4
	httpStatusForbidden      = 403
	httpClientErrorMinimum   = 400
	httpClientErrorMaximum   = 499
	statusPlaceholder        = "vlt-status-request"
)

var (
	ErrPermissionDenied  = errors.New("permission denied")
	ErrEnvironmentToken  = errors.New("VAULT_TOKEN is set; unset it before logging in with vlt")
	ErrOutcomeUnknown    = errors.New("operation outcome is unknown")
	errCommandDispatched = errors.New("external command was dispatched")
)

type Status struct {
	Version string
	Sealed  bool
}

type AuthInfo struct {
	DisplayName string
	FromEnv     bool
}

type Mount struct {
	Path        string
	Description string
}

type mountDetails struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Options     struct {
		Version string `json:"version"`
	} `json:"options"`
}

type Secret struct {
	Data    map[string]any
	Version int
	Deleted bool
}

type LogoutOutcome struct {
	TokenRevoked      bool
	LocalTokenCleared bool
}

type CommandError struct {
	Command         string
	ExitCode        int
	Message         string
	definiteFailure bool
	cause           error
}

type dispatchedCommandError struct {
	err error
}

func (commandError *dispatchedCommandError) Error() string {
	return commandError.err.Error()
}

func (commandError *dispatchedCommandError) Unwrap() []error {
	return []error{commandError.err, errCommandDispatched}
}

func (commandError *CommandError) Error() string {
	if commandError.Message == "" {
		return fmt.Sprintf("vault %s failed with exit code %d", commandError.Command, commandError.ExitCode)
	}

	return fmt.Sprintf("vault %s: %s", commandError.Command, commandError.Message)
}

func (commandError *CommandError) Unwrap() error {
	return commandError.cause
}

type Client struct {
	binary    string
	address   string
	namespace string
	fromEnv   bool
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
	full   bool
}

func New(address, namespace string) (*Client, error) {
	binary, err := exec.LookPath("vault")
	if err != nil {
		return nil, fmt.Errorf("find vault CLI in PATH: %w", err)
	}

	return NewWithBinary(binary, address, namespace), nil
}

func NewWithBinary(binary, address, namespace string) *Client {
	fromEnvironment := EnvironmentTokenSet()

	return &Client{
		binary:    binary,
		address:   strings.TrimRight(address, "/"),
		namespace: namespace,
		fromEnv:   fromEnvironment,
	}
}

func EnvironmentTokenSet() bool {
	return environmentVariableSet("VAULT_TOKEN")
}

// AmbientCredentialVariable returns the first Vault CLI environment variable
// that can carry authentication material independently of the token helper.
func AmbientCredentialVariable() string {
	for _, name := range []string{
		"VAULT_TOKEN",
		"VAULT_MFA",
		"VAULT_HEADERS",
		"VAULT_CLIENT_CERT",
		"VAULT_CLIENT_KEY",
	} {
		if environmentVariableSet(name) {
			return name
		}
	}

	return ""
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	output, err := client.execute(ctx, false, true, "", []int{0, 2}, "status", "-format=json")
	if err != nil {
		return Status{}, err
	}

	var response struct {
		Version string `json:"version"`
		Sealed  *bool  `json:"sealed"`
	}
	if decodeError := decodeJSON(output, &response); decodeError != nil {
		return Status{}, fmt.Errorf("decode vault status: %w", decodeError)
	}
	if strings.TrimSpace(response.Version) == "" {
		return Status{}, errors.New("vault status response has no version")
	}
	if fieldError := validateServerField("Vault version", response.Version); fieldError != nil {
		return Status{}, fieldError
	}
	if response.Sealed == nil {
		return Status{}, errors.New("vault status response has no sealed state")
	}

	return Status{Version: response.Version, Sealed: *response.Sealed}, nil
}

func (client *Client) AuthStatus(ctx context.Context) (AuthInfo, error) {
	output, err := client.execute(ctx, true, true, "", []int{0}, "token", "lookup", "-format=json")
	if err != nil {
		return AuthInfo{FromEnv: client.fromEnv}, err
	}

	var response struct {
		Data *struct {
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if decodeError := decodeJSON(output, &response); decodeError != nil {
		return AuthInfo{FromEnv: client.fromEnv}, fmt.Errorf("decode token status: %w", decodeError)
	}
	if response.Data == nil {
		return AuthInfo{FromEnv: client.fromEnv}, errors.New("vault token status response has no token data")
	}
	if fieldError := validateServerField("token display name", response.Data.DisplayName); fieldError != nil {
		return AuthInfo{FromEnv: client.fromEnv}, fieldError
	}
	return AuthInfo{
		DisplayName: response.Data.DisplayName,
		FromEnv:     client.fromEnv,
	}, nil
}

func (client *Client) HasToken(ctx context.Context) (bool, error) {
	if client.fromEnv {
		return true, nil
	}

	helper, err := cliconfig.DefaultTokenHelper()
	if err != nil {
		return false, fmt.Errorf("load Vault token helper: %w", err)
	}
	token, err := client.getToken(ctx, helper)
	if err != nil {
		return false, fmt.Errorf("read cached Vault token: %w", err)
	}

	return strings.TrimSpace(token) != "", nil
}

func (client *Client) Login(ctx context.Context, token string) error {
	if client.fromEnv {
		return ErrEnvironmentToken
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("token must not be empty")
	}
	if len(token) > MaximumFieldBytes {
		return fmt.Errorf("token exceeds %d bytes", MaximumFieldBytes)
	}

	_, err := client.execute(
		ctx,
		true,
		false,
		token+"\n",
		[]int{0},
		"login",
		"-no-print",
		"-non-interactive",
		"-",
	)

	return mutationError(err)
}

func (client *Client) Logout(ctx context.Context) (LogoutOutcome, error) {
	_, revokeError := client.execute(
		ctx,
		true,
		false,
		"",
		[]int{0},
		"token",
		"revoke",
		"-self",
		"-non-interactive",
	)
	revokeError = mutationError(revokeError)
	outcome := LogoutOutcome{TokenRevoked: revokeError == nil}
	if client.fromEnv {
		return outcome, revokeError
	}

	if eraseError := client.eraseToken(ctx); eraseError != nil {
		return outcome, errors.Join(revokeError, eraseError)
	}
	outcome.LocalTokenCleared = true

	return outcome, revokeError
}

func (client *Client) ClearToken(ctx context.Context) error {
	if client.fromEnv {
		return ErrEnvironmentToken
	}

	return client.eraseToken(ctx)
}

func (client *Client) ListMounts(ctx context.Context) ([]Mount, error) {
	output, err := client.execute(
		ctx,
		true,
		true,
		"",
		[]int{0},
		"secrets",
		"list",
		"-format=json",
		"-non-interactive",
	)
	if err != nil {
		return nil, err
	}

	mounts, decodeError := decodeMounts(output)
	if decodeError != nil {
		return nil, fmt.Errorf("decode secrets engines: %w", decodeError)
	}
	slices.SortFunc(mounts, func(left, right Mount) int { return strings.Compare(left.Path, right.Path) })

	return mounts, nil
}

func (client *Client) List(ctx context.Context, mount, folder string) ([]string, error) {
	if err := validateMount(mount); err != nil {
		return nil, err
	}
	if err := validatePath(folder, true); err != nil {
		return nil, err
	}

	if err := client.requireKVv2(ctx, mount); err != nil {
		return nil, err
	}

	output, err := client.execute(
		ctx,
		true,
		true,
		"",
		[]int{0, 2},
		"kv",
		"list",
		"-mount="+mount,
		"-format=json",
		"-non-interactive",
		"--",
		folder,
	)
	if err != nil {
		return nil, err
	}

	keys, err := decodeKeys(output)
	if err != nil {
		return nil, fmt.Errorf("decode keys for %s/%s: %w", mount, folder, err)
	}
	for index, key := range keys {
		secretPath := strings.TrimSuffix(key, "/")
		if folder != "" {
			secretPath = folder + "/" + secretPath
		}
		if err = validatePath(secretPath, false); err != nil {
			return nil, fmt.Errorf(
				"vault key %d cannot be addressed exactly by Vault CLI: %w",
				index+1,
				err,
			)
		}
	}
	slices.Sort(keys)

	return keys, nil
}

func (client *Client) Get(ctx context.Context, mount, secretPath string) (Secret, error) {
	if err := validateMount(mount); err != nil {
		return Secret{}, err
	}
	if err := validatePath(secretPath, false); err != nil {
		return Secret{}, err
	}

	if err := client.requireKVv2(ctx, mount); err != nil {
		return Secret{}, err
	}

	output, err := client.execute(
		ctx,
		true,
		true,
		"",
		[]int{0},
		"kv",
		"get",
		"-mount="+mount,
		"-format=json",
		"-non-interactive",
		"--",
		secretPath,
	)
	if err != nil {
		return Secret{}, err
	}

	return decodeSecret(output)
}

func (client *Client) Put(
	ctx context.Context,
	mount string,
	secretPath string,
	data map[string]any,
	version int,
) (int, error) {
	if err := validateMount(mount); err != nil {
		return 0, err
	}
	if err := validatePath(secretPath, false); err != nil {
		return 0, err
	}
	if version < 0 {
		return 0, errors.New("secret version must not be negative")
	}
	if version == math.MaxInt {
		return 0, errors.New("secret version is too large")
	}

	payload, err := EncodeSecret(data)
	if err != nil {
		return 0, err
	}

	if err = client.requireKVv2(ctx, mount); err != nil {
		return 0, err
	}

	_, err = client.execute(
		ctx,
		true,
		false,
		string(payload),
		[]int{0},
		"kv",
		"put",
		"-mount="+mount,
		"-cas="+strconv.Itoa(version),
		"-non-interactive",
		"--",
		secretPath,
		"-",
	)
	if err != nil {
		return 0, mutationError(err)
	}

	// A successful KV v2 CAS write advances the exact matched version once. The
	// command exit status is the commit boundary, so no follow-up read is needed.
	return version + 1, nil
}

func (client *Client) Delete(ctx context.Context, mount, secretPath string) error {
	if err := validateMount(mount); err != nil {
		return err
	}
	if err := validatePath(secretPath, false); err != nil {
		return err
	}
	if err := client.requireKVv2(ctx, mount); err != nil {
		return err
	}

	_, err := client.execute(
		ctx,
		true,
		false,
		"",
		[]int{0},
		"kv",
		"delete",
		"-mount="+mount,
		"-non-interactive",
		"--",
		secretPath,
	)

	return mutationError(err)
}

// Check every operation: configured mounts and mounts changed since discovery
// must not fall through to the Vault CLI's KV v1 write/delete behavior.
func (client *Client) requireKVv2(ctx context.Context, mount string) error {
	output, err := client.execute(
		ctx,
		true,
		true,
		"",
		[]int{0},
		"read",
		"-format=json",
		"-non-interactive",
		"--",
		"sys/internal/ui/mounts/"+mount,
	)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			mountDetails

			Path string `json:"path"`
		} `json:"data"`
	}
	if err = decodeJSON(output, &response); err != nil {
		return fmt.Errorf("decode mount %q: %w", mount, err)
	}
	if response.Data.Type != "kv" || response.Data.Options.Version != "2" {
		return fmt.Errorf("mount %q is not KV v2; choose a KV v2 mount", mount)
	}
	if response.Data.Path != mount+"/" {
		return fmt.Errorf("mount %q is not a mount root; enter the KV v2 mount name", mount)
	}
	return nil
}

func decodeKeys(output []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := first.(json.Delim)
	if !ok {
		return nil, errors.New("vault returned keys that are not a JSON array")
	}
	if delimiter == '{' {
		if decoder.More() {
			return nil, errors.New("vault returned keys that are not a JSON array")
		}
		if err = finishCollection(decoder, '}'); err != nil {
			return nil, err
		}

		return []string{}, nil
	}
	if delimiter != '[' {
		return nil, errors.New("vault returned keys that are not a JSON array")
	}

	keys := make([]string, 0)
	for index := 0; decoder.More(); index++ {
		if index >= MaximumCollectionEntries {
			return nil, fmt.Errorf("vault key list contains more than %d entries", MaximumCollectionEntries)
		}
		var key string
		if err = decoder.Decode(&key); err != nil {
			return nil, err
		}
		if err = validateServerField("Vault key", key); err != nil {
			return nil, fmt.Errorf("key %d: %w", index+1, err)
		}
		name := strings.TrimSuffix(key, "/")
		if name == "" || strings.Contains(name, "/") || name == "." || name == ".." || containsControl(name) {
			return nil, fmt.Errorf("invalid key at index %d in Vault list response", index+1)
		}
		keys = append(keys, key)
	}
	if err = finishCollection(decoder, ']'); err != nil {
		return nil, err
	}

	return keys, nil
}

func decodeMounts(output []byte) ([]Mount, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("vault returned secrets engines that are not a JSON object")
	}

	mounts := make([]Mount, 0)
	for index := 0; decoder.More(); index++ {
		if index >= MaximumCollectionEntries {
			return nil, fmt.Errorf("vault secrets engine list contains more than %d entries", MaximumCollectionEntries)
		}
		mount, include, decodeError := decodeMount(decoder)
		if decodeError != nil {
			return nil, decodeError
		}
		if include {
			mounts = append(mounts, mount)
		}
	}
	if err = finishCollection(decoder, '}'); err != nil {
		return nil, err
	}

	return mounts, nil
}

func decodeMount(decoder *json.Decoder) (Mount, bool, error) {
	mountToken, err := decoder.Token()
	if err != nil {
		return Mount{}, false, err
	}
	mountPath, ok := mountToken.(string)
	if !ok {
		return Mount{}, false, errors.New("vault returned a non-string secrets engine path")
	}
	var details mountDetails
	if err = decoder.Decode(&details); err != nil {
		return Mount{}, false, err
	}
	if err = validateServerField("secrets engine path", mountPath); err != nil {
		return Mount{}, false, err
	}
	if err = validateServerField("secrets engine type", details.Type); err != nil {
		return Mount{}, false, err
	}
	if err = validateServerField("secrets engine description", details.Description); err != nil {
		return Mount{}, false, err
	}
	if err = validateServerField("secrets engine version", details.Options.Version); err != nil {
		return Mount{}, false, err
	}
	if details.Type != "kv" || details.Options.Version != "2" {
		return Mount{}, false, nil
	}
	normalizedMount, found := strings.CutSuffix(mountPath, "/")
	if !found {
		return Mount{}, false, errors.New("invalid KV v2 mount from Vault: path has no trailing slash")
	}
	if err = validateMount(normalizedMount); err != nil {
		return Mount{}, false, fmt.Errorf("invalid KV v2 mount from Vault: %w", err)
	}

	return Mount{Path: normalizedMount, Description: details.Description}, true, nil
}

func decodeSecret(output []byte) (Secret, error) {
	var response struct {
		Data struct {
			Data     json.RawMessage `json:"data"`
			Metadata struct {
				Version      int    `json:"version"`
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := decodeJSON(output, &response); err != nil {
		return Secret{}, fmt.Errorf("decode secret response: %w", err)
	}

	if response.Data.Metadata.Version <= 0 {
		return Secret{}, errors.New("vault returned a secret without a valid KV v2 version")
	}
	deleted := response.Data.Metadata.DeletionTime != "" || response.Data.Metadata.Destroyed
	if bytes.Equal(bytes.TrimSpace(response.Data.Data), []byte("null")) {
		if !deleted {
			return Secret{}, errors.New("vault returned null secret data without deletion metadata")
		}

		return Secret{
			Data: map[string]any{}, Version: response.Data.Metadata.Version, Deleted: true,
		}, nil
	}
	// A future deletion_time schedules removal while data remains readable.
	// Trust Vault's null data response instead of comparing server time locally.
	if response.Data.Metadata.Destroyed {
		return Secret{}, errors.New("vault returned secret data with destruction metadata")
	}
	data, err := decodeSecretData(response.Data.Data)
	if err != nil {
		return Secret{}, fmt.Errorf("decode secret data: %w", err)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return Secret{}, fmt.Errorf("measure secret data: %w", err)
	}
	if len(payload) > MaximumSecretBytes {
		return Secret{}, fmt.Errorf("secret exceeds %d bytes", MaximumSecretBytes)
	}

	return Secret{Data: data, Version: response.Data.Metadata.Version}, nil
}

func decodeSecretData(dataJSON []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(dataJSON))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("vault returned secret data that is not a JSON object")
	}

	data := make(map[string]any)
	for index := 0; decoder.More(); index++ {
		if index >= MaximumCollectionEntries {
			return nil, fmt.Errorf("secret contains more than %d top-level entries", MaximumCollectionEntries)
		}
		keyToken, tokenError := decoder.Token()
		if tokenError != nil {
			return nil, tokenError
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("vault returned a non-string secret key")
		}
		if err = validateServerField("secret key", key); err != nil {
			return nil, fmt.Errorf("key %d: %w", index+1, err)
		}
		if _, exists := data[key]; exists {
			return nil, fmt.Errorf("duplicate secret key at byte %d", decoder.InputOffset())
		}
		value, decodeError := strictjson.DecodeValue(decoder)
		if decodeError != nil {
			return nil, decodeError
		}
		data[key] = value
	}
	if err = finishCollection(decoder, '}'); err != nil {
		return nil, err
	}

	return data, nil
}

func finishCollection(decoder *json.Decoder, closing json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != closing {
		return errors.New("invalid JSON collection ending")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON data")
		}

		return fmt.Errorf("decode trailing JSON data: %w", err)
	}

	return nil
}
