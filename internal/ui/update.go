package ui

import (
	"context"
	"strings"
	"time"

	"github.com/antixcode6/go-yt/internal/feed"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	tea "github.com/charmbracelet/bubbletea"
)

// Msg types for async operations.
type (
	subsLoadedMsg   []*subscription.Subscription
	videosLoadedMsg []*feed.Video
	playStartedMsg  struct{}
	errMsg          error
)

type subscriptionAddedMsg struct {
	sub *subscription.Subscription
}

type addErrMsg struct {
	err error
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadSubsCmd(m.store),
		tea.EnterAltScreen,
	)
}

// loadSubsCmd reads subscriptions from the store.
func loadSubsCmd(store *subscription.Store) tea.Cmd {
	return func() tea.Msg {
		return subsLoadedMsg(store.List())
	}
}

// loadVideosCmd fetches videos for a given channel ID.
func loadVideosCmd(fc *feed.Client, channelID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		_, videos, err := fc.FetchChannel(ctx, channelID)
		if err != nil {
			return errMsg(err)
		}
		return videosLoadedMsg(videos)
	}
}

func playCmd(p *player.Client, watchURL string) tea.Cmd {
	return func() tea.Msg {
		if err := p.Play(context.Background(), watchURL); err != nil {
			return errMsg(err)
		}
		return playStartedMsg{}
	}
}

// Update handles messages and key presses.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case subsLoadedMsg:
		m.subs = msg
		return m, nil

	case videosLoadedMsg:
		m.videos = msg
		m.screen = ScreenVideos
		m.cursor = 0
		m.statusMsg = ""
		return m, nil

	case playStartedMsg:
		m.statusMsg = ""
		m.err = nil
		return m, tickCmd()

	case errMsg:
		m.err = msg
		m.statusMsg = ""
		// Defensive: never leave the UI in a state where input mode is set but
		// the modal isn't rendered. If an error happens during or after an
		// input session, drop back to the list view.
		m.inputMode = inputNone
		m.input.Blur()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case subscriptionAddedMsg:
		m.statusMsg = "Subscribed to " + msg.sub.Title
		return m, loadSubsCmd(m.store)

	case addErrMsg:
		m.err = msg.err
		m.statusMsg = ""
		return m, nil

	case tickMsg:
		if m.screen != ScreenPlayer {
			return m, nil
		}

		if m.player.IsIdle() {
			m.screen = ScreenVideos
			m.clampCursor()
			m.statusMsg = ""
			m.err = nil
			m.pos, m.dur, m.paused, m.mediaTitle = 0, 0, false, ""
			return m, nil
		}

		// Refresh cached state from mpv. This is the only place we do IPC
		// reads, and it runs at most twice a second.
		m.pos = m.player.Position()
		m.dur = m.player.Duration()
		m.paused = m.player.Paused()
		m.mediaTitle = m.player.MediaTitle()

		return m, tickCmd()

	}
	return m, nil
}

