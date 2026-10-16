package ui

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/antixcode6/go-yt/internal/feed"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// Msg types for async operations.
type (
	subsLoadedMsg   []*subscription.Subscription
	videosLoadedMsg []*feed.Video
	playStartedMsg  struct{}
	errMsg          error
)

type discoverVideosMsg struct {
	videos []*feed.Video
	title  string
	note   string // non-fatal: some entries were skipped
}

type subscriptionAddedMsg struct {
	sub *subscription.Subscription
}

type addErrMsg struct {
	err error
}

// playerEventMsg carries something the player wants the UI to show: a load
// failure, a retry without cookies, or an unexpected mpv exit.
type playerEventMsg player.Event

// waitPlayerEventCmd blocks until the player reports something. Update
// re-issues it after every event, so exactly one is always waiting.
func waitPlayerEventCmd(p *player.Client) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-p.Events():
			return playerEventMsg(ev)
		case <-p.Done():
			return nil
		}
	}
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadSubsCmd(m.store),
		tea.EnterAltScreen,
		waitPlayerEventCmd(m.player),
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

// startPlayback plays a video the way the configured video output needs.
// With kitty/sixel, mpv must own the terminal while it plays, so the TUI steps
// aside (tea.Exec). Otherwise mpv shows its own window and the TUI keeps its
// player screen.
func (m Model) startPlayback(url, status string) (tea.Model, tea.Cmd) {
	m.err = nil
	if m.player.Terminal() {
		m.statusMsg = ""
		return m, tea.Exec(
			&graphicsPlayer{player: m.player, url: url},
			func(err error) tea.Msg { return playbackFinishedMsg{err: err} },
		)
	}
	m.screen = ScreenPlayer
	m.playbackStarted = false
	m.statusMsg = status
	return m, playCmd(m.player, url)
}

func playCmd(p *player.Client, watchURL string) tea.Cmd {
	return func() tea.Msg {
		if err := p.Play(context.Background(), watchURL); err != nil {
			return errMsg(err)
		}
		return playStartedMsg{}
	}
}

func fetchSearchResultsCmd(fc *feed.Client, feedURL, limit string) tea.Cmd {
	return func() tea.Msg {
		// ytsearch is a yt-dlp pseudo-URL. The number prefix limits results.
		videos, err := fc.FetchHomeFeed(context.Background(), feedURL, limit)
		note, err := partialNote(err)
		if err != nil {
			return errMsg(err)
		}
		return discoverVideosMsg{
			videos: videos,
			title:  feedURL,
			note:   note,
		}
	}
}

func fetchFeedCmd(fc *feed.Client, feedURL, limit string) tea.Cmd {
	return func() tea.Msg {
		videos, err := fc.FetchHomeFeed(context.Background(), feedURL, limit)
		note, err := partialNote(err)
		if err != nil {
			return errMsg(err)
		}
		return discoverVideosMsg{videos: videos, note: note}
	}
}

