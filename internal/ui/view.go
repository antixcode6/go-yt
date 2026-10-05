package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212")).
			MarginBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205"))
)

func (m Model) View() string {
	var b strings.Builder

	switch m.screen {
	case ScreenSubscriptions:
		b.WriteString(m.viewSubscriptions())
	case ScreenVideos:
		b.WriteString(m.viewVideos())
	case ScreenPlayer:
		b.WriteString(m.viewPlayer())
	}

	if m.statusMsg != "" {
		b.WriteString("\n" + statusStyle.Render(m.statusMsg))
	}
	if m.err != nil {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.err.Error()))
	}

	b.WriteString("\n\n" + dimStyle.Render(
		"[enter] select  [v] play URL  [a] add sub  [d] remove  [r] refresh  [q] quit"))
	return b.String()
}

func (m Model) viewSubscriptions() string {
	if m.inputMode != inputNone {
		return m.viewInputModal()
	}

	if m.adding {
		return m.viewAddModal()
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Subscriptions") + "\n")

	if len(m.subs) == 0 {
		b.WriteString(dimStyle.Render("No subscriptions yet. Press 'a' to add a channel."))
		return b.String()
	}

	for i, sub := range m.subs {
		line := fmt.Sprintf("%-40s  %d videos", sub.Title, len(sub.Videos))
		if i == m.cursor {
			b.WriteString(selectedStyle.Render("▸ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m Model) viewInputModal() string {
	var heading string
	switch m.inputMode {
	case inputAddSub:
		heading = "Add subscription"
	case inputPlayURL:
		heading = "Play URL"
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(heading) + "\n\n")
	b.WriteString(m.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("[enter] confirm   [esc] cancel"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Render(b.String())
}

func (m Model) viewAddModal() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Add subscription") + "\n\n")
	b.WriteString(m.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("[enter] confirm   [esc] cancel"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Render(b.String())
}

func (m Model) viewVideos() string {
	if m.inputMode != inputNone {
		return m.viewInputModal()
	}

	var b strings.Builder
	if m.selectedSub < len(m.subs) {
		b.WriteString(titleStyle.Render(m.subs[m.selectedSub].Title) + "\n")
	} else {
		b.WriteString(titleStyle.Render("Videos") + "\n")
	}

	if len(m.videos) == 0 {
		b.WriteString(dimStyle.Render("No videos."))
		return b.String()
	}

	for i, v := range m.videos {
		line := fmt.Sprintf("%s  %s", v.Published.Format("2006-01-02"), v.Title)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render("▸ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m Model) viewPlayer() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Now Playing") + "\n")
	if m.mediaTitle != "" {
		b.WriteString(m.mediaTitle + "\n")
	}
	b.WriteString(fmt.Sprintf("Position: %s / %s\n",
		formatDuration(m.pos), formatDuration(m.dur)))
	if m.paused {
		b.WriteString(statusStyle.Render("⏸ paused") + "\n")
	}
	b.WriteString(dimStyle.Render("Controls: [space] pause  [esc] back"))
	return b.String()
}

func formatDuration(sec float64) string {
	if sec <= 0 {
		return "0:00"
	}
	total := int(sec)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
