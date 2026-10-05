package ui

import (
	"github.com/antixcode6/go-yt/internal/config"
	"github.com/antixcode6/go-yt/internal/feed"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	"github.com/charmbracelet/bubbles/textinput"
)

// Screen identifies which view is currently active.
type Screen int

const (
	ScreenSubscriptions Screen = iota
	ScreenVideos
	ScreenPlayer
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
}

// NewModel constructs the top-level Bubble Tea model. Subscriptions are loaded
// asynchronously by Init(), so this stays cheap and side-effect free.
func NewModel(cfg config.Config, p *player.Client, store *subscription.Store) Model {
	ti := textinput.New()
	ti.Placeholder = "https://youtube.com/@channel or UCxxxx"
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
