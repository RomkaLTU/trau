package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	theme       = defaultTheme(true)
	themeName   string
	themeHexes  map[string]string
	themeIsDark = true
	themeNote   string
)

// SetTheme selects the palette every screen draws from: a preset name plus
// per-role hex overrides. It returns a human-readable note when a value was
// invalid and fell back to the default. The dark variant applies until the
// terminal background has been detected (see setThemeBackground).
func SetTheme(name string, overrides map[string]string) string {
	themeName, themeHexes = name, overrides
	theme, themeNote = resolveTheme(name, overrides, themeIsDark)
	return themeNote
}

func setThemeBackground(isDark bool) {
	themeIsDark = isDark
	theme, _ = resolveTheme(themeName, themeHexes, isDark)
}

// applyThemeFromItems re-resolves the palette from the settings editor's
// current config items, so a saved THEME/THEME_* change recolors the running
// UI without a restart.
func applyThemeFromItems(items []ConfigItem) {
	name := ""
	overrides := map[string]string{}
	for _, it := range items {
		switch {
		case it.Key == "THEME":
			name = it.Value
		case strings.HasPrefix(it.Key, "THEME_") && it.Value != "":
			overrides[strings.ToLower(strings.TrimPrefix(it.Key, "THEME_"))] = it.Value
		}
	}
	SetTheme(name, overrides)
}

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

// DefaultStyles returns the style set for the active theme. Lip Gloss
// automatically respects NO_COLOR and downsamples on low-color terminals, so
// these colors are best-effort and degrade gracefully.
func DefaultStyles() Styles {
	return newStyles(theme)
}

func newStyles(t Theme) Styles {
	return Styles{
		Header:    lipgloss.NewStyle().Bold(true).Foreground(t.Brand),
		Subtle:    lipgloss.NewStyle().Foreground(t.Subtle),
		LogLine:   lipgloss.NewStyle().PaddingLeft(1),
		Spinner:   lipgloss.NewStyle().Foreground(t.Accent),
		Success:   lipgloss.NewStyle().Foreground(t.Success),
		Error:     lipgloss.NewStyle().Foreground(t.Error),
		Warning:   lipgloss.NewStyle().Foreground(t.Warning),
		Info:      lipgloss.NewStyle().Foreground(t.Info),
		Footer:    lipgloss.NewStyle().Foreground(t.Subtle),
		Help:      lipgloss.NewStyle().Foreground(t.Faint),
		Separator: lipgloss.NewStyle().Foreground(t.Faint),

		Pane: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Border).
			Padding(0, 1),
		PaneTitle: lipgloss.NewStyle().Bold(true).Foreground(t.Subtle),

		StepDone:    lipgloss.NewStyle().Foreground(t.Success),
		StepActive:  lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		StepFailed:  lipgloss.NewStyle().Bold(true).Foreground(t.Error),
		StepPending: lipgloss.NewStyle().Foreground(t.Faint),
		StepTag:     lipgloss.NewStyle().Foreground(t.Subtle),

		Banner: lipgloss.NewStyle().Bold(true).Foreground(t.Warning),
		BannerErr: lipgloss.NewStyle().Bold(true).
			Foreground(t.Error),

		SummaryCard: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Brand).
			Padding(1, 2),
		SummaryTitle: lipgloss.NewStyle().Bold(true).Foreground(t.Brand),
	}
}
