package feed

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/lrstanley/go-ytdlp"
	"github.com/mmcdole/gofeed"
)

const minChannelVideos = 15

type Channel struct {
	ID    string
	Title string
	URL   string
}

type Video struct {
	ID          string
	Title       string
	URL         string
	Published   time.Time
	ChannelID   string
	ChannelName string
}

type Client struct {
	parser  *gofeed.Parser
	cookies string
}

func NewClient(cookiesPath string) *Client {
	return &Client{
		parser:  gofeed.NewParser(),
		cookies: cookiesPath,
	}
}

func (c *Client) withCookies() *ytdlp.Command {
	cmd := ytdlp.New()
	if c.cookies != "" {
		cmd = cmd.Cookies(c.cookies)
	}
	return cmd
}

func ChannelFeedURL(channelID string) string {
	return fmt.Sprintf("https://www.youtube.com/feeds/videos.xml?channel_id=%s", channelID)
}

func (c *Client) FetchChannel(ctx context.Context, channelID string) (*Channel, []*Video, error) {
	ch, videos, rssErr := c.fetchViaRSS(ctx, channelID)
	if rssErr == nil && len(videos) > 0 {
		// RSS includes Shorts and can't tell them apart; drop the ones the
		// channel's /shorts tab lists. Best effort: on failure, keep everything.
		if shorts := c.shortIDs(ctx, channelID); len(shorts) > 0 {
			kept := videos[:0]
			for _, v := range videos {
				id := strings.TrimPrefix(v.ID, "yt:video:")
				if shorts[id] {
					log.Printf("dropping short: %s", id) // optional, remove once it works
					continue
				}
				kept = append(kept, v)
			}
			videos = kept
		}
		if len(videos) < minChannelVideos {
			videos = c.topUp(ctx, channelID, ch.Title, videos)
		}
		return ch, videos, nil
	}

	// RSS failed or returned nothing. Fall back to yt-dlp (/videos tab only,
	// so no Shorts, but also no streams).
	videos, ytErr := c.fetchViaYtdlp(ctx, channelID)
	if ytErr != nil {
		return nil, nil, fmt.Errorf("rss: %v; yt-dlp: %w", rssErr, ytErr)
	}
	if len(videos) == 0 {
		return nil, nil, fmt.Errorf("no videos for channel %s", channelID)
	}
	return &Channel{
		ID:    channelID,
		Title: videos[0].ChannelName,
		URL:   "https://www.youtube.com/channel/" + channelID,
	}, videos, nil
}

// shortIDs returns the IDs on a channel's /shorts tab (flat, so it's cheap).
// It returns nil on any failure, including channels with no Shorts tab.
func (c *Client) shortIDs(ctx context.Context, channelID string) map[string]bool {
	// Run returns the result even on a non-zero exit, so a partial list still counts.
	result, _ := c.withCookies().
		FlatPlaylist().
		PlaylistItems(":30").
		PrintJSON().
		SkipDownload().
		Run(ctx, "https://www.youtube.com/channel/"+channelID+"/shorts")
	if result == nil {
		return nil
	}
	infos, err := result.GetExtractedInfo()
	if err != nil {
		return nil
	}
	ids := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info.ID != "" {
			ids[info.ID] = true
		}
	}
	return ids
}

func (c *Client) fetchViaRSS(ctx context.Context, channelID string) (*Channel, []*Video, error) {
	url := ChannelFeedURL(channelID)
	feed, err := c.parser.ParseURLWithContext(url, ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("parse feed %s: %w", url, err)
	}

	ch := &Channel{ID: channelID, Title: feed.Title, URL: url}
	videos := make([]*Video, 0, len(feed.Items))
	for _, item := range feed.Items {
		videos = append(videos, &Video{
			ID:          item.GUID,
			Title:       item.Title,
			URL:         item.Link,
			Published:   *item.PublishedParsed,
			ChannelID:   channelID,
			ChannelName: feed.Title,
		})
	}
	return ch, videos, nil
}

func (c *Client) fetchViaYtdlp(ctx context.Context, channelID string) ([]*Video, error) {
	channelURL := "https://www.youtube.com/channel/" + channelID + "/videos"

	// Resolve the channel display name with a non-flat probe of the first
	// video. Flat-playlist entries don't carry channel metadata.
	channelName := c.resolveChannelName(ctx, channelURL)

	result, err := c.withCookies().
		FlatPlaylist().
		MatchFilters("original_url!*=/shorts/ & url!*=/shorts/").
		PlaylistItems(":20").
		ExtractorArgs("youtubetab:approximate_date").
		PrintJSON().
		SkipDownload().
		Run(ctx, channelURL)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp channel fetch: %w", err)
	}

	infos, err := result.GetExtractedInfo()
	if err != nil {
		return nil, err
	}

	videos := make([]*Video, 0, len(infos))
	for _, info := range infos {
		if info.ID == "" {
			continue
		}
		title := ""
		if info.Title != nil {
			title = *info.Title
		}

		var published time.Time
		if info.Timestamp != nil {
			published = time.Unix(int64(*info.Timestamp), 0)
		}

		videos = append(videos, &Video{
			ID:          info.ID,
			Title:       title,
			URL:         "https://www.youtube.com/watch?v=" + info.ID,
			Published:   published,
			ChannelID:   channelID,
			ChannelName: channelName,
		})
	}
	return videos, nil
}

