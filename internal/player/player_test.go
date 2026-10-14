package player

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests start this test binary again as a fake mpv (the usual
// "helper process" pattern). The fake speaks just enough of mpv's JSON IPC to
// accept loadfile and emit the events and log lines a real mpv would.

func TestMain(m *testing.M) {
	if os.Getenv("GO_YT_FAKE_MPV") == "1" {
		runFakeMPV()
		return
	}
	os.Exit(m.Run())
}

func runFakeMPV() {
	var sock string
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "--input-ipc-server="); ok {
			sock = v
		}
	}
	if sock == "" {
		runFakeTerminalMPV()
		return
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(2)
	}

	scenario := os.Getenv("GO_YT_FAKE_SCENARIO")
	record := os.Getenv("GO_YT_FAKE_RECORD")

	var mu sync.Mutex
	var subs []net.Conn
	broadcast := func(v map[string]any) {
		b, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		for _, c := range subs {
			_, _ = c.Write(append(b, '\n'))
		}
	}
	logMsg := func(prefix, text string) {
		broadcast(map[string]any{"event": "log-message", "prefix": prefix, "level": "error", "text": text + "\n"})
	}
	fail := func(text string) {
		broadcast(map[string]any{"event": "start-file"})
		logMsg("cplayer", "unrelated noise")
		logMsg("ytdl_hook", "ERROR: "+text)
		logMsg("ytdl_hook", "youtube-dl failed: unexpected error occurred")
		broadcast(map[string]any{"event": "end-file", "reason": "error"})
	}

	for {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		go func(nc net.Conn) {
			sc := bufio.NewScanner(nc)
			for sc.Scan() {
				var req struct {
					Command   []any    `json:"command"`
					RequestID *float64 `json:"request_id"`
				}
				if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.Command) == 0 {
					continue
				}
				reply := map[string]any{"error": "success", "data": nil}
				if req.RequestID != nil {
					reply["request_id"] = *req.RequestID
				}
				b, _ := json.Marshal(reply)
				_, _ = nc.Write(append(b, '\n'))

				switch req.Command[0] {
				case "request_log_messages":
					mu.Lock()
					subs = append(subs, nc)
					mu.Unlock()
				case "quit":
					os.Exit(0)
				case "loadfile":
					if record != "" {
						f, _ := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
						line, _ := json.Marshal(req.Command)
						_, _ = f.Write(append(line, '\n'))
						_ = f.Close()
					}
					withCookies := len(req.Command) >= 5
					go func() {
						time.Sleep(50 * time.Millisecond)
						switch {
						case scenario == "die":
							os.Exit(1)
						case scenario == "always-fail" && withCookies:
							fail("[youtube] abc: Sign in to confirm you’re not a bot. See https://github.com/yt-dlp/yt-dlp/wiki/FAQ for details")
						case scenario == "always-fail":
							fail("[youtube] abc: Video unavailable")
						case scenario == "cookies-break" && withCookies:
							fail("[youtube] abc: Sign in to confirm you’re not a bot. See https://github.com/yt-dlp/yt-dlp/wiki/FAQ for details")
						case scenario == "drop-after-load":
							broadcast(map[string]any{"event": "start-file"})
							broadcast(map[string]any{"event": "file-loaded"})
							time.Sleep(50 * time.Millisecond)
							logMsg("ffmpeg", "https: HTTP error 403 Forbidden")
							broadcast(map[string]any{"event": "end-file", "reason": "error"})
						default:
							broadcast(map[string]any{"event": "start-file"})
							broadcast(map[string]any{"event": "file-loaded"})
						}
					}()
				}
			}
		}(nc)
	}
}

type fakeEnv struct {
	c      *Client
	record string
}

func newFake(t *testing.T, scenario, cookies string) fakeEnv {
	t.Helper()

	dir, err := os.MkdirTemp("", "gy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	record := filepath.Join(dir, "loadfiles.jsonl")

	oldBin := mpvBinary
	mpvBinary = os.Args[0]
	t.Cleanup(func() { mpvBinary = oldBin })
	t.Setenv("GO_YT_FAKE_MPV", "1")
	t.Setenv("GO_YT_FAKE_SCENARIO", scenario)
	t.Setenv("GO_YT_FAKE_RECORD", record)

	c, err := New(filepath.Join(dir, "m.sock"), Options{CookiesPath: cookies})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return fakeEnv{c: c, record: record}
}

func (f fakeEnv) loadfiles(t *testing.T) [][]any {
	t.Helper()
	data, _ := os.ReadFile(f.record)
	var out [][]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var cmd []any
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			t.Fatal(err)
		}
		out = append(out, cmd)
	}
	return out
}

