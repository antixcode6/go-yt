package player

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jannson/mpvipc"
	"github.com/lrstanley/go-ytdlp"
)

type StreamSet struct {
	Title       string
	CombinedURL string
	VideoURL    string
	AudioURL    string
}

func (s *StreamSet) IsSeparate() bool {
	return s.VideoURL != "" && s.AudioURL != ""
}

type Options struct {
	VideoOutput string
	AudioOnly   bool
	CookiesPath string

	Protocol GraphicsProtocol
	Cols     int
	Rows     int
}

type Client struct {
	conn    *mpvipc.Connection
	ytdlp   *ytdlp.Command
	proc    *exec.Cmd
	cookies string
}

// New spawns a child mpv process and opens an IPC connection to it.
func New(socketPath string, opts Options) (*Client, error) {
	_ = os.Remove(socketPath)

	args := []string{
		"--idle=yes",
		"--no-terminal",
		"--input-ipc-server=" + socketPath,
	}

	switch opts.Protocol {
	case ProtocolKitty:
		args = append(args,
			"--vo=kitty",
			"--vo-kitty-alt-screen=yes",
			"--vo-kitty-config-clear=yes",
			"--vo-kitty-cols="+strconv.Itoa(opts.Cols),
			"--vo-kitty-rows="+strconv.Itoa(opts.Rows),
		)
		// Shared-memory transfer is much faster but only works locally
		// and isn't supported on Windows or over SSH.
		if os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
			args = append(args, "--vo-kitty-use-shm=yes")
		}
		args = append(args, "--really-quiet", "--profile=sw-fast")

	case ProtocolSixel:
		args = append(args,
			"--vo=sixel",
			"--vo-sixel-alt-screen=no",
			"--vo-sixel-buffered=yes",
			"--vo-sixel-reqcolors=0",
			"--vo-sixel-cols="+strconv.Itoa(opts.Cols),
			"--vo-sixel-rows="+strconv.Itoa(opts.Rows),
			"--really-quiet",
			"--profile=sw-fast",
		)

	default:
		// Fallback to a window.
		args = append(args, "--vo=gpu")
	}

	cmd := exec.Command("mpv", args...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn mpv: %w", err)
	}

	if err := waitForSocket(socketPath, 3*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	conn := mpvipc.NewConnection(socketPath)
	if err := conn.Open(); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("open ipc connection: %w", err)
	}

	return &Client{
		conn:    conn,
		ytdlp:   ytdlp.New(),
		proc:    cmd,
		cookies: opts.CookiesPath,
	}, nil
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("mpv socket %s did not appear within %s", path, timeout)
}

// call sends an mpv command and waits for the response. Unlike blang/mpv,
// mpvipc has no hardcoded timeout — Call blocks until mpv replies. This is
// why large videos no longer fail with "timeout while receiving response".
func (c *Client) call(args ...interface{}) error {
	_, err := c.conn.Call(args...)
	return err
}

