package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/google/renameio/v2"
	"github.com/pelletier/go-toml/v2"
)

const (
	directoryMode      = 0o700
	configFileMode     = 0o600
	maximumConfigBytes = 2 << 20
	maximumFieldBytes  = 64 << 10
)

type Config struct {
	Address   string   `toml:"address"`
	Namespace string   `toml:"namespace,omitempty"`
	Mounts    []string `toml:"mounts,omitempty"`
}

type Store struct {
	path string
}

func DefaultPath() (string, error) {
	// os.UserConfigDir ignores XDG_CONFIG_HOME on macOS, so honor it before the platform fallback.
	root := os.Getenv("XDG_CONFIG_HOME")
	if root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
	} else {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate platform config directory: %w", err)
		}
	}

	return filepath.Join(root, "vlt", "config.toml"), nil
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (store *Store) Path() string {
	return store.path
}

func (store *Store) Load() (Config, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", store.path, err)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", store.path, err)
	}
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config %q exceeds %d bytes", store.path, maximumConfigBytes)
	}

	var loaded Config
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decodeError := decoder.Decode(&loaded); decodeError != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", store.path, decodeError)
	}

	normalized, err := Normalize(loaded)
	if err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", store.path, err)
	}

	return normalized, nil
}

func (store *Store) Save(config Config) error {
	normalized, err := Normalize(config)
	if err != nil {
		return fmt.Errorf("validate config %q: %w", store.path, err)
	}

	data, err := toml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode config %q: %w", store.path, err)
	}
	if len(data) > maximumConfigBytes {
		return fmt.Errorf("config %q exceeds %d bytes", store.path, maximumConfigBytes)
	}
	directory := filepath.Dir(store.path)
	if createError := os.MkdirAll(directory, directoryMode); createError != nil {
		return fmt.Errorf("create config directory %q: %w", directory, createError)
	}
	if chmodError := os.Chmod(directory, directoryMode); chmodError != nil {
		return fmt.Errorf("set config directory permissions %q: %w", directory, chmodError)
	}
	pending, err := renameio.NewPendingFile(store.path, renameio.WithStaticPermissions(configFileMode))
	if err != nil {
		return fmt.Errorf("prepare config %q: %w", store.path, err)
	}
	defer func() { _ = pending.Cleanup() }()
	if _, err = pending.Write(data); err != nil {
		return fmt.Errorf("write config %q: %w", store.path, err)
	}
	if err = pending.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("save config %q: %w", store.path, err)
	}

	return nil
}

func Normalize(config Config) (Config, error) {
	address, err := normalizeAddress(config.Address)
	if err != nil {
		return Config{}, err
	}
	config.Address = address
	config.Namespace, err = normalizeNamespace(config.Namespace)
	if err != nil {
		return Config{}, err
	}

	mounts := make([]string, 0, len(config.Mounts))
	for _, mount := range config.Mounts {
		normalized, normalizeError := normalizeMount(mount)
		if normalizeError != nil {
			return Config{}, normalizeError
		}
		mounts = append(mounts, normalized)
	}
	slices.Sort(mounts)
	config.Mounts = slices.Compact(mounts)

	return config, nil
}

func normalizeAddress(address string) (string, error) {
	if err := validateFieldSize("address", address); err != nil {
		return "", err
	}
	if containsControl(address) {
		return "", errors.New("address must not contain control characters")
	}
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if address == "" {
		return "", nil
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("address is not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("address scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("address must include a host")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("address must use https unless the host is loopback")
	}
	if parsed.User != nil || parsed.EscapedPath() != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || strings.Contains(address, "#") {
		return "", errors.New("address must not contain credentials, a path, a query, or a fragment")
	}

	return address, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)

	return err == nil && address.IsLoopback()
}

func normalizeNamespace(namespace string) (string, error) {
	if err := validateFieldSize("namespace", namespace); err != nil {
		return "", err
	}
	if containsControl(namespace) {
		return "", errors.New("namespace must not contain control characters")
	}
	namespace = trimPathBoundary(namespace)
	return namespace, nil
}

func normalizeMount(mount string) (string, error) {
	if err := validateFieldSize("mount", mount); err != nil {
		return "", err
	}
	if containsControl(mount) {
		return "", errors.New("mount must not contain control characters")
	}
	mount = trimPathBoundary(mount)
	if mount == "" {
		return "", errors.New("mount must not be empty")
	}
	for segment := range strings.SplitSeq(mount, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("mount must not contain empty, '.' or '..' segments")
		}
	}

	return mount, nil
}

func trimPathBoundary(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		return character == '/' || unicode.IsSpace(character)
	})
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validateFieldSize(name, value string) error {
	if len(value) > maximumFieldBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumFieldBytes)
	}

	return nil
}
