package ui

import (
	"github.com/antixcode6/go-yt/internal/config"
	"github.com/antixcode6/go-yt/internal/feed"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Screen identifies which view is currently active.
type Screen int
type inputMode int

const (
	ScreenSubscriptions Screen = iota
	ScreenVideos
	ScreenPlayer
)

const (
	inputNone inputMode = iota
	inputAddSub
	inputPlayURL
)

// Model is the top-level Bubble Tea model.
type Model struct {
	cfg    config.Config
	player *player.Client
	store  *subscription.Store

	// UI state
	screen        Screen
	width, height int
	cursor        int
	selectedSub   int // index into subs slice
	statusMsg     string
	err           error

	// Cached view data.
	subs   []*subscription.Subscription
	videos []*feed.Video // or feed.Video

	// Add subscription input state
	input  textinput.Model
	adding bool // true when the modal input is visible

	pos, dur   float64
	paused     bool
	mediaTitle string

	inputMode inputMode
}

// NewModel constructs the top-level Bubble Tea model. Subscriptions are loaded
// asynchronously by Init(), so this stays cheap and side-effect free.
func NewModel(cfg config.Config, p *player.Client, store *subscription.Store) Model {
	ti := textinput.New()
	ti.CharLimit = 200
	ti.Width = 60

	return Model{
		cfg:    cfg,
		player: p,
		store:  store,
		screen: ScreenSubscriptions,
		input:  ti,
	}
}

// openInput activates the input modal in the given mode with the
// appropriate placeholder, and returns the blink command.
func (m *Model) openInput(mode inputMode) tea.Cmd {
	m.inputMode = mode
	switch mode {
	case inputAddSub:
		m.input.Placeholder = "https://youtube.com/@channel or UCxxxx"
	case inputPlayURL:
		m.input.Placeholder = "https://youtube.com/watch?v=... or video ID"
	}
	m.input.SetValue("")
	m.input.Focus()
	return textinput.Blink
}

// closeInput clears and hides the input modal.
func (m *Model) closeInput() {
	m.inputMode = inputNone
	m.input.Blur()
	m.input.SetValue("")
}
