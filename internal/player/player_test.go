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
