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

	// VideoOutput maps to mpv's --vo option. Common choices:
	//   "tct"  → render video directly in the terminal (experimental; the
	//            TUI must suspend itself while a video is playing)
	//   "null" → no video; pair with AudioOnly for a music-style player
	//   "gpu"  → a normal mpv window alongside the TUI
	VideoOutput string `json:"video_output"`

	// AudioOnly forces `vid=no`, so mpv never opens a video track. Useful
	// when VideoOutput is "null" and you only want sound.
	AudioOnly bool `json:"audio_only"`

	CookiesPath string `json:"cookies_path"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".config", "go-yt")
	return Config{
		SubscriptionsPath: filepath.Join(base, "subscriptions.json"),
		DownloadDir:       filepath.Join(base, "downloads"),
		VideoOutput:       "gpu",
		AudioOnly:         false,
		CookiesPath:       "",
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