func next(t *testing.T, c *Client, wait time.Duration) (Event, bool) {
	t.Helper()
	select {
	case ev := <-c.Events():
		return ev, true
	case <-time.After(wait):
		return Event{}, false
	}
}

func TestRetriesWithoutCookies(t *testing.T) {
	f := newFake(t, "cookies-break", "/x/cookies.txt")
	if err := f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc"); err != nil {
		t.Fatal(err)
	}

	ev, ok := next(t, f.c, 5*time.Second)
	if !ok || ev.Kind != EventRetrying {
		t.Fatalf("want EventRetrying, got %+v (ok=%v)", ev, ok)
	}
	if !strings.Contains(ev.Message, "Sign in to confirm") || strings.Contains(ev.Message, "https://") {
		t.Errorf("unexpected retry message: %q", ev.Message)
	}

	if ev, ok := next(t, f.c, 500*time.Millisecond); ok {
		t.Fatalf("retry should have succeeded, got %+v", ev)
	}

	got := f.loadfiles(t)
	if len(got) != 2 {
		t.Fatalf("want 2 loadfile calls, got %v", got)
	}
	if len(got[0]) != 5 || got[0][4] != "ytdl-raw-options=cookies=/x/cookies.txt" {
		t.Errorf("first attempt should carry the cookies option: %v", got[0])
	}
	if len(got[1]) != 3 {
		t.Errorf("retry should have no options: %v", got[1])
	}
}

func TestLoadFailedNoCookiesConfigured(t *testing.T) {
	f := newFake(t, "always-fail", "")
	_ = f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc")

	ev, ok := next(t, f.c, 5*time.Second)
	if !ok || ev.Kind != EventLoadFailed {
		t.Fatalf("want EventLoadFailed, got %+v (ok=%v)", ev, ok)
	}
	if ev.Message != "Could not play this video: [youtube] abc: Video unavailable" {
		t.Errorf("unexpected message: %q", ev.Message)
	}
	if n := len(f.loadfiles(t)); n != 1 {
		t.Errorf("want exactly 1 loadfile call, got %d", n)
	}
}

func TestLoadFailedWithAndWithoutCookies(t *testing.T) {
	f := newFake(t, "always-fail", "/x/cookies.txt")
	_ = f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc")

	if ev, ok := next(t, f.c, 5*time.Second); !ok || ev.Kind != EventRetrying {
		t.Fatalf("want EventRetrying first, got %+v (ok=%v)", ev, ok)
	}
	ev, ok := next(t, f.c, 5*time.Second)
	if !ok || ev.Kind != EventLoadFailed {
		t.Fatalf("want EventLoadFailed, got %+v (ok=%v)", ev, ok)
	}
	for _, want := range []string{"Without cookies: [youtube] abc: Video unavailable", "With cookies: [youtube] abc: Sign in to confirm"} {
		if !strings.Contains(ev.Message, want) {
			t.Errorf("message %q lacks %q", ev.Message, want)
		}
	}
}

func TestNoRetryAfterPlaybackStarted(t *testing.T) {
	f := newFake(t, "drop-after-load", "/x/cookies.txt")
	_ = f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc")

	ev, ok := next(t, f.c, 5*time.Second)
	if !ok || ev.Kind != EventLoadFailed {
		t.Fatalf("want EventLoadFailed, got %+v (ok=%v)", ev, ok)
	}
	if !strings.HasPrefix(ev.Message, "Playback stopped: https: HTTP error 403 Forbidden") {
		t.Errorf("unexpected message: %q", ev.Message)
	}
	if n := len(f.loadfiles(t)); n != 1 {
		t.Errorf("a mid-playback error must not restart the video; got %d loadfile calls", n)
	}
}