// getString reads a string property, returning "" on any error.
func (c *Client) getString(property string) string {
	v, err := c.conn.Get(property)
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// getFloat reads a float64 property, returning 0 on any error.
func (c *Client) getFloat(property string) float64 {
	v, err := c.conn.Get(property)
	if err != nil {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// getBool reads a bool property, returning false on any error.
func (c *Client) getBool(property string) bool {
	v, err := c.conn.Get(property)
	if err != nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

func (c *Client) ResolveStreams(ctx context.Context, watchURL string) (*StreamSet, error) {
	cmd := c.withCookies().
		Format("bv*+ba/b").
		PrintJSON().
		Simulate().
		NoPlaylist()

	result, err := cmd.Run(ctx, watchURL)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp resolve %s: %w", watchURL, err)
	}

	infos, err := result.GetExtractedInfo()
	if err != nil || len(infos) == 0 {
		return nil, fmt.Errorf("no extracted info for %s", watchURL)
	}
	info := infos[0]

	title := ""
	if info.Title != nil {
		title = *info.Title
	}

	if info.URL != nil && *info.URL != "" {
		return &StreamSet{Title: title, CombinedURL: *info.URL}, nil
	}

	if len(info.RequestedFormats) < 2 {
		return nil, fmt.Errorf("no playable streams for %s (url empty, requested_formats=%d)",
			watchURL, len(info.RequestedFormats))
	}

	set := &StreamSet{Title: title}
	for _, f := range info.RequestedFormats {
		if f.URL == "" {
			continue
		}
		if f.VCodec == nil || *f.VCodec == "none" {
			set.AudioURL = f.URL
		} else if set.VideoURL == "" {
			set.VideoURL = f.URL
		}
	}
	if !set.IsSeparate() {
		return nil, fmt.Errorf("incomplete stream set for %s (video=%v audio=%v)",
			watchURL, set.VideoURL != "", set.AudioURL != "")
	}
	return set, nil
}

func (c *Client) Play(ctx context.Context, watchURL string) error {
	set, err := c.ResolveStreams(ctx, watchURL)
	if err != nil {
		return err
	}

	if set.Title != "" {
		_ = c.conn.Set("force-media-title", set.Title)
	}

	if set.CombinedURL != "" {
		return c.call("loadfile", set.CombinedURL, "replace")
	}

	return c.call("loadfile", set.VideoURL, "replace", -1,
		"audio-file="+set.AudioURL)
}

func (c *Client) Stop() error        { return c.call("stop") }
func (c *Client) TogglePause() error { return c.call("cycle", "pause") }

func (c *Client) Seek(seconds float64) error {
	return c.call("seek", seconds, "absolute")
}

func (c *Client) SeekRelative(delta float64) error {
	return c.call("seek", delta, "relative")
}

func (c *Client) SetVolume(percent int) error {
	return c.conn.Set("volume", percent)
}

func (c *Client) Position() float64 {
	return c.getFloat("time-pos")
}

func (c *Client) Duration() float64 {
	return c.getFloat("duration")
}

func (c *Client) Paused() bool {
	return c.getBool("pause")
}

func (c *Client) IsIdle() bool {
	return c.getBool("idle-active")
}

func (c *Client) MediaTitle() string {
	return c.getString("media-title")
}

func (c *Client) withCookies() *ytdlp.Command {
	cmd := c.ytdlp
	if c.cookies != "" {
		cmd = cmd.Cookies(c.cookies)
	}
	return cmd
}

// Close disconnects from mpv's IPC socket, then terminates the child process.
func (c *Client) Close() error {
	if c.conn != nil && !c.conn.IsClosed() {
		_ = c.call("quit")
		_ = c.conn.Close()
	}

	if c.proc != nil && c.proc.Process != nil {
		done := make(chan error, 1)
		go func() { done <- c.proc.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.proc.Process.Kill()
		}
	}
	return nil
}

// GraphicsProtocol identifies the terminal's native inline-graphics protocol.
type GraphicsProtocol int

const (
	ProtocolNone GraphicsProtocol = iota
	ProtocolKitty
	ProtocolSixel
)

// DetectProtocol returns the best graphics protocol the current terminal
// supports, based on environment variables. It does not probe the terminal
// with escape sequences — env vars are reliable enough for the terminals we
// care about and avoid the complexity of tty round-trips.
func DetectProtocol() GraphicsProtocol {
	term := os.Getenv("TERM")
	termProgram := os.Getenv("TERM_PROGRAM")

	// Kitty graphics: kitty, WezTerm, Ghostty.
	switch {
	case strings.Contains(term, "kitty"):
		return ProtocolKitty
	case termProgram == "WezTerm" || termProgram == "ghostty":
		return ProtocolKitty
	}

	// Sixel: foot, mlterm, contour, WezTerm (fallback), xterm with sixel.
	switch {
	case termProgram == "foot" || strings.HasPrefix(term, "foot"):
		return ProtocolSixel
	case strings.HasPrefix(term, "mlterm"):
		return ProtocolSixel
	case strings.HasPrefix(term, "contour"):
		return ProtocolSixel
	case strings.Contains(term, "sixel"):
		return ProtocolSixel
	}

	return ProtocolNone
}
