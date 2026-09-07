package tui

import (
	"context"
	"sync"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/paginator"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/hu553in/vlt/internal/config"
	"github.com/hu553in/vlt/internal/vaultcli"
)

const (
	minimumWidth     = 90
	minimumHeight    = 24
	operationTimeout = 20 * time.Second
	spinnerDelay     = 200 * time.Millisecond
	// U+007F expands from one UTF-8 byte to six bytes as the JSON escape \u007f.
	maximumEditorEscapeExpansion = 6
	// Bubbles textarea has a hard 10,000-line insertion limit. Use compact JSON above it.
	maximumEditorLines         = 10000
	maximumEditorBytes         = maximumEditorEscapeExpansion * vaultcli.MaximumSecretBytes
	browserWidthRatio          = 45
	percentageBase             = 100
	minimumPaneWidth           = 32
	panelFrameWidth            = 4
	panelFrameHeight           = 2
	screenChromeHeight         = 5
	tableChromeHeight          = 3
	keyColumnRatio             = 3
	minimumTableHeight         = 3
	minimumKeyWidth            = 12
	minimumValueWidth          = 8
	minimumInputWidth          = 20
	maximumInputWidth          = 100
	inputChromeWidth           = 24
	minimumEditorWidth         = 40
	editorChromeWidth          = 10
	minimumEditorHeight        = 8
	editorChromeHeight         = 10
	modalWidth                 = 72
	modalFrameWidth            = 8
	modalPadding               = 2
	errorViewportHeight        = 10
	initialPaneCount           = 2
	keyEscape                  = "esc"
	keyEnter                   = "enter"
	keyQuitInput               = "ctrl+d"
	keyControlO                = "ctrl+o"
	unknownCAS                 = -1
	browserTitle               = "KV mounts"
	kvV2Description            = "KV v2"
	secretDescription          = "secret"
	configuredMountDescription = "configured KV v2"
	authenticationError        = "Could not authenticate"
	cancelAction               = "cancel"
)

type screen uint8

const (
	screenSetup screen = iota
	screenLogin
	screenBrowser
	screenMount
	screenPath
	screenEditor
	screenConfirm
	screenHelp
	screenError
)

type confirmationAction uint8

const (
	confirmationNone confirmationAction = iota
	confirmationQuit
	confirmationLogout
	confirmationSave
	confirmationDiscard
	confirmationDelete
	confirmationPaste
	confirmationSwitchVault
)

type pane uint8

const (
	paneBrowser pane = iota
	paneSecret
)

type itemKind uint8

const (
	itemMount itemKind = iota
	itemFolder
	itemSecret
)

type browserItem struct {
	title       string
	description string
	path        string
	kind        itemKind
}

type copiedEntry struct {
	key         string
	value       any
	sourceMount string
	sourcePath  string
}

type secretTarget struct {
	client *vaultcli.Client
	mount  string
	path   string
}

type pendingSecretWrite struct {
	target   secretTarget
	data     map[string]any
	version  int
	copied   *copiedEntry
	replaces bool
}

type operationGroup struct {
	mu      sync.Mutex
	running sync.WaitGroup
	closed  bool
}

func (group *operationGroup) run(operation func() tea.Msg) tea.Msg {
	group.mu.Lock()
	if group.closed {
		group.mu.Unlock()

		return nil
	}
	group.running.Add(1)
	group.mu.Unlock()
	defer group.running.Done()

	return operation()
}

func (group *operationGroup) close(stop context.CancelFunc) {
	group.mu.Lock()
	group.closed = true
	group.mu.Unlock()
	stop()
	group.running.Wait()
}

func (item browserItem) Title() string {
	return displayText(item.title)
}

func (item browserItem) Description() string {
	return displayText(item.description)
}

func (item browserItem) FilterValue() string {
	return item.title
}

type Model struct {
	store           *config.Store
	config          config.Config
	client          *vaultcli.Client
	auth            vaultcli.AuthInfo
	openURL         func(context.Context, string) error
	loginOpenID     uint64
	loginOpenCancel context.CancelFunc

	screen                   screen
	returnScreen             screen
	errorReturnScreen        screen
	confirmationReturnScreen screen
	confirmation             confirmationAction
	focus                    pane
	width                    int
	height                   int
	dark                     bool
	theme                    theme

	browser      list.Model
	secretTable  secretTableModel
	setupInputs  []textinput.Model
	setupIndex   int
	setupEditing bool
	loginInput   textinput.Model
	mountInput   textinput.Model
	pathInput    textinput.Model
	editor       textarea.Model
	errorView    viewport.Model
	spinner      spinner.Model

	mounts           []vaultcli.Mount
	discoveredMounts []vaultcli.Mount
	currentMount     string
	currentPath      string
	selectedPath     string
	secret           *vaultcli.Secret
	secretKeys       []string
	revealed         []bool
	revealedCount    int
	editorGeneration uint64
	editorPath       string
	editorCAS        int
	editorOriginal   string
	copied           *copiedEntry
	pendingWrite     *pendingSecretWrite
	pendingDelete    *secretTarget
	pendingConfig    *config.Config
	editingMount     string

	reauthenticating   bool
	reauthReturnScreen screen
	reauthRequired     bool
	errorTitle         string
	errorCanReauth     bool
	errorCanEditMount  bool
	errorMount         string

	status                  string
	statusError             bool
	loading                 bool
	showSpinner             bool
	operationID             uint64
	browserFilterGeneration uint64
	browserFilterPending    bool
	cancel                  context.CancelFunc
	lifetime                context.Context
	stop                    context.CancelFunc
	operations              operationGroup
}

