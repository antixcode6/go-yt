package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/lrstanley/go-ytdlp"

	"github.com/antixcode6/go-yt/internal/config"
	"github.com/antixcode6/go-yt/internal/player"
	"github.com/antixcode6/go-yt/internal/subscription"
	"github.com/antixcode6/go-yt/internal/ui"
)

func main() {
	if _, err := ytdlp.Install(context.Background(), nil); err != nil {
		fmt.Fprintf(os.Stderr, "failed to install yt-dlp: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	socketPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("mpv-%d-%d.sock", os.Getuid(), os.Getpid()))

	cols, rows, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		cols, rows = 80, 24
	}

	mpvClient, err := player.New(socketPath, player.Options{
		VideoOutput: cfg.VideoOutput,
		AudioOnly:   cfg.AudioOnly,
		CookiesPath: cfg.CookiesPath,
		Protocol:    player.DetectProtocol(),
		Cols:        cols,
		Rows:        rows,
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start mpv: %v\n", err)
		os.Exit(1)
	}

	defer func() {
		err = mpvClient.Close()
		if err != nil {
			panic(err)
		}
	}()

	subs, err := subscription.LoadAll(cfg.SubscriptionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load subscriptions: %v\n", err)
		os.Exit(1)
	}

	m := ui.NewModel(cfg, mpvClient, subs)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
