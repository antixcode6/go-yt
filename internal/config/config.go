package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds persistent user settings.
type Config struct {
	SubscriptionsPath string `json:"subscriptions_path"`
	DownloadDir       string `json:"download_dir"`

	// VideoOutput says how video is shown:
	//   "auto"  → kitty or sixel graphics if the terminal supports them,
	//             otherwise an mpv window
	//   "kitty" → force the kitty graphics protocol in the terminal
	//   "sixel" → force sixel graphics in the terminal
	//   "gpu"   → a normal mpv window alongside the TUI
	//   "tct"   → text-mode video in the terminal (any other mpv --vo value
	//             works too; "null" disables video)
	VideoOutput string `json:"video_output"`

	// AudioOnly forces `vid=no`, so mpv never opens a video track. Useful
	// when VideoOutput is "null" and you only want sound.
	AudioOnly bool `json:"audio_only"`

	CookiesPath string `json:"cookies_path"`

	// YtdlpFallbackClients are yt-dlp YouTube player clients tried in order
	// when a video will not load (YouTube sometimes withholds the normal
	// stream from a client or account; see yt-dlp issue 12482). Empty turns
	// the fallback off.
	YtdlpFallbackClients []string `json:"ytdlp_fallback_clients"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".config", "go-yt")
	return Config{
		SubscriptionsPath: filepath.Join(base, "subscriptions.json"),
		DownloadDir:       filepath.Join(base, "downloads"),
		VideoOutput:       "auto",
		AudioOnly:         false,
		CookiesPath:       "",

		YtdlpFallbackClients: []string{"web_safari", "android_vr"},
	}
}

func Load() (Config, error) {
	cfg := Default()
	dir := filepath.Dir(cfg.SubscriptionsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return cfg, err
	}

	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Write defaults to disk so the user can edit them.
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return cfg, err
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}