// partialNote turns a feed.PartialError (videos loaded, some entries skipped)
// into a status note; any other error is returned unchanged.
func partialNote(err error) (string, error) {
	var pe *feed.PartialError
	if errors.As(err, &pe) {
		return pe.Error(), nil
	}
	return "", err
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
		m.loading = false
		m.videos = msg
		m.showChannel = false // single-channel view
		m.screen = ScreenVideos
		m.cursor = 0
		m.offset = 0
		m.statusMsg = ""
		m.err = nil
		return m, nil

	case discoverVideosMsg:
		m.loading = false
		m.videos = msg.videos
		m.showChannel = true // mixed-channel view
		m.screen = ScreenVideos
		m.cursor = 0
		m.offset = 0
		m.statusMsg = msg.note
		m.err = nil
		return m, nil

	case playStartedMsg:
		m.statusMsg = ""
		m.err = nil
		m.playbackStarted = false
		return m, tickCmd()

	case playbackFinishedMsg:
		m.playbackStarted = false
		m.statusMsg = ""
		m.err = msg.err
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg
		m.statusMsg = ""
		return m, nil

	case playerEventMsg:
		if msg.Kind == player.EventRetrying {
			// Still loading; just say what is happening.
			m.err = nil
			m.statusMsg = msg.Message
		} else {
			// The load failed for good (or mpv died). Show why and leave the
			// player screen, which would otherwise wait forever for a duration.
			m.loading = false
			m.statusMsg = ""
			m.err = fmt.Errorf("%s", msg.Message)
			if m.screen == ScreenPlayer {
				m.screen = ScreenVideos
				m.clampCursor()
			}
			m.pos, m.dur, m.paused, m.mediaTitle = 0, 0, false, ""
			m.playbackStarted = false
		}
		return m, waitPlayerEventCmd(m.player)

	case spinner.TickMsg:
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)

		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)

	case subscriptionAddedMsg:
		m.statusMsg = "Subscribed to " + msg.sub.Title
		return m, loadSubsCmd(m.store)

	case addErrMsg:
		m.err = msg.err
		m.statusMsg = ""
		return m, nil

	case subsImportedMsg:
		m.loading = false
		m.statusMsg = fmt.Sprintf("Imported %d, skipped %d, failed %d",
			msg.Added, msg.Skipped, msg.Failed)
		if len(msg.Failures) > 0 {
			// Show the first few failures in the status area.
			shown := msg.Failures
			if len(shown) > 3 {
				shown = shown[:3]
			}
			m.err = fmt.Errorf("failed: %s", strings.Join(shown, ", "))
		}
		return m, loadSubsCmd(m.store)

	case tickMsg:
		if m.screen != ScreenPlayer {
			return m, nil
		}

		// Refresh cached state from mpv.
		m.pos = m.player.Position()
		m.dur = m.player.Duration()
		m.paused = m.player.Paused()
		m.mediaTitle = m.player.MediaTitle()

		// Mark playback as started once mpv has a duration. Before that, the
		// file is still resolving/buffering and IsIdle() will be true even
		// though nothing has gone wrong.
		if !m.playbackStarted && m.dur > 0 {
			m.playbackStarted = true
			m.statusMsg = "" // drop any "retrying without cookies" notice
		}

		// Only treat idle as "finished" after playback has actually begun.
		if m.playbackStarted && m.player.IsIdle() {
			m.screen = ScreenVideos
			m.clampCursor()
			m.statusMsg = ""
			m.err = nil
			m.pos, m.dur, m.paused, m.mediaTitle = 0, 0, false, ""
			m.playbackStarted = false
			return m, nil
		}

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
	case ScreenDiscover:
		n = len(m.discoverSources)
	default:
		m.cursor = 0
		m.offset = 0
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

	// Scroll the window to keep the cursor visible.
	visible := m.visibleItems()
	switch {
	case m.cursor < m.offset:
		m.offset = m.cursor
	case m.cursor >= m.offset+visible:
		m.offset = m.cursor - visible + 1
	}

	// Clamp offset so we don't scroll past the end.
	maxOffset := n - visible
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if m.loading {
		if msg.String() == "esc" {
			m.loading = false
		}
		return m, nil
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
		m.clampCursor()
		return m, nil

	case "down", "j":
		if m.cursor < m.maxCursor() {
			m.cursor++
		}
		m.clampCursor()
		return m, nil

	case "pgdown", "ctrl+d":
		m.cursor += m.visibleItems()
		m.clampCursor()
		return m, nil

	case "pgup", "ctrl+u":
		m.cursor -= m.visibleItems()
		m.clampCursor()
		return m, nil

	case "home", "g":
		m.cursor = 0
		m.clampCursor()
		return m, nil

	case "end", "G":
		m.cursor = m.maxCursor()
		m.clampCursor()
		return m, nil

	case "enter":
		if m.screen == ScreenSubscriptions && m.cursor < len(m.subs) {
			sub := m.subs[m.cursor]
			m.selectedSub = m.cursor
			fc := feed.NewClient(m.cfg.CookiesPath)
			m.loading = true
			m.loadingMsg = pickLoadFlavor(sub.Title)
			m.err = nil
			return m, tea.Batch(loadVideosCmd(fc, sub.ChannelID), m.spinner.Tick)
		}

		if m.screen == ScreenVideos && m.cursor < len(m.videos) {
			vid := m.videos[m.cursor]
			return m.startPlayback(vid.URL, "Loading "+vid.Title+"…")
		}

		if m.screen == ScreenDiscover {
			if m.cursor >= len(m.discoverSources) {
				return m, nil
			}
			selected := m.discoverSources[m.cursor]
			fc := feed.NewClient(m.cfg.CookiesPath)

			switch selected {
			case "Subscriptions Feed (Personalized)":
				m.loading = true
				m.loadingMsg = pickLoadFlavor("Loading subscriptions feed…")
				m.videoListTitle = "Subscriptions Feed"
				m.showChannel = true
				return m, tea.Batch(fetchFeedCmd(fc, ":ytsubs", ":20"), m.spinner.Tick)
			case "Watch History (Personalized)":
				m.loading = true
				m.loadingMsg = "Loading watch history…" // uses slice logic, limit = 20 returns index 20, limit = :20 returns up to index 20
				return m, tea.Batch(fetchFeedCmd(fc, ":ythistory", ":20"), m.spinner.Tick)
			case "Search YouTube":
				return m, m.openInput(inputSearch)
			case "Local Recommendations":
				m.loading = true
				m.loadingMsg = pickLoadFlavor("Building local recommendations…")
				return m, m.fetchLocalRecommendationsCmd()
			}
			return m, nil
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

	case "D":
		if m.screen == ScreenSubscriptions {
			m.screen = ScreenDiscover
			m.cursor = 0 // ← was m.discoverCursor = 0
			return m, nil
		}
		return m, nil

	case "i":
		if m.screen != ScreenPlayer {
			return m, m.openInput(inputImportSubs)
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
	switch m.screen {
	case ScreenSubscriptions:
		if len(m.subs) == 0 {
			return 0
		}
		return len(m.subs) - 1
	case ScreenVideos:
		if len(m.videos) == 0 {
			return 0
		}
		return len(m.videos) - 1
	case ScreenDiscover:
		return len(m.discoverSources) - 1
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
			return m.startPlayback(normalizeVideoURL(raw), "Loading…")

		case inputSearch:
			m.screen = ScreenVideos
			m.cursor = 0
			m.offset = 0
			m.statusMsg = ""
			m.err = nil
			m.loading = true
			m.loadingMsg = pickLoadFlavor("Searching for " + raw)
			fc := feed.NewClient(m.cfg.CookiesPath)
			return m, tea.Batch(
				fetchSearchResultsCmd(fc, raw, ":20"),
				m.spinner.Tick,
			)

		case inputImportSubs:
			path := expandHome(raw) // ← use raw, not m.input.Value()
			m.loading = true
			m.loadingMsg = "Importing subscriptions…"
			m.err = nil
			return m, tea.Batch(importSubsCmd(m.store, path), m.spinner.Tick)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

type subsImportedMsg struct {
	Added, Skipped, Failed int
	Failures               []string
}

func importSubsCmd(store *subscription.Store, path string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		result, err := subscription.ImportNewPipe(ctx, store, path)
		if err != nil {
			return errMsg(err)
		}
		return subsImportedMsg{
			Added:    result.Added,
			Skipped:  result.Skipped,
			Failed:   result.Failed,
			Failures: result.Failures,
		}
	}
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

// fetchLocalRecommendationsCmd synthesizes a recommendation list from the
// user's subscriptions. No network calls — it just interleaves the cached
// video lists from each channel, newest first, round-robin style so no
// single channel dominates the top of the feed.
func (m Model) fetchLocalRecommendationsCmd() tea.Cmd {
	// Copy the slice header so the goroutine doesn't race with later updates
	// to m.subs. The underlying Subscription pointers are read-only here.
	subs := append([]*subscription.Subscription(nil), m.subs...)
	return func() tea.Msg {
		videos := synthesizeRecommendations(subs, 30)
		return discoverVideosMsg{
			videos: videos,
			title:  "Local Recommendations",
		}
	}
}

// synthesizeRecommendations interleaves each channel's cached videos,
// taking the newest from each in turn, until limit is reached or all
// channels are exhausted. Deduplicates by video ID in case a video appears
// under multiple channels (collabs, features, etc.).
func synthesizeRecommendations(subs []*subscription.Subscription, limit int) []*feed.Video {
	seen := make(map[string]bool)
	out := make([]*feed.Video, 0, limit)

	// Find the longest video list so we know how many rounds to run.
	maxLen := 0
	for _, sub := range subs {
		if len(sub.Videos) > maxLen {
			maxLen = len(sub.Videos)
		}
	}

	for i := 0; i < maxLen && len(out) < limit; i++ {
		for _, sub := range subs {
			if i >= len(sub.Videos) {
				continue
			}
			v := sub.Videos[i]
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			out = append(out, v)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (m Model) visibleRows() int {
	const chrome = 6
	n := m.height - chrome
	if n < 3 {
		return 3
	}
	return n
}

// loadFlavorLines are shown alongside the spinner while a fetch is in
// progress. Picked once per load so the message is stable while the user
// waits, but varies across loads.
var loadFlavorLines = []string{
	"Tuning the antenna…",
	"Asking YouTube nicely…",
	"Warming up the tubes…",
	"Reassembling the packets…",
	"Waking up the hamsters…",
	"Polishing the pixels…",
	"Paging the CDN…",
}

// pickLoadFlavor returns a random flavor line, optionally prefixed with the
// thing being loaded. Pass an empty string for a context-free message.
func pickLoadFlavor(context string) string {
	line := loadFlavorLines[rand.IntN(len(loadFlavorLines))]
	if context != "" {
		return context + " — " + line
	}
	return line
}

// truncate returns s shortened to at most n runes, with an ellipsis if it
// was cut. Rune-aware so multi-byte characters don't produce invalid UTF-8.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func (m Model) visibleItems() int {
	rows := m.visibleRows()
	if m.screen == ScreenVideos && m.showChannel {
		rows = rows / 2
	}
	if rows < 1 {
		return 1
	}
	return rows
}
