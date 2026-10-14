package player

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var errNoIPC = errors.New("no long-lived mpv in terminal-graphics mode")

// ResolveProtocol maps the video_output setting to a terminal graphics
// protocol: "kitty" and "sixel" force one, "auto" detects it from the
// environment, and anything else (gpu, tct, null, ...) means no terminal
// graphics, so mpv gets that --vo as before. Audio-only never uses graphics.
func ResolveProtocol(videoOutput string, audioOnly bool) GraphicsProtocol {
	if audioOnly {
		return ProtocolNone
	}
	switch videoOutput {
	case "kitty":
		return ProtocolKitty
	case "sixel":
		return ProtocolSixel
	case "auto":
		return DetectProtocol()
	}
	return ProtocolNone
}

// Terminal reports whether videos are drawn in the terminal. If so, play them
// with PlayInTerminal, not Play.
func (c *Client) Terminal() bool { return c.protocol != ProtocolNone }

// terminalArgs are the mpv options for drawing video in the terminal. The
// terminal size is left to mpv, which reads it from its stdout at start.
func terminalArgs(p GraphicsProtocol) []string {
	args := []string{
		"--ytdl-format=bv*+ba/b",
		// Messages on stdout would be mixed into the picture.
		"--really-quiet",
		"--profile=sw-fast",
		// One mpv per video: it must exit when playback ends or is stopped,
		// even if the user's mpv.conf says idle=yes or keep-open=yes.
		"--idle=no",
		"--keep-open=no",
		"--force-window=no",
	}
	switch p {
	case ProtocolKitty:
		args = append(args, "--vo=kitty", "--vo-kitty-alt-screen=yes", "--vo-kitty-config-clear=yes")
		// Shared memory is faster, but if the terminal does not take it the
		// picture is just blank, with no error anywhere. So it is opt-in
		// (GO_YT_KITTY_SHM=1), and local-only.
		if os.Getenv("GO_YT_KITTY_SHM") == "1" && os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
			args = append(args, "--vo-kitty-use-shm=yes")
		}
	case ProtocolSixel:
		args = append(args, "--vo=sixel", "--vo-sixel-alt-screen=no", "--vo-sixel-buffered=yes")
	}
	return args
}

// PlayInTerminal plays one video with its own mpv, attached to the terminal
// given by in/out (what tea.Exec passes to an ExecCommand), and returns when
// mpv exits. mpv draws the picture on out and reads keys (space, arrows, q)
// from in, so nothing else may use the terminal meanwhile.
//
// If a cookies file is configured and the load fails, it plays again without
// it. A load failure is returned as an error holding yt-dlp's reason.
func (c *Client) PlayInTerminal(url string, in io.Reader, out io.Writer) error {
	if !c.Terminal() {
		return errors.New("PlayInTerminal needs a kitty or sixel video_output")
	}
	stdin, stdout := asFile(in, os.Stdin), asFile(out, os.Stdout)

	// mpv's own messages are off, so its log file is how we learn what went
	// wrong. GO_YT_MPV_LOG keeps it for debugging.
	logPath := os.Getenv("GO_YT_MPV_LOG")
	if logPath == "" {
		f, err := os.CreateTemp("", "go-yt-mpv-*.log")
		if err != nil {
			return err
		}
		logPath = f.Name()
		_ = f.Close()
		defer os.Remove(logPath)
	}

	r := c.runMPV(url, c.cookies, logPath, stdin, stdout)
	if r.err != nil || r.reason == "" {
		return r.err
	}
	if c.cookies == "" || !r.loadFailure {
		return errors.New("Could not play this video: " + r.reason)
	}

	plain := c.runMPV(url, "", logPath, stdin, stdout)
	switch {
	case plain.err != nil || plain.reason == "":
		return plain.err
	case plain.reason == r.reason:
		return errors.New("Could not play this video, with or without cookies: " + r.reason)
	}
	return fmt.Errorf("Could not play this video. Without cookies: %s. With cookies: %s.", plain.reason, r.reason)
}

type mpvRun struct {
	err         error  // could not run mpv at all
	reason      string // set if playback failed
	loadFailure bool   // the video itself would not load (worth retrying)
}

