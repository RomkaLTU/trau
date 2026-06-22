package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	colorBrand   = lipgloss.Color("#00D4AA")
	colorAccent  = lipgloss.Color("#7D56F4")
	colorSuccess = lipgloss.Color("#04B575")
	colorError   = lipgloss.Color("#FF4672")
	colorWarning = lipgloss.Color("#F9D423")
	colorInfo    = lipgloss.Color("#00AAFF")
	colorSubtle  = lipgloss.Color("#888888")
	colorFaint   = lipgloss.Color("#555555")
	colorInk     = lipgloss.Color("#0B0B0E")
	colorBarOff  = lipgloss.Color("#2A2A2E")
)

// Styles holds the Lip Gloss styles used by the TUI. They are intentionally
// simple so the TUI stays readable and easy to maintain.
type Styles struct {
	Header    lipgloss.Style
	Subtle    lipgloss.Style
	LogLine   lipgloss.Style
	Spinner   lipgloss.Style
	Success   lipgloss.Style
	Error     lipgloss.Style
	Warning   lipgloss.Style
	Info      lipgloss.Style
	Footer    lipgloss.Style
	Help      lipgloss.Style
	Separator lipgloss.Style

	Pane      lipgloss.Style
	PaneTitle lipgloss.Style

	StepDone    lipgloss.Style
	StepActive  lipgloss.Style
	StepFailed  lipgloss.Style
	StepPending lipgloss.Style
	StepTag     lipgloss.Style

	Banner    lipgloss.Style
	BannerErr lipgloss.Style

	SummaryCard  lipgloss.Style
	SummaryTitle lipgloss.Style
}

// DefaultStyles returns the default style set. Lip Gloss automatically respects
// NO_COLOR, so these colors are best-effort and degrade gracefully.
func DefaultStyles() Styles {
	return Styles{
		Header:    lipgloss.NewStyle().Bold(true).Foreground(colorBrand),
		Subtle:    lipgloss.NewStyle().Foreground(colorSubtle),
		LogLine:   lipgloss.NewStyle().PaddingLeft(1),
		Spinner:   lipgloss.NewStyle().Foreground(colorAccent),
		Success:   lipgloss.NewStyle().Foreground(colorSuccess),
		Error:     lipgloss.NewStyle().Foreground(colorError),
		Warning:   lipgloss.NewStyle().Foreground(colorWarning),
		Info:      lipgloss.NewStyle().Foreground(colorInfo),
		Footer:    lipgloss.NewStyle().Foreground(colorSubtle),
		Help:      lipgloss.NewStyle().Foreground(colorFaint),
		Separator: lipgloss.NewStyle().Foreground(colorFaint),

		Pane: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorFaint).
			Padding(0, 1),
		PaneTitle: lipgloss.NewStyle().Bold(true).Foreground(colorSubtle),

		StepDone:    lipgloss.NewStyle().Foreground(colorSuccess),
		StepActive:  lipgloss.NewStyle().Bold(true).Foreground(colorAccent),
		StepFailed:  lipgloss.NewStyle().Bold(true).Foreground(colorError),
		StepPending: lipgloss.NewStyle().Foreground(colorFaint),
		StepTag:     lipgloss.NewStyle().Foreground(colorSubtle),

		Banner: lipgloss.NewStyle().Bold(true).Foreground(colorWarning),
		BannerErr: lipgloss.NewStyle().Bold(true).
			Foreground(colorError),

		SummaryCard: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand).
			Padding(1, 2),
		SummaryTitle: lipgloss.NewStyle().Bold(true).Foreground(colorBrand),
	}
}
