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
}

type Client struct {
	conn    *mpvipc.Connection
	proc    *exec.Cmd
	cookies string

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

	_ = os.Remove(socketPath)

	args := []string{
		"--idle=yes",
		"--no-terminal",
		"--really-quiet",
		"--input-ipc-server=" + socketPath,
		"--ytdl-format=bv*+ba/b",
	}
	if opts.VideoOutput != "" {
		args = append(args, "--vo="+opts.VideoOutput)
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

	c := &Client{
		proc:    cmd,
		cookies: opts.CookiesPath,
		exited:  make(chan error, 1),
		events:  make(chan Event, 16),
		done:    make(chan struct{}),
	}

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

// Play asks mpv to load a watch URL. mpv resolves it asynchronously through
// yt-dlp, so a nil return only means the request was accepted; failures
// arrive later on Events().
//
// If a cookies file is configured it is used for this attempt only. When the
// load fails, the client retries once without cookies (see Event).
func (c *Client) Play(ctx context.Context, watchURL string) error {
	c.mu.Lock()
	c.cur = loadState{url: watchURL, withCookies: c.cookies != ""}
	c.mu.Unlock()

	if c.cookies != "" {
		return c.call("loadfile", watchURL, "replace", -1,
			"ytdl-raw-options=cookies="+c.cookies)
	}
	return c.call("loadfile", watchURL, "replace")
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
