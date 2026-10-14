package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// EventKind says what an Event is about.
type EventKind int

const (
	// EventRetrying: the load failed with a cookies file, and the client is
	// loading the same URL again without cookies. Informational.
	EventRetrying EventKind = iota + 1
	// EventLoadFailed: mpv could not load the URL (or playback broke off
	// partway). Message holds a one-line reason that is safe to show.
	EventLoadFailed
	// EventMPVExited: the mpv process died while go-yt was running.
	EventMPVExited
)

// Event reports something about playback that the UI should show.
type Event struct {
	Kind    EventKind
	URL     string
	Message string
}

// Events delivers load failures, retries and unexpected mpv exits. Read from
// it continuously; the client blocks (briefly) if the buffer fills up.
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed once Close has been called.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) emit(ev Event) {
	select {
	case c.events <- ev:
	case <-c.done:
	}
}

// loadState tracks the most recent Play call.
type loadState struct {
	url         string
	withCookies bool   // this attempt used the cookies file
	retried     bool   // a cookie-less retry has been started
	loaded      bool   // mpv emitted file-loaded for this attempt
	firstErr    string // why the attempt with cookies failed
}

type logLine struct {
	prefix string
	text   string
}

// mpvEvent holds the fields of mpv's JSON IPC events that we use.
type mpvEvent struct {
	Event  string `json:"event"`
	Reason string `json:"reason"` // end-file
	Prefix string `json:"prefix"` // log-message
	Text   string `json:"text"`   // log-message
}

// watch reads mpv's event stream until the connection closes.
func (c *Client) watch(nc net.Conn) {
	defer nc.Close()

	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var recent []logLine // warning/error lines since the last start-file
	for sc.Scan() {
		var ev mpvEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "start-file":
			recent = recent[:0]
		case "file-loaded":
			c.markLoaded()
		case "log-message":
			recent = append(recent, logLine{ev.Prefix, strings.TrimSpace(ev.Text)})
			if len(recent) > 16 {
				recent = recent[len(recent)-16:]
			}
		case "end-file":
			if ev.Reason == "error" {
				c.endFileError(failureReason(recent))
			}
			recent = recent[:0]
		}
	}
}

func (c *Client) markLoaded() {
	c.mu.Lock()
	if c.cur.url != "" {
		c.cur.loaded = true
	}
	c.mu.Unlock()
}

// endFileError decides what to do when mpv reports that a file failed: retry
// once without cookies, or tell the UI.
func (c *Client) endFileError(reason string) {
	c.mu.Lock()
	cur := c.cur
	retry := cur.url != "" && cur.withCookies && !cur.loaded && !cur.retried
	if retry {
		c.cur.retried = true
		c.cur.withCookies = false
		c.cur.firstErr = reason
	} else {
		c.cur = loadState{}
	}
	c.mu.Unlock()

	if cur.url == "" {
		return // not something Play started
	}

	if retry {
		c.emit(Event{
			Kind:    EventRetrying,
			URL:     cur.url,
			Message: "Playback failed with cookies (" + reason + "); retrying without them...",
		})
		if err := c.call("loadfile", cur.url, "replace"); err != nil {
			c.emit(Event{
				Kind:    EventLoadFailed,
				URL:     cur.url,
				Message: "Retry without cookies failed: " + err.Error(),
			})
		}
		return
	}

	var msg string
	switch {
	case cur.loaded:
		msg = "Playback stopped: " + reason
	case cur.retried && cur.firstErr == reason:
		msg = "Could not play this video, with or without cookies: " + reason
	case cur.retried:
		msg = fmt.Sprintf("Could not play this video. Without cookies: %s. With cookies: %s.", reason, cur.firstErr)
	default:
		msg = "Could not play this video: " + reason
	}
	c.emit(Event{Kind: EventLoadFailed, URL: cur.url, Message: msg})
}

var urlRe = regexp.MustCompile(`https?://\S+`)

// failureReason picks the most useful explanation out of the log lines mpv
// printed while loading: yt-dlp's own error first, then an HTTP error from
// ffmpeg, then whatever was logged last.
func failureReason(lines []logLine) string {
	pick := ""
	for _, l := range lines {
		// "[generic]" errors come from yt-dlp being run on the stream URL after
		// it already failed; they hide the real reason, so only use them last.
		if l.prefix == "ytdl_hook" && strings.HasPrefix(l.text, "ERROR:") && !strings.HasPrefix(l.text, "ERROR: [generic]") {
			pick = l.text
			break
		}
	}
	if pick == "" {
		for _, l := range lines {
			if l.prefix == "ffmpeg" && strings.Contains(strings.ToLower(l.text), "http error") {
				pick = l.text
				break
			}
		}
	}
	if pick == "" {
		for _, l := range lines {
			if l.prefix == "ytdl_hook" && strings.HasPrefix(l.text, "ERROR:") {
				pick = l.text
				break
			}
		}
	}
	if pick == "" && len(lines) > 0 {
		pick = lines[len(lines)-1].text
	}
	if pick == "" {
		return "mpv could not load it (set GO_YT_MPV_LOG and rerun for details)"
	}
	return tidy(pick)
}

// tidy makes an mpv/yt-dlp message fit on one line: no URLs (signed stream
// URLs are long), collapsed whitespace, bounded length, plus a hint for the
// two failures we know how to explain.
func tidy(s string) string {
	s = strings.TrimPrefix(s, "ERROR: ")
	s = urlRe.ReplaceAllString(s, "<url>")
	s = strings.Join(strings.Fields(s), " ")

	hint := ""
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "sign in to confirm"):
		hint = " [YouTube is bot-checking this IP; try another network or fresh cookies]"
	case strings.Contains(low, "403"):
		hint = " [YouTube refused the stream; a cookies file, if used, may be stale]"
	}

	const limit = 160
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit]) + "..."
	}
	return s + hint
}