// tickMsg is emitted periodically while the player screen is active, so the
// UI can refresh position/duration and detect end-of-playback.
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// clampCursor keeps m.cursor inside the bounds of whichever list the current
// screen is showing. Safe to call any time; a no-op when already valid.
func (m *Model) clampCursor() {
	var n int
	switch m.screen {
	case ScreenSubscriptions:
		n = len(m.subs)
	case ScreenVideos:
		n = len(m.videos)
	default:
		m.cursor = 0
		return
	}
	switch {
	case n == 0:
		m.cursor = 0
	case m.cursor < 0:
		m.cursor = 0
	case m.cursor >= n:
		m.cursor = n - 1
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	}

	if m.inputMode != inputNone {
		return m.handleInput(msg)
	}

	if msg.String() == "q" {
		return m, tea.Quit
	}
	m.clampCursor()

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.screen == ScreenPlayer {
			_ = m.player.Stop()
			m.statusMsg = ""
		}
		if m.screen != ScreenSubscriptions {
			m.screen = ScreenSubscriptions
		}
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < m.maxCursor() {
			m.cursor++
		}
		return m, nil

	case "enter":
		if m.screen == ScreenSubscriptions && m.cursor < len(m.subs) {
			sub := m.subs[m.cursor]
			fc := feed.NewClient(m.cfg.CookiesPath)
			return m, loadVideosCmd(fc, sub.ChannelID)
		}
		if m.screen == ScreenVideos && m.cursor < len(m.videos) {
			vid := m.videos[m.cursor]
			m.screen = ScreenPlayer
			m.statusMsg = "Loading " + vid.Title + "…"
			return m, playCmd(m.player, vid.URL)
		}
		return m, nil

	case "a":
		if m.screen == ScreenSubscriptions {
			return m, m.openInput(inputAddSub)
		}
		return m, nil

	case "v":
		// Ad-hoc play: available anywhere except the player screen, where
		// the input would be obscured.
		if m.screen != ScreenPlayer {
			return m, m.openInput(inputPlayURL)
		}
		return m, nil

	case "d":
		if m.screen == ScreenSubscriptions && m.cursor < len(m.subs) {
			sub := m.subs[m.cursor]
			_ = m.store.Remove(sub.ChannelID)
			_ = m.store.Save()
			return m, loadSubsCmd(m.store)
		}
		return m, nil

	case "r":
		fc := feed.NewClient(m.cfg.CookiesPath)
		return m, func() tea.Msg {
			ctx := context.Background()
			if err := m.store.Refresh(ctx, fc); err != nil {
				return errMsg(err)
			}
			return subsLoadedMsg(m.store.List())
		}

	case " ":
		if m.screen == ScreenPlayer {
			m.paused = !m.paused
			return m, pauseCmd(m.player)
		}
		return m, nil
	}
	return m, nil
}

func pauseCmd(p *player.Client) tea.Cmd {
	return func() tea.Msg {
		if err := p.TogglePause(); err != nil {
			return errMsg(err)
		}
		return nil
	}
}

func (m Model) maxCursor() int {
	if m.screen == ScreenSubscriptions {
		if len(m.subs) == 0 {
			return 0
		}
		return len(m.subs) - 1
	}
	if m.screen == ScreenVideos {
		if len(m.videos) == 0 {
			return 0
		}
		return len(m.videos) - 1
	}
	return 0
}

func (m Model) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInput()
		m.err = nil
		m.statusMsg = ""
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil

	case "enter":
		raw := strings.TrimSpace(m.input.Value())
		if raw == "" {
			return m, nil
		}
		mode := m.inputMode
		m.closeInput()

		switch mode {
		case inputAddSub:
			m.statusMsg = "Adding " + raw + "…"
			return m, addSubscriptionCmd(m.store, raw, m.cfg.CookiesPath)

		case inputPlayURL:
			url := normalizeVideoURL(raw)
			m.screen = ScreenPlayer
			m.statusMsg = "Loading…"
			m.err = nil
			return m, playCmd(m.player, url)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// addSubscriptionCmd resolves the input to a channel ID, then adds it to the
// store. Both steps are network-bound, so they run off the UI thread.
func addSubscriptionCmd(store *subscription.Store, raw, cookiesPath string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		channelID, err := feed.ResolveChannelID(ctx, raw)
		if err != nil {
			return addErrMsg{err: err}
		}

		fc := feed.NewClient(cookiesPath)
		sub, err := store.Add(ctx, fc, channelID)
		if err != nil {
			return addErrMsg{err: err}
		}
		if err := store.Save(); err != nil {
			return addErrMsg{err: err}
		}
		return subscriptionAddedMsg{sub: sub}
	}
}

// normalizeVideoURL accepts a full URL, a youtu.be short link, or a bare
// 11-character video ID, and returns something yt-dlp can resolve. Anything
// with a scheme is passed through unchanged; yt-dlp will produce a clear
// error if it's not a valid YouTube URL.
func normalizeVideoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		return raw
	}
	// Bare ID or malformed input — let yt-dlp reject it if wrong.
	return "https://www.youtube.com/watch?v=" + raw
}
