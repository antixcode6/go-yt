# go-yt

A minimal terminal YouTube client. Browse your subscriptions, search, and watch videos without opening a browser.

Built on [Bubble Tea](https://github.com/charmbracelet/bubbletea) for the TUI,
[yt-dlp](https://github.com/yt-dlp/yt-dlp) for extraction, and
[mpv](https://mpv.io/) for playback.

## Features

- **Subscriptions** — manage a local list, fetch latest videos per channel
- **Discover** — personalized feeds, YouTube search, and local recommendations
- **Ad-hoc playback** — paste a URL or video ID and play it directly
- **Import** — bulk-import subscriptions from NewPipe / Pipepipe exports
- **Cookie support** — access age-restricted, members-only, and personalized content
- **External mpv** — video plays in its own window

## Requirements

- Go 1.22+
- `mpv` on your `$PATH`
- `yt-dlp` — installed automatically by the app on first run

## Install

```sh
git clone https://github.com/antixcode6/go-yt
cd go-yt
go build -o go-yt .
```

Or run directly:

```sh
go run .
```

## Setup

The app creates a default config on first run at
`~/.config/go-yt/config.json`:

```json
{
  "subscriptions_path": "/home/user/.config/go-yt/subscriptions.json",
  "download_dir": "/home/user/.config/go-yt/downloads",
  "video_output": "auto",
  "audio_only": false,
  "cookies_path": ""
}
```

### Config options

| Field | Description |
|---|---|
| `video_output` | `auto` uses kitty or sixel graphics when your terminal supports them, else an mpv window. `kitty` / `sixel` force one. Any other value is passed to mpv as `--vo` (`gpu` window, `tct` text, `null` no video). |
| `audio_only` | Adds `--vid=no` to mpv. Pair with `video_output: "null"` for a music-player mode. |
| `cookies_path` | Path to a Netscape-format `cookies.txt`. Required for personalized feeds, subscriptions, and history. |

### Cookies

Personalized feeds (`:ytsubs`, `:ythistory`) and some videos require authentication. Export cookies from a **private browser window**:

1. Open a private/incognito window and log into YouTube
2. Navigate to `https://www.youtube.com/robots.txt` in the same tab
3. Export cookies using an extension like "Get cookies.txt LOCALLY"
4. Close the private window immediately — do not reopen it
5. Save the file and point `cookies_path` at it

Treat the cookies file as a credential:

```sh
chmod 600 ~/.config/go-yt/cookies.txt
```
Alternatively, if you already have yt-dlp installed you can also run `yt-dlp --cookies output.txt --playlist-end 3 --print "%(title)s" ":ytsubs" `


## Keybindings

### Global

| Key | Action |
|---|---|
| `q` / `ctrl+c` | Quit |
| `esc` | Back / cancel |
| `↑` `↓` / `k` `j` | Navigate |
| `enter` | Select |

### Subscriptions

| Key | Action |
|---|---|
| `a` | Add a channel |
| `d` | Remove selected channel |
| `r` | Refresh all subscriptions |
| `i` | Import from NewPipe / Pipepipe |
| `v` | Play an ad-hoc URL |
| `D` | Open Discover |

### Discover

| Key | Action |
|---|---|
| `enter` | Open selected source |

Available sources:

- **Subscriptions Feed** — your personalized `:ytsubs` feed
- **Watch History** — your `:ythistory` feed
- **Search YouTube** — query YouTube via `ytsearch`
- **Local Recommendations** — interleaved latest videos from your own subscriptions

### Player

| Key | Action |
|---|---|
| `space` | Pause / resume |
| `esc` | Stop and return to video list |

## Import from NewPipe / Pipepipe

Export your subscriptions from the app (Settings → Backup → Export subscriptions), extract the JSON from the `.zip` if needed, then press `i`
in the TUI and paste the path.

## Architecture

```
main.go                     entry point, wiring
internal/
  config/                   config file loading and defaults
  feed/                     RSS + yt-dlp feed fetching, channel resolution
  player/                   mpv subprocess management, stream resolution
  subscription/             subscription store, persistence, NewPipe import
  ui/                       Bubble Tea model, view, update loop
```

The player spawns mpv as a child process and communicates over its IPC
socket. Video output, decoding, and events all live inside mpv; the TUI only
sends commands and reads state. This keeps the TUI responsive and isolates
crashes.

## Known limitations

- **`:ytrec` (home recommendations) is unreliable.** YouTube's home feed
  contains Mix playlists (`RD...` IDs) that yt-dlp cannot extract. Workarounds
  exist but the feed is a moving target. Prefer **Local Recommendations**.
- **YouTube's RSS endpoint returns intermittent 404s.** The feed layer falls
  back to yt-dlp automatically, but fetches are slower when RSS is down.
- **No pagination for feeds.** Feeds fetch a fixed 20 items. Channel
  subscriptions support cursor navigation through the full cached list.
- **No download / offline mode.** Playback streams directly.

## License

MIT