//nolint:funlen // Keep the initial widget configuration together; it has no independent behavior.
func New(store *config.Store, loaded config.Config, loadError error) *Model {
	appTheme := newTheme(true)
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	browser := list.New(
		nil,
		delegate,
		minimumWidth/initialPaneCount,
		minimumHeight-screenChromeHeight,
	)
	browser.Title = browserTitle
	browser.Paginator.Type = paginator.Arabic
	browser.Paginator.ArabicFormat = "%d / %d"
	browser.SetShowHelp(false)
	browser.SetStatusBarItemName("entry", "entries")
	browser.DisableQuitKeybindings()

	secretTable := newSecretTable(
		[]table.Column{
			{Title: "Key", Width: minimumInputWidth},
			{Title: "Value", Width: minimumPaneWidth - panelFrameWidth},
		},
		minimumHeight-minimumEditorHeight,
	)

	addressInput := textinput.New()
	addressInput.Prompt = "Address:   "
	addressInput.Placeholder = "https://vault.example.com"
	addressInput.CharLimit = vaultcli.MaximumFieldBytes
	addressInput.SetValue(loaded.Address)

	namespaceInput := textinput.New()
	namespaceInput.Prompt = "Namespace: "
	namespaceInput.Placeholder = "optional"
	namespaceInput.CharLimit = vaultcli.MaximumFieldBytes
	namespaceInput.SetValue(loaded.Namespace)

	loginInput := textinput.New()
	loginInput.Prompt = "Token: "
	loginInput.Placeholder = "hvs..."
	loginInput.EchoMode = textinput.EchoPassword
	loginInput.CharLimit = vaultcli.MaximumFieldBytes

	mountInput := textinput.New()
	mountInput.Prompt = "KV v2 mount: "
	mountInput.Placeholder = secretDescription
	mountInput.CharLimit = vaultcli.MaximumFieldBytes

	pathInput := textinput.New()
	pathInput.Prompt = "Secret path: "
	pathInput.CharLimit = vaultcli.MaximumFieldBytes

	editor := textarea.New()
	editor.CharLimit = maximumEditorBytes
	editor.MaxHeight = maximumEditorLines
	editor.Placeholder = "{}"
	editor.ShowLineNumbers = true
	errorView := viewport.New(
		viewport.WithWidth(modalWidth-appTheme.modal.GetHorizontalFrameSize()),
		viewport.WithHeight(errorViewportHeight),
	)
	errorView.FillHeight = true

	loadingSpinner := spinner.New(spinner.WithSpinner(spinner.Line))

	lifetime, stop := context.WithCancel(context.Background())
	model := Model{
		store:        store,
		config:       loaded,
		screen:       screenSetup,
		returnScreen: screenBrowser,
		focus:        paneBrowser,
		dark:         true,
		theme:        appTheme,
		browser:      browser,
		secretTable:  secretTable,
		setupInputs:  []textinput.Model{addressInput, namespaceInput},
		loginInput:   loginInput,
		mountInput:   mountInput,
		pathInput:    pathInput,
		editor:       editor,
		errorView:    errorView,
		spinner:      loadingSpinner,
		openURL:      openURL,
		lifetime:     lifetime,
		stop:         stop,
	}
	model.applyTheme()

	switch {
	case loadError != nil:
		model.focusSetup(0)
		model.showOperationError("Could not load configuration", loadError)
	case loaded.Address == "":
		model.focusSetup(0)
	default:
		model.screen = screenBrowser
	}

	return &model
}

func (model *Model) Close() {
	model.operations.close(model.stop)
}

func (model *Model) Init() tea.Cmd {
	commands := []tea.Cmd{tea.RequestBackgroundColor}
	if model.config.Address != "" && model.status == "" {
		commands = append(commands, model.startBootstrap(model.config, false))
	}

	return tea.Batch(commands...)
}

func (model *Model) focusSetup(index int) {
	model.setupIndex = index
	for inputIndex := range model.setupInputs {
		if inputIndex == index {
			model.setupInputs[inputIndex].Focus()
		} else {
			model.setupInputs[inputIndex].Blur()
		}
	}
}

func (model *Model) terminalTooSmall() bool {
	return model.width > 0 && (model.width < minimumWidth || model.height < minimumHeight)
}