func (c *Client) runMPV(url, cookies, logPath string, stdin, stdout *os.File) mpvRun {
	args := append([]string(nil), c.termArgs...)
	args = append(args, "--log-file="+logPath)
	if cookies != "" {
		args = append(args, "--ytdl-raw-options=cookies="+cookies)
	}
	args = append(args, "--", url)

	cmd := exec.Command(mpvBinary, args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout // Stderr stays /dev/null: it would corrupt the picture

	err := cmd.Run()
	c.resetTerminal(stdout)
	if err == nil {
		return mpvRun{}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return mpvRun{err: fmt.Errorf("run mpv: %w", err)}
	}

	// mpv's exit codes: 1 = could not start (also for unknown options),
	// 2 = the file could not be played, 3 = some could not. 0 and 4 (quit by
	// signal or Ctrl+C) are normal; -1 means we were killed by a signal.
	switch ee.ExitCode() {
	case 1:
		reason := failureReason(readLogProblems(logPath))
		if strings.HasPrefix(reason, "mpv could not load it") {
			reason = "mpv failed to start; check that your mpv supports the video output (mpv --vo=help)"
		}
		return mpvRun{reason: reason}
	case 2, 3:
		return mpvRun{reason: failureReason(readLogProblems(logPath)), loadFailure: true}
	}
	return mpvRun{}
}

func asFile(v interface{}, fallback *os.File) *os.File {
	if f, ok := v.(*os.File); ok && f != nil {
		return f
	}
	return fallback
}

// logLineRe matches mpv log lines such as
// "[  12.493][e][ytdl_hook] ERROR: ...".
var logLineRe = regexp.MustCompile(`^\[\s*[\d.]+\]\[(\w)\]\[([^\]]+)\] (.*)$`)

// readLogProblems returns the fatal, error and warning lines of an mpv log.
func readLogProblems(path string) []logLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []logLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		m := logLineRe.FindStringSubmatch(sc.Text())
		if m == nil || !strings.Contains("few", m[1]) {
			continue
		}
		lines = append(lines, logLine{prefix: m[2], text: strings.TrimSpace(m[3])})
	}
	return lines
}

// VideoMode describes in one line how video will be shown, and why, so a
// wrong choice is visible without a debugger.
func (c *Client) VideoMode() string {
	switch {
	case c.audioOnly:
		return "video: off (audio_only)"
	case c.protocol == ProtocolKitty:
		return "video: kitty graphics in this terminal"
	case c.protocol == ProtocolSixel:
		return "video: sixel graphics in this terminal"
	case c.videoOutput == "auto":
		return fmt.Sprintf("video: mpv window; no kitty/sixel terminal detected (TERM=%q, KITTY_WINDOW_ID set: %v, inside tmux/screen: %v)",
			os.Getenv("TERM"), os.Getenv("KITTY_WINDOW_ID") != "", os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen"))
	case c.videoOutput == "":
		return "video: mpv default output"
	}
	return fmt.Sprintf(`video: mpv --vo=%s (video_output in config.json; set it to "auto" for kitty/sixel)`, c.videoOutput)
}

// kittyDeleteAll removes every image from the screen being shown (and frees
// their data). q=2 stops kitty from answering on our stdin.
const kittyDeleteAll = "\x1b_Ga=d,d=A,q=2\x1b\\"

// resetTerminal removes whatever mpv left on screen. mpv should clean up after
// itself, but one leftover kitty image would cover the TUI that comes back
// next, so it is done explicitly here.
func (c *Client) resetTerminal(out *os.File) {
	seq := "\x1b[?25h" // show the cursor again
	switch c.protocol {
	case ProtocolKitty:
		// Images belong to a screen, and mpv may have used the alternate one,
		// so clear the images on both.
		seq += kittyDeleteAll + "\x1b[?1049h" + kittyDeleteAll + "\x1b[2J\x1b[?1049l"
	case ProtocolSixel:
		seq += "\x1b[2J\x1b[H" // sixel pixels are part of the screen; wipe them
	}
	_, _ = io.WriteString(out, seq)
}
