package player

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jannson/mpvipc"
)

// mpvBinary is the executable New starts. Tests replace it with a fake.
var mpvBinary = "mpv"

// logHint is appended to startup errors, since mpv runs with --no-terminal
// and would otherwise explain nothing.
const logHint = "set GO_YT_MPV_LOG=/tmp/mpv.log and rerun to see why"

type Options struct {
	VideoOutput string
	AudioOnly   bool
	CookiesPath string

	// FallbackClients are yt-dlp YouTube player clients (web_safari,
	// android_vr, ...) tried one by one when YouTube withholds the stream.
	FallbackClients []string
}

type Client struct {
	conn      *mpvipc.Connection
	proc      *exec.Cmd
	cookies   string
	fallbacks []string // yt-dlp player clients to fall back to

	protocol    GraphicsProtocol // terminal graphics in use, or ProtocolNone
	videoOutput string           // video_output from the config
	audioOnly   bool
	termArgs    []string // mpv arguments for PlayInTerminal

	evConn    net.Conn      // second IPC connection, used only to read events
	exited    chan error    // receives mpv's exit status once the process ends
	events    chan Event    // load failures, retries, unexpected exits
	done      chan struct{} // closed by Close
	closeOnce sync.Once

	mu  sync.Mutex // guards cur
	cur loadState
}

// New spawns a child mpv process and opens an IPC connection to it.
//
// Playback URLs are resolved by mpv's own ytdl hook (system yt-dlp), so mpv
// applies yt-dlp's headers and picks the formats. Set GO_YT_MPV_LOG to a file
// path to get mpv's verbose log while debugging.
func New(socketPath string, opts Options) (*Client, error) {
	// The cookies path is passed through mpv's option syntax, where ',' and
	// '%' are special.
	if strings.ContainsAny(opts.CookiesPath, ",%") {
		return nil, fmt.Errorf("cookies path %q contains ',' or '%%', which mpv's option syntax cannot carry; move the file", opts.CookiesPath)
	}

	for _, name := range opts.FallbackClients {
		if !clientNameRe.MatchString(name) {
			return nil, fmt.Errorf("ytdlp_fallback_clients: %q is not a yt-dlp player client name (use names like web_safari)", name)
		}
	}

	_ = os.Remove(socketPath)

	c := &Client{
		cookies:     opts.CookiesPath,
		fallbacks:   opts.FallbackClients,
		protocol:    ResolveProtocol(opts.VideoOutput, opts.AudioOnly),
		videoOutput: opts.VideoOutput,
		audioOnly:   opts.AudioOnly,
		exited:      make(chan error, 1),
		events:      make(chan Event, 16),
		done:        make(chan struct{}),
	}

	// Kitty/sixel: mpv has to draw on the real terminal, which only a process
	// attached to it can do. There is no long-lived mpv in this mode; each
	// video gets its own (see PlayInTerminal).
	if c.protocol != ProtocolNone {
		if _, err := exec.LookPath(mpvBinary); err != nil {
			return nil, fmt.Errorf("find mpv: %w", err)
		}
		c.termArgs = terminalArgs(c.protocol)
		return c, nil
	}

	args := []string{
		"--idle=yes",
		"--no-terminal",
		"--input-ipc-server=" + socketPath,
		"--ytdl-format=bv*+ba/b",
	}
	if opts.VideoOutput != "" && opts.VideoOutput != "auto" {
		args = append(args, "--vo="+opts.VideoOutput)
	} else if opts.VideoOutput == "auto" {
		args = append(args, "--vo=gpu") // no terminal graphics detected: use a window
	}
	if opts.AudioOnly {
		args = append(args, "--vid=no")
	}
	if logPath := os.Getenv("GO_YT_MPV_LOG"); logPath != "" {
		args = append(args, "--log-file="+logPath, "--msg-level=all=v")
	}

	cmd := exec.Command(mpvBinary, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn mpv: %w", err)
	}
	c.proc = cmd

	// Reap the process and report it if it dies while go-yt is still running.
	go func() {
		err := cmd.Wait()
		c.exited <- err
		select {
		case <-c.done:
		default:
			c.emit(Event{
				Kind:    EventMPVExited,
				Message: fmt.Sprintf("mpv exited unexpectedly (%s); restart go-yt (%s)", exitStatus(err), logHint),
			})
		}
	}()

	evConn, err := dialSocket(socketPath, 3*time.Second, c.exited)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	conn := mpvipc.NewConnection(socketPath)
	if err := conn.Open(); err != nil {
		_ = evConn.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("open ipc connection: %w", err)
	}

	c.conn = conn
	c.evConn = evConn

	// Ask mpv to forward its warning and error log lines to this connection;
	// they carry yt-dlp's explanation when a load fails.
	_, _ = evConn.Write([]byte(`{"command":["request_log_messages","warn"]}` + "\n"))
	go c.watch(evConn)

	return c, nil
}

