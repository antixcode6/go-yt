package ui

import (
	"context"
	"io"
	"time"

	"github.com/antixcode6/go-yt/internal/player"
)

// graphicsPlayer is a tea.ExecCommand that hands the terminal to mpv for
// the duration of playback. Bubble Tea tears down its renderer before Run
// is called and restores it after Run returns.
type graphicsPlayer struct {
	player *player.Client
	url    string

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (p *graphicsPlayer) SetStdin(r io.Reader)  { p.stdin = r }
func (p *graphicsPlayer) SetStdout(w io.Writer) { p.stdout = w }
func (p *graphicsPlayer) SetStderr(w io.Writer) { p.stderr = w }

// Run starts playback and blocks until mpv reports that playback has ended.
// The polling loop is deliberate: mpv's IPC protocol doesn't give us a
// single "playback ended" event to wait on from this side, so we poll
// idle-active at a low frequency.
func (p *graphicsPlayer) Run() error {
	if err := p.player.Play(context.Background(), p.url); err != nil {
		return err
	}

	// Wait for playback to actually start. mpv is briefly idle after a
	// loadfile command while it resolves the stream, so we can't treat
	// "idle" as "finished" until we've seen it transition to non-idle.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !p.player.IsIdle() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Poll until playback ends.
	for {
		if p.player.IsIdle() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// playbackFinishedMsg is delivered after Run returns. The UI uses it to
// clear the "now playing" state.
type playbackFinishedMsg struct{}
