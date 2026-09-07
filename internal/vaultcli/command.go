package vaultcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/hashicorp/vault/api/cliconfig"
	"github.com/hashicorp/vault/api/tokenhelper"
)

func (client *Client) execute(
	ctx context.Context,
	withCredentials bool,
	captureOutput bool,
	input string,
	allowedExitCodes []int,
	arguments ...string,
) ([]byte, error) {
	// The binary is resolved with exec.LookPath and arguments never pass through a shell.
	//nolint:gosec // Intentional external Vault CLI invocation.
	command := exec.CommandContext(
		ctx,
		client.binary,
		arguments...)
	command.Env = client.environment(withCredentials)
	command.Stdin = strings.NewReader(input)
	configureCommandCancellation(command)

	stdout := boundedBuffer{limit: maximumCommandOutputSize}
	stderr := boundedBuffer{limit: maximumErrorLength}
	if captureOutput {
		command.Stdout = &stdout
	} else {
		command.Stdout = io.Discard
	}
	command.Stderr = &stderr

	err := command.Start()
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return nil, fmt.Errorf("vault %s: %w", commandName(arguments), contextError)
		}

		return nil, fmt.Errorf("start vault %s: %w", commandName(arguments), err)
	}

	err = command.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		_ = command.Cancel()
	}
	exitCode := 0
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return nil, dispatchedError(fmt.Errorf("vault %s: %w", commandName(arguments), contextError))
		}
		if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitError.ExitCode()
		} else {
			return nil, dispatchedError(fmt.Errorf("wait for vault %s: %w", commandName(arguments), err))
		}
	}

	if stdout.full {
		return nil, dispatchedError(fmt.Errorf(
			"vault %s output exceeds %d bytes",
			commandName(arguments),
			maximumCommandOutputSize,
		))
	}
	if slices.Contains(allowedExitCodes, exitCode) &&
		(exitCode == 0 || strings.TrimSpace(stderr.buffer.String()) == "") {
		return stdout.buffer.Bytes(), nil
	}

	rawMessage := stderr.buffer.String()
	statusCode, parsed := parseVaultHTTPStatus(rawMessage)
	cause := classifyVaultHTTPStatus(statusCode, parsed)
	message := sanitizeError(rawMessage, input, cause, statusCode, parsed)
	definiteFailure := parsed &&
		statusCode >= httpClientErrorMinimum && statusCode <= httpClientErrorMaximum

	return nil, dispatchedError(&CommandError{
		Command:         commandName(arguments),
		ExitCode:        exitCode,
		Message:         message,
		definiteFailure: definiteFailure,
		cause:           cause,
	})
}

func (client *Client) getToken(ctx context.Context, helper tokenhelper.TokenHelper) (string, error) {
	external, ok := helper.(*tokenhelper.ExternalTokenHelper)
	var token string
	var err error
	if ok {
		token, err = client.runExternalTokenHelper(ctx, external, "get")
	} else {
		token, err = helper.Get()
	}
	if err != nil {
		return "", err
	}
	if len(token) > MaximumFieldBytes {
		return "", fmt.Errorf("cached token exceeds %d bytes", MaximumFieldBytes)
	}

	return token, nil
}

func (client *Client) eraseToken(ctx context.Context) error {
	helper, err := cliconfig.DefaultTokenHelper()
	if err != nil {
		return fmt.Errorf("load Vault token helper: %w", err)
	}
	external, ok := helper.(*tokenhelper.ExternalTokenHelper)
	if !ok {
		if err = helper.Erase(); err != nil {
			return fmt.Errorf("erase cached token: %w", err)
		}

		return nil
	}

	_, err = client.runExternalTokenHelper(ctx, external, "erase")
	if err == nil {
		return nil
	}
	err = fmt.Errorf("erase cached token: %w", err)
	if errors.Is(err, errCommandDispatched) {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}

	return err
}

func (client *Client) runExternalTokenHelper(
	ctx context.Context,
	helper *tokenhelper.ExternalTokenHelper,
	operation string,
) (string, error) {
	arguments := append(slices.Clone(helper.Args), operation)
	// DefaultTokenHelper resolves configured helpers to an existing absolute path;
	// the operation and arguments are supplied by this package, not terminal input.
	//nolint:gosec // Running that configured helper is the Vault token-helper contract.
	command := exec.CommandContext(ctx, helper.BinaryPath, arguments...)
	command.Env = client.tokenHelperEnvironment(helper.Env)
	configureCommandCancellation(command)

	stdout := boundedBuffer{limit: MaximumFieldBytes}
	stderr := boundedBuffer{limit: maximumErrorLength}
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Start(); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return "", contextError
		}

		return "", fmt.Errorf("start token helper %s: %w", operation, err)
	}
	waitError := command.Wait()
	if errors.Is(waitError, exec.ErrWaitDelay) {
		_ = command.Cancel()
	}
	if stdout.full || stderr.full {
		return "", dispatchedError(fmt.Errorf("token helper %s output exceeds its limit", operation))
	}
	if waitError != nil {
		if contextError := ctx.Err(); contextError != nil {
			return "", dispatchedError(contextError)
		}

		return "", dispatchedError(fmt.Errorf("token helper %s: %w", operation, waitError))
	}

	return stdout.buffer.String(), nil
}

func dispatchedError(err error) error {
	return &dispatchedCommandError{err: err}
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit + 1 - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(data[:min(len(data), remaining)])
	}
	if buffer.buffer.Len() > buffer.limit || len(data) > remaining {
		buffer.full = true
	}

	return written, nil
}

func (client *Client) environment(withCredentials bool) []string {
	environment := make([]string, 0, len(os.Environ())+vaultEnvironmentCap)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "VAULT_FORMAT=") ||
			(!withCredentials && (strings.HasPrefix(variable, "VAULT_TOKEN=") ||
				strings.HasPrefix(variable, "VAULT_MFA="))) {
			continue
		}
		environment = append(environment, variable)
	}

	environment = client.targetEnvironment(environment)
	environment = append(environment, "VAULT_FORMAT=json")
	if !withCredentials {
		// A non-empty, non-secret token prevents the Vault CLI from loading the
		// user's real token helper entry for unauthenticated status requests.
		environment = append(environment, "VAULT_TOKEN="+statusPlaceholder)
	}
	return environment
}

func (client *Client) tokenHelperEnvironment(configured []string) []string {
	if configured == nil {
		configured = os.Environ()
	}

	return client.targetEnvironment(configured)
}

func (client *Client) targetEnvironment(source []string) []string {
	environment := make([]string, 0, len(source)+vaultEnvironmentCap)
	for _, variable := range source {
		if strings.HasPrefix(variable, "VAULT_ADDR=") ||
			strings.HasPrefix(variable, "VAULT_AGENT_ADDR=") ||
			strings.HasPrefix(variable, "VAULT_NAMESPACE=") {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(environment, "VAULT_ADDR="+client.address)
	if client.namespace != "" {
		environment = append(environment, "VAULT_NAMESPACE="+client.namespace)
	}

	return environment
}

func environmentVariableSet(name string) bool {
	value, exists := os.LookupEnv(name)

	return exists && value != ""
}
