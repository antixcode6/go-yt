// internal/feed/resolve.go
package feed

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lrstanley/go-ytdlp"
)

var channelIDRe = regexp.MustCompile(`^UC[a-zA-Z0-9_-]{22}$`)

// ResolveChannelID accepts a raw UC… ID, an @handle, a channel URL, or a
// video URL, and returns the canonical UC… channel ID.
func ResolveChannelID(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	// Fast path: already a channel ID.
	if channelIDRe.MatchString(raw) {
		return raw, nil
	}

	// Normalize bare @handles into a URL yt-dlp understands.
	if strings.HasPrefix(raw, "@") {
		raw = "https://www.youtube.com/" + raw
	}

	result, err := ytdlp.New().
		Print("%(channel_id)s").
		PlaylistItems("1"). // only need the first item
		SkipDownload().
		Run(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}

	id := strings.TrimSpace(result.Stdout)
	if id == "" || id == "NA" {
		return "", fmt.Errorf("could not resolve channel ID for %q", raw)
	}
	return id, nil
}