func TestSuccessfulLoadIsQuiet(t *testing.T) {
	f := newFake(t, "ok", "/x/cookies.txt")
	_ = f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc")
	if ev, ok := next(t, f.c, 500*time.Millisecond); ok {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestMPVDiesAfterStart(t *testing.T) {
	f := newFake(t, "die", "")
	_ = f.c.Play(context.Background(), "https://www.youtube.com/watch?v=abc")

	ev, ok := next(t, f.c, 5*time.Second)
	if !ok || ev.Kind != EventMPVExited || !strings.Contains(ev.Message, "exited unexpectedly") {
		t.Fatalf("want EventMPVExited, got %+v (ok=%v)", ev, ok)
	}
}

func TestStartupFailureIsReported(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no 'false' binary")
	}
	old := mpvBinary
	mpvBinary = falseBin
	t.Cleanup(func() { mpvBinary = old })

	dir, _ := os.MkdirTemp("", "gy")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	_, err = New(filepath.Join(dir, "m.sock"), Options{})
	if err == nil || !strings.Contains(err.Error(), "exited before its IPC socket appeared") ||
		!strings.Contains(err.Error(), "GO_YT_MPV_LOG") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCookiesPathWithCommaIsRejected(t *testing.T) {
	if _, err := New("/nonexistent/sock", Options{CookiesPath: "/tmp/a,b.txt"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTidy(t *testing.T) {
	got := tidy("ERROR: [youtube] x: HTTP Error 403: Forbidden see https://example.com/a?b=c now")
	if strings.Contains(got, "https://") || !strings.Contains(got, "<url>") || !strings.Contains(got, "stale") {
		t.Errorf("tidy = %q", got)
	}
}

// --- terminal-graphics mode (kitty/sixel) ---

// runFakeTerminalMPV imitates an mpv started for PlayInTerminal: it "draws" by
// writing to stdout, writes error lines to --log-file, and exits like mpv.
func runFakeTerminalMPV() {
	var logPath string
	cookies := false
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "--log-file="); ok {
			logPath = v
		}
		if strings.HasPrefix(a, "--ytdl-raw-options=cookies=") {
			cookies = true
		}
	}
	if rec := os.Getenv("GO_YT_FAKE_RECORD"); rec != "" {
		f, _ := os.OpenFile(rec, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
		_ = f.Close()
	}
	logErr := func(text string) {
		_ = os.WriteFile(logPath, []byte("[   1.000][v][cplayer] noise\n[   2.000][e][ytdl_hook] ERROR: "+text+"\n"+
			"[   2.100][e][ytdl_hook] ERROR: [generic] videoplayback?x=1: Unable to download webpage: HTTP Error 403\n"), 0o600)
	}
	switch os.Getenv("GO_YT_FAKE_SCENARIO") {
	case "cookies-break":
		if cookies {
			logErr("[youtube] abc: Sign in to confirm you\u2019re not a bot.")
			os.Exit(2)
		}
	case "always-fail":
		if cookies {
			logErr("[youtube] abc: Sign in to confirm you\u2019re not a bot.")
		} else {
			logErr("[youtube] abc: Video unavailable")
		}
		os.Exit(2)
	case "bad-vo":
		os.Exit(1)
	case "ctrl-c":
		os.Stdout.WriteString("FRAME\n")
		os.Exit(4)
	}
	os.Stdout.WriteString("FRAME\n")
	os.Exit(0)
}

type termEnv struct {
	c      *Client
	out    *os.File
	record string
}

func newTerm(t *testing.T, scenario, cookies string) termEnv {
	t.Helper()
	dir := t.TempDir()
	oldBin := mpvBinary
	mpvBinary = os.Args[0]
	t.Cleanup(func() { mpvBinary = oldBin })
	t.Setenv("GO_YT_FAKE_MPV", "1")
	t.Setenv("GO_YT_FAKE_SCENARIO", scenario)
	record := filepath.Join(dir, "runs.txt")
	t.Setenv("GO_YT_FAKE_RECORD", record)

	c, err := New(filepath.Join(dir, "m.sock"), Options{VideoOutput: "kitty", CookiesPath: cookies})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	out, err := os.Create(filepath.Join(dir, "terminal.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	return termEnv{c: c, out: out, record: record}
}

func (e termEnv) drawn(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(e.out.Name())
	return string(b)
}

func (e termEnv) runs(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(e.record)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The bug this guards against: mpv's stdout must be the terminal given to
// PlayInTerminal, or kitty/sixel frames go nowhere and only audio is heard.
func TestTerminalMPVDrawsOnTheGivenTerminal(t *testing.T) {
	e := newTerm(t, "ok", "")
	if e.c.conn != nil || e.c.proc != nil {
		t.Fatal("terminal mode must not start a long-lived mpv")
	}
	if err := e.c.PlayInTerminal("https://www.youtube.com/watch?v=abc", os.Stdin, e.out); err != nil {
		t.Fatal(err)
	}
	got := e.drawn(t)
	if !strings.HasPrefix(got, "FRAME\n") {
		t.Fatalf("mpv's output did not reach the terminal: %q", got)
	}
	if !strings.Contains(got[len("FRAME\n"):], kittyDeleteAll) {
		t.Errorf("leftover kitty images must be deleted after mpv exits: %q", got)
	}
	runs := e.runs(t)
	if len(runs) != 1 {
		t.Fatalf("want one mpv run, got %v", runs)
	}
	for _, want := range []string{"--vo=kitty", "--really-quiet", "--idle=no", "--keep-open=no", "-- https://www.youtube.com/watch?v=abc"} {
		if !strings.Contains(runs[0], want) {
			t.Errorf("args lack %q: %s", want, runs[0])
		}
	}
	for _, bad := range []string{"--no-terminal", "--input-ipc-server", "--vo-kitty-cols"} {
		if strings.Contains(runs[0], bad) {
			t.Errorf("args must not contain %q: %s", bad, runs[0])
		}
	}
}

func TestTerminalRetriesWithoutCookies(t *testing.T) {
	e := newTerm(t, "cookies-break", "/x/cookies.txt")
	if err := e.c.PlayInTerminal("https://www.youtube.com/watch?v=abc", os.Stdin, e.out); err != nil {
		t.Fatal(err)
	}
	runs := e.runs(t)
	if len(runs) != 2 || !strings.Contains(runs[0], "--ytdl-raw-options=cookies=/x/cookies.txt") ||
		strings.Contains(runs[1], "cookies") {
		t.Fatalf("want a run with cookies, then one without: %v", runs)
	}
	if got := e.drawn(t); strings.Count(got, "FRAME\n") != 1 {
		t.Errorf("the video should be drawn exactly once: %q", got)
	}
}

func TestTerminalLoadFailureReasons(t *testing.T) {
	e := newTerm(t, "always-fail", "")
	err := e.c.PlayInTerminal("u", os.Stdin, e.out)
	if err == nil || err.Error() != "Could not play this video: [youtube] abc: Video unavailable" {
		t.Fatalf("got %v", err) // the [generic] hook error must not win
	}
	if n := len(e.runs(t)); n != 1 {
		t.Errorf("no cookies configured, so no retry; got %d runs", n)
	}

	e = newTerm(t, "always-fail", "/x/cookies.txt")
	err = e.c.PlayInTerminal("u", os.Stdin, e.out)
	if err == nil || !strings.Contains(err.Error(), "Without cookies: [youtube] abc: Video unavailable") ||
		!strings.Contains(err.Error(), "With cookies: [youtube] abc: Sign in to confirm") {
		t.Fatalf("got %v", err)
	}
}

func TestTerminalStartFailureAndCtrlC(t *testing.T) {
	e := newTerm(t, "bad-vo", "/x/cookies.txt")
	err := e.c.PlayInTerminal("u", os.Stdin, e.out)
	if err == nil || !strings.Contains(err.Error(), "mpv failed to start") || !strings.Contains(err.Error(), "--vo=help") {
		t.Fatalf("got %v", err)
	}
	if n := len(e.runs(t)); n != 1 {
		t.Errorf("a start failure is not a cookie problem; got %d runs", n)
	}

	e = newTerm(t, "ctrl-c", "")
	if err := e.c.PlayInTerminal("u", os.Stdin, e.out); err != nil {
		t.Errorf("Ctrl+C is a normal exit, got %v", err)
	}
}

func TestTerminalModeHasNoIPC(t *testing.T) {
	e := newTerm(t, "ok", "")
	if e.c.Position() != 0 || e.c.IsIdle() || e.c.MediaTitle() != "" || e.c.Stop() == nil || e.c.SetVolume(5) == nil {
		t.Error("IPC calls must be harmless no-ops/errors in terminal mode")
	}
	if err := e.c.Close(); err != nil {
		t.Error(err)
	}
}

// cleanTermEnv blanks every variable DetectProtocol reads, so tests do not
// depend on the terminal they run in (a developer's kitty sets KITTY_WINDOW_ID).
func cleanTermEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "TMUX"} {
		t.Setenv(k, "")
	}
}

func TestDetectProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want GraphicsProtocol
	}{
		{"kitty TERM", map[string]string{"TERM": "xterm-kitty"}, ProtocolKitty},
		{"kitty with TERM overridden", map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "1"}, ProtocolKitty},
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, ProtocolKitty},
		{"wezterm", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "WezTerm"}, ProtocolKitty},
		{"foot", map[string]string{"TERM": "foot"}, ProtocolSixel},
		{"tmux inside kitty", map[string]string{"TERM": "tmux-256color", "KITTY_WINDOW_ID": "1", "TMUX": "/tmp/tmux-1/default,1,0"}, ProtocolNone},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, ProtocolNone},
	} {
		cleanTermEnv(t)
		for k, v := range tc.env {
			t.Setenv(k, v)
		}
		if got := DetectProtocol(); got != tc.want {
			t.Errorf("%s: DetectProtocol() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestVideoMode(t *testing.T) {
	cleanTermEnv(t)
	t.Setenv("TERM", "xterm-256color")
	if got := (&Client{videoOutput: "auto"}).VideoMode(); !strings.Contains(got, "no kitty/sixel terminal detected") || !strings.Contains(got, "xterm-256color") {
		t.Errorf("auto without a graphics terminal: %q", got)
	}
	if got := (&Client{videoOutput: "gpu"}).VideoMode(); !strings.Contains(got, "--vo=gpu") || !strings.Contains(got, `"auto"`) {
		t.Errorf("gpu: %q", got)
	}
	if got := (&Client{protocol: ProtocolKitty}).VideoMode(); !strings.Contains(got, "kitty graphics") {
		t.Errorf("kitty: %q", got)
	}
}

func TestResolveProtocol(t *testing.T) {
	cleanTermEnv(t)
	t.Setenv("TERM", "xterm-kitty")
	for _, tc := range []struct {
		vo    string
		audio bool
		want  GraphicsProtocol
	}{
		{"auto", false, ProtocolKitty},
		{"gpu", false, ProtocolNone}, // config wins over detection
		{"", false, ProtocolNone},
		{"sixel", false, ProtocolSixel},
		{"kitty", false, ProtocolKitty},
		{"auto", true, ProtocolNone},
	} {
		if got := ResolveProtocol(tc.vo, tc.audio); got != tc.want {
			t.Errorf("ResolveProtocol(%q, %v) = %v, want %v", tc.vo, tc.audio, got, tc.want)
		}
	}
	cleanTermEnv(t)
	t.Setenv("TERM", "foot")
	if got := ResolveProtocol("auto", false); got != ProtocolSixel {
		t.Errorf("foot should detect sixel, got %v", got)
	}
}

func TestReadLogProblemsAndReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mpv.log")
	log := "[  1.000][v][cplayer] verbose noise\n" +
		"[  2.000][w][ffmpeg] https: HTTP error 403 Forbidden\n" +
		"[  2.100][e][stream] Failed to open https://x/y.\n" +
		"[  3.000][e][ytdl_hook] ERROR: [generic] videoplayback: Unable to download webpage\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := readLogProblems(path)
	if len(lines) != 3 {
		t.Fatalf("want 3 problem lines, got %v", lines)
	}
	// Only a [generic] hook error exists, so the ffmpeg HTTP error is the better reason.
	if got := failureReason(lines); !strings.Contains(got, "HTTP error 403") {
		t.Errorf("reason = %q", got)
	}
}

func TestKittyArgsShmIsOptIn(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("GO_YT_KITTY_SHM", "")
	if got := strings.Join(terminalArgs(ProtocolKitty), " "); strings.Contains(got, "use-shm") || !strings.Contains(got, "--vo=kitty") {
		t.Errorf("shm must be off by default: %s", got)
	}
	t.Setenv("GO_YT_KITTY_SHM", "1")
	if got := strings.Join(terminalArgs(ProtocolKitty), " "); !strings.Contains(got, "--vo-kitty-use-shm=yes") {
		t.Errorf("GO_YT_KITTY_SHM=1 should enable shm: %s", got)
	}
	t.Setenv("SSH_CONNECTION", "1.2.3.4 1 5.6.7.8 22")
	if got := strings.Join(terminalArgs(ProtocolKitty), " "); strings.Contains(got, "use-shm") {
		t.Errorf("shm must stay off over ssh: %s", got)
	}
}

func TestResetTerminalPerProtocol(t *testing.T) {
	read := func(c *Client) string {
		f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		c.resetTerminal(f)
		b, _ := os.ReadFile(f.Name())
		return string(b)
	}
	k := read(&Client{protocol: ProtocolKitty})
	if strings.Count(k, kittyDeleteAll) != 2 || !strings.Contains(k, "\x1b[?1049h") || !strings.HasSuffix(k, "\x1b[?1049l") {
		t.Errorf("kitty reset = %q", k)
	}
	if got := read(&Client{protocol: ProtocolSixel}); !strings.Contains(got, "\x1b[2J") || strings.Contains(got, "_G") {
		t.Errorf("sixel reset = %q", got)
	}
}
