package tui

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// huhTheme maps the active trau palette onto huh's style set so the embedded
// onboarding form reads as one language with the rest of the TUI. The form is
// framed by the shared card, so the field Base border is dropped; focus is shown
// by the brightened title and the selector glyph instead.
func huhTheme(t Theme) huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		s := huh.ThemeBase(isDark)

		s.Focused.Base = lipgloss.NewStyle()
		s.Focused.Card = s.Focused.Base
		s.Focused.Title = s.Focused.Title.Foreground(t.Brand).Bold(true)
		s.Focused.NoteTitle = s.Focused.NoteTitle.Foreground(t.Brand).Bold(true).MarginBottom(1)
		s.Focused.Description = s.Focused.Description.Foreground(t.Subtle)
		s.Focused.ErrorIndicator = s.Focused.ErrorIndicator.Foreground(t.Error)
		s.Focused.ErrorMessage = s.Focused.ErrorMessage.Foreground(t.Error)
		s.Focused.SelectSelector = lipgloss.NewStyle().Foreground(t.Info).SetString("▸ ")
		s.Focused.NextIndicator = s.Focused.NextIndicator.Foreground(t.Accent)
		s.Focused.PrevIndicator = s.Focused.PrevIndicator.Foreground(t.Accent)
		s.Focused.Option = s.Focused.Option.Foreground(t.Text)
		s.Focused.SelectedOption = s.Focused.SelectedOption.Foreground(t.Brand).Bold(true)
		s.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(t.Brand).SetString("")
		s.Focused.UnselectedPrefix = lipgloss.NewStyle().SetString("")
		s.Focused.UnselectedOption = s.Focused.UnselectedOption.Foreground(t.Subtle)
		s.Focused.FocusedButton = s.Focused.FocusedButton.Foreground(t.Ink).Background(t.Brand).Bold(true)
		s.Focused.Next = s.Focused.FocusedButton
		s.Focused.BlurredButton = s.Focused.BlurredButton.Foreground(t.Subtle).Background(t.Surface)

		s.Focused.TextInput.Cursor = s.Focused.TextInput.Cursor.Foreground(t.Accent)
		s.Focused.TextInput.Placeholder = s.Focused.TextInput.Placeholder.Foreground(t.Faint)
		s.Focused.TextInput.Prompt = s.Focused.TextInput.Prompt.Foreground(t.Info)
		s.Focused.TextInput.Text = s.Focused.TextInput.Text.Foreground(t.Text)

		s.Blurred = s.Focused
		s.Blurred.NextIndicator = lipgloss.NewStyle()
		s.Blurred.PrevIndicator = lipgloss.NewStyle()

		s.Group.Title = s.Focused.Title
		s.Group.Description = s.Focused.Description
		return s
	})
}
