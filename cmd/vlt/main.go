package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/tui"
)

const (
	version     = "0.1.0"
	description = "A focused TUI for HashiCorp Vault KV v2."
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "vlt:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("vlt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := false
	flags.BoolVar(&showVersion, "v", false, "print the version and exit")
	flags.BoolVar(&showVersion, "version", false, "print the version and exit")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), description)
		_, _ = fmt.Fprintln(flags.Output(), "\nUsage: vlt [flags]\n\nFlags:")
		_, _ = fmt.Fprintln(flags.Output(), "  -h, --help       show this help")
		_, _ = fmt.Fprintln(flags.Output(), "  -v, --version    print the version and exit")
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(os.Stdout)
			flags.Usage()

			return nil
		}

		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if showVersion {
		_, err := fmt.Fprintln(os.Stdout, version)

		return err
	}

	configPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	store := config.NewStore(configPath)
	loaded, loadError := store.Load()
	model := tui.New(store, loaded, loadError)
	defer model.Close()

	_, runError := tea.NewProgram(model).Run()
	if runError != nil && !errors.Is(runError, tea.ErrInterrupted) {
		return fmt.Errorf("run TUI: %w", runError)
	}

	return nil
}
