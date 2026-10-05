package feed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lrstanley/go-ytdlp"
	"github.com/mmcdole/gofeed"
)

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

// FetchChannel returns a channel's metadata and recent videos.
//
// It tries RSS first because it's fast and cheap, but falls through to yt-dlp
// on any error. YouTube's RSS endpoint is intermittently broken — it returns
// 404 for valid channel IDs during peak hours — so yt-dlp is the reliable
// backend and RSS is a best-effort optimization.
func (c *Client) FetchChannel(ctx context.Context, channelID string) (*Channel, []*Video, error) {
	ch, videos, rssErr := c.fetchViaRSS(ctx, channelID)
	if rssErr == nil && len(videos) > 0 {
		return ch, videos, nil
	}

	// RSS failed or returned nothing. Fall back to yt-dlp, which works
	// regardless of the RSS endpoint's health.
	videos, ytErr := c.fetchViaYtdlp(ctx, channelID)
	if ytErr != nil {
		// Both failed. Prefer the RSS error since it's usually more
		// informative (404 vs. a generic yt-dlp extraction error).
		return nil, nil, fmt.Errorf("rss: %v; yt-dlp: %w", rssErr, ytErr)
	}
	if len(videos) == 0 {
		return nil, nil, fmt.Errorf("no videos for channel %s", channelID)
	}

	return &Channel{
		ID:    channelID,
		Title: videos[0].ChannelName, // yt-dlp populates this
		URL:   "https://www.youtube.com/channel/" + channelID,
	}, videos, nil
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
		PlaylistEnd(20).
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

// FetchHomeFeed fetches videos from a personalized feed (e.g., :ytsubs, :ytrec).
func (c *Client) FetchHomeFeed(ctx context.Context, feedURL string) ([]*Video, error) {
	// For personalized feeds, we must use --no-flat-playlist to get full metadata.
	// This is slower but necessary for a good user experience.
	result, err := c.withCookies().
		PlaylistEnd(10).
		PrintJSON().
		SkipDownload().
		Run(ctx, feedURL)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp feed fetch %s: %w", feedURL, err)
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

		videos = append(videos, &Video{ID: info.ID, Title: *info.Title, URL: "https://www.youtube.com/watch?v=" + info.ID /* ... */})
	}
	return videos, nil
}
