package ui

import (
	"fmt"
	"strings"

	"github.com/antixcode6/go-yt/internal/config"
	"github.com/antixcode6/go-yt/internal/feed"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Screen identifies which view is currently active.
type Screen int
type inputMode int

const (
	ScreenSubscriptions Screen = iota
	ScreenVideos
	ScreenPlayer
	ScreenDiscover
)

const (
	inputNone inputMode = iota
	inputAddSub
	inputPlayURL
	inputSearch
	inputImportSubs
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
	subs           []*subscription.Subscription
	videos         []*feed.Video // or feed.Video
	videoListTitle string
	showChannel    bool

	// Add subscription input state
	input  textinput.Model
	adding bool // true when the modal input is visible

	pos, dur   float64
	paused     bool
	mediaTitle string

	inputMode inputMode

	// Discover screen state
	discoverSources []string

	// Loading stuff
	loading    bool
	loadingMsg string
	spinner    spinner.Model

	playbackStarted bool

	offset int
}

// NewModel constructs the top-level Bubble Tea model. Subscriptions are loaded
// asynchronously by Init(), so this stays cheap and side-effect free.
func NewModel(cfg config.Config, p *player.Client, store *subscription.Store) Model {
	ti := textinput.New()
	ti.CharLimit = 200
	ti.Width = 60

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return Model{
		cfg:       cfg,
		player:    p,
		store:     store,
		screen:    ScreenSubscriptions,
		statusMsg: p.VideoMode(),
		input:     ti,
		spinner:   sp,
		discoverSources: []string{
			"Recommended for You (Personalized)",
			"Subscriptions Feed (Personalized)",
			"Watch History (Personalized)",
			"Search YouTube",
			"Local Recommendations",
		},
	}
}

func (m *Model) openInput(mode inputMode) tea.Cmd {
	m.inputMode = mode
	switch mode {
	case inputAddSub:
		m.input.Placeholder = "https://youtube.com/@channel or UCxxxx"
	case inputPlayURL:
		m.input.Placeholder = "https://youtube.com/watch?v=... or video ID"
	case inputSearch:
		m.input.Placeholder = "Search YouTube…"
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

// renderChannelVideoList renders a single channel's videos: one line each,
// with a date prefix. Used when entering a subscription from the main list.
func (m Model) renderChannelVideoList(b *strings.Builder) string {
	visible := m.visibleItems()
	end := m.offset + visible
	if end > len(m.videos) {
		end = len(m.videos)
	}

	for i := m.offset; i < end; i++ {
		v := m.videos[i]
		dateStr := ""
		if !v.Published.IsZero() {
			dateStr = v.Published.Format("2006-01-02") + "  "
		}
		line := dateStr + v.Title
		if i == m.cursor {
			b.WriteString(selectedStyle.Render("▸ " + line))
			b.WriteByte('\n')
		} else {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	if len(m.videos) > visible {
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render(fmt.Sprintf(
			"  %d–%d of %d", m.offset+1, end, len(m.videos))))
	}
	return b.String()
}

// renderMixedVideoList renders videos from multiple channels: two lines per
// video, with the channel name on top and the title indented below. Used for
// Discover feeds (Subscriptions, History, Search, Local Recs).
func (m Model) renderMixedVideoList(b *strings.Builder) string {
	perScreen := m.visibleItems()
	end := m.offset + perScreen
	if end > len(m.videos) {
		end = len(m.videos)
	}

	for i := m.offset; i < end; i++ {
		v := m.videos[i]
		isCursor := i == m.cursor

		// Line 1: channel name (or a placeholder if missing).
		channel := v.ChannelName
		if channel == "" {
			channel = "(unknown channel)"
		}
		channel = truncate(channel, 60)

		// Line 2: video title, indented, and the date if we have one.
		title := truncate(v.Title, 70)
		if !v.Published.IsZero() {
			title = v.Published.Format("2006-01-02") + "  " + title
		}

		if isCursor {
			b.WriteString(selectedStyle.Render("▸ " + channel))
			b.WriteByte('\n')
			b.WriteString(selectedStyle.Render("    " + title))
			b.WriteByte('\n')
		} else {
			b.WriteString("  ")
			b.WriteString(channel)
			b.WriteByte('\n')
			b.WriteString(dimStyle.Render("    " + title))
			b.WriteByte('\n')
		}
	}

	if len(m.videos) > perScreen {
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render(fmt.Sprintf(
			"  %d–%d of %d", m.offset+1, end, len(m.videos))))
	}
	return b.String()
}
