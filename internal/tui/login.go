package tui

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func browserEnvironment() []string {
	// The browser has no Vault CLI role, so it must not inherit Vault-specific variables.
	return slices.DeleteFunc(os.Environ(), func(variable string) bool {
		name, _, found := strings.Cut(variable, "=")

		return found && strings.HasPrefix(name, "VAULT_")
	})
}

type openLoginResultMsg struct {
	id  uint64
	url string
	err error
}

func vaultLoginURL(address, namespace string) string {
	loginURL := address + "/ui/vault/auth"
	if namespace == "" {
		return loginURL
	}

	return loginURL + "?" + url.Values{"namespace": {namespace}}.Encode()
}

func (model *Model) startOpenLogin() tea.Cmd {
	loginURL := vaultLoginURL(model.config.Address, model.config.Namespace)
	openURL := model.openURL
	ctx, cancel := context.WithTimeout(model.lifetime, operationTimeout)
	model.loginOpenID++
	requestID := model.loginOpenID
	model.loginOpenCancel = cancel

	return func() tea.Msg {
		defer cancel()

		return model.operations.run(func() tea.Msg {
			return openLoginResultMsg{id: requestID, url: loginURL, err: openURL(ctx, loginURL)}
		})
	}
}

func (model *Model) invalidateLoginOpen() {
	if model.loginOpenCancel != nil {
		model.loginOpenID++
		model.loginOpenCancel()
	}
	model.loginOpenCancel = nil
}