// resolveChannelName does a non-flat extraction of the channel's first video
// and prints just the channel name. Slower than flat-playlist but the only
// reliable way to get the display name without hitting the RSS endpoint.
func (c *Client) resolveChannelName(ctx context.Context, channelURL string) string {
	result, err := c.withCookies().
		PlaylistItems("1").
		Print("%(channel)s").
		SkipDownload().
		Run(ctx, channelURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// PartialError is returned by FetchHomeFeed together with the videos that did
// load, when yt-dlp skipped some entries (an upcoming live event, a private or
// removed video) and so exited with status 1. Callers should show the videos
// and surface the note, not treat it as a failed fetch.
type PartialError struct {
	Skipped []string // one yt-dlp error message per skipped entry
}

func (e *PartialError) Error() string {
	switch len(e.Skipped) {
	case 0:
		return "yt-dlp skipped some videos"
	case 1:
		return "Skipped 1 video: " + e.Skipped[0]
	}
	return fmt.Sprintf("Skipped %d videos (first: %s)", len(e.Skipped), e.Skipped[0])
}

// skippedEntries returns yt-dlp's per-video ERROR lines, without the
// "ERROR: [youtube] " prefix.
func skippedEntries(stderr string) []string {
	var out []string
	for line := range strings.SplitSeq(stderr, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "ERROR:")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if after, ok := strings.CutPrefix(rest, "[youtube]"); ok {
			rest = strings.TrimSpace(after)
		}
		if rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// FetchHomeFeed fetches videos from a personalized feed (e.g., :ytsubs, :ytrec).
//
// yt-dlp exits 1 if any single entry fails, yet it still prints every entry
// that worked. In that case the videos are returned with a *PartialError.
func (c *Client) FetchHomeFeed(ctx context.Context, feedURL, limit string) ([]*Video, error) {
	// For personalized feeds, we must use --no-flat-playlist to get full metadata.
	// This is slower but necessary for a good user experience.
	result, err := c.withCookies().
		ExtractorArgs("youtubetab:skip=webpage,authcheck").
		MatchFilters("original_url!*=/shorts/ & url!*=/shorts/").
		LazyPlaylist().
		IgnoreErrors().
		PlaylistItems(limit).
		PrintJSON().
		SkipDownload().
		Run(ctx, feedURL)

	var partial *PartialError
	if err != nil {
		_, ok := ytdlp.IsExitCodeError(err)
		if !ok || result == nil || result.ExitCode != 1 {
			return nil, fmt.Errorf("yt-dlp feed fetch %s: %w", feedURL, err)
		}
		partial = &PartialError{Skipped: skippedEntries(result.Stderr)}
	}

	infos, perr := result.GetExtractedInfo()
	if perr != nil {
		return nil, perr
	}

	videos := make([]*Video, 0, len(infos))
	for _, info := range infos {
		if info.ID == "" {
			continue
		}
		v := &Video{ID: info.ID, URL: "https://www.youtube.com/watch?v=" + info.ID}
		if info.Title != nil {
			v.Title = *info.Title
		}
		if info.Channel != nil {
			v.ChannelName = *info.Channel
		}
		videos = append(videos, v)
	}

	if partial != nil {
		if len(videos) == 0 {
			return nil, fmt.Errorf("yt-dlp feed fetch %s: %w", feedURL, err)
		}
		return videos, partial
	}
	return videos, nil
}

// topUp adds videos from the channel's /videos and /streams tabs (neither
// contains Shorts), dedupes by ID, and returns them newest first.
func (c *Client) topUp(ctx context.Context, channelID, channelName string, have []*Video) []*Video {
	seen := make(map[string]bool, len(have))
	for _, v := range have {
		seen[strings.TrimPrefix(v.ID, "yt:video:")] = true
	}
	for _, tab := range []string{"videos", "streams"} {
		result, _ := c.withCookies().
			FlatPlaylist().
			PlaylistItems(":20").
			ExtractorArgs("youtubetab:approximate_date").
			PrintJSON().
			SkipDownload().
			Run(ctx, "https://www.youtube.com/channel/"+channelID+"/"+tab)
		if result == nil {
			continue
		}
		infos, err := result.GetExtractedInfo()
		if err != nil {
			continue
		}
		for _, info := range infos {
			if info.ID == "" || seen[info.ID] {
				continue
			}
			seen[info.ID] = true
			v := &Video{
				ID:          info.ID,
				URL:         "https://www.youtube.com/watch?v=" + info.ID,
				ChannelID:   channelID,
				ChannelName: channelName,
			}
			if info.Title != nil {
				v.Title = *info.Title
			}
			if info.Timestamp != nil {
				v.Published = time.Unix(int64(*info.Timestamp), 0)
			}
			have = append(have, v)
		}
	}
	sort.SliceStable(have, func(i, j int) bool { return have[i].Published.After(have[j].Published) })
	return have
}
