package ui

import (
	"io"

	"github.com/antixcode6/go-yt/internal/player"
)

// graphicsPlayer is a tea.ExecCommand that hands the terminal to mpv for the
// duration of playback (kitty/sixel video). Bubble Tea stops drawing before Run
// is called and takes the terminal back after Run returns.
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

// Run plays the video in an mpv attached to the terminal and returns when it
// exits. mpv handles the keys itself (space pauses, arrows seek, q quits).
func (p *graphicsPlayer) Run() error {
	return p.player.PlayInTerminal(p.url, p.stdin, p.stdout)
}

// playbackFinishedMsg is delivered after Run returns; err says why playback
// failed, if it did.
type playbackFinishedMsg struct{ err error }
