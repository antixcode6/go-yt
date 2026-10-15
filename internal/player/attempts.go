package player

import (
	"fmt"
	"regexp"
	"strings"
)

// An attempt is one way of asking yt-dlp (through mpv's ytdl hook) for a video.
type attempt struct {
	label   string // shown to the user
	cookies string // cookies file; "" means none
	clients string // YouTube player client for yt-dlp; "" means yt-dlp's default
}

// rawOptions is the value for mpv's ytdl-raw-options, or "" for none.
func (a attempt) rawOptions() string {
	var parts []string
	if a.cookies != "" {
		parts = append(parts, "cookies="+a.cookies)
	}
	if a.clients != "" {
		parts = append(parts, "extractor-args=youtube:player_client="+a.clients)
	}
	return strings.Join(parts, ",")
}

// clientNameRe limits fallback client names to what yt-dlp uses (web_safari,
// android_vr, ...), so a config value cannot add options of its own.
var clientNameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// plan lists the ways to try a video, in order: the configured one (with
// cookies if there are any), then without cookies, then each fallback client.
// YouTube sometimes withholds the normal stream URLs from a client or account
// (yt-dlp issue 12482), and another client may still get through.
func (c *Client) plan() []attempt {
	var p []attempt
	if c.cookies != "" {
		p = append(p,
			attempt{label: "with cookies", cookies: c.cookies},
			attempt{label: "without cookies"})
	} else {
		p = append(p, attempt{label: "default"})
	}
	for _, name := range c.fallbacks {
		p = append(p, attempt{label: name + " client", clients: name})
	}
	return p
}

// moreToTry says whether the attempt at index i of plan, which failed with
// reason, should be followed by the next one. A failed cookies attempt is
// always followed by one without cookies.
func (c *Client) moreToTry(plan []attempt, i int, reason string) bool {
	if i+1 >= len(plan) {
		return false
	}
	if i == 0 && c.cookies != "" {
		return true
	}
	return looksBlocked(reason)
}

func looksBlocked(reason string) bool {
	low := strings.ToLower(reason)
	for _, s := range []string{"403", "forbidden", "sign in to confirm", "requested format",
		"no video formats", "po token", "sabr", "missing a url"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

type failure struct{ label, reason string }

// summarize turns the failed attempts into one message for the user.
func summarize(failed []failure) string {
	if len(failed) == 1 {
		return "Could not play this video: " + failed[0].reason
	}
	same := true
	labels := make([]string, len(failed))
	parts := make([]string, len(failed))
	for i, f := range failed {
		same = same && f.reason == failed[0].reason
		labels[i] = f.label
		parts[i] = f.label + ": " + f.reason
	}
	if same {
		return fmt.Sprintf("Could not play this video (tried %s): %s", strings.Join(labels, ", "), failed[0].reason)
	}
	return "Could not play this video. Tried " + strings.Join(parts, "; ") + "."
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// loadWith asks the long-lived mpv to load url using attempt a.
func (c *Client) loadWith(url string, a attempt) error {
	if opt := a.rawOptions(); opt != "" {
		return c.call("loadfile", url, "replace", -1, "ytdl-raw-options="+opt)
	}
	return c.call("loadfile", url, "replace")
}