// dialSocket connects to mpv's IPC socket, giving up early if mpv has already
// exited. Dialing (rather than just stat-ing the path) also avoids racing mpv
// between creating the socket and listening on it.
func dialSocket(path string, timeout time.Duration, exited <-chan error) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("mpv exited before its IPC socket appeared (%s); %s", exitStatus(err), logHint)
		default:
		}
		if nc, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			return nc, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("mpv socket %s did not accept connections within %s; %s", path, timeout, logHint)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// call sends an mpv command and waits for the response. Unlike blang/mpv,
// mpvipc has no hardcoded timeout — Call blocks until mpv replies. This is
// why large videos no longer fail with "timeout while receiving response".
func (c *Client) call(args ...any) error {
	if c.conn == nil {
		return errNoIPC
	}
	_, err := c.conn.Call(args...)
	return err
}

// getString reads a string property, returning "" on any error.
func (c *Client) getString(property string) string {
	if c.conn == nil {
		return ""
	}
	v, err := c.conn.Get(property)
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// getFloat reads a float64 property, returning 0 on any error.
func (c *Client) getFloat(property string) float64 {
	if c.conn == nil {
		return 0
	}
	v, err := c.conn.Get(property)
	if err != nil {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// getBool reads a bool property, returning false on any error.
func (c *Client) getBool(property string) bool {
	if c.conn == nil {
		return false
	}
	v, err := c.conn.Get(property)
	if err != nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Play asks mpv to load a watch URL. mpv resolves it asynchronously through
// yt-dlp, so a nil return only means the request was accepted; failures
// arrive later on Events().
//
// When the load fails, the client tries again the next way (without cookies,
// then each fallback client) and reports progress on Events().
func (c *Client) Play(ctx context.Context, watchURL string) error {
	c.mu.Lock()
	c.cur = loadState{url: watchURL}
	c.mu.Unlock()

	return c.loadWith(watchURL, c.plan()[0])
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
	if c.conn == nil {
		return errNoIPC
	}
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

// Close disconnects from mpv's IPC socket, then terminates the child process.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.done) })

	if c.conn != nil && !c.conn.IsClosed() {
		_ = c.call("quit")
		_ = c.conn.Close()
	}
	if c.evConn != nil {
		_ = c.evConn.Close()
	}

	if c.proc != nil && c.proc.Process != nil {
		select {
		case <-c.exited:
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

	// Inside tmux/screen the graphics escape codes do not reach the terminal
	// unless passthrough is set up, so fall back to a window.
	if os.Getenv("TMUX") != "" || strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux") {
		return ProtocolNone
	}

	// Kitty graphics: kitty, WezTerm, Ghostty. kitty sets KITTY_WINDOW_ID
	// even when TERM is overridden (xterm-256color is a common override).
	switch {
	case strings.Contains(term, "kitty"), os.Getenv("KITTY_WINDOW_ID") != "":
		return ProtocolKitty
	case termProgram == "WezTerm", termProgram == "ghostty", strings.Contains(term, "ghostty"):
		return ProtocolKitty
	}

	// Sixel: foot, mlterm, contour, xterm with sixel.
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
